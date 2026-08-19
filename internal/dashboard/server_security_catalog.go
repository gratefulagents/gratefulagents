package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

type securityCatalogKey struct {
	kind platform.SecurityCatalogKind
	name string
}

type securityCatalogDependency struct {
	key      securityCatalogKey
	required bool
}

type securityCatalogItem struct {
	key          securityCatalogKey
	source       client.Object
	title        string
	description  string
	dependencies []securityCatalogDependency
	ready        bool
	readyMessage string
	state        platform.SecurityCatalogInstallState
}

type securityCatalogSnapshot struct {
	revision     string
	ready        bool
	readyMessage string
	items        map[securityCatalogKey]*securityCatalogItem
	ordered      []*securityCatalogItem
}

var errSecurityCatalogChanged = errors.New("catalog install state changed; refresh the catalog and try again")

func (s *Server) ListSecurityCatalog(ctx context.Context, _ *emptypb.Empty) (*platform.SecurityCatalog, error) {
	namespace, err := s.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		return nil, err
	}
	snapshot, err := s.loadSecurityCatalog(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return snapshot.toProto(), nil
}

func (s *Server) DryRunSecurityCatalogInstall(ctx context.Context, req *platform.SecurityCatalogInstallRequest) (*platform.SecurityCatalogInstallResponse, error) {
	return s.securityCatalogInstall(ctx, req, false)
}

func (s *Server) ApplySecurityCatalogInstall(ctx context.Context, req *platform.SecurityCatalogInstallRequest) (*platform.SecurityCatalogInstallResponse, error) {
	return s.securityCatalogInstall(ctx, req, true)
}

func (s *Server) securityCatalogInstall(ctx context.Context, req *platform.SecurityCatalogInstallRequest, apply bool) (*platform.SecurityCatalogInstallResponse, error) {
	if req == nil {
		return nil, invalidArgument("request is required")
	}
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.loadSecurityCatalog(ctx, namespace)
	if err != nil {
		return nil, err
	}
	if req.GetCatalogRevision() != snapshot.revision {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("security catalog revision is stale; refresh the catalog and try again"))
	}
	order, blocked, err := snapshot.installOrder(req.GetResources())
	if err != nil {
		return nil, err
	}
	response := &platform.SecurityCatalogInstallResponse{CatalogRevision: snapshot.revision}
	resultByKey := make(map[securityCatalogKey]*platform.SecurityCatalogInstallResult, len(order))
	for _, item := range order {
		action, message := securityCatalogPlannedAction(snapshot, item, blocked[item.key], resultByKey)
		result := &platform.SecurityCatalogInstallResult{Entry: item.toProto(), Action: action, Message: message}
		resultByKey[item.key] = result
		response.Results = append(response.Results, result)
	}
	planRevision, err := s.securityCatalogPlanRevision(ctx, namespace, actor.Subject, snapshot, req.GetResources(), order, resultByKey)
	if err != nil {
		return nil, err
	}
	response.PlanRevision = planRevision
	if !apply {
		return response, nil
	}
	if req.GetPlanRevision() != planRevision {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("security catalog installation plan revision is stale; review the plan again before applying"))
	}
	for _, item := range order {
		result := resultByKey[item.key]
		if message := securityCatalogFailedDependency(item, resultByKey); message != "" {
			result.Action = "blocked"
			result.Message = message
			continue
		}
		if result.Action == "blocked" || (result.Action == "unchanged" && securityCatalogOwnershipResourceType(item.key.kind) == "") {
			continue
		}
		installedAction, mutated, installErr := s.installSecurityCatalogItem(ctx, namespace, actor.Subject, item)
		response.Applied = response.Applied || mutated
		if installErr != nil {
			result.Action = "failed"
			result.Message = installErr.Error()
			continue
		}
		result.Action = installedAction
		result.Entry.InstallState = platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_INSTALLED
	}
	return response, nil
}

