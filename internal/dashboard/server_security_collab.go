package dashboard

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/go-github/v68/github"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/githubapp"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// sweepExpiredAcceptedRisks cheaply flips expired accepted_risk findings
// back to open before a read so lists and summaries never show a stale
// suppression. Best-effort: the read stays valid without the sweep.
func (s *Server) sweepExpiredAcceptedRisks(ctx context.Context, sec store.SecurityFindingStore, namespace string) {
	if _, err := sec.ExpireAcceptedRisks(ctx, namespace); err != nil {
		log.Printf("WARN: expiring accepted-risk findings in %s: %v", namespace, err)
	}
}

func securityFindingTrendsProto(in *store.SecurityFindingTrends) *platform.SecurityFindingTrends {
	if in == nil {
		return nil
	}
	return &platform.SecurityFindingTrends{
		TriagedCount:                  in.TriagedCount,
		ResolvedCount:                 in.ResolvedCount,
		AvgTimeToTriageSeconds:        in.AvgTimeToTriageSeconds,
		MedianTimeToTriageSeconds:     in.MedianTimeToTriageSeconds,
		AvgTimeToResolutionSeconds:    in.AvgTimeToResolutionSeconds,
		MedianTimeToResolutionSeconds: in.MedianTimeToResolutionSeconds,
	}
}

// maxSecurityAssigneeLen bounds assignee identifiers.
const maxSecurityAssigneeLen = 200

// UpdateSecurityFindingAssignee sets or clears a finding's assignee,
// recording an audited assignee_changed event.
func (s *Server) UpdateSecurityFindingAssignee(ctx context.Context, req *platform.UpdateSecurityFindingAssigneeRequest) (*platform.SecurityFinding, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	assignee := strings.TrimSpace(req.GetAssignee())
	if utf8.RuneCountInString(assignee) > maxSecurityAssigneeLen {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("assignee exceeds %d characters", maxSecurityAssigneeLen))
	}
	finding, err := s.authorizedSecurityFinding(ctx, sec, req.GetId(), req.GetNamespace(), "")
	if err != nil {
		return nil, err
	}
	if err := sec.SetSecurityFindingAssignee(ctx, finding.Namespace, finding.ID, assignee, actor.Subject); err != nil {
		if errors.Is(err, store.ErrSecurityFindingNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security finding %s not found", finding.ID))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("updating security finding assignee: %w", err))
	}
	return s.reloadSecurityFinding(ctx, sec, finding.Namespace, finding.ID)
}

// UpdateSecurityFindingTicket links (non-empty URL) or unlinks (empty URL)
// an external ticket on a finding.
func (s *Server) UpdateSecurityFindingTicket(ctx context.Context, req *platform.UpdateSecurityFindingTicketRequest) (*platform.SecurityFinding, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	ticketURL := strings.TrimSpace(req.GetTicketUrl())
	if ticketURL != "" {
		if err := store.ValidateSecurityTicketURL(ticketURL); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	provider := strings.ToLower(strings.TrimSpace(req.GetTicketProvider()))
	if utf8.RuneCountInString(provider) > 40 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("ticket provider exceeds 40 characters"))
	}
	finding, err := s.authorizedSecurityFinding(ctx, sec, req.GetId(), req.GetNamespace(), "")
	if err != nil {
		return nil, err
	}
	if err := sec.SetSecurityFindingTicket(ctx, finding.Namespace, finding.ID, ticketURL, provider, actor.Subject); err != nil {
		if errors.Is(err, store.ErrSecurityFindingNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security finding %s not found", finding.ID))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("updating security finding ticket: %w", err))
	}
	return s.reloadSecurityFinding(ctx, sec, finding.Namespace, finding.ID)
}

// securityIssueCreator creates one external issue for a finding ticket. It
// is a seam for tests; the default implementation talks to GitHub with the
// GitHubRepository's configured credentials.
type securityIssueCreator interface {
	CreateIssue(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, title, body string) (string, error)
}

