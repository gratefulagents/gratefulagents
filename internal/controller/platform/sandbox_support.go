package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const agentSandboxProvider = "agent-sandbox"

var (
	errRunSandboxReplaced      = errors.New("stale run sandbox replaced")
	errRunSandboxDrainRequired = errors.New("stale run sandbox requires graceful drain")
)

func createPlanSandbox(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun, runtimeProfile *platformv1alpha1.RuntimeProfile) (*platformv1alpha1.AgentRunSandboxStatus, error) {
	saName := sanitizeDNSLabel("run", run.Name)
	if err := ensureRunRBAC(ctx, c, run, saName); err != nil {
		return nil, err
	}

	var wsPVCName string
	if persistWorkspaceEnabled(runtimeProfile) {
		name, err := ensureWorkspacePVC(ctx, c, run, runtimeProfile)
		if err != nil {
			return nil, err
		}
		wsPVCName = name
	}

	templateName, err := ensureRunSandboxTemplate(ctx, c, run, runtimeProfile, saName, wsPVCName)
	if err != nil {
		return nil, err
	}

	claimName := sandboxClaimName(run)
	claim := buildSandboxClaim(run, templateName, runtimeProfile)
	if err := c.Create(ctx, claim); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating sandbox claim: %w", err)
		}

		existing := &extensionsv1alpha1.SandboxClaim{}
		key := client.ObjectKey{Name: claimName, Namespace: run.Namespace}
		if getErr := c.Get(ctx, key, existing); getErr != nil {
			if apierrors.IsNotFound(getErr) {
				return nil, errRunSandboxReplaced
			}
			return nil, fmt.Errorf("getting existing sandbox claim: %w", getErr)
		}
		replace, replaceErr := shouldReplaceExistingSandboxClaim(ctx, c, run, existing)
		if replaceErr != nil {
			return nil, replaceErr
		}
		if replace {
			// Never delete an assigned claim here. The reconciler must first run
			// releaseRunSandbox so the old pod finishes its final S3 checkpoint.
			if strings.TrimSpace(existing.Status.SandboxStatus.Name) != "" {
				return nil, errRunSandboxDrainRequired
			}
			if delErr := c.Delete(ctx, existing); delErr != nil && !apierrors.IsNotFound(delErr) {
				return nil, fmt.Errorf("deleting unassigned stale sandbox claim: %w", delErr)
			}
			if managedSandboxTemplateName(run) == templateName {
				_ = deleteManagedSandboxTemplateIfExists(ctx, c, run.Namespace, templateName)
			}
			return nil, errRunSandboxReplaced
		}
		if err := clearSandboxClaimLifecycle(ctx, c, existing); err != nil {
			return nil, err
		}
		return &platformv1alpha1.AgentRunSandboxStatus{
			Provider: agentSandboxProvider,
			ClaimRef: &platformv1alpha1.NamedRef{Name: claimName},
		}, nil
	}

	return &platformv1alpha1.AgentRunSandboxStatus{
		Provider: agentSandboxProvider,
		ClaimRef: &platformv1alpha1.NamedRef{Name: claimName},
	}, nil
}

func ensureRunSandboxTemplate(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun, runtimeProfile *platformv1alpha1.RuntimeProfile, saName, workspacePVCName string) (string, error) {
	var baseTemplate *extensionsv1alpha1.SandboxTemplate
	if explicit := explicitSandboxTemplateRef(runtimeProfile); explicit != "" {
		baseTemplate = &extensionsv1alpha1.SandboxTemplate{}
		if err := c.Get(ctx, client.ObjectKey{Name: explicit, Namespace: run.Namespace}, baseTemplate); err != nil {
			if apierrors.IsNotFound(err) {
				return "", fmt.Errorf("sandbox template %s/%s not found", run.Namespace, explicit)
			}
			return "", fmt.Errorf("getting sandbox template %s/%s: %w", run.Namespace, explicit, err)
		}
	}

	name := managedSandboxTemplateName(run)
	template := &extensionsv1alpha1.SandboxTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       run.Namespace,
			Labels:          sandboxTemplateLabels(run),
			OwnerReferences: []metav1.OwnerReference{runOwnerRef(run)},
		},
		Spec: buildManagedSandboxTemplateSpec(run, runtimeProfile, saName, baseTemplate, workspacePVCName,
			resolveMCPServerSecretEnvs(ctx, c, run)),
	}
	if err := c.Create(ctx, template); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("creating sandbox template: %w", err)
	}
	return name, nil
}