func (s *Server) securityCatalogPlanRevision(ctx context.Context, namespace, ownerID string, snapshot *securityCatalogSnapshot, roots []*platform.SecurityCatalogRef, order []*securityCatalogItem, results map[securityCatalogKey]*platform.SecurityCatalogInstallResult) (string, error) {
	type planRoot struct {
		Kind int32
		Name string
	}
	type planItem struct {
		Kind             int32
		Name             string
		Action           string
		InstallState     int32
		Present          bool
		SpecHash         string
		RecordedSpecHash string
		SourceNamespace  string
		OwnerPresent     bool
		OwnerID          string
	}
	planRoots := make([]planRoot, 0, len(roots))
	for _, root := range roots {
		planRoots = append(planRoots, planRoot{Kind: int32(root.GetKind()), Name: strings.TrimSpace(root.GetName())})
	}
	planItems := make([]planItem, 0, len(order))
	for _, item := range order {
		fingerprint := planItem{Kind: int32(item.key.kind), Name: item.key.name, Action: results[item.key].GetAction()}
		current := emptyBootstrapResource(item.source)
		err := s.apiReaderOrClient().Get(ctx, client.ObjectKey{Namespace: namespace, Name: item.key.name}, current)
		if err != nil && !k8serrors.IsNotFound(err) {
			return "", mapK8sError(fmt.Sprintf("read installed catalog %s %s", securityCatalogKindName(item.key.kind), item.key.name), err)
		}
		if err == nil {
			fingerprint.Present = true
			fingerprint.SpecHash, err = bootstrapSpecHash(current)
			if err != nil {
				return "", err
			}
			annotations := current.GetAnnotations()
			fingerprint.RecordedSpecHash = annotations[bootstrapSpecHashAnnotation]
			fingerprint.SourceNamespace = annotations[bootstrapSourceAnnotation]
			state, stateErr := securityCatalogInstallStateForCurrent(item.source, current)
			if stateErr != nil {
				return "", stateErr
			}
			fingerprint.InstallState = int32(state)
		} else {
			fingerprint.InstallState = int32(platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_NOT_INSTALLED)
		}
		resourceType := securityCatalogOwnershipResourceType(item.key.kind)
		if s.stateStore != nil && resourceType != "" && strings.TrimSpace(ownerID) != "" {
			ownership, ownerErr := s.stateStore.GetResourceOwner(ctx, resourceType, item.key.name, namespace)
			if ownerErr != nil {
				return "", connect.NewError(connect.CodeInternal, fmt.Errorf("read ownership for %s %s/%s: %w", securityCatalogKindName(item.key.kind), namespace, item.key.name, ownerErr))
			}
			if ownership != nil {
				fingerprint.OwnerPresent = true
				fingerprint.OwnerID = ownership.OwnerID
			}
		}
		planItems = append(planItems, fingerprint)
	}
	encoded, err := json.Marshal(struct {
		CatalogRevision string
		Roots           []planRoot
		Items           []planItem
	}{snapshot.revision, planRoots, planItems})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func securityCatalogFailedDependency(item *securityCatalogItem, results map[securityCatalogKey]*platform.SecurityCatalogInstallResult) string {
	for _, dependency := range item.dependencies {
		if !dependency.required {
			continue
		}
		if result := results[dependency.key]; result != nil && (result.Action == "blocked" || result.Action == "failed") {
			return fmt.Sprintf("required dependency %s/%s %s", securityCatalogKindName(dependency.key.kind), dependency.key.name, result.Action)
		}
	}
	return ""
}

func securityCatalogPlannedAction(snapshot *securityCatalogSnapshot, item *securityCatalogItem, blockedMessage string, previous map[securityCatalogKey]*platform.SecurityCatalogInstallResult) (string, string) {
	if !snapshot.ready {
		return "blocked", snapshot.readyMessage
	}
	if blockedMessage != "" {
		return "blocked", blockedMessage
	}
	if !item.ready {
		return "blocked", item.readyMessage
	}
	for _, dependency := range item.dependencies {
		if !dependency.required {
			continue
		}
		if result := previous[dependency.key]; result != nil && result.Action == "blocked" {
			return "blocked", fmt.Sprintf("required dependency %s/%s is blocked", securityCatalogKindName(dependency.key.kind), dependency.key.name)
		}
	}
	switch item.state {
	case platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_NOT_INSTALLED:
		return "create", ""
	case platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_INSTALLED:
		return "unchanged", "already installed"
	case platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_UPDATE_AVAILABLE:
		return "refresh", ""
	case platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_MODIFIED:
		return "blocked", "the installed catalog resource was modified"
	case platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_CONFLICT:
		return "blocked", "an unrelated same-name resource already exists"
	default:
		return "blocked", "install state is unavailable"
	}
}

func (s *Server) loadSecurityCatalog(ctx context.Context, destinationNamespace string) (*securityCatalogSnapshot, error) {
	snapshot := &securityCatalogSnapshot{items: map[securityCatalogKey]*securityCatalogItem{}}
	sourceNamespace := strings.TrimSpace(os.Getenv("POD_NAMESPACE"))
	if sourceNamespace == "" {
		snapshot.readyMessage = "POD_NAMESPACE is not configured"
		snapshot.revision = securityCatalogRevision("", "", snapshot.items)
		return snapshot, nil
	}
	bundleVersion, bundleReady, err := bootstrapBundleVersion(ctx, s.apiReaderOrClient(), sourceNamespace)
	if err != nil {
		return nil, err
	}
	snapshot.ready = bundleReady
	if !bundleReady {
		snapshot.readyMessage = "the shipped security catalog is not ready"
	}
	if err := s.listSecurityCatalogSources(ctx, sourceNamespace, snapshot); err != nil {
		return nil, err
	}
	for _, item := range snapshot.items {
		state, err := s.securityCatalogInstallState(ctx, destinationNamespace, item.source)
		if err != nil {
			return nil, err
		}
		item.state = state
		for _, dependency := range item.dependencies {
			if dependency.required && snapshot.items[dependency.key] == nil {
				item.ready = false
				item.readyMessage = fmt.Sprintf("required dependency %s/%s is missing from the catalog", securityCatalogKindName(dependency.key.kind), dependency.key.name)
			}
		}
		snapshot.ordered = append(snapshot.ordered, item)
	}
	sort.Slice(snapshot.ordered, func(i, j int) bool {
		if snapshot.ordered[i].key.kind != snapshot.ordered[j].key.kind {
			return snapshot.ordered[i].key.kind < snapshot.ordered[j].key.kind
		}
		return snapshot.ordered[i].key.name < snapshot.ordered[j].key.name
	})
	snapshot.revision = securityCatalogRevision(bundleVersion, sourceNamespace, snapshot.items)
	return snapshot, nil
}

func (s *Server) listSecurityCatalogSources(ctx context.Context, namespace string, snapshot *securityCatalogSnapshot) error {
	reader := s.apiReaderOrClient()
	var skills platformv1alpha1.SkillList
	if err := reader.List(ctx, &skills, client.InNamespace(namespace)); err != nil {
		return mapK8sError("list catalog Skills", err)
	}
	for i := range skills.Items {
		source := &skills.Items[i]
		if isBootstrapDefault(source) && source.Annotations[securitySkillBundleAnnotation] == "true" {
			description := source.Spec.Description
			if description == "" && source.Status.Resolved != nil {
				description = source.Status.Resolved.Description
			}
			ready := source.Status.ObservedGeneration == source.Generation && source.Status.Resolved != nil && source.Status.Phase == "Ready"
			message := "Skill content has not been resolved"
			if ready {
				message = ""
			}
			snapshot.add(source, platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_SKILL, catalogTitle(source.Name), description, ready, message)
		}
	}

	var workflows triggersv1alpha1.SecurityWorkflowList
	if err := reader.List(ctx, &workflows, client.InNamespace(namespace)); err != nil {
		return mapK8sError("list catalog SecurityWorkflows", err)
	}
	for i := range workflows.Items {
		source := &workflows.Items[i]
		if !isBootstrapDefault(source) {
			continue
		}
		item := snapshot.add(source, platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_WORKFLOW, catalogTitle(source.Name), source.Spec.Description, securityLibraryObjectReady(source.Generation, source.Status), securityLibraryReadyMessage(source.Generation, source.Status))
		for _, task := range source.Spec.Tasks {
			for _, ref := range task.SkillRefs {
				item.addDependency(platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_SKILL, ref.Name, true)
			}
		}
	}

	var rankers triggersv1alpha1.SecurityRankerList
	if err := reader.List(ctx, &rankers, client.InNamespace(namespace)); err != nil {
		return mapK8sError("list catalog SecurityRankers", err)
	}
	for i := range rankers.Items {
		source := &rankers.Items[i]
		if isBootstrapDefault(source) {
			snapshot.add(source, platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_RANKER, catalogTitle(source.Name), source.Spec.Description, securityLibraryObjectReady(source.Generation, source.Status), securityLibraryReadyMessage(source.Generation, source.Status))
		}
	}

	var scripts triggersv1alpha1.SecurityPostScriptList
	if err := reader.List(ctx, &scripts, client.InNamespace(namespace)); err != nil {
		return mapK8sError("list catalog SecurityPostScripts", err)
	}
	for i := range scripts.Items {
		source := &scripts.Items[i]
		if isBootstrapDefault(source) {
			snapshot.add(source, platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_POST_SCRIPT, catalogTitle(source.Name), source.Spec.Description, securityLibraryObjectReady(source.Generation, source.Status), securityLibraryReadyMessage(source.Generation, source.Status))
		}
	}

	var packs triggersv1alpha1.SecurityPolicyPackList
	if err := reader.List(ctx, &packs, client.InNamespace(namespace)); err != nil {
		return mapK8sError("list catalog SecurityPolicyPacks", err)
	}
	for i := range packs.Items {
		source := &packs.Items[i]
		if !isBootstrapDefault(source) {
			continue
		}
		item := snapshot.add(source, platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_POLICY_PACK, catalogTitle(source.Name), source.Spec.Description, securityLibraryObjectReady(source.Generation, source.Status), securityLibraryReadyMessage(source.Generation, source.Status))
		for _, ref := range source.Spec.DefaultRankerRefs {
			item.addDependency(platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_RANKER, ref.Name, true)
		}
		for _, ref := range source.Spec.DefaultPostScriptRefs {
			item.addDependency(platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_POST_SCRIPT, ref.Name, true)
		}
	}

	var programs triggersv1alpha1.SecurityProgramList
	if err := reader.List(ctx, &programs, client.InNamespace(namespace)); err != nil {
		return mapK8sError("list catalog SecurityPrograms", err)
	}
	for i := range programs.Items {
		source := &programs.Items[i]
		if !isBootstrapDefault(source) {
			continue
		}
		description := strings.TrimSpace(source.Spec.Provider)
		if description != "" {
			description += " security program"
		}
		status := securityProgramCatalogStatus(source.Status)
		item := snapshot.add(source, platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_PROGRAM, source.Spec.DisplayName, description, securityLibraryObjectReady(source.Generation, status), securityLibraryReadyMessage(source.Generation, status))
		for _, target := range source.Spec.ScanTargets {
			item.addDependency(platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_WORKFLOW, target.WorkflowRef, true)
			item.addDependency(platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_POLICY_PACK, target.PolicyPackRef, true)
		}
		if target := source.Spec.ScanTarget; target != nil {
			item.addDependency(platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_WORKFLOW, target.WorkflowRef, true)
			item.addDependency(platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_POLICY_PACK, target.PolicyPackRef, true)
		}
		if severity := catalogSeverityRankerName(source.Spec.SeveritySystem); severity != "" {
			item.addDependency(platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_RANKER, severity, false)
		}
	}

	for _, item := range snapshot.items {
		filtered := item.dependencies[:0]
		for _, dependency := range item.dependencies {
			if dependency.required || snapshot.items[dependency.key] != nil {
				filtered = append(filtered, dependency)
			}
		}
		item.dependencies = filtered
		sort.Slice(item.dependencies, func(i, j int) bool {
			if item.dependencies[i].key.kind != item.dependencies[j].key.kind {
				return item.dependencies[i].key.kind < item.dependencies[j].key.kind
			}
			return item.dependencies[i].key.name < item.dependencies[j].key.name
		})
	}
	return nil
}

func (snapshot *securityCatalogSnapshot) add(source client.Object, kind platform.SecurityCatalogKind, title, description string, ready bool, readyMessage string) *securityCatalogItem {
	key := securityCatalogKey{kind: kind, name: source.GetName()}
	if strings.TrimSpace(title) == "" {
		title = catalogTitle(source.GetName())
	}
	item := &securityCatalogItem{key: key, source: source.DeepCopyObject().(client.Object), title: title, description: description, ready: ready, readyMessage: readyMessage}
	snapshot.items[key] = item
	return item
}

func (item *securityCatalogItem) addDependency(kind platform.SecurityCatalogKind, name string, required bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	key := securityCatalogKey{kind: kind, name: name}
	for index := range item.dependencies {
		if item.dependencies[index].key == key {
			item.dependencies[index].required = item.dependencies[index].required || required
			return
		}
	}
	item.dependencies = append(item.dependencies, securityCatalogDependency{key: key, required: required})
}

func securityLibraryObjectReady(generation int64, status triggersv1alpha1.SecurityLibraryResourceStatus) bool {
	condition := meta.FindStatusCondition(status.Conditions, triggersv1alpha1.ConditionSecurityLibraryReady)
	return status.ObservedGeneration == generation && condition != nil && condition.ObservedGeneration == generation && condition.Status == metav1.ConditionTrue
}

func securityLibraryReadyMessage(generation int64, status triggersv1alpha1.SecurityLibraryResourceStatus) string {
	condition := meta.FindStatusCondition(status.Conditions, triggersv1alpha1.ConditionSecurityLibraryReady)
	if condition == nil || status.ObservedGeneration != generation || condition.ObservedGeneration != generation {
		return "resource validation is pending"
	}
	if condition.Status != metav1.ConditionTrue {
		if condition.Message != "" {
			return condition.Message
		}
		return condition.Reason
	}
	return ""
}

func securityProgramCatalogStatus(status triggersv1alpha1.SecurityProgramStatus) triggersv1alpha1.SecurityLibraryResourceStatus {
	return triggersv1alpha1.SecurityLibraryResourceStatus{
		ObservedGeneration: status.ObservedGeneration,
		ReferencedBy:       status.ReferencedBy,
		Conditions:         status.Conditions,
	}
}

func catalogSeverityRankerName(severitySystem string) string {
	severitySystem = strings.TrimSpace(strings.ToLower(severitySystem))
	return strings.Trim(strings.NewReplacer(".", "-", "_", "-", " ", "-").Replace(severitySystem), "-")
}

func catalogTitle(name string) string {
	words := strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(name))
	for i := range words {
		if words[i] != "" {
			words[i] = strings.ToUpper(words[i][:1]) + words[i][1:]
		}
	}
	return strings.Join(words, " ")
}

