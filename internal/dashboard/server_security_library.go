package dashboard

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/security"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// authorizeSecurityLibraryNamespace resolves the namespace a security library
// operation targets: the caller's personal namespace by default; another
// namespace only for admins. Applied to reads and writes alike so library
// resources are never visible across namespaces to non-admins.
func (s *Server) authorizeSecurityLibraryNamespace(ctx context.Context, requested string) (string, error) {
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return "", err
	}
	if reqNS := strings.TrimSpace(requested); reqNS != "" && reqNS != namespace {
		if actor.Role != "admin" && actor.Role != "owner" {
			return "", connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("you do not have permission to manage security resources in namespace %q", reqNS))
		}
		namespace = reqNS
	}
	return namespace, nil
}

// securityLibraryUsage maps each referenced library resource name of the
// given kind to the (sorted) SecurityScan names referencing it.
func (s *Server) securityLibraryUsage(ctx context.Context, namespace, kind string) (map[string][]string, error) {
	scans := &triggersv1alpha1.SecurityScanList{}
	if err := s.k8sClient.List(ctx, scans, client.InNamespace(namespace)); err != nil {
		return nil, mapK8sError("list SecurityScans", err)
	}
	usage := map[string][]string{}
	for i := range scans.Items {
		scan := &scans.Items[i]
		var refs []string
		switch kind {
		case "SecurityWorkflow":
			if scan.Spec.WorkflowRef != nil {
				refs = []string{scan.Spec.WorkflowRef.Name}
			}
		case "SecurityRanker":
			for _, ref := range scan.Spec.RankerRefs {
				refs = append(refs, ref.Name)
			}
		case "SecurityPostScript":
			for _, ref := range scan.Spec.PostScriptRefs {
				refs = append(refs, ref.Name)
			}
		case "SecurityPolicyPack":
			if scan.Spec.PolicyPackRef != nil {
				refs = []string{scan.Spec.PolicyPackRef.Name}
			}
		}
		for _, name := range refs {
			usage[name] = append(usage[name], scan.Name)
		}
	}
	for name := range usage {
		sort.Strings(usage[name])
	}
	return usage, nil
}

// guardSecurityLibraryDelete blocks deletion while SecurityScans still
// reference the resource. kubectl deletes bypass this guard (no webhook);
// affected scans then report Ready=False/UnresolvedReference at the next run.
func (s *Server) guardSecurityLibraryDelete(ctx context.Context, namespace, kind, name string) error {
	usage, err := s.securityLibraryUsage(ctx, namespace, kind)
	if err != nil {
		return err
	}
	if referencing := usage[name]; len(referencing) > 0 {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("%s %q is still referenced by security scans: %s; detach it from those scans first",
				kind, name, strings.Join(referencing, ", ")))
	}
	return nil
}

func securityLibraryValidationErrorsToProto(errs []triggersv1alpha1.SecurityWorkflowFieldError) []*platform.SecurityWorkflowValidationError {
	out := make([]*platform.SecurityWorkflowValidationError, 0, len(errs))
	for _, e := range errs {
		out = append(out, &platform.SecurityWorkflowValidationError{Field: e.Field, Message: e.Message})
	}
	return out
}

func securityLibraryInvalidArgument(errs []triggersv1alpha1.SecurityWorkflowFieldError) error {
	messages := make([]string, 0, len(errs))
	for _, e := range errs {
		messages = append(messages, e.Error())
	}
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s", strings.Join(messages, "; ")))
}

// --- SecurityWorkflow ---

func securityWorkflowTasksFromProto(pbTasks []*platform.SecurityScanTaskConfig) []triggersv1alpha1.SecurityScanTask {
	tasks := make([]triggersv1alpha1.SecurityScanTask, 0, len(pbTasks))
	for _, t := range pbTasks {
		tasks = append(tasks, triggersv1alpha1.SecurityScanTask{
			Name:        strings.TrimSpace(t.GetName()),
			Objective:   t.GetObjective(),
			Category:    strings.TrimSpace(t.GetCategory()),
			DependsOn:   trimmedNonEmpty(t.GetDependsOn()),
			Role:        strings.TrimSpace(t.GetRole()),
			Model:       strings.TrimSpace(t.GetModel()),
			MaxFindings: t.GetMaxFindings(),
		})
	}
	return tasks
}

