package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	// securityPackSchemaVersion identifies the pack document format. Imports
	// reject any other version so incompatible documents fail loudly instead
	// of half-applying.
	securityPackSchemaVersion = "security-pack/v1"

	// securityPackMaxBytes bounds an imported document.
	securityPackMaxBytes = 1 << 20 // 1 MiB

	// securityPackMaxItems bounds the number of items one pack may carry.
	securityPackMaxItems = 200

	securityPackKindWorkflow   = "SecurityWorkflow"
	securityPackKindRanker     = "SecurityRanker"
	securityPackKindPostScript = "SecurityPostScript"
	securityPackKindScan       = "SecurityScan"
	securityPackKindPolicyPack = "SecurityPolicyPack"
)

// securityPackDocument is the on-the-wire JSON pack format. Specs are the
// CRD spec shapes of the exported resources, which keeps packs portable
// across dashboards running the same API version.
type securityPackDocument struct {
	SchemaVersion   string             `json:"schemaVersion"`
	ExportedAt      string             `json:"exportedAt"`
	ExportedBy      string             `json:"exportedBy,omitempty"`
	SourceNamespace string             `json:"sourceNamespace"`
	Items           []securityPackItem `json:"items"`
}

type securityPackItem struct {
	Kind string          `json:"kind"`
	Name string          `json:"name"`
	Spec json.RawMessage `json:"spec"`
}

// sanitizeSecurityScanSpecForExport strips everything that must never leave
// the cluster inside a pack: Kubernetes Secret references and admin-only
// escape hatches. The result is safe to share.
func sanitizeSecurityScanSpecForExport(spec triggersv1alpha1.SecurityScanSpec) triggersv1alpha1.SecurityScanSpec {
	out := *spec.DeepCopy()
	out.Defaults.Secrets = triggersv1alpha1.AgentRunSecrets{}
	out.Defaults.DisableCommandSandbox = false
	out.Defaults.KubernetesAdmin = false
	out.Defaults.DockerInDocker = false
	return out
}

// ExportSecurityPack serializes the requested security resources into a
// versioned JSON document with all credential/secret references stripped.
// Unknown names fail the export so packs are never silently incomplete.
func (s *Server) ExportSecurityPack(ctx context.Context, req *platform.ExportSecurityPackRequest) (*platform.ExportSecurityPackResponse, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	total := len(req.GetWorkflows()) + len(req.GetRankers()) + len(req.GetPostScripts()) + len(req.GetScanConfigs()) + len(req.GetPolicyPacks())
	if total == 0 {
		return nil, invalidArgument("select at least one resource to export")
	}
	if total > securityPackMaxItems {
		return nil, invalidArgument("a pack may contain at most %d items, got %d", securityPackMaxItems, total)
	}

	doc := securityPackDocument{
		SchemaVersion:   securityPackSchemaVersion,
		ExportedAt:      time.Now().UTC().Format(time.RFC3339),
		SourceNamespace: namespace,
	}
	if actor := requestActorFromContext(ctx); actor.Subject != "" {
		doc.ExportedBy = actor.Subject
	}

	appendItem := func(kind, name string, spec any) error {
		raw, err := json.Marshal(spec)
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("encode %s %q: %w", kind, name, err))
		}
		doc.Items = append(doc.Items, securityPackItem{Kind: kind, Name: name, Spec: raw})
		return nil
	}

	for _, name := range dedupeSortedNames(req.GetWorkflows()) {
		cr := &triggersv1alpha1.SecurityWorkflow{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, cr); err != nil {
			return nil, mapK8sError(fmt.Sprintf("get SecurityWorkflow %s/%s", namespace, name), err)
		}
		if err := appendItem(securityPackKindWorkflow, name, cr.Spec); err != nil {
			return nil, err
		}
	}
	for _, name := range dedupeSortedNames(req.GetRankers()) {
		cr := &triggersv1alpha1.SecurityRanker{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, cr); err != nil {
			return nil, mapK8sError(fmt.Sprintf("get SecurityRanker %s/%s", namespace, name), err)
		}
		if err := appendItem(securityPackKindRanker, name, cr.Spec); err != nil {
			return nil, err
		}
	}
	for _, name := range dedupeSortedNames(req.GetPostScripts()) {
		cr := &triggersv1alpha1.SecurityPostScript{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, cr); err != nil {
			return nil, mapK8sError(fmt.Sprintf("get SecurityPostScript %s/%s", namespace, name), err)
		}
		if err := appendItem(securityPackKindPostScript, name, cr.Spec); err != nil {
			return nil, err
		}
	}
	for _, name := range dedupeSortedNames(req.GetPolicyPacks()) {
		cr := &triggersv1alpha1.SecurityPolicyPack{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, cr); err != nil {
			return nil, mapK8sError(fmt.Sprintf("get SecurityPolicyPack %s/%s", namespace, name), err)
		}
		if err := appendItem(securityPackKindPolicyPack, name, cr.Spec); err != nil {
			return nil, err
		}
	}
	for _, name := range dedupeSortedNames(req.GetScanConfigs()) {
		if err := s.requireResourceAccess(ctx, securityScanResourceType, name, namespace, AccessViewer, "export this security scan"); err != nil {
			return nil, err
		}
		cr := &triggersv1alpha1.SecurityScan{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, cr); err != nil {
			return nil, mapK8sError(fmt.Sprintf("get SecurityScan %s/%s", namespace, name), err)
		}
		if err := appendItem(securityPackKindScan, name, sanitizeSecurityScanSpecForExport(cr.Spec)); err != nil {
			return nil, err
		}
	}

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode security pack: %w", err))
	}
	return &platform.ExportSecurityPackResponse{
		Data:      data,
		Filename:  fmt.Sprintf("security-pack-%s-%s.json", namespace, time.Now().UTC().Format("20060102")),
		ItemCount: int32(len(doc.Items)), //nolint:gosec // bounded by securityPackMaxItems
	}, nil
}

