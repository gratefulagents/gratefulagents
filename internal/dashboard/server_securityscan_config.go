package dashboard

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const securityScanResourceType = "securityscan"

var securityScanTaskNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ListSecurityScanConfigs returns the configured SecurityScan CRs (the
// triggers that create scan runs), optionally filtered by namespace. This is
// distinct from ListSecurityScans, which returns persisted scan results.
func (s *Server) ListSecurityScanConfigs(
	ctx context.Context, req *platform.ListSecurityScanConfigsRequest,
) (*platform.ListSecurityScanConfigsResponse, error) {
	scans := &triggersv1alpha1.SecurityScanList{}
	var opts []client.ListOption
	if req.GetNamespace() != "" {
		opts = append(opts, client.InNamespace(req.GetNamespace()))
	}
	if err := s.k8sClient.List(ctx, scans, opts...); err != nil {
		return nil, mapK8sError("list SecurityScans", err)
	}
	resp := &platform.ListSecurityScanConfigsResponse{}
	visible := s.resourceVisibilityFilter(ctx, securityScanResourceType, false)
	for i := range scans.Items {
		cr := &scans.Items[i]
		if !visible(cr.Namespace, cr.Name) {
			continue
		}
		pb := securityScanConfigProto(cr)
		pb.Owner, pb.MyPermission = s.resourceACL(ctx, securityScanResourceType, cr.Name, cr.Namespace)
		resp.Configs = append(resp.Configs, pb)
	}
	return resp, nil
}

// GetSecurityScanConfig returns a single configured SecurityScan CR.
func (s *Server) GetSecurityScanConfig(
	ctx context.Context, req *platform.GetSecurityScanConfigRequest,
) (*platform.SecurityScanConfig, error) {
	err := s.requireResourceAccess(
		ctx, securityScanResourceType, req.GetName(), req.GetNamespace(), AccessViewer, "view this security scan")
	if err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityScan{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.GetNamespace(), Name: req.GetName()}, cr); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get SecurityScan %s/%s", req.GetNamespace(), req.GetName()), err)
	}
	pb := securityScanConfigProto(cr)
	pb.Owner, pb.MyPermission = s.resourceACL(ctx, securityScanResourceType, cr.Name, cr.Namespace)
	return pb, nil
}

// CreateSecurityScan creates a SecurityScan trigger in the caller's personal
// namespace (or an explicitly requested namespace for admins).
func (s *Server) CreateSecurityScan(
	ctx context.Context, req *platform.CreateSecurityScanRequest,
) (*platform.SecurityScanConfig, error) {
	actor := requestActorFromContext(ctx)
	namespace, err := s.authorizeSecurityScanWriteNamespace(ctx, actor, req.GetNamespace())
	if err != nil {
		return nil, err
	}

	spec, provider, authMode, err := securityScanSpecFromRequest(req.GetSpec())
	if err != nil {
		return nil, err
	}
	if req.GetUseSavedCredentials() {
		secrets := triggersv1alpha1.AgentRunSecrets{}
		if err := s.applyProjectSavedCredentials(ctx, namespace, provider, authMode, &secrets); err != nil {
			return nil, err
		}
		spec.Defaults.Secrets = secrets
	}

	name := sanitizeDNSLabel(req.GetName())
	if name == "" {
		name = generateSecurityScanName()
	}
	if len(name) > maxDNSLabelLen {
		name = strings.Trim(name[:maxDNSLabelLen], "-")
	}

	policyCleanup, err := s.applyTriggerPolicies(ctx, namespace, name, req.GetPolicies(), &spec.Defaults)
	if err != nil {
		return nil, err
	}
	rollbackPolicies := func() {
		for _, fn := range policyCleanup {
			fn()
		}
	}

	cr := &triggersv1alpha1.SecurityScan{
		TypeMeta: metav1.TypeMeta{
			APIVersion: triggersv1alpha1.GroupVersion.String(),
			Kind:       "SecurityScan",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: *spec,
	}
	if err := s.k8sClient.Create(ctx, cr); err != nil {
		rollbackPolicies()
		if k8serrors.IsAlreadyExists(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("SecurityScan %s/%s already exists", namespace, name))
		}
		return nil, mapK8sError("create SecurityScan", err)
	}

	// Record resource ownership. This must succeed: an unowned scan is treated
	// as system-created and becomes visible to every authenticated user, so a
	// silently dropped ownership record would leak the scan's configuration.
	if s.stateStore != nil && actor.Subject != "" {
		if err := s.stateStore.SetResourceOwner(ctx, securityScanResourceType, cr.Name, cr.Namespace, actor.Subject); err != nil {
			_ = s.k8sClient.Delete(ctx, cr)
			rollbackPolicies()
			return nil, connect.NewError(connect.CodeInternal,
				fmt.Errorf("record ownership for SecurityScan %s/%s: %w", cr.Namespace, cr.Name, err))
		}
	}

	return securityScanConfigProto(cr), nil
}

