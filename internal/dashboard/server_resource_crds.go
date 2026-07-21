package dashboard

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func invalidArgument(format string, args ...any) error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(format, args...))
}

func validateResourceName(name string) error {
	if problems := validation.IsDNS1123Subdomain(name); len(problems) != 0 {
		return invalidArgument("invalid name %q: %s", name, strings.Join(problems, "; "))
	}
	return nil
}

func dashboardNamespace(ctx context.Context, s *Server) (string, error) {
	return s.ensureUserNamespace(ctx, requestActorFromContext(ctx))
}

func validateDashboardResourceNamespace(requested, personal string) error {
	requested = strings.TrimSpace(requested)
	if requested != "" && requested != personal {
		return invalidArgument("namespace is server-managed for personal resources")
	}
	return nil
}

func dashboardExtraWritablePaths(paths []string) ([]string, error) {
	cleaned := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		path := filepath.Clean(strings.TrimSpace(raw))
		if path == "." || !filepath.IsAbs(path) {
			return nil, invalidArgument("extra_writable_paths entries must be absolute: %q", raw)
		}
		for _, root := range []string{"/", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/usr", "/home", "/root", "/run", "/var/lib", "/var/log", "/var/run", "/var/spool", "/var/tmp", "/tmp", "/proc", "/dev", "/workspace"} {
			if path == root || root != "/" && strings.HasPrefix(path, root+"/") {
				return nil, invalidArgument("extra_writable_paths entry %q is a protected path", raw)
			}
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		cleaned = append(cleaned, path)
	}
	return cleaned, nil
}

func dashboardAbsolutePaths(field string, paths []string) ([]string, error) {
	cleaned := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		path := filepath.Clean(strings.TrimSpace(raw))
		if path == "." || !filepath.IsAbs(path) {
			return nil, invalidArgument("%s entries must be absolute: %q", field, raw)
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		cleaned = append(cleaned, path)
	}
	return cleaned, nil
}

func dashboardResourceList(field string, values map[string]string) (corev1.ResourceList, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(corev1.ResourceList, len(values))
	for rawName, rawQuantity := range values {
		name := strings.TrimSpace(rawName)
		quantity, err := resource.ParseQuantity(strings.TrimSpace(rawQuantity))
		if name == "" || len(validation.IsQualifiedName(name)) != 0 || err != nil || quantity.Sign() < 0 {
			return nil, invalidArgument("invalid %s resource %q=%q", field, rawName, rawQuantity)
		}
		out[corev1.ResourceName(name)] = quantity
	}
	return out, nil
}

func positiveDuration(field, value string) (metav1.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return metav1.Duration{}, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return metav1.Duration{}, invalidArgument("%s must be a positive Go duration", field)
	}
	return metav1.Duration{Duration: d}, nil
}

func optionalNamedRef(field, value string) (*platformv1alpha1.NamedRef, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if err := validateResourceName(value); err != nil {
		return nil, invalidArgument("invalid %s %q", field, value)
	}
	return &platformv1alpha1.NamedRef{Name: value}, nil
}

