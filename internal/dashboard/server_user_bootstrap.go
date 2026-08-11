package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

const (
	bootstrapDefaultAnnotation       = "platform.gratefulagents.dev/bootstrap-default"
	bootstrapSourceAnnotation        = "platform.gratefulagents.dev/bootstrap-source-namespace"
	bootstrapReadyLabel              = "platform.gratefulagents.dev/bootstrap-ready"
	bootstrapBundleVersionKey        = "bundle-version"
	bootstrapSyncedVersionAnnotation = "platform.gratefulagents.dev/bootstrap-synced-version"
	bootstrapSpecHashAnnotation      = "platform.gratefulagents.dev/bootstrap-spec-hash"
	bootstrapSyncProtocolVersion     = "v4"
)

// syncBootstrapResources makes the chart's namespaced, reusable defaults
// available where a user's runs can actually reference them. The Helm chart
// installs these resources in the manager namespace, while security library
// references are deliberately namespace-local. Curated security Skills are
// intentionally excluded and installed only after an explicit user action;
// unrelated bootstrap Skills continue to seed normally.
//
// Only explicitly marked chart defaults are copied; arbitrary resources in the
// manager namespace remain private. Existing resources win so a user can edit
// or replace a seeded default without a later request silently reverting it.
func (s *Server) syncBootstrapResources(ctx context.Context, targetNamespace string) error {
	sourceNamespace := strings.TrimSpace(os.Getenv("POD_NAMESPACE"))
	if sourceNamespace == "" || sourceNamespace == targetNamespace {
		return nil
	}

	reader := s.apiReader
	if reader == nil {
		reader = s.k8sClient
	}
	bundleVersion, ready, err := bootstrapBundleVersion(ctx, reader, sourceNamespace)
	if err != nil || !ready {
		return err
	}
	bundleVersion = bootstrapSyncProtocolVersion + ":" + bundleVersion
	target := &corev1.Namespace{}
	if err := reader.Get(ctx, client.ObjectKey{Name: targetNamespace}, target); err != nil {
		return mapK8sError("read personal namespace bootstrap state", err)
	}
	if target.Annotations[bootstrapSyncedVersionAnnotation] == bundleVersion {
		return nil
	}

	var skills platformv1alpha1.SkillList
	if err := reader.List(ctx, &skills, client.InNamespace(sourceNamespace)); err != nil {
		return mapK8sError("list bootstrap Skills", err)
	}
	for i := range skills.Items {
		source := &skills.Items[i]
		if !isBootstrapDefault(source) || source.Annotations[securitySkillBundleAnnotation] == "true" {
			continue
		}
		if err := s.createBootstrapResource(ctx, source, &platformv1alpha1.Skill{
			ObjectMeta: bootstrapObjectMeta(source, targetNamespace), Spec: source.DeepCopy().Spec,
		}); err != nil {
			return err
		}
	}

	var workflows triggersv1alpha1.SecurityWorkflowList
	if err := reader.List(ctx, &workflows, client.InNamespace(sourceNamespace)); err != nil {
		return mapK8sError("list bootstrap SecurityWorkflows", err)
	}
	for i := range workflows.Items {
		source := &workflows.Items[i]
		if isBootstrapDefault(source) {
			if err := s.createBootstrapResource(ctx, source, &triggersv1alpha1.SecurityWorkflow{
				ObjectMeta: bootstrapObjectMeta(source, targetNamespace), Spec: source.DeepCopy().Spec,
			}); err != nil {
				return err
			}
		}
	}

	var rankers triggersv1alpha1.SecurityRankerList
	if err := reader.List(ctx, &rankers, client.InNamespace(sourceNamespace)); err != nil {
		return mapK8sError("list bootstrap SecurityRankers", err)
	}
	for i := range rankers.Items {
		source := &rankers.Items[i]
		if isBootstrapDefault(source) {
			if err := s.createBootstrapResource(ctx, source, &triggersv1alpha1.SecurityRanker{
				ObjectMeta: bootstrapObjectMeta(source, targetNamespace), Spec: source.DeepCopy().Spec,
			}); err != nil {
				return err
			}
		}
	}

	var scripts triggersv1alpha1.SecurityPostScriptList
	if err := reader.List(ctx, &scripts, client.InNamespace(sourceNamespace)); err != nil {
		return mapK8sError("list bootstrap SecurityPostScripts", err)
	}
	for i := range scripts.Items {
		source := &scripts.Items[i]
		if isBootstrapDefault(source) {
			if err := s.createBootstrapResource(ctx, source, &triggersv1alpha1.SecurityPostScript{
				ObjectMeta: bootstrapObjectMeta(source, targetNamespace), Spec: source.DeepCopy().Spec,
			}); err != nil {
				return err
			}
		}
	}

	var packs triggersv1alpha1.SecurityPolicyPackList
	if err := reader.List(ctx, &packs, client.InNamespace(sourceNamespace)); err != nil {
		return mapK8sError("list bootstrap SecurityPolicyPacks", err)
	}
	for i := range packs.Items {
		source := &packs.Items[i]
		if isBootstrapDefault(source) {
			if err := s.createBootstrapResource(ctx, source, &triggersv1alpha1.SecurityPolicyPack{
				ObjectMeta: bootstrapObjectMeta(source, targetNamespace), Spec: source.DeepCopy().Spec,
			}); err != nil {
				return err
			}
		}
	}

	var programs triggersv1alpha1.SecurityProgramList
	if err := reader.List(ctx, &programs, client.InNamespace(sourceNamespace)); err != nil {
		return mapK8sError("list bootstrap SecurityPrograms", err)
	}
	for i := range programs.Items {
		source := &programs.Items[i]
		if isBootstrapDefault(source) {
			if err := s.createBootstrapResource(ctx, source, &triggersv1alpha1.SecurityProgram{
				ObjectMeta: bootstrapObjectMeta(source, targetNamespace), Spec: source.DeepCopy().Spec,
			}); err != nil {
				return err
			}
		}
	}
	if err := s.markBootstrapSynced(ctx, reader, targetNamespace, bundleVersion); err != nil {
		return err
	}
	return nil
}