// UpdateSecurityScan replaces the spec of an existing SecurityScan from the
// request.
func (s *Server) UpdateSecurityScan(
	ctx context.Context, req *platform.UpdateSecurityScanRequest,
) (*platform.SecurityScanConfig, error) {
	namespace := strings.TrimSpace(req.GetNamespace())
	name := strings.TrimSpace(req.GetName())
	if namespace == "" || name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace and name are required"))
	}
	err := s.requireResourceAccess(
		ctx, securityScanResourceType, name, namespace, AccessCollaborator, "update this security scan")
	if err != nil {
		return nil, err
	}

	existing := &triggersv1alpha1.SecurityScan{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, existing); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get SecurityScan %s/%s", namespace, name), err)
	}

	spec, provider, authMode, err := securityScanSpecFromRequest(req.GetSpec())
	if err != nil {
		return nil, err
	}
	if req.GetUseSavedCredentials() {
		secrets := triggersv1alpha1.AgentRunSecrets{}
		if err := s.applyProjectSavedCredentials(ctx, namespace, provider, authMode, &secrets); err != nil {
			return nil, err
		}
		spec.Defaults.Secrets = secrets
	}

	policyCleanup, err := s.applyTriggerPolicies(ctx, namespace, name, req.GetPolicies(), &spec.Defaults)
	if err != nil {
		return nil, err
	}

	preserveAdminOnlyTriggerDefaults(&spec.Defaults, existing.Spec.Defaults)
	existing.Spec = *spec
	if err := s.k8sClient.Update(ctx, existing); err != nil {
		for _, fn := range policyCleanup {
			fn()
		}
		return nil, mapK8sError("update SecurityScan", err)
	}
	return securityScanConfigProto(existing), nil
}

// RunSecurityScanNow stamps a run-now annotation token on a SecurityScan so
// the controller creates an immediate AgentRun without a spec edit. The token
// is opaque and unique per request; the controller records consumed tokens in
// status.lastManualRunToken, so retried or concurrent duplicate requests never
// create two runs, and concurrencyPolicy Forbid surfaces ConcurrencyBlocked
// on the scan status instead of double-running.
func (s *Server) RunSecurityScanNow(
	ctx context.Context, req *platform.RunSecurityScanNowRequest,
) (*platform.SecurityScanConfig, error) {
	namespace := strings.TrimSpace(req.GetNamespace())
	name := strings.TrimSpace(req.GetName())
	if namespace == "" || name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace and name are required"))
	}
	err := s.requireResourceAccess(
		ctx, securityScanResourceType, name, namespace, AccessCollaborator, "run this security scan")
	if err != nil {
		return nil, err
	}

	token := time.Now().UTC().Format(time.RFC3339Nano)
	var updated *triggersv1alpha1.SecurityScan
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cr := &triggersv1alpha1.SecurityScan{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, cr); err != nil {
			return err
		}
		if cr.Spec.Suspend {
			return connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("security scan %s/%s is suspended; resume it before requesting a run", namespace, name))
		}
		if cr.Annotations == nil {
			cr.Annotations = map[string]string{}
		}
		cr.Annotations[triggersv1alpha1.SecurityScanRunNowAnnotation] = token
		if err := s.k8sClient.Update(ctx, cr); err != nil {
			return err
		}
		updated = cr
		return nil
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, mapK8sError(fmt.Sprintf("run SecurityScan %s/%s now", namespace, name), err)
	}
	pb := securityScanConfigProto(updated)
	pb.Owner, pb.MyPermission = s.resourceACL(ctx, securityScanResourceType, updated.Name, updated.Namespace)
	return pb, nil
}

// DeleteSecurityScan deletes a SecurityScan trigger.
func (s *Server) DeleteSecurityScan(ctx context.Context, req *platform.DeleteSecurityScanRequest) (*emptypb.Empty, error) {
	namespace := strings.TrimSpace(req.GetNamespace())
	name := strings.TrimSpace(req.GetName())
	if namespace == "" || name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace and name are required"))
	}
	err := s.requireResourceAccess(
		ctx, securityScanResourceType, name, namespace, AccessCollaborator, "delete this security scan")
	if err != nil {
		return nil, err
	}

	cr := &triggersv1alpha1.SecurityScan{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, cr); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get SecurityScan %s/%s", namespace, name), err)
	}
	if err := s.k8sClient.Delete(ctx, cr); err != nil && !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("delete SecurityScan", err)
	}
	return &emptypb.Empty{}, nil
}