// githubSecurityIssueCreator resolves the GitHubRepository's credential (PAT
// secret or GitHub App installation token) and creates the issue. Tokens are
// used for the one call and never stored or logged.
type githubSecurityIssueCreator struct {
	k8s    client.Client
	minter *githubapp.KeyedMinter
}

func (c *githubSecurityIssueCreator) CreateIssue(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, title, body string) (string, error) {
	token, err := c.resolveToken(ctx, gh)
	if err != nil {
		return "", err
	}
	ghc := github.NewClient(nil).WithAuthToken(token)
	issue, _, err := ghc.Issues.Create(ctx, gh.Spec.Owner, gh.Spec.Repo, &github.IssueRequest{
		Title: &title,
		Body:  &body,
	})
	if err != nil {
		return "", fmt.Errorf("creating GitHub issue: %w", err)
	}
	return issue.GetHTMLURL(), nil
}

func (c *githubSecurityIssueCreator) resolveToken(ctx context.Context, gh *triggersv1alpha1.GitHubRepository) (string, error) {
	if gh.Spec.GitHubApp != nil {
		secretName := strings.TrimSpace(gh.Spec.GitHubApp.PrivateKeySecret)
		if secretName == "" {
			return "", fmt.Errorf("GitHubRepository %s/%s spec.githubApp.privateKeySecret is required", gh.Namespace, gh.Name)
		}
		secret := &corev1.Secret{}
		if err := c.k8s.Get(ctx, client.ObjectKey{Namespace: gh.Namespace, Name: secretName}, secret); err != nil {
			return "", fmt.Errorf("getting GitHub App private key secret: %w", err)
		}
		key := secret.Data[githubapp.PrivateKeySecretKey]
		if len(key) == 0 {
			return "", fmt.Errorf("GitHub App private key secret %s has no key %q", secretName, githubapp.PrivateKeySecretKey)
		}
		minter := c.minter
		if minter == nil {
			minter = githubapp.NewKeyedMinter()
		}
		return minter.MintInstallationToken(ctx, gh.Spec.GitHubApp.AppID, gh.Spec.GitHubApp.InstallationID, key)
	}
	secretName := strings.TrimSpace(gh.Spec.GitHubTokenSecret)
	if secretName == "" {
		return "", fmt.Errorf("GitHubRepository %s/%s configures neither githubTokenSecret nor githubApp", gh.Namespace, gh.Name)
	}
	secret := &corev1.Secret{}
	if err := c.k8s.Get(ctx, client.ObjectKey{Namespace: gh.Namespace, Name: secretName}, secret); err != nil {
		return "", fmt.Errorf("getting GitHub token secret: %w", err)
	}
	token := strings.TrimSpace(string(secret.Data[githubapp.TokenSecretKey]))
	if token == "" {
		return "", fmt.Errorf("GitHub token secret %s has no key %q", secretName, githubapp.TokenSecretKey)
	}
	return token, nil
}

// securityIssueCreatorFor returns the injected test creator or the default
// GitHub-backed one.
func (s *Server) securityIssueCreatorFor() securityIssueCreator {
	if s.securityTicketCreator != nil {
		return s.securityTicketCreator
	}
	return &githubSecurityIssueCreator{k8s: s.k8sClient, minter: s.githubAppMinter}
}

// buildSecurityTicketBody renders the non-secret issue body: identifying
// metadata and a link back to the dashboard finding, never raw evidence,
// impact analysis, or attack vectors.
func buildSecurityTicketBody(finding *store.SecurityFindingRecord, extra string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Severity:** %s\n", finding.Severity)
	if finding.Category != "" {
		fmt.Fprintf(&b, "**Category:** %s\n", finding.Category)
	}
	if finding.FilePath != "" {
		location := finding.FilePath
		if finding.StartLine > 0 {
			location = fmt.Sprintf("%s:%d", location, finding.StartLine)
		}
		fmt.Fprintf(&b, "**Location:** `%s`\n", location)
	}
	if finding.Repository != "" {
		fmt.Fprintf(&b, "**Repository:** %s\n", finding.Repository)
	}
	fmt.Fprintf(&b, "\nFull details (evidence, impact, remediation) are in the security dashboard:\n`/security/%s/%s/findings/%s`\n",
		finding.Namespace, finding.RunName, finding.ID)
	if extra = strings.TrimSpace(extra); extra != "" {
		fmt.Fprintf(&b, "\n%s\n", extra)
	}
	return b.String()
}

