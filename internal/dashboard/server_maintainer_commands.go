package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// IssueMaintainerCommand creates an authenticated human-issued maintainer
// command for the specified work item. It gates on editor-level access and
// derives all server-controlled fields (issuer proof, repository, preconditions)
// so the browser never constructs them directly.
func (s *Server) IssueMaintainerCommand(ctx context.Context, req *platform.IssueMaintainerCommandRequest) (*platform.IssueMaintainerCommandResponse, error) {
	if req.GetNamespace() == "" || req.GetRepositoryName() == "" || req.GetWorkItemName() == "" || req.GetType() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace, repository_name, work_item_name, and type are required"))
	}

	actor, ok := requestActorFromContextOK(ctx)
	if !ok || actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authenticated identity required to issue maintainer commands"))
	}
	if actor.Role != "admin" && actor.Role != "owner" && s.checkResourceAccess(ctx, githubRepositoryResourceType, req.RepositoryName, req.Namespace) < AccessCollaborator {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("permission denied to act on this repository"))
	}

	// Validate the command type enum.
	cmdType, err := parseMaintainerCommandType(req.Type)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Load GitHubRepository.
	repo := &triggersv1alpha1.GitHubRepository{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.RepositoryName}, repo); err != nil {
		return nil, mapK8sError("get GitHubRepository", err)
	}

	// Load work item.
	item := &triggersv1alpha1.MaintainerWorkItem{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.WorkItemName}, item); err != nil {
		return nil, mapK8sError("get MaintainerWorkItem", err)
	}

	// Verify the work item belongs to the declared repository.
	if item.Spec.RepositoryRef.Name != req.RepositoryName {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("work item %q does not belong to repository %q", req.WorkItemName, req.RepositoryName))
	}

	// Staleness check: if the sequence has advanced since the UI last read it,
	// tell the caller to refresh rather than letting the API server reject it.
	if item.Status.ProjectionSequence != req.ExpectedProjectionSequence {
		return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("work item projection sequence changed (%d → %d); refresh the dashboard and try again", req.ExpectedProjectionSequence, item.Status.ProjectionSequence))
	}

	preconditions := triggersv1alpha1.MaintainerWorkItemCommandPreconditions{
		WorkItemName:       item.Name,
		WorkItemUID:        item.UID,
		ProjectionSequence: item.Status.ProjectionSequence,
		ResourceVersion:    item.ResourceVersion,
	}

	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	temporaryKey := idempotencyKey
	if temporaryKey == "" {
		temporaryKey = "temporary"
	}

	// Build the typed payload before deriving an omitted key because the canonical
	// payload hash intentionally excludes the key.
	spec, err := s.buildMaintainerCommandSpec(ctx, req, cmdType, repo, item, preconditions, temporaryKey, actor.Subject)
	if err != nil {
		return nil, err
	}
	spec.PayloadHash = triggersv1alpha1.MaintainerWorkItemCommandSpecPayloadHash(spec)
	if idempotencyKey == "" {
		sum := sha256.Sum256([]byte(actor.Subject + "\x00" + spec.PayloadHash + "\x00" + string(item.UID)))
		idempotencyKey = fmt.Sprintf("human-%s-%s", strings.ToLower(string(cmdType)), hex.EncodeToString(sum[:]))
	}
	spec.IdempotencyKey = idempotencyKey

	// Load the repository-scoped human command capability secret.
	secretName := triggersv1alpha1.MaintainerHumanCommandCapabilitySecretName(repo.Name)
	cap := &corev1.Secret{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: repo.Namespace, Name: secretName}, cap); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get human command capability secret %q", secretName), err)
	}
	capKey := cap.Data[triggersv1alpha1.MaintainerCommandCapabilitySecretKey]
	if string(cap.Data[triggersv1alpha1.MaintainerCommandCapabilityRepositoryNameKey]) != repo.Name ||
		string(cap.Data[triggersv1alpha1.MaintainerCommandCapabilityRepositoryUIDKey]) != string(repo.UID) ||
		len(capKey) < 32 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("human command capability secret is invalid or not yet ready"))
	}

	proof := triggersv1alpha1.MaintainerHumanCommandProof(capKey, repo.Name, repo.UID, idempotencyKey, spec.PayloadHash, actor.Subject)
	spec.HumanIssuer = &triggersv1alpha1.MaintainerWorkItemCommandHumanIssuer{
		Subject:     actor.Subject,
		DisplayName: actor.Name,
		Proof:       proof,
	}

	command := &triggersv1alpha1.MaintainerWorkItemCommand{
		ObjectMeta: metav1.ObjectMeta{
			Name:      triggersv1alpha1.MaintainerWorkItemCommandName(repo.Name, idempotencyKey),
			Namespace: repo.Namespace,
			Labels: map[string]string{
				triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey:  repo.Name,
				triggersv1alpha1.MaintainerWorkItemIssueNumberLabelKey: strconv.Itoa(int(item.Spec.IssueNumber)),
				triggersv1alpha1.MaintainerWorkItemNameLabelKey:        item.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(repo, triggersv1alpha1.GroupVersion.WithKind("GitHubRepository")),
			},
		},
		Spec: spec,
	}

	if err := s.k8sClient.Create(ctx, command); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, mapK8sError("create MaintainerWorkItemCommand", err)
		}
		existing := &triggersv1alpha1.MaintainerWorkItemCommand{}
		if err := s.k8sClient.Get(ctx, client.ObjectKeyFromObject(command), existing); err != nil {
			return nil, mapK8sError("get existing MaintainerWorkItemCommand", err)
		}
		if existing.Spec.RepositoryRef.Name != spec.RepositoryRef.Name ||
			existing.Spec.Preconditions.WorkItemUID != spec.Preconditions.WorkItemUID ||
			existing.Spec.PayloadHash != spec.PayloadHash ||
			existing.Spec.Type != spec.Type ||
			!metav1.IsControlledBy(existing, repo) ||
			existing.Spec.HumanIssuer == nil ||
			existing.Spec.HumanIssuer.Subject != actor.Subject {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("idempotency key is already used for a different maintainer command"))
		}
		command = existing
	}

	phase := string(command.Status.Phase)
	if phase == "" {
		phase = string(triggersv1alpha1.MaintainerWorkItemCommandPhasePending)
	}

	// Re-read the work item so the response reflects any changes the controller
	// may have applied between our initial load and now.
	fresh := &triggersv1alpha1.MaintainerWorkItem{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.WorkItemName}, fresh); err != nil {
		fresh = item // fall back to the version we already have
	}
	items := &triggersv1alpha1.MaintainerWorkItemList{}
	if err := s.k8sClient.List(ctx, items, client.InNamespace(req.Namespace)); err != nil {
		return nil, mapK8sError(fmt.Sprintf("list MaintainerWorkItems in %s", req.Namespace), err)
	}
	itemIndex := make(map[string]*triggersv1alpha1.MaintainerWorkItem, len(items.Items))
	for i := range items.Items {
		itemIndex[items.Items[i].Name] = &items.Items[i]
	}
	pbItem := maintainerWorkItemToProto(fresh, itemIndex)

	msg := ""
	if command.Status.Result != nil {
		msg = command.Status.Result.Message
	}

	return &platform.IssueMaintainerCommandResponse{
		CommandName: command.Name,
		Phase:       phase,
		Message:     msg,
		Item:        pbItem,
	}, nil
}

