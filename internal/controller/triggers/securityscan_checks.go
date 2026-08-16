package triggers

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/go-github/v68/github"
	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/security"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// SecurityCheckRun is one desired GitHub check for a scanned commit. The
// summary is built by the controller and contains only severity counts and a
// dashboard link unless spec.checks.includeFindingSummaries opted in to
// titles and locations; evidence never enters this struct.
type SecurityCheckRun struct {
	// Name is the check name shown in the GitHub UI.
	Name string
	// Revision is the commit SHA the check reports on.
	Revision string
	// Conclusion is success, failure, or neutral.
	Conclusion string
	Title      string
	Summary    string
	// DetailsURL optionally links back to the dashboard scan detail.
	DetailsURL string
}

// SecurityCheckPublisher publishes GitHub checks and SARIF uploads for scan
// runs. The default implementation talks to GitHub with the trigger
// repository's credentials; tests inject fakes.
type SecurityCheckPublisher interface {
	// PublishCheck creates (or re-creates) the check and returns its URL.
	PublishCheck(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, check SecurityCheckRun) (string, error)
	// UploadSARIF uploads the SARIF document to GitHub code scanning for
	// (revision, ref) and returns the upload id.
	UploadSARIF(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, revision, ref, sarif string) (string, error)
}

// githubSecurityCheckPublisher publishes through the GitHub API. GitHub App
// credentials create real check runs; PAT credentials fall back to commit
// statuses, which is the closest a PAT can get (check runs are App-only).
type githubSecurityCheckPublisher struct {
	client client.Client
	minter gitHubAppTokenMinter
}

func (p *githubSecurityCheckPublisher) PublishCheck(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, check SecurityCheckRun) (string, error) {
	token, err := resolveGitHubToken(ctx, p.client, gh, p.minter)
	if err != nil {
		return "", err
	}
	ghc := github.NewClient(nil).WithAuthToken(token)
	if gh.Spec.GitHubApp != nil {
		status := "completed"
		opts := github.CreateCheckRunOptions{
			Name:       check.Name,
			HeadSHA:    check.Revision,
			Status:     &status,
			Conclusion: &check.Conclusion,
			Output: &github.CheckRunOutput{
				Title:   &check.Title,
				Summary: &check.Summary,
			},
		}
		if check.DetailsURL != "" {
			opts.DetailsURL = &check.DetailsURL
		}
		run, _, err := ghc.Checks.CreateCheckRun(ctx, gh.Spec.Owner, gh.Spec.Repo, opts)
		if err != nil {
			return "", fmt.Errorf("creating check run: %w", err)
		}
		return run.GetHTMLURL(), nil
	}

	state := "success"
	if check.Conclusion == "failure" {
		state = "failure"
	}
	description := check.Title
	if len(description) > 140 {
		description = description[:140]
	}
	status := &github.RepoStatus{
		State:       &state,
		Context:     &check.Name,
		Description: &description,
	}
	if check.DetailsURL != "" {
		status.TargetURL = &check.DetailsURL
	}
	created, _, err := ghc.Repositories.CreateStatus(ctx, gh.Spec.Owner, gh.Spec.Repo, check.Revision, status)
	if err != nil {
		return "", fmt.Errorf("creating commit status: %w", err)
	}
	return created.GetURL(), nil
}

func (p *githubSecurityCheckPublisher) UploadSARIF(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, revision, ref, sarif string) (string, error) {
	token, err := resolveGitHubToken(ctx, p.client, gh, p.minter)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(sarif)); err != nil {
		return "", fmt.Errorf("compressing SARIF: %w", err)
	}
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("compressing SARIF: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	ghc := github.NewClient(nil).WithAuthToken(token)
	analysis := &github.SarifAnalysis{
		CommitSHA: &revision,
		Ref:       &ref,
		Sarif:     &encoded,
	}
	id, _, err := ghc.CodeScanning.UploadSarif(ctx, gh.Spec.Owner, gh.Spec.Repo, analysis)
	if err != nil {
		return "", fmt.Errorf("uploading SARIF to code scanning: %w", err)
	}
	return id.GetID(), nil
}

