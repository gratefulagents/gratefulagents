package platform

import (
	"context"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	agentsandboxv1alpha1 "sigs.k8s.io/agent-sandbox/api/v1alpha1"
	agentsandboxextensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRuntimeProfileReconcilerSetsResolvedDefaultsHash(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	profile := &platformv1alpha1.RuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "interactive",
			Namespace:  "default",
			Generation: 3,
		},
		Spec: platformv1alpha1.RuntimeProfileSpec{
			Security: &platformv1alpha1.RuntimeProfileSecurity{
				PermissionMode: platformv1alpha1.PermissionMode("read-only"),
				DefaultTimeout: metav1.Duration{Duration: 30 * time.Minute},
			},
			Admission: &platformv1alpha1.RuntimeProfileAdmission{
				MaxConcurrentRuns: 3,
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.RuntimeProfile{}).
		WithObjects(profile).
		Build()

	reconciler := &RuntimeProfileReconciler{Client: c}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(profile)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updated := &platformv1alpha1.RuntimeProfile{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(profile), updated); err != nil {
		t.Fatalf("Get(RuntimeProfile) error = %v", err)
	}
	if updated.Status.Phase != "Ready" {
		t.Fatalf("Status.Phase = %q, want Ready", updated.Status.Phase)
	}
	if updated.Status.ResolvedDefaultsHash == "" {
		t.Fatal("Status.ResolvedDefaultsHash = empty, want populated hash")
	}
	if len(updated.Status.Conditions) != 1 {
		t.Fatalf("len(Status.Conditions) = %d, want 1", len(updated.Status.Conditions))
	}
	cond := updated.Status.Conditions[0]
	if cond.Type != "Ready" || cond.Status != metav1.ConditionTrue || cond.Reason != "ResolvedDefaults" {
		t.Fatalf("Condition = %#v, want Ready=True/ResolvedDefaults", cond)
	}
	if cond.ObservedGeneration != 3 {
		t.Fatalf("Condition.ObservedGeneration = %d, want 3", cond.ObservedGeneration)
	}
}

func TestRuntimeProfileResolvedDefaultsHashTracksSpecChanges(t *testing.T) {
	t.Parallel()

	base := &platformv1alpha1.RuntimeProfile{
		Spec: platformv1alpha1.RuntimeProfileSpec{
			Security: &platformv1alpha1.RuntimeProfileSecurity{
				PermissionMode: platformv1alpha1.PermissionMode("workspace-write"),
				DefaultTimeout: metav1.Duration{Duration: 45 * time.Minute},
			},
		},
	}
	same := base.DeepCopy()
	changed := base.DeepCopy()
	changed.Spec.Security.DefaultTimeout = metav1.Duration{Duration: 90 * time.Minute}

	baseHash, err := runtimeProfileResolvedDefaultsHash(base)
	if err != nil {
		t.Fatalf("runtimeProfileResolvedDefaultsHash(base) error = %v", err)
	}
	sameHash, err := runtimeProfileResolvedDefaultsHash(same)
	if err != nil {
		t.Fatalf("runtimeProfileResolvedDefaultsHash(same) error = %v", err)
	}
	changedHash, err := runtimeProfileResolvedDefaultsHash(changed)
	if err != nil {
		t.Fatalf("runtimeProfileResolvedDefaultsHash(changed) error = %v", err)
	}

	if baseHash != sameHash {
		t.Fatalf("same spec hash mismatch: %q != %q", baseHash, sameHash)
	}
	if baseHash == changedHash {
		t.Fatalf("changed spec hash = %q, want different from base hash", changedHash)
	}
}

func TestEnsureWorkspacePVCRejectsInvalidWorkspaceSizeWithoutPanic(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bad-workspace-size",
			Namespace: "default",
			UID:       types.UID("bad-workspace-size-uid"),
		},
	}
	profile := &platformv1alpha1.RuntimeProfile{
		Spec: platformv1alpha1.RuntimeProfileSpec{
			Sandbox: &platformv1alpha1.RuntimeProfileSandbox{
				WorkspaceSize: "not-a-quantity",
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("ensureWorkspacePVC() panicked: %v", recovered)
		}
	}()
	_, err := ensureWorkspacePVC(context.Background(), c, run, profile)
	if err == nil {
		t.Fatal("ensureWorkspacePVC() error = nil, want invalid workspaceSize error")
	}
	if !strings.Contains(err.Error(), "invalid RuntimeProfile sandbox workspaceSize") {
		t.Fatalf("error = %v, want invalid workspaceSize", err)
	}
}