// buildMaintainerCommandSpec constructs the type-specific command spec from the
// request, applying server-side derivation for fields the browser must not set.
func (s *Server) buildMaintainerCommandSpec(
	ctx context.Context,
	req *platform.IssueMaintainerCommandRequest,
	cmdType triggersv1alpha1.MaintainerWorkItemCommandType,
	repo *triggersv1alpha1.GitHubRepository,
	item *triggersv1alpha1.MaintainerWorkItem,
	preconditions triggersv1alpha1.MaintainerWorkItemCommandPreconditions,
	idempotencyKey string,
	actorSubject string,
) (triggersv1alpha1.MaintainerWorkItemCommandSpec, error) {
	spec := triggersv1alpha1.MaintainerWorkItemCommandSpec{
		RepositoryRef:  item.Spec.RepositoryRef,
		IdempotencyKey: idempotencyKey,
		Preconditions:  preconditions,
		Type:           cmdType,
	}

	switch cmdType {
	case triggersv1alpha1.MaintainerWorkItemCommandTypeTriageIssue:
		in := req.GetTriage()
		if in == nil {
			return spec, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("triage input is required for type TriageIssue"))
		}
		disp, err := parseMaintainerDisposition(in.Disposition)
		if err != nil {
			return spec, connect.NewError(connect.CodeInvalidArgument, err)
		}
		if strings.TrimSpace(in.EvidenceSummary) == "" {
			return spec, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("triage.evidence_summary is required"))
		}
		triage := &triggersv1alpha1.MaintainerTriageCommand{
			IssueNumber:     item.Spec.IssueNumber,
			Disposition:     disp,
			EvidenceSummary: strings.TrimSpace(in.EvidenceSummary),
		}
		if in.AcceptedScope != nil {
			triage.AcceptedScope = triggersv1alpha1.MaintainerAcceptedScope{
				Statement:          in.AcceptedScope.Statement,
				AcceptanceCriteria: append([]string(nil), in.AcceptedScope.AcceptanceCriteria...),
			}
		}
		if in.CloseReason != "" {
			cr, err := parseMaintainerCloseReason(in.CloseReason)
			if err != nil {
				return spec, connect.NewError(connect.CodeInvalidArgument, err)
			}
			triage.CloseReason = &cr
		}
		spec.Triage = triage

	case triggersv1alpha1.MaintainerWorkItemCommandTypeBreakdownIssue:
		in := req.GetBreakdown()
		if in == nil {
			return spec, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("breakdown input is required for type BreakdownIssue"))
		}
		children, err := s.resolveWorkItemRefs(ctx, repo.Namespace, repo.Name, in.ChildWorkItemNames)
		if err != nil {
			return spec, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("resolving children: %w", err))
		}
		deps, err := s.resolveWorkItemRefs(ctx, repo.Namespace, repo.Name, in.DependencyWorkItemNames)
		if err != nil {
			return spec, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("resolving dependencies: %w", err))
		}
		spec.Breakdown = &triggersv1alpha1.MaintainerBreakdownCommand{
			IssueNumber:  item.Spec.IssueNumber,
			Children:     children,
			Dependencies: deps,
		}

	case triggersv1alpha1.MaintainerWorkItemCommandTypeRequestDecision:
		in := req.GetRequestDecision()
		if in == nil {
			return spec, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request_decision input is required for type RequestDecision"))
		}
		if strings.TrimSpace(in.DecisionId) == "" || strings.TrimSpace(in.Question) == "" {
			return spec, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request_decision.decision_id and question are required"))
		}
		spec.RequestDecision = &triggersv1alpha1.MaintainerRequestDecisionCommand{
			IssueNumber: item.Spec.IssueNumber,
			DecisionID:  strings.TrimSpace(in.DecisionId),
			Question:    strings.TrimSpace(in.Question),
			Options:     append([]string(nil), in.Options...),
		}

	case triggersv1alpha1.MaintainerWorkItemCommandTypeResolveDecision:
		in := req.GetResolveDecision()
		if in == nil {
			return spec, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("resolve_decision input is required for type ResolveDecision"))
		}
		if strings.TrimSpace(in.DecisionId) == "" || strings.TrimSpace(in.Answer) == "" {
			return spec, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("resolve_decision.decision_id and answer are required"))
		}
		spec.ResolveDecision = &triggersv1alpha1.MaintainerResolveDecisionCommand{
			IssueNumber: item.Spec.IssueNumber,
			DecisionID:  strings.TrimSpace(in.DecisionId),
			// Subject is always derived from the authenticated actor, never the client.
			HumanAnswer: triggersv1alpha1.MaintainerAuthenticatedHumanAnswer{
				Subject: actorSubject,
				Answer:  strings.TrimSpace(in.Answer),
			},
		}

	case triggersv1alpha1.MaintainerWorkItemCommandTypeDispatchWorkItem:
		in := req.GetDispatch()
		if in == nil {
			return spec, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("dispatch input is required for type DispatchWorkItem"))
		}
		mode := strings.TrimSpace(in.Mode)
		if mode == "" {
			// Default from repository spec; fall back to "autopilot".
			if ms := repo.Spec.Maintainer; ms != nil && ms.DispatchModeRef != "" {
				mode = ms.DispatchModeRef
			} else {
				mode = "autopilot"
			}
		}
		spec.Dispatch = &triggersv1alpha1.MaintainerDispatchWorkItemCommand{
			IssueNumber: item.Spec.IssueNumber,
			Mode:        mode,
		}

	case triggersv1alpha1.MaintainerWorkItemCommandTypeRequestMerge:
		in := req.GetRequestMerge()
		if in == nil {
			return spec, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request_merge input is required for type RequestMerge"))
		}
		if in.PullRequestNumber < 1 {
			return spec, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request_merge.pull_request_number must be ≥ 1"))
		}
		if strings.TrimSpace(in.ExpectedHeadSha) == "" {
			return spec, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request_merge.expected_head_sha is required"))
		}
		method, err := parseMaintainerMergeMethod(in.MergeMethod)
		if err != nil {
			return spec, connect.NewError(connect.CodeInvalidArgument, err)
		}
		// Derive the repository owner/repo from the GitHubRepository spec.
		ghRepo := repo.Spec.Owner + "/" + repo.Spec.Repo
		spec.RequestMerge = &triggersv1alpha1.MaintainerRequestMergeCommand{
			IssueNumber:       item.Spec.IssueNumber,
			Repository:        ghRepo,
			PullRequestNumber: in.PullRequestNumber,
			ExpectedHeadSHA:   strings.TrimSpace(in.ExpectedHeadSha),
			MergeMethod:       method,
		}

	case triggersv1alpha1.MaintainerWorkItemCommandTypeFinalizeWorkItem:
		in := req.GetFinalize()
		if in == nil {
			return spec, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("finalize input is required for type FinalizeWorkItem"))
		}
		if strings.TrimSpace(in.DeliverySummary) == "" || strings.TrimSpace(in.DeliveryEvidence) == "" {
			return spec, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("finalize.delivery_summary and delivery_evidence are required"))
		}
		if item.Spec.AcceptedScope == nil {
			return spec, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("work item has no accepted scope to attest"))
		}
		scopeHash := triggersv1alpha1.MaintainerAcceptedScopeHash(item.Spec.AcceptedScope)
		// Finalization attests the complete projected implementer set. The
		// controller deliberately rejects partial sets so no run can be omitted
		// from the terminal success transition.
		implementerSet := make(map[string]struct{}, len(item.Status.AgentRuns))
		for _, run := range item.Status.AgentRuns {
			if run.Role == triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer && strings.TrimSpace(run.Name) != "" {
				implementerSet[run.Name] = struct{}{}
			}
		}
		implementerRunNames := make([]string, 0, len(implementerSet))
		for name := range implementerSet {
			implementerRunNames = append(implementerRunNames, name)
		}
		sort.Strings(implementerRunNames)
		spec.Finalize = &triggersv1alpha1.MaintainerFinalizeWorkItemCommand{
			IssueNumber:         item.Spec.IssueNumber,
			AcceptedScopeHash:   scopeHash,
			DeliverySummary:     strings.TrimSpace(in.DeliverySummary),
			DeliveryEvidence:    strings.TrimSpace(in.DeliveryEvidence),
			ImplementerRunNames: implementerRunNames,
		}
	}

	return spec, nil
}