// ImportSecurityPack validates a pack document and reports per-item
// outcomes. Nothing is created unless apply is true; validation is identical
// to manual authoring and secret-bearing fields are stripped again on import
// as defense in depth.
func (s *Server) ImportSecurityPack(ctx context.Context, req *platform.ImportSecurityPackRequest) (*platform.ImportSecurityPackResponse, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	data := req.GetData()
	if len(data) == 0 {
		return nil, invalidArgument("data is required")
	}
	if len(data) > securityPackMaxBytes {
		return nil, invalidArgument("pack exceeds the %d byte limit", securityPackMaxBytes)
	}
	var doc securityPackDocument
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, invalidArgument("pack is not a valid security pack document: %v", err)
	}
	if doc.SchemaVersion != securityPackSchemaVersion {
		return nil, invalidArgument("unsupported pack schema version %q (want %q)", doc.SchemaVersion, securityPackSchemaVersion)
	}
	if len(doc.Items) == 0 {
		return nil, invalidArgument("pack contains no items")
	}
	if len(doc.Items) > securityPackMaxItems {
		return nil, invalidArgument("pack contains %d items; at most %d are allowed", len(doc.Items), securityPackMaxItems)
	}
	policy := req.GetCollisionPolicy()
	if policy == platform.SecurityPackCollisionPolicy_SECURITY_PACK_COLLISION_POLICY_UNSPECIFIED {
		policy = platform.SecurityPackCollisionPolicy_SECURITY_PACK_COLLISION_POLICY_FAIL
	}

	resp := &platform.ImportSecurityPackResponse{
		Applied:         req.GetApply(),
		SchemaVersion:   doc.SchemaVersion,
		SourceNamespace: doc.SourceNamespace,
		ExportedAt:      doc.ExportedAt,
		ExportedBy:      doc.ExportedBy,
	}
	// Track names claimed within this import so a pack with duplicate names
	// cannot race itself.
	claimed := map[string]bool{}
	for _, item := range doc.Items {
		resp.Items = append(resp.Items, s.importSecurityPackItem(ctx, namespace, item, policy, req.GetApply(), claimed))
	}
	return resp, nil
}

type decodedSecurityPackItem struct {
	buildCR func(name string) client.Object
	probe   func(name string) client.Object
	errs    []triggersv1alpha1.SecurityWorkflowFieldError
}