func securityCatalogRevision(bundleVersion, namespace string, items map[securityCatalogKey]*securityCatalogItem) string {
	type revisionEntry struct {
		Kind         int32
		Name         string
		SpecHash     string
		Ready        bool
		Dependencies []securityCatalogDependency
	}
	entries := make([]revisionEntry, 0, len(items))
	for _, item := range items {
		hash, _ := bootstrapSpecHash(item.source)
		entries = append(entries, revisionEntry{Kind: int32(item.key.kind), Name: item.key.name, SpecHash: hash, Ready: item.ready, Dependencies: item.dependencies})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].Name < entries[j].Name
	})
	encoded, _ := json.Marshal(struct {
		BundleVersion string
		Namespace     string
		Entries       []revisionEntry
	}{bundleVersion, namespace, entries})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (snapshot *securityCatalogSnapshot) toProto() *platform.SecurityCatalog {
	catalog := &platform.SecurityCatalog{Revision: snapshot.revision, Ready: snapshot.ready, ReadinessMessage: snapshot.readyMessage}
	for _, item := range snapshot.ordered {
		catalog.Entries = append(catalog.Entries, item.toProto())
	}
	return catalog
}

func (item *securityCatalogItem) toProto() *platform.SecurityCatalogEntry {
	entry := &platform.SecurityCatalogEntry{
		Resource:         &platform.SecurityCatalogRef{Kind: item.key.kind, Name: item.key.name},
		Title:            item.title,
		Description:      item.description,
		Ready:            item.ready,
		ReadinessMessage: item.readyMessage,
		InstallState:     item.state,
	}
	for _, dependency := range item.dependencies {
		entry.Dependencies = append(entry.Dependencies, &platform.SecurityCatalogDependency{
			Resource: &platform.SecurityCatalogRef{Kind: dependency.key.kind, Name: dependency.key.name},
			Required: dependency.required,
		})
	}
	return entry
}