func securityWorkflowSpecFromProto(pb *platform.SecurityWorkflowResource) (triggersv1alpha1.SecurityWorkflowSpec, error) {
	if pb == nil {
		return triggersv1alpha1.SecurityWorkflowSpec{}, invalidArgument("workflow is required")
	}
	if err := validateResourceName(pb.GetName()); err != nil {
		return triggersv1alpha1.SecurityWorkflowSpec{}, err
	}
	if p := pb.GetParallelism(); p != 0 && (p < 1 || p > 16) {
		return triggersv1alpha1.SecurityWorkflowSpec{}, invalidArgument(
			"parallelism %d out of range (want 0 for none, or 1-16)", p)
	}
	tasks := securityWorkflowTasksFromProto(pb.GetTasks())
	if errs := triggersv1alpha1.ValidateSecurityWorkflowTasks(tasks); len(errs) != 0 {
		return triggersv1alpha1.SecurityWorkflowSpec{}, securityLibraryInvalidArgument(errs)
	}
	return triggersv1alpha1.SecurityWorkflowSpec{
		Description: strings.TrimSpace(pb.GetDescription()),
		Tasks:       tasks,
		Parallelism: pb.GetParallelism(),
	}, nil
}

func securityWorkflowToProto(cr *triggersv1alpha1.SecurityWorkflow, referencing []string) *platform.SecurityWorkflowResource {
	pb := &platform.SecurityWorkflowResource{
		Namespace:        cr.Namespace,
		Name:             cr.Name,
		Description:      cr.Spec.Description,
		Parallelism:      cr.Spec.Parallelism,
		UsageCount:       int32(len(referencing)), //nolint:gosec // scan counts stay far below int32 bounds
		ReferencingScans: referencing,
		Generation:       cr.Generation,
		CreatedAtUnix:    cr.CreationTimestamp.Unix(),
	}
	for _, t := range cr.Spec.Tasks {
		pb.Tasks = append(pb.Tasks, &platform.SecurityScanTaskConfig{
			Name:        t.Name,
			Objective:   t.Objective,
			Category:    t.Category,
			DependsOn:   append([]string(nil), t.DependsOn...),
			Role:        t.Role,
			Model:       t.Model,
			MaxFindings: t.MaxFindings,
		})
	}
	return pb
}

func (s *Server) ListSecurityWorkflows(ctx context.Context, req *platform.ListSecurityWorkflowsRequest) (*platform.ListSecurityWorkflowsResponse, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	list := &triggersv1alpha1.SecurityWorkflowList{}
	if err := s.k8sClient.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, mapK8sError("list SecurityWorkflows", err)
	}
	usage, err := s.securityLibraryUsage(ctx, namespace, "SecurityWorkflow")
	if err != nil {
		return nil, err
	}
	resp := &platform.ListSecurityWorkflowsResponse{}
	for i := range list.Items {
		cr := &list.Items[i]
		resp.Workflows = append(resp.Workflows, securityWorkflowToProto(cr, usage[cr.Name]))
	}
	sort.Slice(resp.Workflows, func(i, j int) bool { return resp.Workflows[i].Name < resp.Workflows[j].Name })
	return resp, nil
}

func (s *Server) GetSecurityWorkflow(ctx context.Context, req *platform.GetSecurityWorkflowRequest) (*platform.SecurityWorkflowResource, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	if err := validateResourceName(req.GetName()); err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityWorkflow{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: req.GetName()}, cr); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get SecurityWorkflow %s/%s", namespace, req.GetName()), err)
	}
	usage, err := s.securityLibraryUsage(ctx, namespace, "SecurityWorkflow")
	if err != nil {
		return nil, err
	}
	return securityWorkflowToProto(cr, usage[cr.Name]), nil
}

func (s *Server) CreateSecurityWorkflow(ctx context.Context, req *platform.CreateSecurityWorkflowRequest) (*platform.SecurityWorkflowResource, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetWorkflow().GetNamespace())
	if err != nil {
		return nil, err
	}
	spec, err := securityWorkflowSpecFromProto(req.GetWorkflow())
	if err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: req.GetWorkflow().GetName(), Namespace: namespace},
		Spec:       spec,
	}
	if err := s.k8sClient.Create(ctx, cr); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("SecurityWorkflow %s/%s already exists", namespace, cr.Name))
		}
		return nil, mapK8sError("create SecurityWorkflow", err)
	}
	return securityWorkflowToProto(cr, nil), nil
}