func decodeSecurityPackItem(item securityPackItem, namespace string) (decodedSecurityPackItem, error) {
	decoded := decodedSecurityPackItem{}
	switch item.Kind {
	case securityPackKindWorkflow:
		var spec triggersv1alpha1.SecurityWorkflowSpec
		if err := json.Unmarshal(item.Spec, &spec); err != nil {
			return decoded, fmt.Errorf("invalid SecurityWorkflow spec: %w", err)
		}
		decoded.errs = triggersv1alpha1.ValidateSecurityWorkflowTasks(spec.Tasks)
		decoded.errs = append(decoded.errs, securityWorkflowParameterErrors(spec.Parameters)...)
		if parallelism := spec.Parallelism; parallelism != 0 && (parallelism < 1 || parallelism > 16) {
			decoded.errs = append(decoded.errs, triggersv1alpha1.SecurityWorkflowFieldError{
				Field: "parallelism", Message: fmt.Sprintf("parallelism %d out of range (want 0 for none, or 1-16)", parallelism),
			})
		}
		decoded.probe = func(string) client.Object { return &triggersv1alpha1.SecurityWorkflow{} }
		decoded.buildCR = func(name string) client.Object {
			return &triggersv1alpha1.SecurityWorkflow{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: spec}
		}
	case securityPackKindRanker:
		var spec triggersv1alpha1.SecurityRankerSpec
		if err := json.Unmarshal(item.Spec, &spec); err != nil {
			return decoded, fmt.Errorf("invalid SecurityRanker spec: %w", err)
		}
		decoded.errs = triggersv1alpha1.ValidateSecurityRankerRules(spec.Rules)
		decoded.probe = func(string) client.Object { return &triggersv1alpha1.SecurityRanker{} }
		decoded.buildCR = func(name string) client.Object {
			return &triggersv1alpha1.SecurityRanker{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: spec}
		}
	case securityPackKindPostScript:
		var spec triggersv1alpha1.SecurityPostScriptSpec
		if err := json.Unmarshal(item.Spec, &spec); err != nil {
			return decoded, fmt.Errorf("invalid SecurityPostScript spec: %w", err)
		}
		decoded.errs = triggersv1alpha1.ValidateSecurityPostScriptSpec(spec)
		decoded.probe = func(string) client.Object { return &triggersv1alpha1.SecurityPostScript{} }
		decoded.buildCR = func(name string) client.Object {
			return &triggersv1alpha1.SecurityPostScript{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: spec}
		}
	case securityPackKindPolicyPack:
		var spec triggersv1alpha1.SecurityPolicyPackSpec
		if err := json.Unmarshal(item.Spec, &spec); err != nil {
			return decoded, fmt.Errorf("invalid SecurityPolicyPack spec: %w", err)
		}
		decoded.errs = triggersv1alpha1.ValidateSecurityPolicyPackSpec(spec)
		decoded.probe = func(string) client.Object { return &triggersv1alpha1.SecurityPolicyPack{} }
		decoded.buildCR = func(name string) client.Object {
			return &triggersv1alpha1.SecurityPolicyPack{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: spec}
		}
	case securityPackKindScan:
		var spec triggersv1alpha1.SecurityScanSpec
		if err := json.Unmarshal(item.Spec, &spec); err != nil {
			return decoded, fmt.Errorf("invalid SecurityScan spec: %w", err)
		}
		spec = sanitizeSecurityScanSpecForExport(spec)
		decoded.errs = validateImportedSecurityScanSpec(&spec)
		decoded.probe = func(string) client.Object { return &triggersv1alpha1.SecurityScan{} }
		decoded.buildCR = func(name string) client.Object {
			return &triggersv1alpha1.SecurityScan{TypeMeta: metav1.TypeMeta{APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: "SecurityScan"}, ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: spec}
		}
	default:
		return decoded, fmt.Errorf("unsupported item kind %q", item.Kind)
	}
	return decoded, nil
}

func (s *Server) importSecurityPackItem(
	ctx context.Context, namespace string, item securityPackItem,
	policy platform.SecurityPackCollisionPolicy, apply bool, claimed map[string]bool,
) *platform.SecurityPackItemResult {
	result := &platform.SecurityPackItemResult{Kind: item.Kind, Name: item.Name}
	fail := func(format string, args ...any) *platform.SecurityPackItemResult {
		result.Action = "failed"
		result.Error = fmt.Sprintf(format, args...)
		return result
	}

	if err := validateResourceName(item.Name); err != nil {
		return fail("invalid name %q", item.Name)
	}
	if len(item.Spec) == 0 {
		return fail("item has no spec")
	}

	// Decode and validate the spec exactly like manual authoring would.
	decoded, err := decodeSecurityPackItem(item, namespace)
	if err != nil {
		return fail("%v", err)
	}
	buildCR, probe, errs := decoded.buildCR, decoded.probe, decoded.errs
	if len(errs) != 0 {
		result.Action = "failed"
		result.Error = "spec failed validation"
		result.ValidationErrors = securityLibraryValidationErrorsToProto(errs)
		return result
	}

	// Collision handling: an existing resource of the same kind/name (or one
	// claimed earlier in this pack) triggers the requested policy.
	exists := func(name string) (bool, error) {
		if claimed[item.Kind+"/"+name] {
			return true, nil
		}
		err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, probe(name))
		if err == nil {
			return true, nil
		}
		if k8serrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	finalName := item.Name
	taken, err := exists(finalName)
	if err != nil {
		return fail("check for existing %s: %v", item.Kind, err)
	}
	if taken {
		switch policy {
		case platform.SecurityPackCollisionPolicy_SECURITY_PACK_COLLISION_POLICY_SKIP:
			result.Action = "skipped"
			result.FinalName = finalName
			return result
		case platform.SecurityPackCollisionPolicy_SECURITY_PACK_COLLISION_POLICY_RENAME:
			renamed := ""
			for i := 2; i <= 99; i++ {
				candidate := fmt.Sprintf("%s-%d", item.Name, i)
				if len(candidate) > maxDNSLabelLen {
					candidate = strings.Trim(candidate[:maxDNSLabelLen], "-")
				}
				taken, err := exists(candidate)
				if err != nil {
					return fail("check for existing %s: %v", item.Kind, err)
				}
				if !taken {
					renamed = candidate
					break
				}
			}
			if renamed == "" {
				return fail("no free name found for %q", item.Name)
			}
			finalName = renamed
		default:
			return fail("%s %q already exists in namespace %q", item.Kind, item.Name, namespace)
		}
	}
	claimed[item.Kind+"/"+finalName] = true
	result.FinalName = finalName

	if !apply {
		result.Action = "would-create"
		return result
	}
	cr := buildCR(finalName)
	if err := s.k8sClient.Create(ctx, cr); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return fail("%s %q already exists in namespace %q", item.Kind, finalName, namespace)
		}
		return fail("create %s %q: %v", item.Kind, finalName, err)
	}
	// Imported scan configs are owned by the importer, exactly like ones
	// created through CreateSecurityScan; an unowned scan would leak to every
	// authenticated user.
	if item.Kind == securityPackKindScan && s.stateStore != nil {
		if actor := requestActorFromContext(ctx); actor.Subject != "" {
			if err := s.stateStore.SetResourceOwner(ctx, securityScanResourceType, finalName, namespace, actor.Subject); err != nil {
				_ = s.k8sClient.Delete(ctx, cr)
				return fail("record ownership for SecurityScan %q: %v", finalName, err)
			}
		}
	}
	if finalName != item.Name {
		result.Action = "renamed"
	} else {
		result.Action = "created"
	}
	return result
}