// authorizeSecurityScanWriteNamespace resolves the namespace a scan write
// targets: the caller's personal namespace by default; another namespace only
// for admins.
func (s *Server) authorizeSecurityScanWriteNamespace(
	ctx context.Context, actor requestActor, requested string,
) (string, error) {
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return "", err
	}
	if reqNS := strings.TrimSpace(requested); reqNS != "" && reqNS != namespace {
		if actor.Role != "admin" && actor.Role != "owner" {
			return "", connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("you do not have permission to manage security scans in namespace %q", reqNS))
		}
		namespace = reqNS
	}
	return namespace, nil
}

// generateSecurityScanName derives a DNS-1123 name for an unnamed scan.
func generateSecurityScanName() string {
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	if len(suffix) > 6 {
		suffix = suffix[len(suffix)-6:]
	}
	return "securityscan-" + suffix
}

// securityScanBudgetsFromProto converts and validates a budgets block shared
// by the SecurityScan and SecurityPolicyPack conversions. A nil/empty proto
// yields nil (no budgets).
func securityScanBudgetsFromProto(pb *platform.SecurityScanBudgetsConfig) (*triggersv1alpha1.SecurityScanBudgets, error) {
	if pb == nil {
		return nil, nil
	}
	budgets := &triggersv1alpha1.SecurityScanBudgets{
		MaxModelJobs:      pb.GetMaxModelJobs(),
		MaxCostUSD:        strings.TrimSpace(pb.GetMaxCostUsd()),
		MaxTokens:         pb.GetMaxTokens(),
		MaxFindings:       pb.GetMaxFindings(),
		MaxValidationJobs: pb.GetMaxValidationJobs(),
	}
	if value := strings.TrimSpace(pb.GetMaxRuntime()); value != "" {
		d, err := time.ParseDuration(value)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("invalid budgets.max_runtime %q: %w", value, err))
		}
		budgets.MaxRuntime = metav1.Duration{Duration: d}
	}
	if errs := triggersv1alpha1.ValidateSecurityScanBudgets("budgets", budgets); len(errs) != 0 {
		return nil, securityLibraryInvalidArgument(errs)
	}
	if budgets.IsZero() {
		return nil, nil
	}
	return budgets, nil
}

// securityScanBudgetsToProto converts a budgets block for the SecurityScan
// and SecurityPolicyPack protos. Nil/empty budgets yield nil.
func securityScanBudgetsToProto(b *triggersv1alpha1.SecurityScanBudgets) *platform.SecurityScanBudgetsConfig {
	if b == nil || b.IsZero() {
		return nil
	}
	pb := &platform.SecurityScanBudgetsConfig{
		MaxModelJobs:      b.MaxModelJobs,
		MaxCostUsd:        b.MaxCostUSD,
		MaxTokens:         b.MaxTokens,
		MaxFindings:       b.MaxFindings,
		MaxValidationJobs: b.MaxValidationJobs,
	}
	if b.MaxRuntime.Duration != 0 {
		pb.MaxRuntime = b.MaxRuntime.Duration.String()
	}
	return pb
}