func (s *Server) UpdateSecurityWorkflow(ctx context.Context, req *platform.UpdateSecurityWorkflowRequest) (*platform.SecurityWorkflowResource, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetWorkflow().GetNamespace())
	if err != nil {
		return nil, err
	}
	spec, err := securityWorkflowSpecFromProto(req.GetWorkflow())
	if err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityWorkflow{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: req.GetWorkflow().GetName()}, cr); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get SecurityWorkflow %s/%s", namespace, req.GetWorkflow().GetName()), err)
	}
	cr.Spec = spec
	if err := s.k8sClient.Update(ctx, cr); err != nil {
		return nil, mapK8sError("update SecurityWorkflow", err)
	}
	usage, err := s.securityLibraryUsage(ctx, namespace, "SecurityWorkflow")
	if err != nil {
		return nil, err
	}
	return securityWorkflowToProto(cr, usage[cr.Name]), nil
}

func (s *Server) DeleteSecurityWorkflow(ctx context.Context, req *platform.DeleteSecurityWorkflowRequest) (*emptypb.Empty, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	if err := validateResourceName(req.GetName()); err != nil {
		return nil, err
	}
	if err := s.guardSecurityLibraryDelete(ctx, namespace, "SecurityWorkflow", req.GetName()); err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityWorkflow{ObjectMeta: metav1.ObjectMeta{Name: req.GetName(), Namespace: namespace}}
	if err := s.k8sClient.Delete(ctx, cr); err != nil && !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("delete SecurityWorkflow", err)
	}
	return &emptypb.Empty{}, nil
}

// ValidateSecurityWorkflow runs the full workflow validation without
// persisting anything and returns structured field errors. Create and Update
// run the same validation.
func (s *Server) ValidateSecurityWorkflow(ctx context.Context, req *platform.ValidateSecurityWorkflowRequest) (*platform.ValidateSecurityWorkflowResponse, error) {
	if _, err := s.authorizeSecurityLibraryNamespace(ctx, ""); err != nil {
		return nil, err
	}
	tasks := securityWorkflowTasksFromProto(req.GetTasks())
	errs := triggersv1alpha1.ValidateSecurityWorkflowTasks(tasks)
	if p := req.GetParallelism(); p != 0 && (p < 1 || p > 16) {
		errs = append(errs, triggersv1alpha1.SecurityWorkflowFieldError{
			Field:   "parallelism",
			Message: fmt.Sprintf("parallelism %d out of range (want 0 for none, or 1-16)", p),
		})
	}
	return &platform.ValidateSecurityWorkflowResponse{
		Valid:  len(errs) == 0,
		Errors: securityLibraryValidationErrorsToProto(errs),
	}, nil
}

// --- SecurityRanker ---

func securityRankerSpecFromProto(pb *platform.SecurityRankerResource) (triggersv1alpha1.SecurityRankerSpec, error) {
	if pb == nil {
		return triggersv1alpha1.SecurityRankerSpec{}, invalidArgument("ranker is required")
	}
	if err := validateResourceName(pb.GetName()); err != nil {
		return triggersv1alpha1.SecurityRankerSpec{}, err
	}
	rules := pb.GetRules()
	if errs := triggersv1alpha1.ValidateSecurityRankerRules(rules); len(errs) != 0 {
		return triggersv1alpha1.SecurityRankerSpec{}, securityLibraryInvalidArgument(errs)
	}
	// The rules language is permissive (directives plus prose); parsing here
	// keeps the dashboard aligned with how scan runs interpret the text.
	security.ParseRankRules(strings.Join(rules, "\n"))
	return triggersv1alpha1.SecurityRankerSpec{
		Description: strings.TrimSpace(pb.GetDescription()),
		Rules:       append([]string(nil), rules...),
	}, nil
}

func securityRankerToProto(cr *triggersv1alpha1.SecurityRanker, referencing []string) *platform.SecurityRankerResource {
	return &platform.SecurityRankerResource{
		Namespace:        cr.Namespace,
		Name:             cr.Name,
		Description:      cr.Spec.Description,
		Rules:            append([]string(nil), cr.Spec.Rules...),
		UsageCount:       int32(len(referencing)), //nolint:gosec // scan counts stay far below int32 bounds
		ReferencingScans: referencing,
		Generation:       cr.Generation,
		CreatedAtUnix:    cr.CreationTimestamp.Unix(),
	}
}