func (r *SecurityScanReconciler) checkPublisher() SecurityCheckPublisher {
	if r.CheckPublisher != nil {
		return r.CheckPublisher
	}
	return &githubSecurityCheckPublisher{client: r.Client, minter: nil}
}

// securityScanCheckConclusion maps the scan run outcome and the configured
// severity policy onto a GitHub check conclusion:
//   - a run that did not succeed is neutral (the policy cannot be evaluated),
//   - failOnSeverity with open findings at or above the threshold is failure,
//   - failOnSeverity with none is success,
//   - no failOnSeverity is neutral (informational).
func securityScanCheckConclusion(phase platformv1alpha1.AgentRunPhase, failOnSeverity string, openBySeverity map[string]int32) (string, string) {
	if phase != platformv1alpha1.AgentRunPhaseSucceeded {
		return "neutral", fmt.Sprintf("Security scan did not complete (run %s)", strings.ToLower(string(phase)))
	}
	if failOnSeverity == "" {
		return "neutral", "Security scan completed (no failure threshold configured)"
	}
	if n := openFindingsAtOrAbove(openBySeverity, failOnSeverity); n > 0 {
		return "failure", fmt.Sprintf("Security scan found %d open finding(s) at or above severity %q", n, failOnSeverity)
	}
	return "success", fmt.Sprintf("Security scan found no open findings at or above severity %q", failOnSeverity)
}