func TestReconcileRunAppliesRuntimeProfileSandboxOverridesAndWarmPool(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(rbac): %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "runtime-profile-overrides",
			Namespace: "default",
			UID:       types.UID("runtime-profile-overrides-uid"),
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository:        platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BaseBranch: "main"},
			WorkflowMode:      platformv1alpha1.WorkflowModeAuto,
			Model:             "gpt-5.4",
			Image:             "ghcr.io/example/worker:latest",
			RuntimeProfileRef: &platformv1alpha1.NamedRef{Name: "sandboxed"},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhasePending,
			Queue: &platformv1alpha1.AgentRunQueueStatus{State: "Queued"},
		},
	}
	profile := &platformv1alpha1.RuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "sandboxed", Namespace: "default"},
		Spec: platformv1alpha1.RuntimeProfileSpec{
			Sandbox: &platformv1alpha1.RuntimeProfileSandbox{
				SandboxTemplateRef:  &platformv1alpha1.NamedRef{Name: "shared-template"},
				RuntimeClassName:    "gvisor",
				WarmPoolRef:         &platformv1alpha1.NamedRef{Name: "fast-pool"},
				EnablePrivateProcfs: true,
			},
			Security: &platformv1alpha1.RuntimeProfileSecurity{
				EgressMode: platformv1alpha1.EgressMode("disabled"),
			},
			Resources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
				},
			},
		},
	}
	baseTemplate := &agentsandboxextensionsv1alpha1.SandboxTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-template", Namespace: "default"},
		Spec: agentsandboxextensionsv1alpha1.SandboxTemplateSpec{
			PodTemplate: agentsandboxv1alpha1.PodTemplate{
				ObjectMeta: agentsandboxv1alpha1.PodMetadata{
					Labels:      map[string]string{"from-base": "yes"},
					Annotations: map[string]string{"base-annotation": "true"},
				},
			},
			NetworkPolicyManagement: agentsandboxextensionsv1alpha1.NetworkPolicyManagementUnmanaged,
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run, profile, baseTemplate).
		Build()

	reconciler := &AgentRunReconciler{Client: c}
	result, err := reconciler.reconcileRun(context.Background(), run)
	if err != nil {
		t.Fatalf("reconcileRun() error = %v", err)
	}
	if result.RequeueAfter != 2*time.Second {
		t.Fatalf("RequeueAfter = %s, want 2s", result.RequeueAfter)
	}

	claim := &agentsandboxextensionsv1alpha1.SandboxClaim{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: sandboxClaimName(run), Namespace: run.Namespace}, claim); err != nil {
		t.Fatalf("Get(SandboxClaim) error = %v", err)
	}
	if claim.Spec.WarmPool == nil || string(*claim.Spec.WarmPool) != "fast-pool" {
		t.Fatalf("Claim.Spec.WarmPool = %#v, want fast-pool", claim.Spec.WarmPool)
	}

	template := &agentsandboxextensionsv1alpha1.SandboxTemplate{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: managedSandboxTemplateName(run), Namespace: run.Namespace}, template); err != nil {
		t.Fatalf("Get(managed SandboxTemplate) error = %v", err)
	}
	if template.Spec.NetworkPolicyManagement != agentsandboxextensionsv1alpha1.NetworkPolicyManagementUnmanaged {
		t.Fatalf("NetworkPolicyManagement = %q, want Unmanaged", template.Spec.NetworkPolicyManagement)
	}
	if template.Spec.PodTemplate.ObjectMeta.Labels["from-base"] != "yes" {
		t.Fatalf("base label missing from managed template: %#v", template.Spec.PodTemplate.ObjectMeta.Labels)
	}
	if template.Spec.PodTemplate.ObjectMeta.Annotations["base-annotation"] != "true" {
		t.Fatalf("base annotation missing from managed template: %#v", template.Spec.PodTemplate.ObjectMeta.Annotations)
	}
	if template.Spec.PodTemplate.Spec.RuntimeClassName == nil || *template.Spec.PodTemplate.Spec.RuntimeClassName != "gvisor" {
		t.Fatalf("RuntimeClassName = %#v, want gvisor", template.Spec.PodTemplate.Spec.RuntimeClassName)
	}
	if template.Spec.PodTemplate.Spec.HostUsers == nil || *template.Spec.PodTemplate.Spec.HostUsers {
		t.Fatalf("HostUsers = %#v, want false for private procfs", template.Spec.PodTemplate.Spec.HostUsers)
	}
	workerSecurityContext := template.Spec.PodTemplate.Spec.Containers[0].SecurityContext
	if workerSecurityContext == nil || workerSecurityContext.ProcMount == nil || *workerSecurityContext.ProcMount != corev1.UnmaskedProcMount {
		t.Fatalf("worker ProcMount = %#v, want %q", workerSecurityContext, corev1.UnmaskedProcMount)
	}
	if template.Spec.NetworkPolicy == nil {
		t.Fatal("NetworkPolicy = nil, want explicit disabled policy")
	}
	container := template.Spec.PodTemplate.Spec.Containers[0]
	if got := container.Resources.Requests.Cpu().String(); got != "500m" {
		t.Fatalf("Resources.Requests.CPU = %q, want 500m", got)
	}
	if got := container.Resources.Limits.Memory().String(); got != "4Gi" {
		t.Fatalf("Resources.Limits.Memory = %q, want 4Gi", got)
	}
}

