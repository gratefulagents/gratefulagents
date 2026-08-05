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

	spec := &triggersv1alpha1.SecurityScanSpec{
		RepoURL:           repoURL,
		BaseBranch:        strings.TrimSpace(pb.GetBaseBranch()),
		Revision:          strings.TrimSpace(pb.GetRevision()),
		AdditionalRepos:   trimmedNonEmpty(pb.GetAdditionalRepos()),
		Scope:             securityScanScopeFromProto(pb.GetScope()),
		Workflow:          workflow,
		Parallelism:       pb.GetParallelism(),
		SeverityRankers:   rankers,
		PostScripts:       postScripts,
		Dedupe:            dedupe,
		MinSeverity:       strings.TrimSpace(pb.GetMinSeverity()),
		FailOnSeverity:    strings.TrimSpace(pb.GetFailOnSeverity()),
		Schedule:          strings.TrimSpace(pb.GetSchedule()),
		TimeZone:          strings.TrimSpace(pb.GetTimeZone()),
		Suspend:           pb.GetSuspend(),
		ConcurrencyPolicy: policy,
		Defaults:          defaults,
		MaxRuntime:        maxRuntime,
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

func securityScanRankersFromProto(pbRankers []*platform.SecurityRankerConfig) ([]triggersv1alpha1.SecurityRanker, error) {
	var out []triggersv1alpha1.SecurityRanker
	for _, r := range pbRankers {
		if strings.TrimSpace(r.GetName()) == "" || strings.TrimSpace(r.GetRules()) == "" {
			return nil, fmt.Errorf("severity rankers need both a name and rules")
		}
		out = append(out, triggersv1alpha1.SecurityRanker{
			Name:  strings.TrimSpace(r.GetName()),
			Rules: r.GetRules(),
		})
	}
	return out, nil
}

func securityScanPostScriptsFromProto(pbScripts []*platform.SecurityPostScriptConfig) ([]triggersv1alpha1.SecurityPostScript, error) {
	var out []triggersv1alpha1.SecurityPostScript
	for _, p := range pbScripts {
		if strings.TrimSpace(p.GetName()) == "" || strings.TrimSpace(p.GetPrompt()) == "" {
			return nil, fmt.Errorf("post-scripts need both a name and a prompt")
		}
		switch strings.TrimSpace(p.GetRunOn()) {
		case "", "all", "confirmed", "high-and-above":
		default:
			return nil, fmt.Errorf("invalid post-script run_on %q (want all, confirmed, or high-and-above)", p.GetRunOn())
		}
		out = append(out, triggersv1alpha1.SecurityPostScript{
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
	return pb
}