// securityScanCheckSummary renders the check summary. The default contains
// only severity counts and a dashboard link; findingLines (opt-in titles and
// locations, never evidence) are appended when non-empty.
func securityScanCheckSummary(scan *triggersv1alpha1.SecurityScan, runName string, openBySeverity map[string]int32, findingLines []string, dashboardBaseURL string) string {
	var b strings.Builder
	b.WriteString("## Security scan results\n\n")
	b.WriteString("| Severity | Open findings |\n|---|---|\n")
	for _, severity := range securityScanSeverities {
		fmt.Fprintf(&b, "| %s | %d |\n", severity, openBySeverity[severity])
	}
	if len(findingLines) > 0 {
		b.WriteString("\n### Open findings\n\n")
		for _, line := range findingLines {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	path := fmt.Sprintf("/security/%s/%s", scan.Namespace, runName)
	if dashboardBaseURL != "" {
		fmt.Fprintf(&b, "\nFull details: %s%s\n", strings.TrimRight(dashboardBaseURL, "/"), path)
	} else {
		fmt.Fprintf(&b, "\nFull details are in the security dashboard: `%s`\n", path)
	}
	return b.String()
}

// securityScanCheckFindingLines renders the opt-in per-finding lines: title
// and location only — descriptions, evidence, impact, and attack vectors are
// intentionally excluded.
func securityScanCheckFindingLines(findings []store.SecurityFindingRecord, max int) []string {
	open := make([]store.SecurityFindingRecord, 0, len(findings))
	for _, f := range findings {
		if f.Status == store.SecurityFindingStatusOpen && f.DuplicateOf == nil {
			open = append(open, f)
		}
	}
	sort.SliceStable(open, func(i, j int) bool {
		return securitySeverityRank(open[i].Severity) > securitySeverityRank(open[j].Severity)
	})
	if len(open) > max {
		open = open[:max]
	}
	lines := make([]string, 0, len(open))
	for _, f := range open {
		location := ""
		if f.FilePath != "" {
			location = f.FilePath
			if f.StartLine > 0 {
				location = fmt.Sprintf("%s:%d", location, f.StartLine)
			}
			location = fmt.Sprintf(" (`%s`)", location)
		}
		lines = append(lines, fmt.Sprintf("- **%s**: %s%s", f.Severity, f.Title, location))
	}
	return lines
}

// securitySeverityRank orders severities; higher is more severe.
func securitySeverityRank(severity string) int {
	for i, s := range securityScanSeverities {
		if s == severity {
			return len(securityScanSeverities) - i
		}
	}
	return 0
}

// securityScanCheckStateHash fingerprints the desired check so triage-driven
// count changes re-publish and identical states do not.
func securityScanCheckStateHash(check SecurityCheckRun, sarifWanted bool) string {
	sum := sha1.Sum([]byte(strings.Join([]string{
		check.Name, check.Revision, check.Conclusion, check.Title, check.Summary, fmt.Sprintf("sarif=%t", sarifWanted),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// runTriggerEvent decodes the event annotation stamped on a scan run, or nil.
func runTriggerEvent(run *platformv1alpha1.AgentRun) *SecurityScanTriggerEvent {
	raw := strings.TrimSpace(run.Annotations[triggersv1alpha1.SecurityScanEventAnnotation])
	if raw == "" {
		return nil
	}
	ev := &SecurityScanTriggerEvent{}
	if err := json.Unmarshal([]byte(raw), ev); err != nil {
		return nil
	}
	return ev
}

// securityScanSARIFRef derives the git ref GitHub code scanning requires for
// a SARIF upload.
func securityScanSARIFRef(scan *triggersv1alpha1.SecurityScan, ev *SecurityScanTriggerEvent) string {
	if ev != nil {
		if ev.Source == "pull_request" && ev.PRNumber > 0 {
			return fmt.Sprintf("refs/pull/%d/head", ev.PRNumber)
		}
		if ev.Branch != "" {
			return "refs/heads/" + ev.Branch
		}
	}
	return "refs/heads/" + scan.Spec.EffectiveBaseBranch()
}

func (r *SecurityScanReconciler) uploadRunCheckSARIF(
	ctx context.Context, scan *triggersv1alpha1.SecurityScan, runName string, ev *SecurityScanTriggerEvent,
	gh *triggersv1alpha1.GitHubRepository, revision string, status *triggersv1alpha1.SecurityScanCheckStatus,
) bool {
	retry := false
	sarif, err := r.scanRunSARIF(ctx, scan, runName)
	switch {
	case err != nil:
		status.SARIFError = "reading SARIF artifact: " + err.Error()
		retry = true
	case sarif == "":
		status.SARIFError = "the run stored no SARIF report artifact"
	default:
		if _, err := r.checkPublisher().UploadSARIF(ctx, gh, revision, securityScanSARIFRef(scan, ev), sarif); err != nil {
			status.SARIFError = err.Error()
			retry = true
		} else {
			status.SARIFUploaded = true
		}
	}
	if status.SARIFError != "" {
		r.recordScanEvent(scan, corev1.EventTypeWarning, "SARIFUploadFailed", status.SARIFError)
	}
	return retry
}

// publishRunCheck publishes the GitHub check for the scan's last run when
// checks are enabled, the run is terminal, and it carries a platform-stamped
// revision. Publishing is idempotent per state hash and re-runs whenever the
// desired state changes (for example after findings are triaged). Failures
// never corrupt scan state: they are recorded in status.lastCheck.error plus
// a Warning event, and the returned retry flag makes the caller requeue.
func (r *SecurityScanReconciler) publishRunCheck(ctx context.Context, scan *triggersv1alpha1.SecurityScan) bool {
	checks := scan.Spec.Checks
	if checks == nil || !checks.Enabled || scan.Status.LastRunName == "" {
		return false
	}
	log := logf.FromContext(ctx)
	run := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: scan.Namespace, Name: scan.Status.LastRunName}, run); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "failed to get scan AgentRun for check publishing", "run", scan.Status.LastRunName)
		}
		return false
	}
	if !isCronRunTerminal(run.Status.Phase) {
		return false
	}
	revision := strings.TrimSpace(run.Annotations[triggersv1alpha1.SecurityScanRevisionAnnotation])
	if revision == "" {
		return false
	}

	summary := r.summarizeFindings(ctx, scan)
	openBySeverity := map[string]int32{}
	if summary != nil {
		openBySeverity = summary.openBySeverity
	}
	conclusion, title := securityScanCheckConclusion(run.Status.Phase, r.effectiveFailOnSeverity(ctx, scan), openBySeverity)

	var findingLines []string
	if checks.IncludeFindingSummaries && r.Findings != nil {
		findings, err := r.Findings.ListSecurityFindings(ctx, store.SecurityFindingFilter{
			Namespace: scan.Namespace, ScanName: scan.Name, Status: store.SecurityFindingStatusOpen, Limit: 200,
		})
		if err != nil {
			log.Error(err, "failed to list findings for check summary", "scan", scan.Name)
		} else {
			findingLines = securityScanCheckFindingLines(findings, 20)
		}
	}

	check := SecurityCheckRun{
		Name:       "security-scan/" + scan.Name,
		Revision:   revision,
		Conclusion: conclusion,
		Title:      title,
		Summary:    securityScanCheckSummary(scan, run.Name, openBySeverity, findingLines, r.DashboardBaseURL),
	}
	if r.DashboardBaseURL != "" {
		check.DetailsURL = fmt.Sprintf("%s/security/%s/%s", strings.TrimRight(r.DashboardBaseURL, "/"), scan.Namespace, run.Name)
	}
	sarifWanted := checks.UploadSARIF && run.Status.Phase == platformv1alpha1.AgentRunPhaseSucceeded
	hash := securityScanCheckStateHash(check, sarifWanted)

	last := scan.Status.LastCheck
	sarifDone := last != nil && last.RunName == run.Name && last.SARIFUploaded
	if last != nil && last.StateHash == hash && last.Error == "" && last.SARIFError == "" && (!sarifWanted || sarifDone) {
		return false
	}

	recordFailure := func(message string) {
		r.recordScanEvent(scan, corev1.EventTypeWarning, "CheckPublishFailed", message)
		if err := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastCheck = &triggersv1alpha1.SecurityScanCheckStatus{
				RunName:       run.Name,
				Revision:      revision,
				Conclusion:    conclusion,
				Error:         message,
				SARIFUploaded: sarifDone,
			}
		}); err != nil {
			log.Error(err, "failed to record check publish failure", "scan", scan.Name)
		}
	}

	gh, err := r.triggerRepository(ctx, scan)
	if err != nil {
		recordFailure("resolving check credentials: " + err.Error())
		return true
	}
	url, err := r.checkPublisher().PublishCheck(ctx, gh, check)
	if err != nil {
		recordFailure("publishing check: " + err.Error())
		return true
	}

	now := metav1.NewTime(r.now())
	newStatus := &triggersv1alpha1.SecurityScanCheckStatus{
		RunName:       run.Name,
		Revision:      revision,
		Conclusion:    conclusion,
		URL:           url,
		PublishedAt:   &now,
		StateHash:     hash,
		SARIFUploaded: sarifDone,
	}
	retry := false
	if sarifWanted && !sarifDone {
		retry = r.uploadRunCheckSARIF(ctx, scan, run.Name, runTriggerEvent(run), gh, revision, newStatus)
	}

	if err := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.LastCheck = newStatus
	}); err != nil {
		log.Error(err, "failed to record published check", "scan", scan.Name)
		return true
	}
	return retry
}