// CreateSecurityFindingTicket creates an external issue for a finding and
// links it. Only provider "github" creates tickets; Linear is link-only via
// UpdateSecurityFindingTicket.
func (s *Server) CreateSecurityFindingTicket(ctx context.Context, req *platform.CreateSecurityFindingTicketRequest) (*platform.SecurityFinding, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	provider := strings.ToLower(strings.TrimSpace(req.GetProvider()))
	if provider != "github" {
		return nil, connect.NewError(connect.CodeUnimplemented,
			fmt.Errorf("ticket creation is only supported for provider \"github\"; create the %s issue manually and link it with UpdateSecurityFindingTicket", req.GetProvider()))
	}
	repoRef := strings.TrimSpace(req.GetRepositoryRef())
	if repoRef == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("repository_ref is required"))
	}
	finding, err := s.authorizedSecurityFinding(ctx, sec, req.GetId(), req.GetNamespace(), "")
	if err != nil {
		return nil, err
	}
	if finding.TicketURL != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("finding already has a linked ticket (%s); unlink it first", finding.TicketURL))
	}
	// The GitHubRepository must live in the finding's (authorized)
	// namespace: credentials can never be borrowed across namespaces.
	gh := &triggersv1alpha1.GitHubRepository{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: finding.Namespace, Name: repoRef}, gh); err != nil {
		return nil, mapK8sError("get GitHubRepository", err)
	}
	title := strings.TrimSpace(req.GetTitle())
	if title == "" {
		title = fmt.Sprintf("[security] %s", finding.Title)
	}
	if utf8.RuneCountInString(title) > 250 {
		title = string([]rune(title)[:250])
	}
	body := buildSecurityTicketBody(finding, req.GetBody())
	url, err := s.securityIssueCreatorFor().CreateIssue(ctx, gh, title, body)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("creating ticket: %w", err))
	}
	if err := sec.SetSecurityFindingTicket(ctx, finding.Namespace, finding.ID, url, "github", actor.Subject); err != nil {
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("ticket created at %s but linking it failed: %w", url, err))
	}
	return s.reloadSecurityFinding(ctx, sec, finding.Namespace, finding.ID)
}

// maxBulkSecurityFindingIDs bounds one bulk triage batch.
const maxBulkSecurityFindingIDs = 200