// resolveWorkItemRefs loads each work item by name and returns its reference
// (name + UID). Returns an error when any name is missing, duplicated, or belongs to another repository.
func (s *Server) resolveWorkItemRefs(ctx context.Context, namespace, repositoryName string, names []string) ([]triggersv1alpha1.MaintainerWorkItemReference, error) {
	refs := make([]triggersv1alpha1.MaintainerWorkItemReference, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return nil, fmt.Errorf("work item names must be non-empty and unique")
		}
		seen[name] = true
		wi := &triggersv1alpha1.MaintainerWorkItem{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, wi); err != nil {
			return nil, fmt.Errorf("work item %q: %w", name, err)
		}
		if wi.Spec.RepositoryRef.Name != repositoryName {
			return nil, fmt.Errorf("work item %q does not belong to repository %q", name, repositoryName)
		}
		refs = append(refs, triggersv1alpha1.MaintainerWorkItemReference{Name: wi.Name, UID: wi.UID})
	}
	return refs, nil
}

// parseMaintainerCommandType returns the typed constant or an error.
func parseMaintainerCommandType(s string) (triggersv1alpha1.MaintainerWorkItemCommandType, error) {
	switch triggersv1alpha1.MaintainerWorkItemCommandType(s) {
	case triggersv1alpha1.MaintainerWorkItemCommandTypeTriageIssue,
		triggersv1alpha1.MaintainerWorkItemCommandTypeBreakdownIssue,
		triggersv1alpha1.MaintainerWorkItemCommandTypeRequestDecision,
		triggersv1alpha1.MaintainerWorkItemCommandTypeResolveDecision,
		triggersv1alpha1.MaintainerWorkItemCommandTypeDispatchWorkItem,
		triggersv1alpha1.MaintainerWorkItemCommandTypeRequestMerge,
		triggersv1alpha1.MaintainerWorkItemCommandTypeFinalizeWorkItem:
		return triggersv1alpha1.MaintainerWorkItemCommandType(s), nil
	default:
		return "", fmt.Errorf("unknown command type %q; valid types: TriageIssue, BreakdownIssue, RequestDecision, ResolveDecision, DispatchWorkItem, RequestMerge, FinalizeWorkItem", s)
	}
}