// scanRunSARIF loads the SARIF artifact stored by the run's report
// submission, or "" when none exists yet. The run's own session is resolved
// directly; the scan record's session is only a fallback for coordinator
// runs predating session-by-run lookups. (Deterministic task runs share ONE
// execution-level scan record, so a record lookup by task run name finds
// nothing — the artifact still lives in that run's session.)
func (r *SecurityScanReconciler) scanRunSARIF(ctx context.Context, scan *triggersv1alpha1.SecurityScan, runName string) (string, error) {
	if r.Findings == nil || r.StateStore == nil {
		return "", fmt.Errorf("no findings store configured")
	}
	var sessionID *uuid.UUID
	if sess, err := r.StateStore.GetSessionByRun(ctx, runName, scan.Namespace); err == nil && sess != nil {
		sessionID = &sess.ID
	}
	if sessionID == nil {
		rec, err := r.Findings.GetSecurityScan(ctx, scan.Namespace, runName)
		if err != nil {
			return "", err
		}
		if rec == nil || rec.SessionID == nil {
			return "", nil
		}
		sessionID = rec.SessionID
	}
	art, err := r.StateStore.GetArtifact(ctx, *sessionID, security.SARIFArtifactKind)
	if err != nil {
		return "", err
	}
	if art == nil {
		return "", nil
	}
	return strings.TrimSpace(art.Content), nil
}