func bootstrapBundleVersion(ctx context.Context, reader client.Reader, namespace string) (string, bool, error) {
	markers := &corev1.ConfigMapList{}
	if err := reader.List(ctx, markers, client.InNamespace(namespace), client.MatchingLabels{
		bootstrapReadyLabel: "true",
	}); err != nil {
		return "", false, mapK8sError("list bootstrap readiness markers", err)
	}
	if len(markers.Items) == 0 {
		return "", false, nil
	}
	if len(markers.Items) != 1 {
		return "", false, fmt.Errorf("expected one bootstrap readiness marker in namespace %q, found %d", namespace, len(markers.Items))
	}
	version := strings.TrimSpace(markers.Items[0].Data[bootstrapBundleVersionKey])
	if version == "" {
		return "", false, fmt.Errorf("bootstrap readiness marker %s/%s has no %q", namespace, markers.Items[0].Name, bootstrapBundleVersionKey)
	}
	return version, true, nil
}

func (s *Server) markBootstrapSynced(ctx context.Context, reader client.Reader, namespace, version string) error {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current := &corev1.Namespace{}
		if err := reader.Get(ctx, client.ObjectKey{Name: namespace}, current); err != nil {
			return err
		}
		before := current.DeepCopy()
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		current.Annotations[bootstrapSyncedVersionAnnotation] = version
		return s.k8sClient.Patch(ctx, current, client.MergeFrom(before))
	})
	if err != nil {
		return mapK8sError("record personal namespace bootstrap state", err)
	}
	return nil
}

func isBootstrapDefault(object client.Object) bool {
	return object.GetAnnotations()[bootstrapDefaultAnnotation] == "true"
}

func bootstrapObjectMeta(source client.Object, namespace string) metav1.ObjectMeta {
	annotations := maps.Clone(source.GetAnnotations())
	if annotations == nil {
		annotations = map[string]string{}
	}
	for key := range annotations {
		if strings.HasPrefix(key, "helm.sh/") {
			delete(annotations, key)
		}
	}
	annotations[bootstrapSourceAnnotation] = source.GetNamespace()
	return metav1.ObjectMeta{
		Name:        source.GetName(),
		Namespace:   namespace,
		Labels:      maps.Clone(source.GetLabels()),
		Annotations: annotations,
	}
}