func TestReconcileRunQueuesWhenRuntimeProfileMaxConcurrentRunsReached(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(rbac): %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	profile := &platformv1alpha1.RuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "busy", Namespace: "default"},
		Spec: platformv1alpha1.RuntimeProfileSpec{
			Admission: &platformv1alpha1.RuntimeProfileAdmission{
				MaxConcurrentRuns: 1,
			},
		},
	}
	active := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "active", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			RuntimeProfileRef: &platformv1alpha1.NamedRef{Name: "busy"},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
			Queue: &platformv1alpha1.AgentRunQueueStatus{State: "Running"},
		},
	}
	queued := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "queued", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			RuntimeProfileRef: &platformv1alpha1.NamedRef{Name: "busy"},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhasePending,
			Queue: &platformv1alpha1.AgentRunQueueStatus{State: "Queued"},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(profile, active, queued).
		Build()

	reconciler := &AgentRunReconciler{Client: c}
	result, err := reconciler.reconcileRun(context.Background(), queued)
	if err != nil {
		t.Fatalf("reconcileRun() error = %v", err)
	}
	if result.RequeueAfter != runtimeProfileAdmissionRequeueAfter {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, runtimeProfileAdmissionRequeueAfter)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(queued), updated); err != nil {
		t.Fatalf("Get(queued run) error = %v", err)
	}
	if updated.Status.Queue == nil || !strings.Contains(updated.Status.Queue.BlockedReason, "maxConcurrentRuns=1") {
		t.Fatalf("BlockedReason = %#v, want maxConcurrentRuns message", updated.Status.Queue)
	}
}

func TestReconcileRunQueuesWhenRuntimeProfileNamespaceLimitReached(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(rbac): %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	addAgentSandboxSchemes(t, scheme)

	profile := &platformv1alpha1.RuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "namespaced", Namespace: "default"},
		Spec: platformv1alpha1.RuntimeProfileSpec{
			Admission: &platformv1alpha1.RuntimeProfileAdmission{
				PerNamespaceMaxConcurrentRuns: 1,
			},
		},
	}
	active := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "active", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			RuntimeProfileRef: &platformv1alpha1.NamedRef{Name: "namespaced"},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseBlocked,
			Queue: &platformv1alpha1.AgentRunQueueStatus{State: "Blocked", BlockedReason: "waiting-for-user"},
		},
	}
	queued := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "queued", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			RuntimeProfileRef: &platformv1alpha1.NamedRef{Name: "namespaced"},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhasePending,
			Queue: &platformv1alpha1.AgentRunQueueStatus{State: "Queued"},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(profile, active, queued).
		Build()

	reconciler := &AgentRunReconciler{Client: c}
	result, err := reconciler.reconcileRun(context.Background(), queued)
	if err != nil {
		t.Fatalf("reconcileRun() error = %v", err)
	}
	if result.RequeueAfter != runtimeProfileAdmissionRequeueAfter {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, runtimeProfileAdmissionRequeueAfter)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(queued), updated); err != nil {
		t.Fatalf("Get(queued run) error = %v", err)
	}
	if updated.Status.Queue == nil || !strings.Contains(updated.Status.Queue.BlockedReason, "perNamespaceMaxConcurrentRuns=1") {
		t.Fatalf("BlockedReason = %#v, want perNamespaceMaxConcurrentRuns message", updated.Status.Queue)
	}
}

func TestReconcileRunFailsWhenRuntimeProfileAdmissionBecomesStale(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(rbac): %v", err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	profile := &platformv1alpha1.RuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "stale", Namespace: "default"},
		Spec: platformv1alpha1.RuntimeProfileSpec{
			Admission: &platformv1alpha1.RuntimeProfileAdmission{
				StaleRunTimeout: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "waiting-too-long", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			RuntimeProfileRef: &platformv1alpha1.NamedRef{Name: "stale"},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     platformv1alpha1.AgentRunPhasePending,
			Queue:     &platformv1alpha1.AgentRunQueueStatus{State: "Queued"},
			StartedAt: &metav1.Time{Time: time.Now().Add(-10 * time.Minute)},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(profile, run).
		Build()

	reconciler := &AgentRunReconciler{Client: c}
	if _, err := reconciler.reconcileRun(context.Background(), run); err != nil {
		t.Fatalf("reconcileRun() error = %v", err)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(run), updated); err != nil {
		t.Fatalf("Get(run) error = %v", err)
	}
	if updated.Status.Phase != platformv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("Phase = %q, want Failed", updated.Status.Phase)
	}
	if !strings.Contains(updated.Status.LastError, "staleRunTimeout") {
		t.Fatalf("LastError = %q, want staleRunTimeout message", updated.Status.LastError)
	}
}
