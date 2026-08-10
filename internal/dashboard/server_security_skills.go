package dashboard

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"google.golang.org/protobuf/types/known/emptypb"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const securitySkillBundleAnnotation = "platform.gratefulagents.dev/security-skill"

const (
	securitySkillsStateNotInstalled       = "not_installed"
	securitySkillsStatePartiallyInstalled = "partially_installed"
	securitySkillsStateInstalled          = "installed"
	securitySkillsStateUnavailable        = "unavailable"
)

// GetSecuritySkillsStatus reports the curated security skill bundle state in
// the authenticated user's namespace. This read never installs a Skill.
func (s *Server) GetSecuritySkillsStatus(ctx context.Context, _ *emptypb.Empty) (*platform.SecuritySkillsStatus, error) {
	namespace, err := s.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		return nil, err
	}
	return s.securitySkillsStatus(ctx, namespace)
}

// InstallSecuritySkills copies the curated bootstrap security skills into the
// authenticated user's namespace. Replays are safe. A same-name user resource,
// or a previously seeded resource the user changed, is preserved as a conflict.
func (s *Server) InstallSecuritySkills(ctx context.Context, _ *emptypb.Empty) (*platform.SecuritySkillsStatus, error) {
	namespace, err := s.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		return nil, err
	}
	sources, ready, err := s.securitySkillSources(ctx)
	if err != nil {
		return nil, err
	}
	if !ready {
		return securitySkillsUnavailable(namespace), nil
	}
	for i := range sources {
		if err := s.installSecuritySkill(ctx, namespace, &sources[i]); err != nil {
			return nil, err
		}
	}
	return s.securitySkillsStatusFromSources(ctx, namespace, sources)
}

func (s *Server) securitySkillsStatus(ctx context.Context, namespace string) (*platform.SecuritySkillsStatus, error) {
	sources, ready, err := s.securitySkillSources(ctx)
	if err != nil {
		return nil, err
	}
	if !ready {
		return securitySkillsUnavailable(namespace), nil
	}
	return s.securitySkillsStatusFromSources(ctx, namespace, sources)
}