func (s *Server) ListSecurityRankers(ctx context.Context, req *platform.ListSecurityRankersRequest) (*platform.ListSecurityRankersResponse, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	list := &triggersv1alpha1.SecurityRankerList{}
	if err := s.k8sClient.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, mapK8sError("list SecurityRankers", err)
	}
	usage, err := s.securityLibraryUsage(ctx, namespace, "SecurityRanker")
	if err != nil {
		return nil, err
	}
	resp := &platform.ListSecurityRankersResponse{}
	for i := range list.Items {
		cr := &list.Items[i]
		resp.Rankers = append(resp.Rankers, securityRankerToProto(cr, usage[cr.Name]))
	}
	sort.Slice(resp.Rankers, func(i, j int) bool { return resp.Rankers[i].Name < resp.Rankers[j].Name })
	return resp, nil
}

func (s *Server) GetSecurityRanker(ctx context.Context, req *platform.GetSecurityRankerRequest) (*platform.SecurityRankerResource, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	if err := validateResourceName(req.GetName()); err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityRanker{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: req.GetName()}, cr); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get SecurityRanker %s/%s", namespace, req.GetName()), err)
	}
	usage, err := s.securityLibraryUsage(ctx, namespace, "SecurityRanker")
	if err != nil {
		return nil, err
	}
	return securityRankerToProto(cr, usage[cr.Name]), nil
}

func (s *Server) CreateSecurityRanker(ctx context.Context, req *platform.CreateSecurityRankerRequest) (*platform.SecurityRankerResource, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetRanker().GetNamespace())
	if err != nil {
		return nil, err
	}
	spec, err := securityRankerSpecFromProto(req.GetRanker())
	if err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityRanker{
		ObjectMeta: metav1.ObjectMeta{Name: req.GetRanker().GetName(), Namespace: namespace},
		Spec:       spec,
	}
	if err := s.k8sClient.Create(ctx, cr); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("SecurityRanker %s/%s already exists", namespace, cr.Name))
		}
		return nil, mapK8sError("create SecurityRanker", err)
	}
	return securityRankerToProto(cr, nil), nil
}

func (s *Server) UpdateSecurityRanker(ctx context.Context, req *platform.UpdateSecurityRankerRequest) (*platform.SecurityRankerResource, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetRanker().GetNamespace())
	if err != nil {
		return nil, err
	}
	spec, err := securityRankerSpecFromProto(req.GetRanker())
	if err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityRanker{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: req.GetRanker().GetName()}, cr); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get SecurityRanker %s/%s", namespace, req.GetRanker().GetName()), err)
	}
	cr.Spec = spec
	if err := s.k8sClient.Update(ctx, cr); err != nil {
		return nil, mapK8sError("update SecurityRanker", err)
	}
	usage, err := s.securityLibraryUsage(ctx, namespace, "SecurityRanker")
	if err != nil {
		return nil, err
	}
	return securityRankerToProto(cr, usage[cr.Name]), nil
}

func (s *Server) DeleteSecurityRanker(ctx context.Context, req *platform.DeleteSecurityRankerRequest) (*emptypb.Empty, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	if err := validateResourceName(req.GetName()); err != nil {
		return nil, err
	}
	if err := s.guardSecurityLibraryDelete(ctx, namespace, "SecurityRanker", req.GetName()); err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityRanker{ObjectMeta: metav1.ObjectMeta{Name: req.GetName(), Namespace: namespace}}
	if err := s.k8sClient.Delete(ctx, cr); err != nil && !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("delete SecurityRanker", err)
	}
	return &emptypb.Empty{}, nil
}

// --- SecurityPostScript ---

func securityPostScriptSpecFromProto(pb *platform.SecurityPostScriptResource) (triggersv1alpha1.SecurityPostScriptSpec, error) {
	if pb == nil {
		return triggersv1alpha1.SecurityPostScriptSpec{}, invalidArgument("post_script is required")
	}
	if err := validateResourceName(pb.GetName()); err != nil {
		return triggersv1alpha1.SecurityPostScriptSpec{}, err
	}
	spec := triggersv1alpha1.SecurityPostScriptSpec{
		Description: strings.TrimSpace(pb.GetDescription()),
		Prompt:      pb.GetPrompt(),
		RunOn:       strings.TrimSpace(pb.GetRunOn()),
	}
	if errs := triggersv1alpha1.ValidateSecurityPostScriptSpec(spec); len(errs) != 0 {
		return triggersv1alpha1.SecurityPostScriptSpec{}, securityLibraryInvalidArgument(errs)
	}
	return spec, nil
}