func (snapshot *securityCatalogSnapshot) installOrder(refs []*platform.SecurityCatalogRef) ([]*securityCatalogItem, map[securityCatalogKey]string, error) {
	if len(refs) == 0 {
		return nil, nil, invalidArgument("at least one catalog resource is required")
	}
	selected := make([]securityCatalogKey, 0, len(refs))
	for _, ref := range refs {
		if ref == nil || ref.GetKind() == platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_UNSPECIFIED || strings.TrimSpace(ref.GetName()) == "" {
			return nil, nil, invalidArgument("each catalog resource requires a kind and name")
		}
		key := securityCatalogKey{kind: ref.GetKind(), name: strings.TrimSpace(ref.GetName())}
		if snapshot.items[key] == nil {
			return nil, nil, invalidArgument("catalog resource %s/%s does not exist", securityCatalogKindName(key.kind), key.name)
		}
		selected = append(selected, key)
	}
	visited := map[securityCatalogKey]bool{}
	visiting := map[securityCatalogKey]bool{}
	blocked := map[securityCatalogKey]string{}
	var order []*securityCatalogItem
	var visit func(securityCatalogKey) error
	visit = func(key securityCatalogKey) error {
		if visited[key] {
			return nil
		}
		if visiting[key] {
			return invalidArgument("catalog dependency cycle includes %s/%s", securityCatalogKindName(key.kind), key.name)
		}
		visiting[key] = true
		item := snapshot.items[key]
		for _, dependency := range item.dependencies {
			if snapshot.items[dependency.key] == nil {
				if dependency.required {
					blocked[key] = fmt.Sprintf("required dependency %s/%s is missing from the catalog", securityCatalogKindName(dependency.key.kind), dependency.key.name)
				}
				continue
			}
			if err := visit(dependency.key); err != nil {
				return err
			}
		}
		visiting[key] = false
		visited[key] = true
		order = append(order, item)
		return nil
	}
	for _, key := range selected {
		if err := visit(key); err != nil {
			return nil, nil, err
		}
	}
	return order, blocked, nil
}