func runtimeProfileSpec(p *platform.RuntimeProfile) (platformv1alpha1.RuntimeProfileSpec, error) {
	if p == nil {
		return platformv1alpha1.RuntimeProfileSpec{}, invalidArgument("profile is required")
	}
	if err := validateResourceName(p.Name); err != nil {
		return platformv1alpha1.RuntimeProfileSpec{}, err
	}
	if p.PermissionMode != "read-only" && p.PermissionMode != "workspace-write" && p.PermissionMode != "danger-full-access" {
		return platformv1alpha1.RuntimeProfileSpec{}, invalidArgument("invalid permission_mode %q", p.PermissionMode)
	}
	if p.EgressMode != "unrestricted" && p.EgressMode != "restricted" && p.EgressMode != "disabled" {
		return platformv1alpha1.RuntimeProfileSpec{}, invalidArgument("invalid egress_mode %q", p.EgressMode)
	}
	gitRemoteWrites := p.GetGitRemoteWrites()
	if gitRemoteWrites == "" {
		gitRemoteWrites = string(platformv1alpha1.GitRemoteWritesEnabled)
	}
	if gitRemoteWrites != string(platformv1alpha1.GitRemoteWritesEnabled) && gitRemoteWrites != string(platformv1alpha1.GitRemoteWritesDisabled) {
		return platformv1alpha1.RuntimeProfileSpec{}, invalidArgument("invalid git_remote_writes %q", p.GetGitRemoteWrites())
	}
	defaultTimeout, err := positiveDuration("default_timeout", p.DefaultTimeout)
	if err != nil {
		return platformv1alpha1.RuntimeProfileSpec{}, err
	}
	security := &platformv1alpha1.RuntimeProfileSecurity{
		PermissionMode:  platformv1alpha1.PermissionMode(p.PermissionMode),
		GitRemoteWrites: platformv1alpha1.GitRemoteWrites(gitRemoteWrites),
		EgressMode:      platformv1alpha1.EgressMode(p.EgressMode),
		DefaultTimeout:  defaultTimeout,
	}
	if p.WorkspaceSize != "" {
		if !regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(Ki|Mi|Gi|Ti|Pi|Ei|m|k|M|G|T|P|E)?$`).MatchString(p.WorkspaceSize) {
			return platformv1alpha1.RuntimeProfileSpec{}, invalidArgument("invalid workspace_size %q", p.WorkspaceSize)
		}
		if _, err := resource.ParseQuantity(p.WorkspaceSize); err != nil {
			return platformv1alpha1.RuntimeProfileSpec{}, invalidArgument("invalid workspace_size %q", p.WorkspaceSize)
		}
	}
	sandboxTemplateRef, err := optionalNamedRef("sandbox_template_ref", p.SandboxTemplateRef)
	if err != nil {
		return platformv1alpha1.RuntimeProfileSpec{}, err
	}
	warmPoolRef, err := optionalNamedRef("warm_pool_ref", p.WarmPoolRef)
	if err != nil {
		return platformv1alpha1.RuntimeProfileSpec{}, err
	}
	if p.RuntimeClassName != "" {
		if err := validateResourceName(p.RuntimeClassName); err != nil {
			return platformv1alpha1.RuntimeProfileSpec{}, invalidArgument("invalid runtime_class_name %q", p.RuntimeClassName)
		}
	}
	commandPath, err := dashboardAbsolutePaths("command_path", p.CommandPath)
	if err != nil {
		return platformv1alpha1.RuntimeProfileSpec{}, err
	}
	pathPrepend, err := dashboardAbsolutePaths("command_path_prepend", p.CommandPathPrepend)
	if err != nil {
		return platformv1alpha1.RuntimeProfileSpec{}, err
	}
	pathAppend, err := dashboardAbsolutePaths("command_path_append", p.CommandPathAppend)
	if err != nil {
		return platformv1alpha1.RuntimeProfileSpec{}, err
	}
	readOnlyPaths, err := dashboardAbsolutePaths("extra_read_only_paths", p.ExtraReadOnlyPaths)
	if err != nil {
		return platformv1alpha1.RuntimeProfileSpec{}, err
	}
	writablePaths, err := dashboardExtraWritablePaths(p.ExtraWritablePaths)
	if err != nil {
		return platformv1alpha1.RuntimeProfileSpec{}, err
	}
	sandboxSpec := &platformv1alpha1.RuntimeProfileSandbox{
		SandboxTemplateRef:  sandboxTemplateRef,
		RuntimeClassName:    strings.TrimSpace(p.RuntimeClassName),
		WarmPoolRef:         warmPoolRef,
		PersistWorkspace:    p.PersistWorkspace,
		WorkspaceSize:       p.WorkspaceSize,
		EnablePrivateProcfs: p.EnablePrivateProcfs,
	}
	if len(commandPath)+len(pathPrepend)+len(pathAppend)+len(readOnlyPaths)+len(writablePaths)+len(p.CommandEnv) > 0 {
		sandboxSpec.CommandSandbox = &platformv1alpha1.RuntimeProfileCommandSandbox{
			Path:               commandPath,
			PathPrepend:        pathPrepend,
			PathAppend:         pathAppend,
			ExtraReadOnlyPaths: readOnlyPaths,
			ExtraWritablePaths: writablePaths,
			Env:                maps.Clone(p.CommandEnv),
		}
	}
	requests, err := dashboardResourceList("resource_requests", p.ResourceRequests)
	if err != nil {
		return platformv1alpha1.RuntimeProfileSpec{}, err
	}
	limits, err := dashboardResourceList("resource_limits", p.ResourceLimits)
	if err != nil {
		return platformv1alpha1.RuntimeProfileSpec{}, err
	}
	for name, request := range requests {
		if limit, exists := limits[name]; exists && request.Cmp(limit) > 0 {
			return platformv1alpha1.RuntimeProfileSpec{}, invalidArgument("resource request %q exceeds its limit", name)
		}
	}
	if len(p.ResourceClaims) > 0 {
		return platformv1alpha1.RuntimeProfileSpec{}, invalidArgument("resource_claims are unsupported until RuntimeProfile can configure matching pod-level claim sources")
	}
	var resources *corev1.ResourceRequirements
	if len(requests)+len(limits) > 0 {
		resources = &corev1.ResourceRequirements{Requests: requests, Limits: limits}
	}
	if p.MaxConcurrentRuns < 0 || p.PerNamespaceMaxConcurrentRuns < 0 {
		return platformv1alpha1.RuntimeProfileSpec{}, invalidArgument("admission concurrency limits cannot be negative")
	}
	staleRunTimeout, err := positiveDuration("stale_run_timeout", p.StaleRunTimeout)
	if err != nil {
		return platformv1alpha1.RuntimeProfileSpec{}, err
	}
	var admission *platformv1alpha1.RuntimeProfileAdmission
	if p.MaxConcurrentRuns != 0 || p.PerNamespaceMaxConcurrentRuns != 0 || staleRunTimeout.Duration != 0 {
		admission = &platformv1alpha1.RuntimeProfileAdmission{
			MaxConcurrentRuns:             p.MaxConcurrentRuns,
			PerNamespaceMaxConcurrentRuns: p.PerNamespaceMaxConcurrentRuns,
			StaleRunTimeout:               staleRunTimeout,
		}
	}
	return platformv1alpha1.RuntimeProfileSpec{Security: security, Sandbox: sandboxSpec, Resources: resources, Admission: admission}, nil
}

func runtimeProfileToProto(v *platformv1alpha1.RuntimeProfile) *platform.RuntimeProfile {
	p := &platform.RuntimeProfile{Name: v.Name, Namespace: v.Namespace}
	if v.Spec.Security != nil {
		p.PermissionMode = string(v.Spec.Security.PermissionMode)
		gitRemoteWrites := string(platformv1alpha1.NormalizeGitRemoteWrites(v.Spec.Security.GitRemoteWrites))
		p.GitRemoteWrites = &gitRemoteWrites
		p.EgressMode = string(v.Spec.Security.EgressMode)
		if v.Spec.Security.DefaultTimeout.Duration != 0 {
			p.DefaultTimeout = v.Spec.Security.DefaultTimeout.Duration.String()
		}
	}
	if sandbox := v.Spec.Sandbox; sandbox != nil {
		if sandbox.SandboxTemplateRef != nil {
			p.SandboxTemplateRef = sandbox.SandboxTemplateRef.Name
		}
		p.RuntimeClassName = sandbox.RuntimeClassName
		if sandbox.WarmPoolRef != nil {
			p.WarmPoolRef = sandbox.WarmPoolRef.Name
		}
		p.PersistWorkspace = sandbox.PersistWorkspace
		p.WorkspaceSize = sandbox.WorkspaceSize
		p.EnablePrivateProcfs = sandbox.EnablePrivateProcfs
		if command := sandbox.CommandSandbox; command != nil {
			p.CommandPath = append([]string(nil), command.Path...)
			p.CommandPathPrepend = append([]string(nil), command.PathPrepend...)
			p.CommandPathAppend = append([]string(nil), command.PathAppend...)
			p.ExtraReadOnlyPaths = append([]string(nil), command.ExtraReadOnlyPaths...)
			p.ExtraWritablePaths = append([]string(nil), command.ExtraWritablePaths...)
			p.CommandEnv = maps.Clone(command.Env)
		}
	}
	if resources := v.Spec.Resources; resources != nil {
		p.ResourceRequests = make(map[string]string, len(resources.Requests))
		for name, quantity := range resources.Requests {
			p.ResourceRequests[string(name)] = quantity.String()
		}
		p.ResourceLimits = make(map[string]string, len(resources.Limits))
		for name, quantity := range resources.Limits {
			p.ResourceLimits[string(name)] = quantity.String()
		}
		for _, claim := range resources.Claims {
			p.ResourceClaims = append(p.ResourceClaims, &platform.RuntimeResourceClaim{Name: claim.Name, Request: claim.Request})
		}
	}
	if admission := v.Spec.Admission; admission != nil {
		p.MaxConcurrentRuns = admission.MaxConcurrentRuns
		p.PerNamespaceMaxConcurrentRuns = admission.PerNamespaceMaxConcurrentRuns
		if admission.StaleRunTimeout.Duration != 0 {
			p.StaleRunTimeout = admission.StaleRunTimeout.Duration.String()
		}
	}
	return p
}

func applyLegacyRuntimeProfileUpdate(current *platformv1alpha1.RuntimeProfileSpec, requested platformv1alpha1.RuntimeProfileSpec) {
	if current.Security == nil {
		current.Security = &platformv1alpha1.RuntimeProfileSecurity{}
	}
	current.Security.PermissionMode = requested.Security.PermissionMode
	current.Security.EgressMode = requested.Security.EgressMode
	current.Security.DefaultTimeout = requested.Security.DefaultTimeout
	if current.Sandbox == nil {
		current.Sandbox = &platformv1alpha1.RuntimeProfileSandbox{}
	}
	current.Sandbox.PersistWorkspace = requested.Sandbox.PersistWorkspace
	current.Sandbox.WorkspaceSize = requested.Sandbox.WorkspaceSize
	if current.Sandbox.CommandSandbox == nil {
		current.Sandbox.CommandSandbox = &platformv1alpha1.RuntimeProfileCommandSandbox{}
	}
	current.Sandbox.CommandSandbox.ExtraWritablePaths = nil
	if requested.Sandbox.CommandSandbox != nil {
		current.Sandbox.CommandSandbox.ExtraWritablePaths = append([]string(nil), requested.Sandbox.CommandSandbox.ExtraWritablePaths...)
	}
}

func (s *Server) ListRuntimeProfiles(ctx context.Context, _ *platform.ListRuntimeProfilesRequest) (*platform.ListRuntimeProfilesResponse, error) {
	ns, err := dashboardNamespace(ctx, s)
	if err != nil {
		return nil, err
	}
	var list platformv1alpha1.RuntimeProfileList
	if err := s.k8sClient.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, mapK8sError("list RuntimeProfiles", err)
	}
	out := &platform.ListRuntimeProfilesResponse{Namespace: ns}
	for i := range list.Items {
		out.Profiles = append(out.Profiles, runtimeProfileToProto(&list.Items[i]))
	}
	sort.Slice(out.Profiles, func(i, j int) bool { return out.Profiles[i].Name < out.Profiles[j].Name })
	return out, nil
}
func (s *Server) CreateRuntimeProfile(ctx context.Context, req *platform.CreateRuntimeProfileRequest) (*platform.RuntimeProfile, error) {
	ns, e := dashboardNamespace(ctx, s)
	if e != nil {
		return nil, e
	}
	if e = validateDashboardResourceNamespace(req.GetProfile().GetNamespace(), ns); e != nil {
		return nil, e
	}
	spec, e := runtimeProfileSpec(req.GetProfile())
	if e != nil {
		return nil, e
	}
	v := &platformv1alpha1.RuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: req.Profile.Name, Namespace: ns}, Spec: spec}
	if e = s.k8sClient.Create(ctx, v); e != nil {
		return nil, mapK8sError("create RuntimeProfile", e)
	}
	return runtimeProfileToProto(v), nil
}
func (s *Server) UpdateRuntimeProfile(ctx context.Context, req *platform.UpdateRuntimeProfileRequest) (*platform.RuntimeProfile, error) {
	ns, e := dashboardNamespace(ctx, s)
	if e != nil {
		return nil, e
	}
	if e = validateDashboardResourceNamespace(req.GetProfile().GetNamespace(), ns); e != nil {
		return nil, e
	}
	spec, e := runtimeProfileSpec(req.GetProfile())
	if e != nil {
		return nil, e
	}
	v := &platformv1alpha1.RuntimeProfile{}
	if e = s.k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: req.Profile.Name}, v); e != nil {
		return nil, mapK8sError("read RuntimeProfile", e)
	}
	if req.Profile.ReplaceSpec {
		// Presence-aware security fields must survive updates from older clients
		// that replace the spec but do not know about the field.
		if req.Profile.GitRemoteWrites == nil && v.Spec.Security != nil {
			spec.Security.GitRemoteWrites = v.Spec.Security.GitRemoteWrites
		}
		// Container resource-claim references are not dashboard-owned because
		// RuntimeProfile cannot yet describe the matching pod-level sources.
		// Preserve externally managed references while replacing every field the
		// dashboard can configure.
		if v.Spec.Resources != nil && len(v.Spec.Resources.Claims) > 0 {
			if spec.Resources == nil {
				spec.Resources = &corev1.ResourceRequirements{}
			}
			spec.Resources.Claims = append([]corev1.ResourceClaim(nil), v.Spec.Resources.Claims...)
		}
		v.Spec = spec
	} else {
		applyLegacyRuntimeProfileUpdate(&v.Spec, spec)
	}
	if e = s.k8sClient.Update(ctx, v); e != nil {
		return nil, mapK8sError("update RuntimeProfile", e)
	}
	return runtimeProfileToProto(v), nil
}
func (s *Server) DeleteRuntimeProfile(ctx context.Context, req *platform.DeleteRuntimeProfileRequest) error {
	ns, e := dashboardNamespace(ctx, s)
	if e != nil {
		return e
	}
	if e = validateResourceName(req.GetName()); e != nil {
		return e
	}
	if e = s.k8sClient.Delete(ctx, &platformv1alpha1.RuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: req.Name, Namespace: ns}}); e != nil {
		return mapK8sError("delete RuntimeProfile", e)
	}
	return nil
}

func mcpPolicySpec(p *platform.MCPPolicy) (platformv1alpha1.MCPPolicySpec, error) {
	if p == nil {
		return platformv1alpha1.MCPPolicySpec{}, invalidArgument("policy is required")
	}
	if e := validateResourceName(p.Name); e != nil {
		return platformv1alpha1.MCPPolicySpec{}, e
	}
	if p.DefaultAction != "Allow" && p.DefaultAction != "Deny" {
		return platformv1alpha1.MCPPolicySpec{}, invalidArgument("invalid default_action %q", p.DefaultAction)
	}
	s := platformv1alpha1.MCPPolicySpec{DefaultAction: platformv1alpha1.MCPDefaultAction(p.DefaultAction)}
	for _, a := range p.AllowedServers {
		if a == nil || validateResourceName(a.Name) != nil {
			return s, invalidArgument("invalid allowed server name")
		}
		s.AllowedServers = append(s.AllowedServers, platformv1alpha1.MCPAllowedServer{Name: a.Name, Tools: append([]string(nil), a.Tools...)})
	}
	if p.BreakGlass != nil {
		s.BreakGlass = &platformv1alpha1.MCPBreakGlass{
			Enabled:            p.BreakGlass.Enabled,
			RequireAuditReason: p.BreakGlass.RequireAuditReason,
			AdminMediated:      p.BreakGlass.AdminMediated,
		}
	}
	return s, nil
}
func mcpPolicyToProto(v *platformv1alpha1.MCPPolicy) *platform.MCPPolicy {
	p := &platform.MCPPolicy{Name: v.Name, Namespace: v.Namespace, DefaultAction: string(v.Spec.DefaultAction)}
	for _, a := range v.Spec.AllowedServers {
		p.AllowedServers = append(p.AllowedServers, &platform.MCPAllowedServer{Name: a.Name, Tools: append([]string(nil), a.Tools...)})
	}
	if v.Spec.BreakGlass != nil {
		p.BreakGlass = &platform.MCPBreakGlass{
			Enabled:            v.Spec.BreakGlass.Enabled,
			RequireAuditReason: v.Spec.BreakGlass.RequireAuditReason,
			AdminMediated:      v.Spec.BreakGlass.AdminMediated,
		}
	}
	return p
}
func (s *Server) ListMCPPolicies(ctx context.Context, _ *platform.ListMCPPoliciesRequest) (*platform.ListMCPPoliciesResponse, error) {
	ns, e := dashboardNamespace(ctx, s)
	if e != nil {
		return nil, e
	}
	var l platformv1alpha1.MCPPolicyList
	if e = s.k8sClient.List(ctx, &l, client.InNamespace(ns)); e != nil {
		return nil, mapK8sError("list MCPPolicies", e)
	}
	o := &platform.ListMCPPoliciesResponse{Namespace: ns}
	for i := range l.Items {
		o.Policies = append(o.Policies, mcpPolicyToProto(&l.Items[i]))
	}
	sort.Slice(o.Policies, func(i, j int) bool { return o.Policies[i].Name < o.Policies[j].Name })
	return o, nil
}
func (s *Server) CreateMCPPolicy(ctx context.Context, r *platform.CreateMCPPolicyRequest) (*platform.MCPPolicy, error) {
	ns, e := dashboardNamespace(ctx, s)
	if e != nil {
		return nil, e
	}
	if e = validateDashboardResourceNamespace(r.GetPolicy().GetNamespace(), ns); e != nil {
		return nil, e
	}
	sp, e := mcpPolicySpec(r.GetPolicy())
	if e != nil {
		return nil, e
	}
	v := &platformv1alpha1.MCPPolicy{ObjectMeta: metav1.ObjectMeta{Name: r.Policy.Name, Namespace: ns}, Spec: sp}
	if e = s.k8sClient.Create(ctx, v); e != nil {
		return nil, mapK8sError("create MCPPolicy", e)
	}
	return mcpPolicyToProto(v), nil
}
func (s *Server) UpdateMCPPolicy(ctx context.Context, r *platform.UpdateMCPPolicyRequest) (*platform.MCPPolicy, error) {
	ns, e := dashboardNamespace(ctx, s)
	if e != nil {
		return nil, e
	}
	if e = validateDashboardResourceNamespace(r.GetPolicy().GetNamespace(), ns); e != nil {
		return nil, e
	}
	sp, e := mcpPolicySpec(r.GetPolicy())
	if e != nil {
		return nil, e
	}
	v := &platformv1alpha1.MCPPolicy{}
	if e = s.k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: r.Policy.Name}, v); e != nil {
		return nil, mapK8sError("read MCPPolicy", e)
	}
	if r.Policy.ReplaceSpec {
		v.Spec = sp
	} else {
		v.Spec.DefaultAction = sp.DefaultAction
		v.Spec.AllowedServers = sp.AllowedServers
	}
	if e = s.k8sClient.Update(ctx, v); e != nil {
		return nil, mapK8sError("update MCPPolicy", e)
	}
	return mcpPolicyToProto(v), nil
}
func (s *Server) DeleteMCPPolicy(ctx context.Context, r *platform.DeleteMCPPolicyRequest) error {
	ns, e := dashboardNamespace(ctx, s)
	if e != nil {
		return e
	}
	if e = validateResourceName(r.GetName()); e != nil {
		return e
	}
	if e = s.k8sClient.Delete(ctx, &platformv1alpha1.MCPPolicy{ObjectMeta: metav1.ObjectMeta{Name: r.Name, Namespace: ns}}); e != nil {
		return mapK8sError("delete MCPPolicy", e)
	}
	return nil
}

func guardrailSpec(p *platform.GuardrailPolicy) (platformv1alpha1.GuardrailPolicySpec, error) {
	if p == nil {
		return platformv1alpha1.GuardrailPolicySpec{}, invalidArgument("policy is required")
	}
	if e := validateResourceName(p.Name); e != nil {
		return platformv1alpha1.GuardrailPolicySpec{}, e
	}
	var s platformv1alpha1.GuardrailPolicySpec
	for _, r := range p.Rules {
		if r == nil || strings.TrimSpace(r.Name) == "" {
			return s, invalidArgument("guardrail rule name is required")
		}
		if r.Type != "tool-input" && r.Type != "tool-output" {
			return s, invalidArgument("invalid guardrail rule type %q", r.Type)
		}
		if r.Action != "block" && r.Action != "warn" && r.Action != "log" {
			return s, invalidArgument("invalid guardrail rule action %q", r.Action)
		}
		if _, e := regexp.Compile(r.Regex); e != nil {
			return s, invalidArgument("invalid guardrail rule regex %q", r.Regex)
		}
		s.Rules = append(s.Rules, platformv1alpha1.GuardrailRule{Name: r.Name, Type: r.Type, ToolPattern: r.ToolPattern, Regex: r.Regex, Action: r.Action, Message: r.Message})
	}
	return s, nil
}
func guardrailToProto(v *platformv1alpha1.GuardrailPolicy) *platform.GuardrailPolicy {
	p := &platform.GuardrailPolicy{Name: v.Name, Namespace: v.Namespace}
	for _, r := range v.Spec.Rules {
		p.Rules = append(p.Rules, &platform.GuardrailRule{Name: r.Name, Type: r.Type, ToolPattern: r.ToolPattern, Regex: r.Regex, Action: r.Action, Message: r.Message})
	}
	return p
}
func (s *Server) ListGuardrailPolicies(ctx context.Context, _ *platform.ListGuardrailPoliciesRequest) (*platform.ListGuardrailPoliciesResponse, error) {
	ns, e := dashboardNamespace(ctx, s)
	if e != nil {
		return nil, e
	}
	var l platformv1alpha1.GuardrailPolicyList
	if e = s.k8sClient.List(ctx, &l, client.InNamespace(ns)); e != nil {
		return nil, mapK8sError("list GuardrailPolicies", e)
	}
	o := &platform.ListGuardrailPoliciesResponse{Namespace: ns}
	for i := range l.Items {
		o.Policies = append(o.Policies, guardrailToProto(&l.Items[i]))
	}
	sort.Slice(o.Policies, func(i, j int) bool { return o.Policies[i].Name < o.Policies[j].Name })
	return o, nil
}
func (s *Server) CreateGuardrailPolicy(ctx context.Context, r *platform.CreateGuardrailPolicyRequest) (*platform.GuardrailPolicy, error) {
	ns, e := dashboardNamespace(ctx, s)
	if e != nil {
		return nil, e
	}
	if e = validateDashboardResourceNamespace(r.GetPolicy().GetNamespace(), ns); e != nil {
		return nil, e
	}
	sp, e := guardrailSpec(r.GetPolicy())
	if e != nil {
		return nil, e
	}
	v := &platformv1alpha1.GuardrailPolicy{ObjectMeta: metav1.ObjectMeta{Name: r.Policy.Name, Namespace: ns}, Spec: sp}
	if e = s.k8sClient.Create(ctx, v); e != nil {
		return nil, mapK8sError("create GuardrailPolicy", e)
	}
	return guardrailToProto(v), nil
}
func (s *Server) UpdateGuardrailPolicy(ctx context.Context, r *platform.UpdateGuardrailPolicyRequest) (*platform.GuardrailPolicy, error) {
	ns, e := dashboardNamespace(ctx, s)
	if e != nil {
		return nil, e
	}
	if e = validateDashboardResourceNamespace(r.GetPolicy().GetNamespace(), ns); e != nil {
		return nil, e
	}
	sp, e := guardrailSpec(r.GetPolicy())
	if e != nil {
		return nil, e
	}
	v := &platformv1alpha1.GuardrailPolicy{}
	if e = s.k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: r.Policy.Name}, v); e != nil {
		return nil, mapK8sError("read GuardrailPolicy", e)
	}
	v.Spec.Rules = sp.Rules
	if e = s.k8sClient.Update(ctx, v); e != nil {
		return nil, mapK8sError("update GuardrailPolicy", e)
	}
	return guardrailToProto(v), nil
}
func (s *Server) DeleteGuardrailPolicy(ctx context.Context, r *platform.DeleteGuardrailPolicyRequest) error {
	ns, e := dashboardNamespace(ctx, s)
	if e != nil {
		return e
	}
	if e = validateResourceName(r.GetName()); e != nil {
		return e
	}
	if e = s.k8sClient.Delete(ctx, &platformv1alpha1.GuardrailPolicy{ObjectMeta: metav1.ObjectMeta{Name: r.Name, Namespace: ns}}); e != nil {
		return mapK8sError("delete GuardrailPolicy", e)
	}
	return nil
}

func requireCatalogActor(ctx context.Context, admin bool) error {
	a, recorded := requestActorFromContextOK(ctx)
	if !recorded {
		return nil
	}
	if strings.TrimSpace(a.Subject) == "" {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	if admin && !actorIsAdmin(a) {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("administrator access required"))
	}
	return nil
}
func modeSpec(p *platform.ModeTemplate) (platformv1alpha1.ModeTemplateSpec, error) {
	if p == nil {
		return platformv1alpha1.ModeTemplateSpec{}, invalidArgument("template is required")
	}
	if e := validateResourceName(p.Name); e != nil {
		return platformv1alpha1.ModeTemplateSpec{}, e
	}
	if p.Category != "direct" && p.Category != "orchestrated" {
		return platformv1alpha1.ModeTemplateSpec{}, invalidArgument("invalid category %q", p.Category)
	}
	if p.ExecutionStrategy != "serial" && p.ExecutionStrategy != "parallel" && p.ExecutionStrategy != "pipeline" {
		return platformv1alpha1.ModeTemplateSpec{}, invalidArgument("invalid execution_strategy %q", p.ExecutionStrategy)
	}
	if p.PermissionMode != "" && p.PermissionMode != "read-only" && p.PermissionMode != "workspace-write" && p.PermissionMode != "danger-full-access" {
		return platformv1alpha1.ModeTemplateSpec{}, invalidArgument("invalid permission_mode %q", p.PermissionMode)
	}
	s := platformv1alpha1.ModeTemplateSpec{Name: p.Name, Version: p.Version, DisplayName: p.DisplayName, Description: p.Description, Category: platformv1alpha1.ModeCategory(p.Category), ExecutionStrategy: platformv1alpha1.ModeExecutionStrategy(p.ExecutionStrategy), Instructions: p.Instructions, Autonomous: p.Autonomous, PermissionMode: platformv1alpha1.PermissionMode(p.PermissionMode), AllowedMutatingTools: append([]string(nil), p.AllowedMutatingTools...)}
	if c := p.Constraints; c != nil {
		if c.MaxTurns < 0 || c.MaxRuntimeMinutes < 0 || c.MaxRetries < 0 || c.SubagentMaxTurns < 0 || c.MaxConcurrentSubagents < 0 {
			return s, invalidArgument("mode constraints cannot be negative")
		}
		s.Constraints = &platformv1alpha1.ModeConstraints{MaxTurns: c.MaxTurns, MaxRuntimeMinutes: c.MaxRuntimeMinutes, MaxRetries: c.MaxRetries, SubAgentMaxTurns: c.SubagentMaxTurns, MaxConcurrentSubAgents: c.MaxConcurrentSubagents}
	}
	for _, n := range p.DefaultMcpServerRefs {
		if e := validateResourceName(n); e != nil {
			return s, e
		}
		s.DefaultMCPServerRefs = append(s.DefaultMCPServerRefs, platformv1alpha1.NamedRef{Name: n})
	}
	for _, n := range p.DefaultSkillRefs {
		if e := validateResourceName(n); e != nil {
			return s, e
		}
		s.DefaultSkillRefs = append(s.DefaultSkillRefs, platformv1alpha1.NamedRef{Name: n})
	}
	return s, nil
}
func (s *Server) ListModeTemplates(ctx context.Context, _ *platform.ListModeTemplatesRequest) (*platform.ListModeTemplatesResponse, error) {
	if e := requireCatalogActor(ctx, false); e != nil {
		return nil, e
	}
	var l platformv1alpha1.ModeTemplateList
	if e := s.k8sClient.List(ctx, &l); e != nil {
		return nil, mapK8sError("list ModeTemplates", e)
	}
	o := &platform.ListModeTemplatesResponse{}
	for i := range l.Items {
		o.Templates = append(o.Templates, k8sModeTemplateToProto(&l.Items[i]))
	}
	sort.Slice(o.Templates, func(i, j int) bool { return o.Templates[i].Name < o.Templates[j].Name })
	return o, nil
}
func (s *Server) CreateModeTemplate(ctx context.Context, r *platform.CreateModeTemplateRequest) (*platform.ModeTemplate, error) {
	if e := requireCatalogActor(ctx, false); e != nil {
		return nil, e
	}
	sp, e := modeSpec(r.GetTemplate())
	if e != nil {
		return nil, e
	}
	v := &platformv1alpha1.ModeTemplate{ObjectMeta: metav1.ObjectMeta{Name: r.Template.Name}, Spec: sp}
	if e = s.k8sClient.Create(ctx, v); e != nil {
		return nil, mapK8sError("create ModeTemplate", e)
	}
	return k8sModeTemplateToProto(v), nil
}
func (s *Server) UpdateModeTemplate(ctx context.Context, r *platform.UpdateModeTemplateRequest) (*platform.ModeTemplate, error) {
	if e := requireCatalogActor(ctx, true); e != nil {
		return nil, e
	}
	sp, e := modeSpec(r.GetTemplate())
	if e != nil {
		return nil, e
	}
	v := &platformv1alpha1.ModeTemplate{}
	if e = s.k8sClient.Get(ctx, types.NamespacedName{Name: r.Template.Name}, v); e != nil {
		return nil, mapK8sError("read ModeTemplate", e)
	}
	// Constraints are intentionally optional in the dashboard editor. Preserve
	// GitOps-managed limits when the request does not own that subset.
	if sp.Constraints == nil {
		sp.Constraints = v.Spec.Constraints.DeepCopy()
	}
	v.Spec = sp
	if e = s.k8sClient.Update(ctx, v); e != nil {
		return nil, mapK8sError("update ModeTemplate", e)
	}
	return k8sModeTemplateToProto(v), nil
}
func (s *Server) DeleteModeTemplate(ctx context.Context, r *platform.DeleteModeTemplateRequest) error {
	if e := requireCatalogActor(ctx, true); e != nil {
		return e
	}
	if e := validateResourceName(r.GetName()); e != nil {
		return e
	}
	if e := s.k8sClient.Delete(ctx, &platformv1alpha1.ModeTemplate{ObjectMeta: metav1.ObjectMeta{Name: r.Name}}); e != nil {
		return mapK8sError("delete ModeTemplate", e)
	}
	return nil
}

func roleSpec(p *platform.RoleInstruction) (platformv1alpha1.RoleInstructionSpec, error) {
	if p == nil {
		return platformv1alpha1.RoleInstructionSpec{}, invalidArgument("instruction is required")
	}
	if e := validateResourceName(p.Name); e != nil {
		return platformv1alpha1.RoleInstructionSpec{}, e
	}
	if strings.TrimSpace(p.Instructions) == "" {
		return platformv1alpha1.RoleInstructionSpec{}, invalidArgument("instructions are required")
	}
	if p.ToolAccess != "full" && p.ToolAccess != "read-only" && p.ToolAccess != "analysis" && p.ToolAccess != "execution" {
		return platformv1alpha1.RoleInstructionSpec{}, invalidArgument("invalid tool_access %q", p.ToolAccess)
	}
	modelsByProvider, err := normalizeRoleModelsByProvider(p.ModelsByProvider)
	if err != nil {
		return platformv1alpha1.RoleInstructionSpec{}, err
	}
	reasoningLevel, err := resolveReasoningLevel(p.ReasoningLevel)
	if err != nil {
		return platformv1alpha1.RoleInstructionSpec{}, invalidArgument("invalid reasoning_level %q", p.ReasoningLevel)
	}
	return platformv1alpha1.RoleInstructionSpec{
		Description:      p.Description,
		Instructions:     p.Instructions,
		ToolAccess:       p.ToolAccess,
		Model:            strings.TrimSpace(p.Model),
		ModelsByProvider: modelsByProvider,
		ReasoningLevel:   reasoningLevel,
	}, nil
}

func normalizeRoleModelsByProvider(models map[string]string) (map[string]string, error) {
	if len(models) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(models))
	for provider, model := range models {
		provider = strings.ToLower(strings.TrimSpace(provider))
		model = strings.TrimSpace(model)
		if provider == "" {
			return nil, invalidArgument("role model provider must not be empty")
		}
		if model == "" {
			return nil, invalidArgument("role model for provider %q must not be empty", provider)
		}
		if _, exists := out[provider]; exists {
			return nil, invalidArgument("role model provider %q is duplicated", provider)
		}
		out[provider] = model
	}
	return out, nil
}

func roleToProto(v *platformv1alpha1.RoleInstruction) *platform.RoleInstruction {
	return &platform.RoleInstruction{
		Name:             v.Name,
		Description:      v.Spec.Description,
		Instructions:     v.Spec.Instructions,
		ToolAccess:       v.Spec.ToolAccess,
		Model:            v.Spec.Model,
		ModelsByProvider: maps.Clone(v.Spec.ModelsByProvider),
		ReasoningLevel:   string(v.Spec.ReasoningLevel),
	}
}
func (s *Server) ListRoleInstructions(ctx context.Context, _ *platform.ListRoleInstructionsRequest) (*platform.ListRoleInstructionsResponse, error) {
	if e := requireCatalogActor(ctx, false); e != nil {
		return nil, e
	}
	var l platformv1alpha1.RoleInstructionList
	if e := s.k8sClient.List(ctx, &l); e != nil {
		return nil, mapK8sError("list RoleInstructions", e)
	}
	o := &platform.ListRoleInstructionsResponse{}
	for i := range l.Items {
		o.Instructions = append(o.Instructions, roleToProto(&l.Items[i]))
	}
	sort.Slice(o.Instructions, func(i, j int) bool { return o.Instructions[i].Name < o.Instructions[j].Name })
	return o, nil
}
func (s *Server) CreateRoleInstruction(ctx context.Context, r *platform.CreateRoleInstructionRequest) (*platform.RoleInstruction, error) {
	if e := requireCatalogActor(ctx, true); e != nil {
		return nil, e
	}
	sp, e := roleSpec(r.GetInstruction())
	if e != nil {
		return nil, e
	}
	v := &platformv1alpha1.RoleInstruction{ObjectMeta: metav1.ObjectMeta{Name: r.Instruction.Name}, Spec: sp}
	if e = s.k8sClient.Create(ctx, v); e != nil {
		return nil, mapK8sError("create RoleInstruction", e)
	}
	return roleToProto(v), nil
}
func (s *Server) UpdateRoleInstruction(ctx context.Context, r *platform.UpdateRoleInstructionRequest) (*platform.RoleInstruction, error) {
	if e := requireCatalogActor(ctx, true); e != nil {
		return nil, e
	}
	sp, e := roleSpec(r.GetInstruction())
	if e != nil {
		return nil, e
	}
	v := &platformv1alpha1.RoleInstruction{}
	if e = s.k8sClient.Get(ctx, types.NamespacedName{Name: r.Instruction.Name}, v); e != nil {
		return nil, mapK8sError("read RoleInstruction", e)
	}
	v.Spec = sp
	if e = s.k8sClient.Update(ctx, v); e != nil {
		return nil, mapK8sError("update RoleInstruction", e)
	}
	return roleToProto(v), nil
}
func (s *Server) DeleteRoleInstruction(ctx context.Context, r *platform.DeleteRoleInstructionRequest) error {
	if e := requireCatalogActor(ctx, true); e != nil {
		return e
	}
	if e := validateResourceName(r.GetName()); e != nil {
		return e
	}
	if e := s.k8sClient.Delete(ctx, &platformv1alpha1.RoleInstruction{ObjectMeta: metav1.ObjectMeta{Name: r.Name}}); e != nil {
		return mapK8sError("delete RoleInstruction", e)
	}
	return nil
}