func securityPostScriptToProto(cr *triggersv1alpha1.SecurityPostScript, referencing []string) *platform.SecurityPostScriptResource {
	return &platform.SecurityPostScriptResource{
		Namespace:        cr.Namespace,
		Name:             cr.Name,
		Description:      cr.Spec.Description,
		Prompt:           cr.Spec.Prompt,
		RunOn:            cr.Spec.RunOn,
		UsageCount:       int32(len(referencing)), //nolint:gosec // scan counts stay far below int32 bounds
		ReferencingScans: referencing,
		Generation:       cr.Generation,
		CreatedAtUnix:    cr.CreationTimestamp.Unix(),
	}
}

func (s *Server) ListSecurityPostScripts(ctx context.Context, req *platform.ListSecurityPostScriptsRequest) (*platform.ListSecurityPostScriptsResponse, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	list := &triggersv1alpha1.SecurityPostScriptList{}
	if err := s.k8sClient.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, mapK8sError("list SecurityPostScripts", err)
	}
	usage, err := s.securityLibraryUsage(ctx, namespace, "SecurityPostScript")
	if err != nil {
		return nil, err
	}
	resp := &platform.ListSecurityPostScriptsResponse{}
	for i := range list.Items {
		cr := &list.Items[i]
		resp.PostScripts = append(resp.PostScripts, securityPostScriptToProto(cr, usage[cr.Name]))
	}
	sort.Slice(resp.PostScripts, func(i, j int) bool { return resp.PostScripts[i].Name < resp.PostScripts[j].Name })
	return resp, nil
}

func (s *Server) GetSecurityPostScript(ctx context.Context, req *platform.GetSecurityPostScriptRequest) (*platform.SecurityPostScriptResource, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	if err := validateResourceName(req.GetName()); err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityPostScript{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: req.GetName()}, cr); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get SecurityPostScript %s/%s", namespace, req.GetName()), err)
	}
	usage, err := s.securityLibraryUsage(ctx, namespace, "SecurityPostScript")
	if err != nil {
		return nil, err
	}
	return securityPostScriptToProto(cr, usage[cr.Name]), nil
}

func (s *Server) CreateSecurityPostScript(ctx context.Context, req *platform.CreateSecurityPostScriptRequest) (*platform.SecurityPostScriptResource, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetPostScript().GetNamespace())
	if err != nil {
		return nil, err
	}
	spec, err := securityPostScriptSpecFromProto(req.GetPostScript())
	if err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityPostScript{
		ObjectMeta: metav1.ObjectMeta{Name: req.GetPostScript().GetName(), Namespace: namespace},
		Spec:       spec,
	}
	if err := s.k8sClient.Create(ctx, cr); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("SecurityPostScript %s/%s already exists", namespace, cr.Name))
		}
		return nil, mapK8sError("create SecurityPostScript", err)
	}
	return securityPostScriptToProto(cr, nil), nil
}

func (s *Server) UpdateSecurityPostScript(ctx context.Context, req *platform.UpdateSecurityPostScriptRequest) (*platform.SecurityPostScriptResource, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetPostScript().GetNamespace())
	if err != nil {
		return nil, err
	}
	spec, err := securityPostScriptSpecFromProto(req.GetPostScript())
	if err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityPostScript{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: req.GetPostScript().GetName()}, cr); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get SecurityPostScript %s/%s", namespace, req.GetPostScript().GetName()), err)
	}
	cr.Spec = spec
	if err := s.k8sClient.Update(ctx, cr); err != nil {
		return nil, mapK8sError("update SecurityPostScript", err)
	}
	usage, err := s.securityLibraryUsage(ctx, namespace, "SecurityPostScript")
	if err != nil {
		return nil, err
	}
	return securityPostScriptToProto(cr, usage[cr.Name]), nil
}

func (s *Server) DeleteSecurityPostScript(ctx context.Context, req *platform.DeleteSecurityPostScriptRequest) (*emptypb.Empty, error) {
	namespace, err := s.authorizeSecurityLibraryNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	if err := validateResourceName(req.GetName()); err != nil {
		return nil, err
	}
	if err := s.guardSecurityLibraryDelete(ctx, namespace, "SecurityPostScript", req.GetName()); err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.SecurityPostScript{ObjectMeta: metav1.ObjectMeta{Name: req.GetName(), Namespace: namespace}}
	if err := s.k8sClient.Delete(ctx, cr); err != nil && !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("delete SecurityPostScript", err)
	}
	return &emptypb.Empty{}, nil
}