func explicitSandboxTemplateRef(runtimeProfile *platformv1alpha1.RuntimeProfile) string {
	if runtimeProfile == nil || runtimeProfile.Spec.Sandbox == nil || runtimeProfile.Spec.Sandbox.SandboxTemplateRef == nil {
		return ""
	}
	return strings.TrimSpace(runtimeProfile.Spec.Sandbox.SandboxTemplateRef.Name)
}

func buildManagedSandboxTemplateSpec(
	run *platformv1alpha1.AgentRun,
	runtimeProfile *platformv1alpha1.RuntimeProfile,
	saName string,
	baseTemplate *extensionsv1alpha1.SandboxTemplate,
	workspacePVCName string,
	secretEnvs []corev1.EnvVar,
) extensionsv1alpha1.SandboxTemplateSpec {
	envs := runExecutionEnvVars(run)
	envs = append(envs, secretEnvs...)
	podSpec := buildCommonPodSpec(run, saName, []string{"/opt/gratefulagents/bin/agent", "run"}, envs, nil, nil)
	podSpec.AutomountServiceAccountToken = boolPtr(true)
	applyRuntimeProfileSandboxOverrides(&podSpec, runtimeProfile, workspacePVCName)

	spec := extensionsv1alpha1.SandboxTemplateSpec{}
	if baseTemplate != nil {
		spec = *baseTemplate.Spec.DeepCopy()
	}
	spec.PodTemplate = sandboxv1alpha1.PodTemplate{
		Spec: podSpec,
		ObjectMeta: sandboxv1alpha1.PodMetadata{
			Labels: map[string]string{
				"platform.gratefulagents.dev/owner-run":     run.Name,
				"platform.gratefulagents.dev/owner-run-uid": string(run.UID),
			},
			Annotations: map[string]string{},
		},
	}
	if baseTemplate != nil {
		spec.PodTemplate.ObjectMeta = *baseTemplate.Spec.PodTemplate.ObjectMeta.DeepCopy()
		if spec.PodTemplate.ObjectMeta.Labels == nil {
			spec.PodTemplate.ObjectMeta.Labels = map[string]string{}
		}
		spec.PodTemplate.ObjectMeta.Labels["platform.gratefulagents.dev/owner-run"] = run.Name
		spec.PodTemplate.ObjectMeta.Labels["platform.gratefulagents.dev/owner-run-uid"] = string(run.UID)
	}
	if spec.NetworkPolicyManagement == "" {
		spec.NetworkPolicyManagement = extensionsv1alpha1.NetworkPolicyManagementManaged
	}
	if networkPolicy := buildRuntimeProfileNetworkPolicy(runtimeProfile); networkPolicy != nil {
		spec.NetworkPolicy = networkPolicy
	}
	return spec
}