func securityCatalogKindName(kind platform.SecurityCatalogKind) string {
	switch kind {
	case platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_SKILL:
		return "Skill"
	case platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_WORKFLOW:
		return "SecurityWorkflow"
	case platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_RANKER:
		return "SecurityRanker"
	case platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_POST_SCRIPT:
		return "SecurityPostScript"
	case platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_POLICY_PACK:
		return "SecurityPolicyPack"
	case platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_PROGRAM:
		return "SecurityProgram"
	default:
		return "unknown"
	}
}

func (s *Server) securityCatalogInstallState(ctx context.Context, namespace string, source client.Object) (platform.SecurityCatalogInstallState, error) {
	current := emptyBootstrapResource(source)
	if err := s.apiReaderOrClient().Get(ctx, client.ObjectKey{Namespace: namespace, Name: source.GetName()}, current); err != nil {
		if k8serrors.IsNotFound(err) {
			return platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_NOT_INSTALLED, nil
		}
		return 0, mapK8sError(fmt.Sprintf("read installed catalog %s %s", source.GetObjectKind().GroupVersionKind().Kind, source.GetName()), err)
	}
	return securityCatalogInstallStateForCurrent(source, current)
}

func securityCatalogInstallStateForCurrent(source, current client.Object) (platform.SecurityCatalogInstallState, error) {
	if current.GetAnnotations()[bootstrapSourceAnnotation] != source.GetNamespace() {
		return platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_CONFLICT, nil
	}
	currentHash, err := bootstrapSpecHash(current)
	if err != nil {
		return 0, err
	}
	desiredHash, err := bootstrapSpecHash(source)
	if err != nil {
		return 0, err
	}
	recordedHash := current.GetAnnotations()[bootstrapSpecHashAnnotation]
	if currentHash == desiredHash && recordedHash == desiredHash {
		return platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_INSTALLED, nil
	}
	if currentHash == desiredHash {
		return platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_UPDATE_AVAILABLE, nil
	}
	if (recordedHash != "" && currentHash == recordedHash) || bootstrapReplacesSpecHash(source, currentHash) {
		return platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_UPDATE_AVAILABLE, nil
	}
	return platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_MODIFIED, nil
}