// BulkUpdateSecurityFindingStatus applies a status and/or assignee change to
// many findings in one atomic transaction. On failure the whole batch is
// rolled back and the response reports which item failed and that the rest
// were aborted.
func (s *Server) BulkUpdateSecurityFindingStatus(ctx context.Context, req *platform.BulkUpdateSecurityFindingStatusRequest) (*platform.BulkUpdateSecurityFindingStatusResponse, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}
	visible, hidden, err := s.securityScanVisibility(ctx, namespace)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if scanName := req.GetScanName(); scanName != "" && !visible(scanName) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security scan %s/%s not found", namespace, scanName))
	}
	rawIDs := req.GetIds()
	if len(rawIDs) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at least one finding id is required"))
	}
	if len(rawIDs) > maxBulkSecurityFindingIDs {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at most %d findings per bulk update", maxBulkSecurityFindingIDs))
	}
	ids := make([]uuid.UUID, 0, len(rawIDs))
	for _, raw := range rawIDs {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid finding id %q", raw))
		}
		ids = append(ids, id)
	}
	// Without a scan scope the ids alone select the findings, so each one
	// must be checked against the caller's scan visibility. A hidden
	// finding fails the whole batch with the same NotFound a missing one
	// would produce (no UUID-existence oracle); missing ids fall through to
	// the store's per-id outcome reporting.
	if req.GetScanName() == "" && len(hidden) > 0 {
		for _, id := range ids {
			finding, err := sec.GetSecurityFinding(ctx, namespace, id)
			if err != nil && !errors.Is(err, store.ErrSecurityFindingNotFound) {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("getting security finding: %w", err))
			}
			if finding != nil && !visible(finding.ScanName) {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security finding %s not found", id))
			}
		}
	}

	upd := store.SecurityFindingBulkUpdate{Note: req.GetNote(), Actor: actor.Subject}
	if status := strings.TrimSpace(req.GetStatus()); status != "" {
		if !store.ValidSecurityFindingStatus(status) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid finding status %q", status))
		}
		upd.Status = &status
	}
	if req.GetSetAssignee() {
		assignee := strings.TrimSpace(req.GetAssignee())
		if utf8.RuneCountInString(assignee) > maxSecurityAssigneeLen {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("assignee exceeds %d characters", maxSecurityAssigneeLen))
		}
		upd.Assignee = &assignee
	}
	if upd.Status == nil && upd.Assignee == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("bulk update changes nothing: set a status or an assignee"))
	}
	if req.GetAcceptedRiskExpiresAt() != nil {
		if upd.Status == nil || *upd.Status != store.SecurityFindingStatusAcceptedRisk {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("accepted_risk_expires_at is only valid with status %q", store.SecurityFindingStatusAcceptedRisk))
		}
		t := req.GetAcceptedRiskExpiresAt().AsTime()
		if !t.After(time.Now()) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("accepted_risk_expires_at must be in the future"))
		}
		upd.AcceptedRiskExpiresAt = &t
	}

	resp := &platform.BulkUpdateSecurityFindingStatusResponse{}
	err = sec.BulkUpdateSecurityFindings(ctx, namespace, req.GetScanName(), ids, upd)
	if err != nil {
		var bulkErr *store.BulkSecurityFindingError
		if !errors.As(err, &bulkErr) {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("bulk updating security findings: %w", err))
		}
		for _, id := range ids {
			outcome := &platform.BulkUpdateSecurityFindingOutcome{Id: id.String()}
			if id == bulkErr.FindingID {
				outcome.Error = bulkErr.Err.Error()
			} else {
				outcome.Error = "aborted: batch rolled back"
			}
			resp.Results = append(resp.Results, outcome)
		}
		return resp, nil
	}
	for _, id := range ids {
		resp.Results = append(resp.Results, &platform.BulkUpdateSecurityFindingOutcome{Id: id.String(), Ok: true})
	}
	resp.Updated = int32(len(ids))
	s.nudgeSecurityScanStatusRefresh(ctx, namespace, req.GetScanName())
	return resp, nil
}

const (
	maxSecuritySavedFilterNameLen  = 100
	maxSecuritySavedFilterQueryLen = 4096
)

func securitySavedFilterProto(in *store.SecuritySavedFilter) *platform.SecuritySavedFilter {
	return &platform.SecuritySavedFilter{
		Id:        in.ID.String(),
		Namespace: in.Namespace,
		Owner:     in.Owner,
		Name:      in.Name,
		Query:     string(in.Query),
		CreatedAt: timestamppb.New(in.CreatedAt),
		UpdatedAt: timestamppb.New(in.UpdatedAt),
	}
}

// ListSecuritySavedFilters lists the calling user's saved filters in the
// namespace. Filters are private: one user can never see another's.
func (s *Server) ListSecuritySavedFilters(ctx context.Context, req *platform.ListSecuritySavedFiltersRequest) (*platform.ListSecuritySavedFiltersResponse, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}
	filters, err := sec.ListSecuritySavedFilters(ctx, namespace, actor.Subject)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing security saved filters: %w", err))
	}
	resp := &platform.ListSecuritySavedFiltersResponse{}
	for i := range filters {
		resp.Filters = append(resp.Filters, securitySavedFilterProto(&filters[i]))
	}
	return resp, nil
}