func applyRuntimeProfileSandboxOverrides(podSpec *corev1.PodSpec, runtimeProfile *platformv1alpha1.RuntimeProfile, workspacePVCName string) {
	if podSpec == nil || runtimeProfile == nil {
		return
	}
	if runtimeProfile.Spec.Sandbox != nil {
		if runtimeClassName := strings.TrimSpace(runtimeProfile.Spec.Sandbox.RuntimeClassName); runtimeClassName != "" {
			podSpec.RuntimeClassName = &runtimeClassName
		}
		if runtimeProfile.Spec.Sandbox.EnablePrivateProcfs {
			// Kubernetes requires an Unmasked procMount to run inside a pod user
			// namespace. Bubblewrap then replaces this worker-level procfs with a
			// fresh procfs for its child PID namespace, rather than exposing the
			// agent process and its environment to model-controlled commands.
			podSpec.HostUsers = boolPtr(false)
			if len(podSpec.Containers) > 0 {
				if podSpec.Containers[0].SecurityContext == nil {
					podSpec.Containers[0].SecurityContext = &corev1.SecurityContext{}
				}
				procMount := corev1.UnmaskedProcMount
				podSpec.Containers[0].SecurityContext.ProcMount = &procMount
			}
		}
	}
	if runtimeProfile.Spec.Resources != nil && len(podSpec.Containers) > 0 {
		podSpec.Containers[0].Resources = *runtimeProfile.Spec.Resources.DeepCopy()
	}
	applyRuntimeProfileCommandSandboxConfig(podSpec, runtimeProfile)
	if len(podSpec.Containers) > 0 {
		// RuntimeProfile values may replace the default writable-path env; the
		// platform scratch mount is mandatory and must survive that override.
		ensureWorkspaceScratchSandboxConfig(&podSpec.Containers[0])
	}
	if workspacePVCName != "" {
		for i, v := range podSpec.Volumes {
			if v.Name == "workspace" {
				podSpec.Volumes[i].VolumeSource = corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: workspacePVCName,
					},
				}
				break
			}
		}
	}
}

func applyRuntimeProfileCommandSandboxConfig(podSpec *corev1.PodSpec, runtimeProfile *platformv1alpha1.RuntimeProfile) {
	if podSpec == nil || len(podSpec.Containers) == 0 {
		return
	}
	upsertContainerEnv(&podSpec.Containers[0], runtimeProfileCommandSandboxConfigEnvs(runtimeProfile)...)
}

func upsertContainerEnv(container *corev1.Container, envs ...corev1.EnvVar) {
	if container == nil {
		return
	}
	for _, next := range envs {
		if strings.TrimSpace(next.Name) == "" {
			continue
		}
		replaced := false
		for i := range container.Env {
			if container.Env[i].Name == next.Name {
				container.Env[i] = next
				replaced = true
				break
			}
		}
		if !replaced {
			container.Env = append(container.Env, next)
		}
	}
}

func buildRuntimeProfileNetworkPolicy(runtimeProfile *platformv1alpha1.RuntimeProfile) *extensionsv1alpha1.NetworkPolicySpec {
	mode := platformv1alpha1.EgressMode("restricted")
	if runtimeProfile != nil && runtimeProfile.Spec.Security != nil && runtimeProfile.Spec.Security.EgressMode != "" {
		mode = runtimeProfile.Spec.Security.EgressMode
	}

	switch mode {
	case platformv1alpha1.EgressMode("unrestricted"):
		return &extensionsv1alpha1.NetworkPolicySpec{
			Egress: []networkingv1.NetworkPolicyEgressRule{{}},
		}
	case platformv1alpha1.EgressMode("disabled"):
		return &extensionsv1alpha1.NetworkPolicySpec{}
	default:
		// nil means "secure default" in agent-sandbox:
		// public internet only, internal ranges blocked.
		return nil
	}
}

func sandboxClaimName(run *platformv1alpha1.AgentRun) string {
	return sanitizeDNSLabel("run", run.Name)
}

func managedSandboxTemplateName(run *platformv1alpha1.AgentRun) string {
	return sanitizeDNSLabel("run-tpl", run.Name)
}

func runOwnerRef(run *platformv1alpha1.AgentRun) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         platformv1alpha1.GroupVersion.String(),
		Kind:               "AgentRun",
		Name:               run.Name,
		UID:                run.UID,
		Controller:         boolPtr(true),
		BlockOwnerDeletion: boolPtr(true),
	}
}

func sandboxTemplateLabels(run *platformv1alpha1.AgentRun) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":                "gratefulagents",
		"app.kubernetes.io/component":           "agent-runner-template",
		"platform.gratefulagents.dev/owner-run": run.Name,
	}
}