func (s *Server) installSecurityCatalogItem(ctx context.Context, namespace, ownerID string, item *securityCatalogItem) (string, bool, error) {
	desired := catalogDestinationObject(item.source, namespace)
	desiredHash, err := bootstrapSpecHash(desired)
	if err != nil {
		return "", false, err
	}
	annotations := desired.GetAnnotations()
	annotations[bootstrapSpecHashAnnotation] = desiredHash
	desired.SetAnnotations(annotations)
	resourceType := securityCatalogOwnershipResourceType(item.key.kind)
	claimOwnership := s.stateStore != nil && resourceType != "" && strings.TrimSpace(ownerID) != ""
	if claimOwnership {
		ownership, err := s.stateStore.GetResourceOwner(ctx, resourceType, desired.GetName(), namespace)
		if err != nil {
			return "", false, connect.NewError(connect.CodeInternal, fmt.Errorf("read ownership for %s %s/%s: %w", securityCatalogKindName(item.key.kind), namespace, item.key.name, err))
		}
		if ownership != nil && ownership.OwnerID != ownerID {
			return "", false, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s %s/%s is owned by another user", securityCatalogKindName(item.key.kind), namespace, item.key.name))
		}
		claimOwnership = ownership == nil
	}
	action := "unchanged"
	var created client.Object
	var updatedBefore client.Object
	var updatedAfter client.Object
	mutated := false
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current := emptyBootstrapResource(desired)
		key := client.ObjectKey{Namespace: namespace, Name: desired.GetName()}
		if err := s.apiReaderOrClient().Get(ctx, key, current); err != nil {
			if !k8serrors.IsNotFound(err) {
				return err
			}
			candidate := desired.DeepCopyObject().(client.Object)
			if err := s.k8sClient.Create(ctx, candidate); err != nil {
				if k8serrors.IsAlreadyExists(err) {
					return k8serrors.NewConflict(schema.GroupResource{Resource: "securitycatalogresources"}, desired.GetName(), err)
				}
				return err
			}
			created = candidate
			action = "created"
			mutated = true
			return nil
		}
		if current.GetAnnotations()[bootstrapSourceAnnotation] != item.source.GetNamespace() {
			return errSecurityCatalogChanged
		}
		currentHash, err := bootstrapSpecHash(current)
		if err != nil {
			return err
		}
		recordedHash := current.GetAnnotations()[bootstrapSpecHashAnnotation]
		if currentHash == desiredHash && recordedHash == desiredHash {
			return nil
		}
		if currentHash != desiredHash && !((recordedHash != "" && currentHash == recordedHash) || bootstrapReplacesSpecHash(item.source, currentHash)) {
			return errSecurityCatalogChanged
		}
		before := current.DeepCopyObject().(client.Object)
		copyBootstrapSpec(current, desired)
		updatedAnnotations := maps.Clone(current.GetAnnotations())
		if updatedAnnotations == nil {
			updatedAnnotations = map[string]string{}
		}
		updatedAnnotations[bootstrapDefaultAnnotation] = "true"
		updatedAnnotations[bootstrapSourceAnnotation] = item.source.GetNamespace()
		updatedAnnotations[bootstrapSpecHashAnnotation] = desiredHash
		current.SetAnnotations(updatedAnnotations)
		if err := s.k8sClient.Patch(ctx, current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
		updatedBefore = before
		updatedAfter = current.DeepCopyObject().(client.Object)
		if currentHash == desiredHash {
			action = "adopted"
		} else {
			action = "refreshed"
		}
		mutated = true
		return nil
	})
	if err != nil {
		if errors.Is(err, errSecurityCatalogChanged) {
			return "", mutated, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return "", mutated, mapK8sError(fmt.Sprintf("install catalog %s %s", securityCatalogKindName(item.key.kind), item.key.name), err)
	}
	if claimOwnership {
		ownership, ownerErr := s.stateStore.GetResourceOwner(ctx, resourceType, desired.GetName(), namespace)
		if ownerErr == nil && ownership != nil && ownership.OwnerID == ownerID {
			return action, mutated, nil
		}
		code := connect.CodeInternal
		if ownerErr == nil && ownership != nil {
			code = connect.CodeFailedPrecondition
			ownerErr = fmt.Errorf("%s %s/%s is owned by another user", securityCatalogKindName(item.key.kind), namespace, item.key.name)
		} else if ownerErr == nil {
			ownerErr = s.stateStore.SetResourceOwner(ctx, resourceType, desired.GetName(), namespace, ownerID)
		}
		if ownerErr != nil {
			var rollbackErr error
			if created != nil {
				rollbackErr = s.rollbackSecurityCatalogCreate(ctx, created)
			} else if updatedBefore != nil {
				rollbackErr = s.rollbackSecurityCatalogUpdate(ctx, updatedBefore, updatedAfter)
			}
			if rollbackErr == nil {
				mutated = false
			} else {
				ownerErr = errors.Join(ownerErr, rollbackErr)
			}
			return "", mutated, connect.NewError(code, fmt.Errorf("record ownership for %s %s/%s: %w", securityCatalogKindName(item.key.kind), namespace, item.key.name, ownerErr))
		}
		mutated = true
		if action == "unchanged" {
			action = "claimed"
		}
	}
	return action, mutated, nil
}

