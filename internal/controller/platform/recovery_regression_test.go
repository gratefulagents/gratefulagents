package platform

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestStoppedRunWithoutSandboxReleasesBindings(t *testing.T) {
	for _, phase := range []platformv1alpha1.AgentRunPhase{platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhaseCancelled, platformv1alpha1.AgentRunPhasePaused} {
		t.Run(string(phase), func(t *testing.T) {
			run := pausedRun("no-sandbox", timeoutPauseReason(6*time.Hour), "", 7*time.Hour)
			run.Status.Phase = phase
			binding := runClusterRoleBinding(run)
			c := fake.NewClientBuilder().WithScheme(newReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(run, binding).Build()
			if _, err := (&AgentRunReconciler{Client: c}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(binding), &rbacv1.ClusterRoleBinding{}); !apierrors.IsNotFound(err) {
				t.Fatalf("binding remains: %v", err)
			}
		})
	}
}

func TestTimeoutExtensionAfterLongPauseStartsFreshWindow(t *testing.T) {
	run := pausedRun("long-timeout-pause", timeoutPauseReason(6*time.Hour), "", 48*time.Hour)
	run.Spec.Limits.MaxRuntime.Duration = 7 * time.Hour
	c := fake.NewClientBuilder().WithScheme(newReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(run).Build()
	if _, err := (&AgentRunReconciler{Client: c}).reconcilePausedRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	got := getRun(t, c, run)
	if got.Status.Phase != platformv1alpha1.AgentRunPhaseProvisioning || runPastTimeout(got) {
		t.Fatalf("runtime extension remained blocked by paused time: %#v", got.Status)
	}
	if runConsumesAdmissionSlot(got) {
		t.Fatal("resumed run bypasses admission before reserving capacity")
	}
}

func TestTimeoutObservationRechecksExtendedLimit(t *testing.T) {
	run := provisioningRun("extended-while-observed", time.Now().Add(-7*time.Hour))
	fresh := run.DeepCopy()
	fresh.Spec.Limits.MaxRuntime.Duration = 12 * time.Hour
	c := fake.NewClientBuilder().WithScheme(newReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(fresh).Build()
	if err := (&AgentRunReconciler{Client: c}).markRunPaused(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if getRun(t, c, run).Status.Phase == platformv1alpha1.AgentRunPhasePaused {
		t.Fatal("stale timeout observation ignored extended limit")
	}
}

func TestProvisioningRefusesUnownedResources(t *testing.T) {
	run := provisioningRun("ownership", time.Now())
	sa := sandboxRunResourceName("run", run)
	for _, obj := range []client.Object{
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: sa, Namespace: run.Namespace}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: sa + "-role", Namespace: run.Namespace}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: sa + "-binding", Namespace: run.Namespace}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: workspacePVCName(run), Namespace: run.Namespace}},
	} {
		t.Run(obj.GetName(), func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(newReconcilerTestScheme(t)).WithObjects(obj).Build()
			var err error
			if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
				_, err = ensureWorkspacePVC(context.Background(), c, run, nil)
			} else {
				err = ensureRunRBAC(context.Background(), c, run, sa)
			}
			if !isPermanentProvisioningError(err) {
				t.Fatalf("unowned resource accepted: %v", err)
			}
		})
	}
}

func TestSandboxTemplateRefreshesChangedRenderedSpec(t *testing.T) {
	run := provisioningRun("refresh-template", time.Now())
	c := fake.NewClientBuilder().WithScheme(newReconcilerTestScheme(t)).Build()
	name, err := ensureRunSandboxTemplate(context.Background(), c, run, nil, "sa", "")
	if err != nil {
		t.Fatal(err)
	}
	run.Spec.Image = "worker:new"
	if _, err := ensureRunSandboxTemplate(context.Background(), c, run, nil, "sa", ""); err != nil {
		t.Fatal(err)
	}
	got := &extensionsv1alpha1.SandboxTemplate{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: run.Namespace, Name: name}, got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.PodTemplate.Spec.Containers[0].Image != "worker:new" {
		t.Fatalf("template retained stale spec: %v", got.Spec.PodTemplate.Spec.Containers)
	}
	rv := got.ResourceVersion
	if _, err := ensureRunSandboxTemplate(context.Background(), c, run, nil, "sa", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(got), got); err != nil {
		t.Fatal(err)
	}
	if got.ResourceVersion != rv {
		t.Fatal("unchanged template was rewritten")
	}
}