func buildSandboxClaim(run *platformv1alpha1.AgentRun, templateName string, runtimeProfile *platformv1alpha1.RuntimeProfile) *extensionsv1alpha1.SandboxClaim {
	var warmPool *extensionsv1alpha1.WarmPoolPolicy
	if runtimeProfile != nil && runtimeProfile.Spec.Sandbox != nil && runtimeProfile.Spec.Sandbox.WarmPoolRef != nil {
		if name := strings.TrimSpace(runtimeProfile.Spec.Sandbox.WarmPoolRef.Name); name != "" {
			policy := extensionsv1alpha1.WarmPoolPolicy(name)
			warmPool = &policy
		}
	}
	return &extensionsv1alpha1.SandboxClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:            sandboxClaimName(run),
			Namespace:       run.Namespace,
			Labels:          sandboxClaimLabels(run),
			OwnerReferences: []metav1.OwnerReference{runOwnerRef(run)},
		},
		Spec: extensionsv1alpha1.SandboxClaimSpec{
			TemplateRef: extensionsv1alpha1.SandboxTemplateRef{Name: templateName},
			// AgentRun timeout is controller-owned. SandboxClaim Retain still
			// deletes the underlying Sandbox resources when the claim expires.
			WarmPool: warmPool,
		},
	}
}

func sandboxClaimLabels(run *platformv1alpha1.AgentRun) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":                "gratefulagents",
		"app.kubernetes.io/component":           "agent-runner-claim",
		"platform.gratefulagents.dev/owner-run": run.Name,
	}
}

// metaHarnessLabel marks a run as a Meta-Harness evaluation. Its value is
// the candidate identity assigned by the evaluation orchestrator.
const metaHarnessLabel = "platform.gratefulagents.dev/metaharness"

// runExecutionEnvVars builds the base runner environment shared by the pod
// and SandboxClaim execution paths. Meta-Harness capture stays disabled
// unless the run carries the metaharness label (per-run enablement, with the
// label value propagated as the candidate identity) or the operator enabled
// it manager-wide via ENABLE_METAHARNESS=true.
func runExecutionEnvVars(run *platformv1alpha1.AgentRun) []corev1.EnvVar {
	envs := []corev1.EnvVar{
		{Name: "PLANTASK_NAME", Value: run.Name},
		{Name: "PLANTASK_UID", Value: string(run.UID)},
	}
	if run.Spec.Debug {
		envs = append(envs, corev1.EnvVar{Name: "AI_DEBUG", Value: "1"})
	}
	candidate := run.Labels[metaHarnessLabel]
	if candidate != "" || strings.EqualFold(os.Getenv("ENABLE_METAHARNESS"), "true") {
		envs = append(envs, corev1.EnvVar{Name: "ENABLE_METAHARNESS", Value: "true"})
		if candidate != "" {
			envs = append(envs, corev1.EnvVar{Name: "METAHARNESS_CANDIDATE", Value: candidate})
		}
	}
	return envs
}

func shouldReplaceExistingSandboxClaim(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun, claim *extensionsv1alpha1.SandboxClaim) (bool, error) {
	if claim == nil {
		return false, nil
	}
	if claim.DeletionTimestamp != nil {
		return false, errRunSandboxReplaced
	}

	sandboxName := strings.TrimSpace(claim.Status.SandboxStatus.Name)
	if sandboxName == "" {
		// No sandbox was assigned, so there is no worker process to drain.
		return sandboxClaimExpired(claim), nil
	}
	podName, err := resolveSandboxPodName(ctx, c, run.Namespace, sandboxName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	if podName == "" {
		return false, nil
	}

	pod := &corev1.Pod{}
	if err := c.Get(ctx, client.ObjectKey{Name: podName, Namespace: run.Namespace}, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("getting sandbox pod %s/%s: %w", run.Namespace, podName, err)
	}
	switch pod.Status.Phase {
	case corev1.PodSucceeded, corev1.PodFailed:
		return true, nil
	default:
		return false, nil
	}
}

func clearSandboxClaimLifecycle(ctx context.Context, c client.Client, claim *extensionsv1alpha1.SandboxClaim) error {
	if claim == nil || claim.Spec.Lifecycle == nil {
		return nil
	}
	patch := client.MergeFrom(claim.DeepCopy())
	claim.Spec.Lifecycle = nil
	if err := c.Patch(ctx, claim, patch); err != nil {
		return fmt.Errorf("clearing sandbox claim lifecycle %s/%s: %w", claim.Namespace, claim.Name, err)
	}
	return nil
}

func deleteManagedSandboxTemplateIfExists(ctx context.Context, c client.Client, namespace, name string) error {
	template := &extensionsv1alpha1.SandboxTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
	if err := c.Delete(ctx, template); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting stale sandbox template %s/%s: %w", namespace, name, err)
	}
	return nil
}