func (s *Server) createBootstrapResource(ctx context.Context, source, target client.Object) error {
	desiredHash, err := bootstrapSpecHash(target)
	if err != nil {
		return fmt.Errorf("hash bootstrap %T %s: %w", source, source.GetName(), err)
	}
	targetAnnotations := target.GetAnnotations()
	if targetAnnotations == nil {
		targetAnnotations = map[string]string{}
	}
	targetAnnotations[bootstrapSpecHashAnnotation] = desiredHash
	target.SetAnnotations(targetAnnotations)

	current := emptyBootstrapResource(target)
	key := client.ObjectKeyFromObject(target)
	if err := s.apiReaderOrClient().Get(ctx, key, current); err != nil {
		if !k8serrors.IsNotFound(err) {
			return mapK8sError(fmt.Sprintf("read seeded %T %s", source, source.GetName()), err)
		}
		if err := s.k8sClient.Create(ctx, target); err != nil && !k8serrors.IsAlreadyExists(err) {
			return mapK8sError(fmt.Sprintf("seed bootstrap %T %s", source, source.GetName()), err)
		}
		return nil
	}

	currentHash, err := bootstrapSpecHash(current)
	if err != nil {
		return fmt.Errorf("hash current %T %s: %w", current, current.GetName(), err)
	}
	currentAnnotations := current.GetAnnotations()
	previousSeedHash := currentAnnotations[bootstrapSpecHashAnnotation]
	if previousSeedHash != "" && previousSeedHash != currentHash {
		return nil // The user changed the previously seeded spec.
	}
	if previousSeedHash == "" {
		// A hashless object is only a legacy bootstrap copy when it carries
		// bootstrap provenance. Never adopt a user-created resource merely
		// because its name and spec happen to match a new default.
		if currentAnnotations[bootstrapDefaultAnnotation] != "true" ||
			currentAnnotations[bootstrapSourceAnnotation] != source.GetNamespace() {
			return nil
		}
		if currentHash != desiredHash {
			return nil // The user changed the legacy seeded spec.
		}
	}

	before := current.DeepCopyObject().(client.Object)
	copyBootstrapSpec(current, target)
	annotations := maps.Clone(current.GetAnnotations())
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[bootstrapDefaultAnnotation] = "true"
	annotations[bootstrapSourceAnnotation] = source.GetNamespace()
	annotations[bootstrapSpecHashAnnotation] = desiredHash
	current.SetAnnotations(annotations)
	if err := s.k8sClient.Patch(ctx, current, client.MergeFrom(before)); err != nil {
		return mapK8sError(fmt.Sprintf("refresh seeded %T %s", source, source.GetName()), err)
	}
	return nil
}

func (s *Server) apiReaderOrClient() client.Reader {
	if s.apiReader != nil {
		return s.apiReader
	}
	return s.k8sClient
}

func bootstrapSpecHash(object client.Object) (string, error) {
	var spec any
	switch typed := object.(type) {
	case *platformv1alpha1.Skill:
		spec = typed.Spec
	case *triggersv1alpha1.SecurityWorkflow:
		spec = typed.Spec
	case *triggersv1alpha1.SecurityRanker:
		spec = typed.Spec
	case *triggersv1alpha1.SecurityPostScript:
		spec = typed.Spec
	case *triggersv1alpha1.SecurityPolicyPack:
		spec = typed.Spec
	case *triggersv1alpha1.SecurityProgram:
		spec = typed.Spec
	default:
		return "", fmt.Errorf("unsupported bootstrap resource %T", object)
	}
	contents, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:]), nil
}

func emptyBootstrapResource(object client.Object) client.Object {
	switch object.(type) {
	case *platformv1alpha1.Skill:
		return &platformv1alpha1.Skill{}
	case *triggersv1alpha1.SecurityWorkflow:
		return &triggersv1alpha1.SecurityWorkflow{}
	case *triggersv1alpha1.SecurityRanker:
		return &triggersv1alpha1.SecurityRanker{}
	case *triggersv1alpha1.SecurityPostScript:
		return &triggersv1alpha1.SecurityPostScript{}
	case *triggersv1alpha1.SecurityPolicyPack:
		return &triggersv1alpha1.SecurityPolicyPack{}
	case *triggersv1alpha1.SecurityProgram:
		return &triggersv1alpha1.SecurityProgram{}
	default:
		panic(fmt.Sprintf("unsupported bootstrap resource %T", object))
	}
}

func copyBootstrapSpec(destination, source client.Object) {
	switch dst := destination.(type) {
	case *platformv1alpha1.Skill:
		dst.Spec = source.(*platformv1alpha1.Skill).DeepCopy().Spec
	case *triggersv1alpha1.SecurityWorkflow:
		dst.Spec = source.(*triggersv1alpha1.SecurityWorkflow).DeepCopy().Spec
	case *triggersv1alpha1.SecurityRanker:
		dst.Spec = source.(*triggersv1alpha1.SecurityRanker).DeepCopy().Spec
	case *triggersv1alpha1.SecurityPostScript:
		dst.Spec = source.(*triggersv1alpha1.SecurityPostScript).DeepCopy().Spec
	case *triggersv1alpha1.SecurityPolicyPack:
		dst.Spec = source.(*triggersv1alpha1.SecurityPolicyPack).DeepCopy().Spec
	case *triggersv1alpha1.SecurityProgram:
		dst.Spec = source.(*triggersv1alpha1.SecurityProgram).DeepCopy().Spec
	default:
		panic(fmt.Sprintf("unsupported bootstrap resource %T", destination))
	}
}