func (s *Server) securitySkillSources(ctx context.Context) ([]platformv1alpha1.Skill, bool, error) {
	sourceNamespace := strings.TrimSpace(os.Getenv("POD_NAMESPACE"))
	if sourceNamespace == "" {
		return nil, false, nil
	}
	reader := s.apiReaderOrClient()
	_, ready, err := bootstrapBundleVersion(ctx, reader, sourceNamespace)
	if err != nil || !ready {
		return nil, ready, err
	}
	var list platformv1alpha1.SkillList
	if err := reader.List(ctx, &list, client.InNamespace(sourceNamespace)); err != nil {
		return nil, false, mapK8sError("list security skill bundle", err)
	}
	sources := make([]platformv1alpha1.Skill, 0, len(list.Items))
	for i := range list.Items {
		skill := &list.Items[i]
		if isBootstrapDefault(skill) && skill.Annotations[securitySkillBundleAnnotation] == "true" {
			sources = append(sources, *skill.DeepCopy())
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	return sources, len(sources) > 0, nil
}

func (s *Server) securitySkillsStatusFromSources(ctx context.Context, namespace string, sources []platformv1alpha1.Skill) (*platform.SecuritySkillsStatus, error) {
	status := &platform.SecuritySkillsStatus{
		Namespace:      namespace,
		AvailableCount: int32(len(sources)), // #nosec G115 -- bundle size is operator-bounded.
	}
	for i := range sources {
		installed, conflict, err := s.securitySkillState(ctx, namespace, &sources[i])
		if err != nil {
			return nil, err
		}
		if installed {
			status.InstalledCount++
		}
		if conflict {
			status.ConflictCount++
		}
	}
	switch {
	case status.AvailableCount == 0:
		status.State = securitySkillsStateUnavailable
	case status.InstalledCount == status.AvailableCount:
		status.State = securitySkillsStateInstalled
	case status.InstalledCount == 0 && status.ConflictCount == 0:
		status.State = securitySkillsStateNotInstalled
	default:
		status.State = securitySkillsStatePartiallyInstalled
	}
	return status, nil
}

func (s *Server) securitySkillState(ctx context.Context, namespace string, source *platformv1alpha1.Skill) (installed, conflict bool, err error) {
	current := &platformv1alpha1.Skill{}
	if err := s.apiReaderOrClient().Get(ctx, client.ObjectKey{Namespace: namespace, Name: source.Name}, current); err != nil {
		if k8serrors.IsNotFound(err) {
			return false, false, nil
		}
		return false, false, mapK8sError(fmt.Sprintf("read security skill %s", source.Name), err)
	}
	if current.Annotations[bootstrapSourceAnnotation] != source.Namespace {
		return false, true, nil
	}
	currentHash, err := bootstrapSpecHash(current)
	if err != nil {
		return false, false, fmt.Errorf("hash installed security Skill %s: %w", current.Name, err)
	}
	desired := &platformv1alpha1.Skill{Spec: source.DeepCopy().Spec}
	desiredHash, err := bootstrapSpecHash(desired)
	if err != nil {
		return false, false, fmt.Errorf("hash desired security Skill %s: %w", source.Name, err)
	}
	recordedHash := current.Annotations[bootstrapSpecHashAnnotation]
	if currentHash == desiredHash {
		// Exact legacy copies remain usable, but need one explicit install to
		// record provenance before future bundle versions can refresh them.
		return recordedHash != "", false, nil
	}
	if recordedHash == "" || currentHash != recordedHash {
		return false, true, nil
	}
	return false, false, nil // An untouched older bundle version can be refreshed.
}

func (s *Server) installSecuritySkill(ctx context.Context, namespace string, source *platformv1alpha1.Skill) error {
	desired := &platformv1alpha1.Skill{
		ObjectMeta: bootstrapObjectMeta(source, namespace),
		Spec:       source.DeepCopy().Spec,
	}
	desiredHash, err := bootstrapSpecHash(desired)
	if err != nil {
		return fmt.Errorf("hash security Skill %s: %w", source.Name, err)
	}
	desired.Annotations[bootstrapSpecHashAnnotation] = desiredHash

	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current := &platformv1alpha1.Skill{}
		key := client.ObjectKey{Namespace: namespace, Name: source.Name}
		if err := s.apiReaderOrClient().Get(ctx, key, current); err != nil {
			if !k8serrors.IsNotFound(err) {
				return err
			}
			if err := s.k8sClient.Create(ctx, desired.DeepCopy()); err != nil {
				if k8serrors.IsAlreadyExists(err) {
					return nil // A concurrent creator wins; status will report a conflict if needed.
				}
				return err
			}
			return nil
		}
		if current.Annotations[bootstrapSourceAnnotation] != source.Namespace {
			return nil
		}
		currentHash, err := bootstrapSpecHash(current)
		if err != nil {
			return err
		}
		recordedHash := current.Annotations[bootstrapSpecHashAnnotation]
		if (recordedHash == "" && currentHash != desiredHash) || (recordedHash != "" && currentHash != recordedHash) {
			return nil
		}
		before := current.DeepCopy()
		current.Spec = source.DeepCopy().Spec
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		current.Annotations[bootstrapDefaultAnnotation] = "true"
		current.Annotations[bootstrapSourceAnnotation] = source.Namespace
		current.Annotations[bootstrapSpecHashAnnotation] = desiredHash
		current.Annotations[securitySkillBundleAnnotation] = "true"
		return s.k8sClient.Patch(ctx, current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	})
	if err != nil {
		return mapK8sError(fmt.Sprintf("install security Skill %s", source.Name), err)
	}
	return nil
}

func securitySkillsUnavailable(namespace string) *platform.SecuritySkillsStatus {
	return &platform.SecuritySkillsStatus{
		Namespace: namespace,
		State:     securitySkillsStateUnavailable,
	}
}