// SaveSecuritySavedFilter creates or replaces the calling user's saved
// filter by name.
func (s *Server) SaveSecuritySavedFilter(ctx context.Context, req *platform.SaveSecuritySavedFilterRequest) (*platform.SecuritySavedFilter, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("filter name is required"))
	}
	if utf8.RuneCountInString(name) > maxSecuritySavedFilterNameLen {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("filter name exceeds %d characters", maxSecuritySavedFilterNameLen))
	}
	query := strings.TrimSpace(req.GetQuery())
	if query == "" {
		query = "{}"
	}
	if len(query) > maxSecuritySavedFilterQueryLen {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("filter query exceeds %d bytes", maxSecuritySavedFilterQueryLen))
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(query), &parsed); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("filter query must be a JSON object: %w", err))
	}
	saved, err := sec.SaveSecuritySavedFilter(ctx, &store.SecuritySavedFilter{
		Namespace: namespace,
		Owner:     actor.Subject,
		Name:      name,
		Query:     json.RawMessage(query),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("saving security saved filter: %w", err))
	}
	return securitySavedFilterProto(saved), nil
}

// DeleteSecuritySavedFilter removes one of the calling user's saved filters.
func (s *Server) DeleteSecuritySavedFilter(ctx context.Context, req *platform.DeleteSecuritySavedFilterRequest) (*emptypb.Empty, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("filter name is required"))
	}
	if err := sec.DeleteSecuritySavedFilter(ctx, namespace, actor.Subject, name); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("deleting security saved filter: %w", err))
	}
	return &emptypb.Empty{}, nil
}

// ExportSecurityFindingAuditLog returns every audit event for a scan's
// findings as a downloadable CSV or JSON document.
func (s *Server) ExportSecurityFindingAuditLog(ctx context.Context, req *platform.ExportSecurityFindingAuditLogRequest) (*platform.ExportSecurityFindingAuditLogResponse, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}
	scanName := strings.TrimSpace(req.GetScanName())
	if scanName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("scan_name is required"))
	}
	visible, _, err := s.securityScanVisibility(ctx, namespace)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !visible(scanName) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security scan %s/%s not found", namespace, scanName))
	}
	format := strings.ToLower(strings.TrimSpace(req.GetFormat()))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown export format %q (want \"csv\" or \"json\")", req.GetFormat()))
	}
	records, err := sec.ExportSecurityFindingEvents(ctx, namespace, scanName, 0)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("exporting security finding events: %w", err))
	}
	resp := &platform.ExportSecurityFindingAuditLogResponse{
		Filename:   fmt.Sprintf("security-audit-%s.%s", scanName, format),
		EventCount: int32(len(records)),
	}
	if format == "json" {
		resp.ContentType = "application/json"
		content, err := securityAuditJSON(records)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encoding audit export: %w", err))
		}
		resp.Content = content
		return resp, nil
	}
	resp.ContentType = "text/csv"
	content, err := securityAuditCSV(records)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encoding audit export: %w", err))
	}
	resp.Content = content
	return resp, nil
}