func TestMCPSecretEnvsPropagatesSkillReadFailures(t *testing.T) {
	run := provisioningRun("mcp-skill", time.Now())
	run.Spec.SkillRefs = []platformv1alpha1.NamedRef{{Name: "skill"}}
	injected := apierrors.NewTimeoutError("unavailable", 1)
	c := fake.NewClientBuilder().WithScheme(newReconcilerTestScheme(t)).WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*platformv1alpha1.Skill); ok {
			return injected
		}
		return c.Get(ctx, key, obj, opts...)
	}}).Build()
	if _, err := resolveMCPServerSecretEnvs(context.Background(), c, run); !errors.Is(err, injected) {
		t.Fatalf("skill error swallowed: %v", err)
	}
}

func TestConcurrentAdmissionConfirmsCapacityBeyondStaleCache(t *testing.T) {
	ctx := context.Background()
	profile := &platformv1alpha1.RuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "limited", Namespace: "default"}, Spec: platformv1alpha1.RuntimeProfileSpec{Admission: &platformv1alpha1.RuntimeProfileAdmission{MaxConcurrentRuns: 1}}}
	first, second := provisioningRun("first", time.Now()), provisioningRun("second", time.Now())
	for _, run := range []*platformv1alpha1.AgentRun{first, second} {
		run.Spec.RuntimeProfileRef = &platformv1alpha1.NamedRef{Name: profile.Name}
	}
	base := fake.NewClientBuilder().WithScheme(newReconcilerTestScheme(t)).WithStatusSubresource(first).WithObjects(first, second, profile).Build()
	cached := interceptor.NewClient(base, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if runs, ok := list.(*platformv1alpha1.AgentRunList); ok {
			runs.Items = []platformv1alpha1.AgentRun{*first.DeepCopy(), *second.DeepCopy()}
			return nil
		}
		return c.List(ctx, list, opts...)
	}})
	r := &AgentRunReconciler{Client: cached, APIReader: base}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, run := range []*platformv1alpha1.AgentRun{first, second} {
		wg.Go(func() { _, err := r.reconcileRun(ctx, run); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	admitted := 0
	for _, run := range []*platformv1alpha1.AgentRun{first, second} {
		if getRun(t, base, run).Status.Phase == platformv1alpha1.AgentRunPhaseAdmitted {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("admitted %d runs into one slot", admitted)
	}
}

func TestAdmissionReservationSurvivesProvisioningFailure(t *testing.T) {
	run := provisioningRun("reserved", time.Now())
	c := fake.NewClientBuilder().WithScheme(newReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(run).WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		return apierrors.NewTimeoutError("unavailable", 1)
	}}).Build()
	if _, err := (&AgentRunReconciler{Client: c}).reconcileRun(context.Background(), run); err == nil {
		t.Fatal("expected provisioning error")
	}
	if !runConsumesAdmissionSlot(getRun(t, c, run)) {
		t.Fatal("failed provisioning lost admission reservation")
	}
}

func TestQueuedSandboxDoesNotOverwriteNewInteractiveStatus(t *testing.T) {
	for _, phase := range []platformv1alpha1.AgentRunPhase{platformv1alpha1.AgentRunPhaseQuestion, platformv1alpha1.AgentRunPhaseBlocked, platformv1alpha1.AgentRunPhaseWaitingApproval} {
		run := provisioningRun("interactive", time.Now())
		fresh := run.DeepCopy()
		fresh.Status.Phase = phase
		c := fake.NewClientBuilder().WithScheme(newReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(fresh).Build()
		sandbox := &platformv1alpha1.AgentRunSandboxStatus{Provider: agentSandboxProvider, ClaimRef: &platformv1alpha1.NamedRef{Name: "claim"}}
		if _, err := (&AgentRunReconciler{Client: c}).patchSandboxQueued(context.Background(), run, sandbox); err != nil {
			t.Fatal(err)
		}
		got := getRun(t, c, run)
		if got.Status.Phase != phase || got.Status.Sandbox == nil {
			t.Fatalf("overwrote interactive status: %#v", got.Status)
		}
	}
}

func TestPodObservationsDoNotOverwriteNewAttempt(t *testing.T) {
	for _, write := range []func(*AgentRunReconciler, *platformv1alpha1.AgentRun) error{
		func(r *AgentRunReconciler, run *platformv1alpha1.AgentRun) error {
			return r.patchRunning(context.Background(), run)
		},
		func(r *AgentRunReconciler, run *platformv1alpha1.AgentRun) error {
			return r.patchSucceeded(context.Background(), run)
		},
		func(r *AgentRunReconciler, run *platformv1alpha1.AgentRun) error {
			return r.markRunFailed(context.Background(), run, errors.New("old pod"))
		},
		func(r *AgentRunReconciler, run *platformv1alpha1.AgentRun) error {
			return r.markRunPaused(context.Background(), run)
		},
	} {
		run := provisioningRun("new-attempt", time.Now())
		fresh := run.DeepCopy()
		now := metav1.Now()
		fresh.Status.LastWakeTime = &now
		c := fake.NewClientBuilder().WithScheme(newReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(fresh).Build()
		if err := write(&AgentRunReconciler{Client: c}, run); err != nil {
			t.Fatal(err)
		}
		if got := getRun(t, c, run); got.Status.Phase != platformv1alpha1.AgentRunPhasePending {
			t.Fatalf("new attempt changed: %s", got.Status.Phase)
		}
	}
}

func TestTeamSummaryConflictRecomputesChildren(t *testing.T) {
	parent := newTeamParentRun()
	child := newOwnedTeamChild(parent, "child", "implement", "coder")
	child.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
	conflicts := 0
	c := fake.NewClientBuilder().WithScheme(newReconcilerTestScheme(t)).WithStatusSubresource(parent).WithObjects(parent, child).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		if obj.GetName() == parent.Name && conflicts == 0 {
			conflicts++
			updated := &platformv1alpha1.AgentRun{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(child), updated); err != nil {
				return err
			}
			updated.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
			if err := c.Status().Update(ctx, updated); err != nil {
				return err
			}
			return apierrors.NewConflict(schema.GroupResource{Resource: "agentruns"}, parent.Name, errors.New("concurrent update"))
		}
		return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
	}}).Build()
	if _, err := (&AgentRunReconciler{Client: c}).syncTeamParentStatus(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	got := getRun(t, c, parent)
	if conflicts != 1 || len(got.Status.Children) != 1 || got.Status.Children[0].Phase != platformv1alpha1.AgentRunPhaseSucceeded {
		t.Fatalf("stale summary after conflict: %#v", got.Status.Children)
	}
}

func TestMissingSandboxReferenceHasStartupDeadline(t *testing.T) {
	run := provisioningRun("missing-ref", time.Now().Add(-time.Hour))
	run.Status.Phase = platformv1alpha1.AgentRunPhaseAdmitted
	run.Status.Sandbox = &platformv1alpha1.AgentRunSandboxStatus{Provider: agentSandboxProvider}
	c := fake.NewClientBuilder().WithScheme(newReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(run).Build()
	if _, err := (&AgentRunReconciler{Client: c}).monitorAgentSandbox(context.Background(), run, time.Second); err != nil {
		t.Fatal(err)
	}
	if got := getRun(t, c, run); got.Status.Phase != platformv1alpha1.AgentRunPhaseFailed || !strings.Contains(got.Status.LastError, "did not start") {
		t.Fatalf("unbounded startup: %#v", got.Status)
	}
}

func TestAutomaticResumeGetsFreshAdmissionDeadline(t *testing.T) {
	run := pausedRun("resume-stale-admission", timeoutPauseReason(6*time.Hour), "", 48*time.Hour)
	run.Spec.Limits.MaxRuntime.Duration = 7 * time.Hour
	c := fake.NewClientBuilder().WithScheme(newReconcilerTestScheme(t)).WithStatusSubresource(run).WithObjects(run).Build()
	r := &AgentRunReconciler{Client: c}
	if _, err := r.reconcilePausedRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	resumed := getRun(t, c, run)
	profile := &platformv1alpha1.RuntimeProfile{Spec: platformv1alpha1.RuntimeProfileSpec{
		Admission: &platformv1alpha1.RuntimeProfileAdmission{StaleRunTimeout: metav1.Duration{Duration: 90 * time.Minute}},
	}}
	result, err := r.enforceRuntimeProfileAdmission(context.Background(), resumed, profile)
	if err != nil || result != nil {
		t.Fatalf("fresh attempt rejected as stale: result=%v err=%v", result, err)
	}
	if got := getRun(t, c, run); got.Status.Phase != platformv1alpha1.AgentRunPhaseProvisioning {
		t.Fatalf("fresh resume phase=%s error=%s", got.Status.Phase, got.Status.LastError)
	}
}