func securityCatalogOwnershipResourceType(kind platform.SecurityCatalogKind) string {
	switch kind {
	case platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_SKILL:
		return skillResourceType
	case platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_PROGRAM:
		return securityProgramResourceType
	default:
		return ""
	}
}

func catalogDestinationObject(source client.Object, namespace string) client.Object {
	destination := emptyBootstrapResource(source)
	destination.SetName(source.GetName())
	destination.SetNamespace(namespace)
	meta := bootstrapObjectMeta(source, namespace)
	destination.SetLabels(meta.Labels)
	destination.SetAnnotations(meta.Annotations)
	copyBootstrapSpec(destination, source)
	return destination
}

func (s *Server) rollbackSecurityCatalogCreate(ctx context.Context, created client.Object) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	uid := created.GetUID()
	resourceVersion := created.GetResourceVersion()
	preconditions := client.Preconditions{UID: &uid, ResourceVersion: &resourceVersion}
	if err := s.k8sClient.Delete(cleanupCtx, created, preconditions); err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("roll back unowned catalog resource %s/%s: %w", created.GetNamespace(), created.GetName(), err)
	}
	return nil
}

func (s *Server) rollbackSecurityCatalogUpdate(ctx context.Context, before, updated client.Object) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	current := emptyBootstrapResource(updated)
	key := client.ObjectKeyFromObject(updated)
	if err := s.k8sClient.Get(cleanupCtx, key, current); err != nil {
		return fmt.Errorf("read catalog resource %s/%s for rollback: %w", key.Namespace, key.Name, err)
	}
	if current.GetUID() != updated.GetUID() || current.GetResourceVersion() != updated.GetResourceVersion() {
		return fmt.Errorf("roll back catalog resource %s/%s: resource changed after installation", key.Namespace, key.Name)
	}
	restore := before.DeepCopyObject().(client.Object)
	restore.SetUID(updated.GetUID())
	restore.SetResourceVersion(updated.GetResourceVersion())
	restore.SetGeneration(updated.GetGeneration())
	if err := s.k8sClient.Patch(cleanupCtx, restore, client.MergeFromWithOptions(updated, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("roll back catalog resource %s/%s: %w", key.Namespace, key.Name, err)
	}
	return nil
}