// parseMaintainerDisposition validates and returns the disposition constant.
func parseMaintainerDisposition(s string) (triggersv1alpha1.MaintainerWorkItemDisposition, error) {
	switch triggersv1alpha1.MaintainerWorkItemDisposition(s) {
	case triggersv1alpha1.MaintainerWorkItemDispositionNotActionable,
		triggersv1alpha1.MaintainerWorkItemDispositionBounded,
		triggersv1alpha1.MaintainerWorkItemDispositionDecomposable,
		triggersv1alpha1.MaintainerWorkItemDispositionDiscovery,
		triggersv1alpha1.MaintainerWorkItemDispositionEscalated:
		return triggersv1alpha1.MaintainerWorkItemDisposition(s), nil
	default:
		return "", fmt.Errorf("unknown disposition %q; valid: NotActionable, Bounded, Decomposable, Discovery, Escalated", s)
	}
}

// parseMaintainerCloseReason validates and returns the close reason constant.
func parseMaintainerCloseReason(s string) (triggersv1alpha1.MaintainerWorkItemCloseReason, error) {
	switch triggersv1alpha1.MaintainerWorkItemCloseReason(s) {
	case triggersv1alpha1.MaintainerWorkItemCloseReasonNotPlanned,
		triggersv1alpha1.MaintainerWorkItemCloseReasonCompleted:
		return triggersv1alpha1.MaintainerWorkItemCloseReason(s), nil
	default:
		return "", fmt.Errorf("unknown close_reason %q; valid: not_planned, completed", s)
	}
}

// parseMaintainerMergeMethod validates and returns the merge method constant.
func parseMaintainerMergeMethod(s string) (triggersv1alpha1.MaintainerWorkItemMergeMethod, error) {
	switch triggersv1alpha1.MaintainerWorkItemMergeMethod(s) {
	case triggersv1alpha1.MaintainerWorkItemMergeMethodSquash,
		triggersv1alpha1.MaintainerWorkItemMergeMethodMerge,
		triggersv1alpha1.MaintainerWorkItemMergeMethodRebase:
		return triggersv1alpha1.MaintainerWorkItemMergeMethod(s), nil
	default:
		return "", fmt.Errorf("unknown merge_method %q; valid: squash, merge, rebase", s)
	}
}