// securityScanSpecFromRequest validates the shared create/update spec and
// builds the SecurityScanSpec, also returning the resolved provider and auth
// mode so callers can wire saved credentials.
func securityScanSpecFromRequest(
	pb *platform.SecurityScanConfigSpec,
) (*triggersv1alpha1.SecurityScanSpec, string, platformv1alpha1.AgentRunAuthMode, error) {
	if pb == nil {
		pb = &platform.SecurityScanConfigSpec{}
	}
	repoURL := strings.TrimSpace(pb.GetRepoUrl())
	if repoURL == "" {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("repo_url is required"))
	}
	if err := validateSecurityScanSchedule(pb.GetSchedule(), pb.GetTimeZone()); err != nil {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	policy, err := securityScanConcurrencyPolicy(pb.GetConcurrencyPolicy())
	if err != nil {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := validateSecuritySeverity("min_severity", pb.GetMinSeverity()); err != nil {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := validateSecuritySeverity("fail_on_severity", pb.GetFailOnSeverity()); err != nil {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	if p := pb.GetParallelism(); p != 0 && (p < 1 || p > 16) {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("parallelism %d out of range (want 0 for default, or 1-16)", p))
	}
	workflow, err := securityScanWorkflowFromProto(pb.GetWorkflow())
	if err != nil {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	workflowRef, err := securityResourceRefFromProto("workflow_ref", pb.GetWorkflowRef())
	if err != nil {
		return nil, "", "", err
	}
	if workflowRef != nil && len(workflow) > 0 {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("workflow_ref and an inline workflow are mutually exclusive: reference a SecurityWorkflow or define the workflow inline, not both"))
	}
	rankerRefs, err := securityResourceRefsFromProto("ranker_refs", pb.GetRankerRefs())
	if err != nil {
		return nil, "", "", err
	}
	postScriptRefs, err := securityResourceRefsFromProto("post_script_refs", pb.GetPostScriptRefs())
	if err != nil {
		return nil, "", "", err
	}
	policyPackRef, err := securityResourceRefFromProto("policy_pack_ref", pb.GetPolicyPackRef())
	if err != nil {
		return nil, "", "", err
	}
	rankers, err := securityScanRankersFromProto(pb.GetSeverityRankers())
	if err != nil {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	postScripts, err := securityScanPostScriptsFromProto(pb.GetPostScripts())
	if err != nil {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	dedupe, err := securityScanDedupeFromProto(pb.GetDedupe())
	if err != nil {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	budgets, err := securityScanBudgetsFromProto(pb.GetBudgets())
	if err != nil {
		return nil, "", "", err
	}
	var maxRuntime metav1.Duration
	if value := strings.TrimSpace(pb.GetMaxRuntime()); value != "" {
		d, err := time.ParseDuration(value)
		if err != nil {
			return nil, "", "", connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("invalid max_runtime %q: %w", value, err))
		}
		maxRuntime = metav1.Duration{Duration: d}
	}
	defaults, provider, authMode, err := protoDefaultsToCRD(pb.GetDefaults())
	if err != nil {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	triggers, err := securityScanTriggersFromProto(pb.GetTriggers())
	if err != nil {
		return nil, "", "", err
	}
	checks, err := securityScanChecksFromProto(pb.GetChecks(), triggers)
	if err != nil {
		return nil, "", "", err
	}
	notifications, err := securityScanNotificationsFromProto(pb.GetNotifications())
	if err != nil {
		return nil, "", "", err
	}

	spec := &triggersv1alpha1.SecurityScanSpec{
		RepoURL:           repoURL,
		BaseBranch:        strings.TrimSpace(pb.GetBaseBranch()),
		Revision:          strings.TrimSpace(pb.GetRevision()),
		AdditionalRepos:   trimmedNonEmpty(pb.GetAdditionalRepos()),
		Scope:             securityScanScopeFromProto(pb.GetScope()),
		Workflow:          workflow,
		WorkflowRef:       workflowRef,
		Parallelism:       pb.GetParallelism(),
		SeverityRankers:   rankers,
		RankerRefs:        rankerRefs,
		PostScripts:       postScripts,
		PostScriptRefs:    postScriptRefs,
		PolicyPackRef:     policyPackRef,
		Dedupe:            dedupe,
		MinSeverity:       strings.TrimSpace(pb.GetMinSeverity()),
		FailOnSeverity:    strings.TrimSpace(pb.GetFailOnSeverity()),
		Schedule:          strings.TrimSpace(pb.GetSchedule()),
		TimeZone:          strings.TrimSpace(pb.GetTimeZone()),
		Suspend:           pb.GetSuspend(),
		ConcurrencyPolicy: policy,
		Defaults:          defaults,
		MaxRuntime:        maxRuntime,
		Budgets:           budgets,
		Triggers:          triggers,
		Checks:            checks,
		Notifications:     notifications,
	}
	return spec, provider, authMode, nil
}

// validateSecurityScanSchedule accepts an empty schedule (run-once scans) and
// otherwise applies the same parser the Cron dashboard path uses.
func validateSecurityScanSchedule(schedule, timeZone string) error {
	if strings.TrimSpace(schedule) == "" {
		if zone := strings.TrimSpace(timeZone); zone != "" {
			if _, err := time.LoadLocation(zone); err != nil {
				return fmt.Errorf("invalid time_zone %q: %w", zone, err)
			}
		}
		return nil
	}
	return validateCronSchedule(schedule, timeZone)
}

func securityScanConcurrencyPolicy(value string) (triggersv1alpha1.SecurityScanConcurrencyPolicy, error) {
	switch strings.TrimSpace(value) {
	case "":
		return "", nil
	case string(triggersv1alpha1.SecurityScanConcurrencyAllow):
		return triggersv1alpha1.SecurityScanConcurrencyAllow, nil
	case string(triggersv1alpha1.SecurityScanConcurrencyForbid):
		return triggersv1alpha1.SecurityScanConcurrencyForbid, nil
	default:
		return "", fmt.Errorf("invalid concurrency_policy %q (want Allow or Forbid)", value)
	}
}

func validateSecuritySeverity(field, value string) error {
	switch strings.TrimSpace(value) {
	case "", "critical", "high", "medium", "low", "info":
		return nil
	default:
		return fmt.Errorf("invalid %s %q (want critical, high, medium, low, or info)", field, value)
	}
}

// securityScanWorkflowFromProto validates that task names are unique DNS
// labels and every depends_on entry names another task in the workflow.
func securityScanWorkflowFromProto(pbTasks []*platform.SecurityScanTaskConfig) ([]triggersv1alpha1.SecurityScanTask, error) {
	if len(pbTasks) == 0 {
		return nil, nil
	}
	names := make(map[string]bool, len(pbTasks))
	tasks := make([]triggersv1alpha1.SecurityScanTask, 0, len(pbTasks))
	for _, t := range pbTasks {
		name := strings.TrimSpace(t.GetName())
		if name == "" || len(name) > maxDNSLabelLen || !securityScanTaskNamePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid workflow task name %q (want a DNS-1123 label)", t.GetName())
		}
		if names[name] {
			return nil, fmt.Errorf("duplicate workflow task name %q", name)
		}
		names[name] = true
		if strings.TrimSpace(t.GetObjective()) == "" {
			return nil, fmt.Errorf("workflow task %q needs an objective", name)
		}
		if t.GetMaxFindings() < 0 {
			return nil, fmt.Errorf("workflow task %q max_findings must not be negative", name)
		}
		tasks = append(tasks, triggersv1alpha1.SecurityScanTask{
			Name:        name,
			Objective:   t.GetObjective(),
			Category:    strings.TrimSpace(t.GetCategory()),
			DependsOn:   trimmedNonEmpty(t.GetDependsOn()),
			Role:        strings.TrimSpace(t.GetRole()),
			Model:       strings.TrimSpace(t.GetModel()),
			MaxFindings: t.GetMaxFindings(),
		})
	}
	for _, task := range tasks {
		for _, dep := range task.DependsOn {
			if !names[dep] {
				return nil, fmt.Errorf("workflow task %q depends on unknown task %q", task.Name, dep)
			}
			if dep == task.Name {
				return nil, fmt.Errorf("workflow task %q cannot depend on itself", task.Name)
			}
		}
	}
	return tasks, nil
}

// securityResourceRefFromProto validates an optional single library resource
// reference name.
func securityResourceRefFromProto(field, name string) (*triggersv1alpha1.SecurityResourceRef, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	if err := validateResourceName(name); err != nil {
		return nil, invalidArgument("invalid %s %q", field, name)
	}
	return &triggersv1alpha1.SecurityResourceRef{Name: name}, nil
}

// securityResourceRefsFromProto validates a list of library resource
// reference names, rejecting duplicates.
func securityResourceRefsFromProto(field string, names []string) ([]triggersv1alpha1.SecurityResourceRef, error) {
	out := make([]triggersv1alpha1.SecurityResourceRef, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if err := validateResourceName(name); err != nil {
			return nil, invalidArgument("invalid %s entry %q", field, name)
		}
		if seen[name] {
			return nil, invalidArgument("duplicate %s entry %q", field, name)
		}
		seen[name] = true
		out = append(out, triggersv1alpha1.SecurityResourceRef{Name: name})
	}
	return out, nil
}

func securityScanRankersFromProto(pbRankers []*platform.SecurityRankerConfig) ([]triggersv1alpha1.SecurityScanRanker, error) {
	out := make([]triggersv1alpha1.SecurityScanRanker, 0, len(pbRankers))
	for _, r := range pbRankers {
		if strings.TrimSpace(r.GetName()) == "" || strings.TrimSpace(r.GetRules()) == "" {
			return nil, fmt.Errorf("severity rankers need both a name and rules")
		}
		out = append(out, triggersv1alpha1.SecurityScanRanker{
			Name:  strings.TrimSpace(r.GetName()),
			Rules: r.GetRules(),
		})
	}
	return out, nil
}

func securityScanPostScriptsFromProto(pbScripts []*platform.SecurityPostScriptConfig) ([]triggersv1alpha1.SecurityScanPostScript, error) {
	out := make([]triggersv1alpha1.SecurityScanPostScript, 0, len(pbScripts))
	for _, p := range pbScripts {
		if strings.TrimSpace(p.GetName()) == "" || strings.TrimSpace(p.GetPrompt()) == "" {
			return nil, fmt.Errorf("post-scripts need both a name and a prompt")
		}
		switch strings.TrimSpace(p.GetRunOn()) {
		case "", "all", "confirmed", "high-and-above":
		default:
			return nil, fmt.Errorf("invalid post-script run_on %q (want all, confirmed, or high-and-above)", p.GetRunOn())
		}
		out = append(out, triggersv1alpha1.SecurityScanPostScript{
			Name:   strings.TrimSpace(p.GetName()),
			Prompt: p.GetPrompt(),
			RunOn:  strings.TrimSpace(p.GetRunOn()),
		})
	}
	return out, nil
}

func securityScanDedupeFromProto(pb *platform.SecurityScanDedupeConfig) (*triggersv1alpha1.SecurityScanDedupe, error) {
	if pb == nil {
		return nil, nil
	}
	permille := pb.GetSimilarityThresholdPermille()
	if permille < 0 || permille > 1000 {
		return nil, fmt.Errorf("dedupe similarity_threshold_permille %d out of range (want 0-1000)", permille)
	}
	enabled := pb.GetEnabled()
	return &triggersv1alpha1.SecurityScanDedupe{
		Enabled:                     &enabled,
		SimilarityThresholdPermille: permille,
	}, nil
}

func securityScanScopeFromProto(pb *platform.SecurityScanScopeConfig) *triggersv1alpha1.SecurityScanScope {
	if pb == nil {
		return nil
	}
	scope := &triggersv1alpha1.SecurityScanScope{
		Focus:        strings.TrimSpace(pb.GetFocus()),
		IncludePaths: trimmedNonEmpty(pb.GetIncludePaths()),
		ExcludePaths: trimmedNonEmpty(pb.GetExcludePaths()),
		Languages:    trimmedNonEmpty(pb.GetLanguages()),
	}
	if scope.Focus == "" && scope.IncludePaths == nil && scope.ExcludePaths == nil && scope.Languages == nil {
		return nil
	}
	return scope
}

func trimmedNonEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func securityScanConfigProto(cr *triggersv1alpha1.SecurityScan) *platform.SecurityScanConfig {
	pb := &platform.SecurityScanConfig{
		Namespace:     cr.Namespace,
		Name:          cr.Name,
		Spec:          securityScanSpecToProto(&cr.Spec),
		Phase:         cr.Status.Phase,
		LastRunName:   cr.Status.LastRunName,
		RunsCreated:   cr.Status.RunsCreated,
		LastError:     cr.Status.LastError,
		CreatedAtUnix: cr.CreationTimestamp.Unix(),
	}
	if cr.Status.LastScanTime != nil {
		pb.LastScanTimeUnix = cr.Status.LastScanTime.Unix()
	}
	if cr.Status.NextScheduleTime != nil {
		pb.NextScheduleTimeUnix = cr.Status.NextScheduleTime.Unix()
	}
	if f := cr.Status.Findings; f != nil {
		pb.FindingCounts = map[string]int32{
			"total": f.Total, "open": f.Open, "critical": f.Critical,
			"high": f.High, "medium": f.Medium, "low": f.Low, "info": f.Info,
		}
	}
	for _, c := range cr.Status.Conditions {
		if c.Type == triggersv1alpha1.ConditionSecurityScanReady {
			pb.ConditionReady = string(c.Status)
			break
		}
	}
	if pb.ConditionReady == "" {
		pb.ConditionReady = string(metav1.ConditionUnknown)
	}
	pb.LastEventRevision = cr.Status.LastEventRevision
	pb.EventRunsCreated = cr.Status.EventRunsCreated
	if budget := cr.Status.Budget; budget != nil {
		pb.EffectiveBudgets = securityScanBudgetsToProto(budget.Effective)
		pb.BudgetExceeded = budget.Exceeded
		pb.BudgetMessage = budget.Message
	}
	if retention := cr.Status.Retention; retention != nil {
		pb.Retention = &platform.SecurityScanRetentionState{
			ScansPurged:       retention.ScansPurged,
			FindingsPurged:    retention.FindingsPurged,
			ReportsPurged:     retention.ReportsPurged,
			EvidenceRedacted:  retention.EvidenceRedacted,
			PocRedacted:       retention.PoCRedacted,
			AuditEventsPurged: retention.AuditEventsPurged,
			MoreWork:          retention.MoreWork,
			LastError:         retention.LastError,
		}
		if retention.LastSweepTime != nil {
			pb.Retention.LastSweepTimeUnix = retention.LastSweepTime.Unix()
		}
	}
	if check := cr.Status.LastCheck; check != nil {
		pb.LastCheck = &platform.SecurityScanCheckState{
			RunName:       check.RunName,
			Revision:      check.Revision,
			Conclusion:    check.Conclusion,
			Url:           check.URL,
			Error:         check.Error,
			SarifUploaded: check.SARIFUploaded,
			SarifError:    check.SARIFError,
		}
		if check.PublishedAt != nil {
			pb.LastCheck.PublishedAtUnix = check.PublishedAt.Unix()
		}
	}
	if notif := cr.Status.LastNotifications; notif != nil {
		pb.LastNotifications = &platform.SecurityScanNotificationState{
			LastRunName: notif.LastRunName,
			Sent:        notif.Sent,
			Suppressed:  notif.Suppressed,
			LastError:   notif.LastError,
		}
		if notif.LastNotifiedAt != nil {
			pb.LastNotifications.LastNotifiedAtUnix = notif.LastNotifiedAt.Unix()
		}
	}
	return pb
}

func securityScanSpecToProto(spec *triggersv1alpha1.SecurityScanSpec) *platform.SecurityScanConfigSpec {
	pb := &platform.SecurityScanConfigSpec{
		RepoUrl:           spec.RepoURL,
		BaseBranch:        spec.BaseBranch,
		Revision:          spec.Revision,
		AdditionalRepos:   append([]string(nil), spec.AdditionalRepos...),
		Parallelism:       spec.Parallelism,
		MinSeverity:       spec.MinSeverity,
		FailOnSeverity:    spec.FailOnSeverity,
		Schedule:          spec.Schedule,
		TimeZone:          spec.TimeZone,
		Suspend:           spec.Suspend,
		ConcurrencyPolicy: string(spec.ConcurrencyPolicy),
		Defaults:          crdDefaultsToProto(spec.Defaults),
	}
	if spec.MaxRuntime.Duration != 0 {
		pb.MaxRuntime = spec.MaxRuntime.Duration.String()
	}
	pb.Budgets = securityScanBudgetsToProto(spec.Budgets)
	if spec.WorkflowRef != nil {
		pb.WorkflowRef = spec.WorkflowRef.Name
	}
	for _, ref := range spec.RankerRefs {
		pb.RankerRefs = append(pb.RankerRefs, ref.Name)
	}
	for _, ref := range spec.PostScriptRefs {
		pb.PostScriptRefs = append(pb.PostScriptRefs, ref.Name)
	}
	if spec.PolicyPackRef != nil {
		pb.PolicyPackRef = spec.PolicyPackRef.Name
	}
	if scope := spec.Scope; scope != nil {
		pb.Scope = &platform.SecurityScanScopeConfig{
			Focus:        scope.Focus,
			IncludePaths: append([]string(nil), scope.IncludePaths...),
			ExcludePaths: append([]string(nil), scope.ExcludePaths...),
			Languages:    append([]string(nil), scope.Languages...),
		}
	}
	for _, t := range spec.Workflow {
		pb.Workflow = append(pb.Workflow, &platform.SecurityScanTaskConfig{
			Name:        t.Name,
			Objective:   t.Objective,
			Category:    t.Category,
			DependsOn:   append([]string(nil), t.DependsOn...),
			Role:        t.Role,
			Model:       t.Model,
			MaxFindings: t.MaxFindings,
		})
	}
	for _, r := range spec.SeverityRankers {
		pb.SeverityRankers = append(pb.SeverityRankers, &platform.SecurityRankerConfig{Name: r.Name, Rules: r.Rules})
	}
	for _, p := range spec.PostScripts {
		pb.PostScripts = append(pb.PostScripts, &platform.SecurityPostScriptConfig{
			Name: p.Name, Prompt: p.Prompt, RunOn: p.RunOn,
		})
	}
	if d := spec.Dedupe; d != nil {
		pb.Dedupe = &platform.SecurityScanDedupeConfig{
			Enabled:                     d.Enabled == nil || *d.Enabled,
			SimilarityThresholdPermille: d.SimilarityThresholdPermille,
		}
	}
	if t := spec.Triggers; t != nil {
		pb.Triggers = &platform.SecurityScanTriggersConfig{
			OnPullRequest: t.OnPullRequest,
			OnPush:        t.OnPush,
			Branches:      append([]string(nil), t.Branches...),
			DiffScope:     t.DiffScope,
			AllowForks:    t.AllowForks,
		}
		if t.RepositoryRef != nil {
			pb.Triggers.RepositoryRef = t.RepositoryRef.Name
		}
	}
	if c := spec.Checks; c != nil {
		pb.Checks = &platform.SecurityScanChecksConfig{
			Enabled:                 c.Enabled,
			IncludeFindingSummaries: c.IncludeFindingSummaries,
			UploadSarif:             c.UploadSARIF,
		}
	}
	for _, rule := range spec.Notifications {
		pbRule := &platform.SecurityScanNotificationRuleConfig{
			Name:        rule.Name,
			MinSeverity: rule.MinSeverity,
			NotifyOn:    rule.NotifyOn,
		}
		if rule.Slack != nil {
			pbRule.SlackWebhookSecretRef = rule.Slack.WebhookSecretRef
		}
		if rule.GitHubIssues != nil {
			pbRule.GithubIssues = true
			if rule.GitHubIssues.RepositoryRef != nil {
				pbRule.GithubRepositoryRef = rule.GitHubIssues.RepositoryRef.Name
			}
		}
		if rule.Linear != nil {
			pbRule.LinearApiKeySecretRef = rule.Linear.APIKeySecretRef
			pbRule.LinearTeamId = rule.Linear.TeamID
		}
		pb.Notifications = append(pb.Notifications, pbRule)
	}
	return pb
}

// securityScanTriggersFromProto validates and converts the triggers config.
func securityScanTriggersFromProto(pb *platform.SecurityScanTriggersConfig) (*triggersv1alpha1.SecurityScanTriggers, error) {
	if pb == nil {
		return nil, nil
	}
	repoRef := strings.TrimSpace(pb.GetRepositoryRef())
	if (pb.GetOnPullRequest() || pb.GetOnPush()) && repoRef == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("triggers.repository_ref is required when on_pull_request or on_push is set"))
	}
	t := &triggersv1alpha1.SecurityScanTriggers{
		OnPullRequest: pb.GetOnPullRequest(),
		OnPush:        pb.GetOnPush(),
		Branches:      trimmedNonEmpty(pb.GetBranches()),
		DiffScope:     pb.GetDiffScope(),
		AllowForks:    pb.GetAllowForks(),
	}
	if repoRef != "" {
		t.RepositoryRef = &triggersv1alpha1.SecurityResourceRef{Name: repoRef}
	}
	if !t.OnPullRequest && !t.OnPush && !t.DiffScope && !t.AllowForks && t.RepositoryRef == nil && len(t.Branches) == 0 {
		return nil, nil
	}
	return t, nil
}

// securityScanChecksFromProto converts the checks config.
func securityScanChecksFromProto(pb *platform.SecurityScanChecksConfig, triggers *triggersv1alpha1.SecurityScanTriggers) (*triggersv1alpha1.SecurityScanChecks, error) {
	if pb == nil {
		return nil, nil
	}
	if pb.GetEnabled() && (triggers == nil || triggers.RepositoryRef == nil) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("checks.enabled requires triggers.repository_ref: the referenced GitHubRepository supplies the credentials that publish checks"))
	}
	if !pb.GetEnabled() && !pb.GetIncludeFindingSummaries() && !pb.GetUploadSarif() {
		return nil, nil
	}
	return &triggersv1alpha1.SecurityScanChecks{
		Enabled:                 pb.GetEnabled(),
		IncludeFindingSummaries: pb.GetIncludeFindingSummaries(),
		UploadSARIF:             pb.GetUploadSarif(),
	}, nil
}