func (r *AgentRunReconciler) monitorAgentSandbox(ctx context.Context, run *platformv1alpha1.AgentRun, requeueAfter time.Duration) (ctrl.Result, error) {
	if run == nil || run.Status.Sandbox == nil || run.Status.Sandbox.ClaimRef == nil {
		if run != nil && run.Status.Sandbox != nil && run.Status.Sandbox.SandboxRef != nil {
			return r.monitorPod(ctx, run, requeueAfter)
		}
		return ctrl.Result{}, nil
	}

	claimName := strings.TrimSpace(run.Status.Sandbox.ClaimRef.Name)
	if claimName == "" {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	claim := &extensionsv1alpha1.SandboxClaim{}
	if err := r.Get(ctx, client.ObjectKey{Name: claimName, Namespace: run.Namespace}, claim); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.markRunFailed(ctx, run, fmt.Errorf("sandbox claim %s disappeared", claimName))
		}
		return ctrl.Result{}, err
	}
	if sandboxClaimExpired(claim) {
		if runPastTimeout(run) {
			return ctrl.Result{}, r.markRunPaused(ctx, run, effectiveTimeout(run))
		}
		drained, err := r.releaseRunSandbox(ctx, run)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !drained {
			return ctrl.Result{Requeue: true}, nil
		}
		if err := clearRunSandboxStatus(ctx, r.Client, run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err := clearSandboxClaimLifecycle(ctx, r.Client, claim); err != nil {
		return ctrl.Result{}, err
	}
	if claimErr := claimReadyFailure(claim); claimErr != nil {
		return ctrl.Result{}, r.markRunFailed(ctx, run, claimErr)
	}

	podName := ""
	sandboxName := strings.TrimSpace(claim.Status.SandboxStatus.Name)
	if sandboxName != "" {
		resolvedPodName, err := resolveSandboxPodName(ctx, r.Client, run.Namespace, sandboxName)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return ctrl.Result{RequeueAfter: requeueAfter}, nil
			}
			return ctrl.Result{}, err
		}
		podName = resolvedPodName
	}
	if podName == "" && run.Status.Sandbox.SandboxRef != nil {
		podName = strings.TrimSpace(run.Status.Sandbox.SandboxRef.Name)
	}
	if podName == "" {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	if err := syncAgentSandboxRefs(ctx, r.Client, run, claimName, podName); err != nil {
		return ctrl.Result{}, fmt.Errorf("syncing sandbox refs: %w", err)
	}
	return r.monitorPodName(ctx, run, podName, requeueAfter)
}

func clearRunSandboxStatus(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) error {
	return retryAgentRunStatusPatch(ctx, c, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		fresh.Status.Sandbox = nil
	})
}

func syncAgentSandboxRefs(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun, claimName, podName string) error {
	if run != nil && run.Status.Sandbox != nil &&
		strings.EqualFold(run.Status.Sandbox.Provider, agentSandboxProvider) &&
		run.Status.Sandbox.ClaimRef != nil && run.Status.Sandbox.ClaimRef.Name == claimName &&
		((run.Status.Sandbox.SandboxRef == nil && strings.TrimSpace(podName) == "") ||
			(run.Status.Sandbox.SandboxRef != nil && run.Status.Sandbox.SandboxRef.Name == podName)) {
		return nil
	}

	return retryAgentRunStatusPatch(ctx, c, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		if fresh.Status.Sandbox == nil {
			fresh.Status.Sandbox = &platformv1alpha1.AgentRunSandboxStatus{}
		}
		fresh.Status.Sandbox.Provider = agentSandboxProvider
		fresh.Status.Sandbox.ClaimRef = &platformv1alpha1.NamedRef{Name: claimName}
		if strings.TrimSpace(podName) != "" {
			fresh.Status.Sandbox.SandboxRef = &platformv1alpha1.NamedRef{Name: podName}
		}
	})
}