// publishExecutionCheck publishes the single aggregate GitHub check for a
// terminal deterministic execution. Per-task runs never publish: this is
// gated on the execution being terminal and deduped through the same
// status.lastCheck state hash the coordinator path uses, so it fires exactly
// once per desired check state and survives controller restarts. The check
// requires a platform-stamped revision (a trigger event or a pinned
// spec.revision), matching the coordinator behavior.
func (r *SecurityScanReconciler) publishExecutionCheck(ctx context.Context, scan *triggersv1alpha1.SecurityScan, exec *triggersv1alpha1.SecurityScanExecutionStatus) bool {
	checks := scan.Spec.Checks
	if checks == nil || !checks.Enabled || !securityScanExecutionTerminal(exec.Phase) {
		return false
	}
	log := logf.FromContext(ctx)
	ev := securityScanExecutionEvent(scan, exec)
	revision := strings.TrimSpace(scan.Spec.Revision)
	if ev != nil {
		revision = strings.TrimSpace(ev.Revision)
	}
	if revision == "" {
		return false
	}
	reportRuns := securityScanExecutionReportRuns(exec)
	reportRun := scan.Name
	if len(reportRuns) > 0 {
		reportRun = reportRuns[len(reportRuns)-1]
	}

	runPhase := platformv1alpha1.AgentRunPhaseFailed
	switch exec.Phase {
	case triggersv1alpha1.SecurityScanExecutionPhaseSucceeded:
		runPhase = platformv1alpha1.AgentRunPhaseSucceeded
	case triggersv1alpha1.SecurityScanExecutionPhaseCancelled:
		// The check title names the phase, so a scan the user stopped must
		// read "cancelled" and not "failed". The conclusion is unchanged:
		// anything short of Succeeded stays neutral.
		runPhase = platformv1alpha1.AgentRunPhaseCancelled
	}
	summary := r.summarizeFindings(ctx, scan)
	openBySeverity := map[string]int32{}
	if summary != nil {
		openBySeverity = summary.openBySeverity
	}
	conclusion, title := securityScanCheckConclusion(runPhase, r.effectiveFailOnSeverity(ctx, scan), openBySeverity)

	var findingLines []string
	if checks.IncludeFindingSummaries && r.Findings != nil {
		findings, err := r.Findings.ListSecurityFindings(ctx, store.SecurityFindingFilter{
			Namespace: scan.Namespace, ScanName: scan.Name, Status: store.SecurityFindingStatusOpen, Limit: 200,
		})
		if err != nil {
			log.Error(err, "failed to list findings for check summary", "scan", scan.Name)
		} else {
			findingLines = securityScanCheckFindingLines(findings, 20)
		}
	}

	check := SecurityCheckRun{
		Name:       "security-scan/" + scan.Name,
		Revision:   revision,
		Conclusion: conclusion,
		Title:      title,
		Summary:    securityScanCheckSummary(scan, reportRun, openBySeverity, findingLines, r.DashboardBaseURL),
	}
	if r.DashboardBaseURL != "" {
		check.DetailsURL = fmt.Sprintf("%s/security/%s/%s", strings.TrimRight(r.DashboardBaseURL, "/"), scan.Namespace, reportRun)
	}
	sarifWanted := checks.UploadSARIF && exec.Phase == triggersv1alpha1.SecurityScanExecutionPhaseSucceeded
	hash := securityScanCheckStateHash(check, sarifWanted)

	last := scan.Status.LastCheck
	sarifDone := last != nil && last.RunName == reportRun && last.SARIFUploaded
	if last != nil && last.StateHash == hash && last.Error == "" && last.SARIFError == "" && (!sarifWanted || sarifDone) {
		return false
	}

	recordFailure := func(message string) {
		r.recordScanEvent(scan, corev1.EventTypeWarning, "CheckPublishFailed", message)
		if err := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastCheck = &triggersv1alpha1.SecurityScanCheckStatus{
				RunName:       reportRun,
				Revision:      revision,
				Conclusion:    conclusion,
				Error:         message,
				SARIFUploaded: sarifDone,
			}
		}); err != nil {
			log.Error(err, "failed to record check publish failure", "scan", scan.Name)
		}
	}

	gh, err := r.triggerRepository(ctx, scan)
	if err != nil {
		recordFailure("resolving check credentials: " + err.Error())
		return true
	}
	url, err := r.checkPublisher().PublishCheck(ctx, gh, check)
	if err != nil {
		recordFailure("publishing check: " + err.Error())
		return true
	}

	now := metav1.NewTime(r.now())
	newStatus := &triggersv1alpha1.SecurityScanCheckStatus{
		RunName:       reportRun,
		Revision:      revision,
		Conclusion:    conclusion,
		URL:           url,
		PublishedAt:   &now,
		StateHash:     hash,
		SARIFUploaded: sarifDone,
	}
	retry := false
	if sarifWanted && !sarifDone {
		retry = r.uploadExecutionCheckSARIF(ctx, scan, reportRuns, ev, gh, revision, newStatus)
	}

	if err := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.LastCheck = newStatus
	}); err != nil {
		log.Error(err, "failed to record published check", "scan", scan.Name)
		return true
	}
	return retry
}