// validateImportedSecurityScanSpec applies the same field rules the manual
// scan-config path enforces, expressed as structured errors.
func validateImportedSecurityScanSpec(spec *triggersv1alpha1.SecurityScanSpec) []triggersv1alpha1.SecurityWorkflowFieldError {
	var errs []triggersv1alpha1.SecurityWorkflowFieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, triggersv1alpha1.SecurityWorkflowFieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	if strings.TrimSpace(spec.RepoURL) == "" {
		add("repoURL", "repoURL is required")
	}
	if spec.WorkflowRef != nil && len(spec.Workflow) > 0 {
		add("workflowRef", "workflowRef and an inline workflow cannot both be set")
	}
	if len(spec.Workflow) > 0 {
		errs = append(errs, triggersv1alpha1.ValidateSecurityWorkflowTasks(spec.Workflow)...)
	}
	if spec.Parallelism != 0 && (spec.Parallelism < 1 || spec.Parallelism > 16) {
		add("parallelism", "parallelism %d out of range (want 1-16)", spec.Parallelism)
	}
	if spec.Schedule != "" {
		if err := validateSecurityScanSchedule(spec.Schedule, spec.TimeZone); err != nil {
			add("schedule", "%v", err)
		}
	}
	if spec.MinSeverity != "" {
		if err := validateSecuritySeverity("minSeverity", spec.MinSeverity); err != nil {
			add("minSeverity", "%v", err)
		}
	}
	if spec.FailOnSeverity != "" {
		if err := validateSecuritySeverity("failOnSeverity", spec.FailOnSeverity); err != nil {
			add("failOnSeverity", "%v", err)
		}
	}
	if _, err := securityScanConcurrencyPolicy(string(spec.ConcurrencyPolicy)); err != nil {
		add("concurrencyPolicy", "%v", err)
	}
	if e := spec.Execution; e != nil {
		switch e.Mode {
		case "", triggersv1alpha1.SecurityScanExecutionModeCoordinator, triggersv1alpha1.SecurityScanExecutionModeDeterministic:
		default:
			add("execution.mode", "invalid mode %q (want coordinator or deterministic)", e.Mode)
		}
		if e.TaskMaxRetries != nil && (*e.TaskMaxRetries < 0 || *e.TaskMaxRetries > 10) {
			add("execution.taskMaxRetries", "taskMaxRetries %d out of range (want 0-10)", *e.TaskMaxRetries)
		}
		if e.RetryBackoff.Duration < 0 {
			add("execution.retryBackoff", "retryBackoff must not be negative")
		}
	}
	if len(spec.ParameterValues) > 32 {
		add("parameterValues", "at most 32 entries are allowed, got %d", len(spec.ParameterValues))
	}
	for _, name := range sortedKeys(spec.ParameterValues) {
		if len(name) > 63 || !securityScanParameterNamePattern.MatchString(name) {
			add("parameterValues", "invalid parameter name %q (want an identifier like snake_case, at most 63 characters)", name)
		}
	}
	return errs
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func dedupeSortedNames(names []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