func resolveSandboxPodName(ctx context.Context, c client.Client, namespace, sandboxName string) (string, error) {
	sandboxName = strings.TrimSpace(sandboxName)
	if sandboxName == "" {
		return "", nil
	}
	sandbox := &sandboxv1alpha1.Sandbox{}
	if err := c.Get(ctx, client.ObjectKey{Name: sandboxName, Namespace: namespace}, sandbox); err != nil {
		return "", err
	}
	if podName := strings.TrimSpace(sandbox.Annotations[sandboxv1alpha1.SandboxPodNameAnnotation]); podName != "" {
		return podName, nil
	}
	return sandbox.Name, nil
}

func claimReadyFailure(claim *extensionsv1alpha1.SandboxClaim) error {
	if claim == nil {
		return nil
	}
	ready := apimeta.FindStatusCondition(claim.Status.Conditions, string(sandboxv1alpha1.SandboxConditionReady))
	if ready == nil || ready.Status != metav1.ConditionFalse {
		return nil
	}
	switch ready.Reason {
	case "TemplateNotFound", "InvalidMetadata", "ReconcilerError":
		if strings.TrimSpace(ready.Message) != "" {
			return errors.New(ready.Message)
		}
		return fmt.Errorf("sandbox claim %s is not ready: %s", claim.Name, ready.Reason)
	default:
		return nil
	}
}

func sandboxClaimExpired(claim *extensionsv1alpha1.SandboxClaim) bool {
	if claim == nil {
		return false
	}
	ready := apimeta.FindStatusCondition(claim.Status.Conditions, string(sandboxv1alpha1.SandboxConditionReady))
	if ready == nil {
		return false
	}
	return ready.Reason == extensionsv1alpha1.ClaimExpiredReason ||
		ready.Reason == sandboxv1alpha1.SandboxReasonExpired
}

func persistWorkspaceEnabled(runtimeProfile *platformv1alpha1.RuntimeProfile) bool {
	return runtimeProfile != nil &&
		runtimeProfile.Spec.Sandbox != nil &&
		runtimeProfile.Spec.Sandbox.PersistWorkspace
}

func workspacePVCName(run *platformv1alpha1.AgentRun) string {
	return sanitizeDNSLabel("ws", run.Name)
}

func ensureWorkspacePVC(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun, runtimeProfile *platformv1alpha1.RuntimeProfile) (string, error) {
	name := workspacePVCName(run)

	existing := &corev1.PersistentVolumeClaim{}
	if err := c.Get(ctx, client.ObjectKey{Name: name, Namespace: run.Namespace}, existing); err == nil {
		return name, nil
	}

	storageSize := "10Gi"
	if runtimeProfile != nil && runtimeProfile.Spec.Sandbox != nil && runtimeProfile.Spec.Sandbox.WorkspaceSize != "" {
		storageSize = runtimeProfile.Spec.Sandbox.WorkspaceSize
	}
	storageQuantity, err := resource.ParseQuantity(storageSize)
	if err != nil {
		return "", fmt.Errorf("invalid RuntimeProfile sandbox workspaceSize %q: %w", storageSize, err)
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: run.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":                "gratefulagents",
				"app.kubernetes.io/component":           "workspace",
				"platform.gratefulagents.dev/owner-run": run.Name,
			},
			OwnerReferences: []metav1.OwnerReference{runOwnerRef(run)},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageQuantity,
				},
			},
		},
	}
	if err := c.Create(ctx, pvc); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return name, nil
		}
		return "", fmt.Errorf("creating workspace PVC: %w", err)
	}
	return name, nil
}