// uploadExecutionCheckSARIF aggregates the SARIF artifacts stored by a
// terminal deterministic execution's succeeded task runs — every sink task
// stores its own report, so all of them contribute — into one document and
// uploads it once. Runs that stored no SARIF artifact contribute nothing.
func (r *SecurityScanReconciler) uploadExecutionCheckSARIF(
	ctx context.Context, scan *triggersv1alpha1.SecurityScan, runNames []string, ev *SecurityScanTriggerEvent,
	gh *triggersv1alpha1.GitHubRepository, revision string, status *triggersv1alpha1.SecurityScanCheckStatus,
) bool {
	retry := false
	var docs []string
	for _, runName := range runNames {
		sarif, err := r.scanRunSARIF(ctx, scan, runName)
		if err != nil {
			status.SARIFError = fmt.Sprintf("reading SARIF artifact of run %s: %s", runName, err.Error())
			retry = true
			break
		}
		if sarif != "" {
			docs = append(docs, sarif)
		}
	}
	if status.SARIFError == "" {
		switch merged, err := mergeSecuritySARIFDocuments(docs); {
		case err != nil:
			status.SARIFError = "merging SARIF artifacts: " + err.Error()
		case merged == "":
			status.SARIFError = "no task run stored a SARIF report artifact"
		default:
			if _, err := r.checkPublisher().UploadSARIF(ctx, gh, revision, securityScanSARIFRef(scan, ev), merged); err != nil {
				status.SARIFError = err.Error()
				retry = true
			} else {
				status.SARIFUploaded = true
			}
		}
	}
	if status.SARIFError != "" {
		r.recordScanEvent(scan, corev1.EventTypeWarning, "SARIFUploadFailed", status.SARIFError)
	}
	return retry
}

// mergeSecuritySARIFDocuments folds several SARIF documents into one by
// concatenating their runs arrays onto the first document; a single document
// is returned verbatim.
func mergeSecuritySARIFDocuments(docs []string) (string, error) {
	if len(docs) == 0 {
		return "", nil
	}
	if len(docs) == 1 {
		return docs[0], nil
	}
	var base map[string]json.RawMessage
	if err := json.Unmarshal([]byte(docs[0]), &base); err != nil {
		return "", err
	}
	runs := []json.RawMessage{}
	for _, doc := range docs {
		var parsed struct {
			Runs []json.RawMessage `json:"runs"`
		}
		if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
			return "", err
		}
		runs = append(runs, parsed.Runs...)
	}
	mergedRuns, err := json.Marshal(runs)
	if err != nil {
		return "", err
	}
	base["runs"] = mergedRuns
	merged, err := json.Marshal(base)
	if err != nil {
		return "", err
	}
	return string(merged), nil
}