// securityScanNotificationsFromProto validates and converts notification
// rules.
func securityScanNotificationsFromProto(pbRules []*platform.SecurityScanNotificationRuleConfig) ([]triggersv1alpha1.SecurityScanNotificationRule, error) {
	if len(pbRules) == 0 {
		return nil, nil
	}
	invalid := func(format string, args ...any) error {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(format, args...))
	}
	seen := map[string]bool{}
	rules := make([]triggersv1alpha1.SecurityScanNotificationRule, 0, len(pbRules))
	for i, pb := range pbRules {
		name := strings.TrimSpace(pb.GetName())
		if name == "" {
			return nil, invalid("notifications[%d].name is required", i)
		}
		if len(name) > 63 {
			return nil, invalid("notifications[%d].name exceeds 63 characters", i)
		}
		if seen[name] {
			return nil, invalid("notifications[%d].name %q is duplicated: rule names must be unique", i, name)
		}
		seen[name] = true
		if err := validateSecuritySeverity(fmt.Sprintf("notifications[%d].min_severity", i), pb.GetMinSeverity()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		switch strings.TrimSpace(pb.GetNotifyOn()) {
		case "", "new", "regressed", "new-and-regressed":
		default:
			return nil, invalid("notifications[%d].notify_on %q invalid (want new, regressed, or new-and-regressed)", i, pb.GetNotifyOn())
		}
		rule := triggersv1alpha1.SecurityScanNotificationRule{
			Name:        name,
			MinSeverity: strings.TrimSpace(pb.GetMinSeverity()),
			NotifyOn:    strings.TrimSpace(pb.GetNotifyOn()),
		}
		if ref := strings.TrimSpace(pb.GetSlackWebhookSecretRef()); ref != "" {
			rule.Slack = &triggersv1alpha1.SecurityScanSlackNotification{WebhookSecretRef: ref}
		}
		if pb.GetGithubIssues() {
			rule.GitHubIssues = &triggersv1alpha1.SecurityScanGitHubIssueNotification{}
			if ref := strings.TrimSpace(pb.GetGithubRepositoryRef()); ref != "" {
				rule.GitHubIssues.RepositoryRef = &triggersv1alpha1.SecurityResourceRef{Name: ref}
			}
		}
		linearKey := strings.TrimSpace(pb.GetLinearApiKeySecretRef())
		linearTeam := strings.TrimSpace(pb.GetLinearTeamId())
		if (linearKey == "") != (linearTeam == "") {
			return nil, invalid("notifications[%d]: linear_api_key_secret_ref and linear_team_id must be set together", i)
		}
		if linearKey != "" {
			rule.Linear = &triggersv1alpha1.SecurityScanLinearNotification{APIKeySecretRef: linearKey, TeamID: linearTeam}
		}
		if rule.Slack == nil && rule.GitHubIssues == nil && rule.Linear == nil {
			return nil, invalid("notifications[%d] (%q) configures no channel: set slack, github_issues, and/or linear", i, name)
		}
		rules = append(rules, rule)
	}
	return rules, nil
}