func securityAuditCSV(records []store.SecurityFindingAuditRecord) ([]byte, error) {
	var b strings.Builder
	w := csv.NewWriter(&b)
	if err := w.Write([]string{"event_id", "created_at", "finding_id", "fingerprint", "title", "severity",
		"finding_status", "event_type", "actor", "note", "detail"}); err != nil {
		return nil, err
	}
	for i := range records {
		r := &records[i]
		if err := w.Write([]string{
			fmt.Sprint(r.Event.ID),
			r.Event.CreatedAt.UTC().Format(time.RFC3339),
			r.FindingID.String(),
			r.Fingerprint,
			r.Title,
			r.Severity,
			r.Status,
			r.Event.EventType,
			r.Event.Actor,
			r.Event.Note,
			string(r.Event.Detail),
		}); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func securityAuditJSON(records []store.SecurityFindingAuditRecord) ([]byte, error) {
	type entry struct {
		EventID     int64           `json:"event_id"`
		CreatedAt   time.Time       `json:"created_at"`
		FindingID   string          `json:"finding_id"`
		Fingerprint string          `json:"fingerprint"`
		Title       string          `json:"title"`
		Severity    string          `json:"severity"`
		Status      string          `json:"finding_status"`
		EventType   string          `json:"event_type"`
		Actor       string          `json:"actor"`
		Note        string          `json:"note,omitempty"`
		Detail      json.RawMessage `json:"detail,omitempty"`
	}
	entries := make([]entry, 0, len(records))
	for i := range records {
		r := &records[i]
		entries = append(entries, entry{
			EventID:     r.Event.ID,
			CreatedAt:   r.Event.CreatedAt.UTC(),
			FindingID:   r.FindingID.String(),
			Fingerprint: r.Fingerprint,
			Title:       r.Title,
			Severity:    r.Severity,
			Status:      r.Status,
			EventType:   r.Event.EventType,
			Actor:       r.Event.Actor,
			Note:        r.Event.Note,
			Detail:      r.Event.Detail,
		})
	}
	return json.MarshalIndent(entries, "", "  ")
}

// reloadSecurityFinding rereads a finding after a mutation so the response
// reflects the stored row.
func (s *Server) reloadSecurityFinding(ctx context.Context, sec store.SecurityFindingStore, namespace string, id uuid.UUID) (*platform.SecurityFinding, error) {
	updated, err := sec.GetSecurityFinding(ctx, namespace, id)
	if err != nil || updated == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reloading security finding: %w", err))
	}
	return securityFindingProto(updated), nil
}

// Connect adapter methods.

func (h *PlatformServiceConnectHandler) UpdateSecurityFindingAssignee(ctx context.Context, req *connect.Request[platform.UpdateSecurityFindingAssigneeRequest]) (*connect.Response[platform.SecurityFinding], error) {
	resp, err := h.srv.UpdateSecurityFindingAssignee(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateSecurityFindingTicket(ctx context.Context, req *connect.Request[platform.UpdateSecurityFindingTicketRequest]) (*connect.Response[platform.SecurityFinding], error) {
	resp, err := h.srv.UpdateSecurityFindingTicket(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CreateSecurityFindingTicket(ctx context.Context, req *connect.Request[platform.CreateSecurityFindingTicketRequest]) (*connect.Response[platform.SecurityFinding], error) {
	resp, err := h.srv.CreateSecurityFindingTicket(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) BulkUpdateSecurityFindingStatus(ctx context.Context, req *connect.Request[platform.BulkUpdateSecurityFindingStatusRequest]) (*connect.Response[platform.BulkUpdateSecurityFindingStatusResponse], error) {
	resp, err := h.srv.BulkUpdateSecurityFindingStatus(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListSecuritySavedFilters(ctx context.Context, req *connect.Request[platform.ListSecuritySavedFiltersRequest]) (*connect.Response[platform.ListSecuritySavedFiltersResponse], error) {
	resp, err := h.srv.ListSecuritySavedFilters(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) SaveSecuritySavedFilter(ctx context.Context, req *connect.Request[platform.SaveSecuritySavedFilterRequest]) (*connect.Response[platform.SecuritySavedFilter], error) {
	resp, err := h.srv.SaveSecuritySavedFilter(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteSecuritySavedFilter(ctx context.Context, req *connect.Request[platform.DeleteSecuritySavedFilterRequest]) (*connect.Response[emptypb.Empty], error) {
	resp, err := h.srv.DeleteSecuritySavedFilter(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ExportSecurityFindingAuditLog(ctx context.Context, req *connect.Request[platform.ExportSecurityFindingAuditLogRequest]) (*connect.Response[platform.ExportSecurityFindingAuditLogResponse], error) {
	resp, err := h.srv.ExportSecurityFindingAuditLog(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
