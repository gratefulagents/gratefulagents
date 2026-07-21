package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	agentpolicy "github.com/gratefulagents/sdk/pkg/agentsdk/policy"
)

func fastStartupRetries(t *testing.T) {
	t.Helper()
	prevDelay := startupPermissionRetryDelay
	startupPermissionRetryDelay = time.Millisecond
	t.Cleanup(func() { startupPermissionRetryDelay = prevDelay })
}

func permissionModeScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return scheme
}

func writeProfileRun() (*platformv1alpha1.AgentRun, *platformv1alpha1.RuntimeProfile) {
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			RuntimeProfileRef: &platformv1alpha1.NamedRef{Name: "profile-1"},
		},
	}
	profile := &platformv1alpha1.RuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "profile-1", Namespace: "default"},
		Spec: platformv1alpha1.RuntimeProfileSpec{
			Security: &platformv1alpha1.RuntimeProfileSecurity{
				PermissionMode: platformv1alpha1.PermissionModeWorkspaceWrite,
			},
		},
	}
	return run, profile
}

func TestResolveStartupPermissionModeResolvesDisabledGitRemoteWrites(t *testing.T) {
	fastStartupRetries(t)
	run, profile := writeProfileRun()
	profile.Spec.Security.GitRemoteWrites = platformv1alpha1.GitRemoteWritesDisabled
	c := fake.NewClientBuilder().WithScheme(permissionModeScheme(t)).WithObjects(run, profile).Build()

	res := resolveStartupPermissionMode(context.Background(), c, "run-1", "default")
	if res.GitRemoteWrites != agentpolicy.GitRemoteWritesDisabled {
		t.Fatalf("GitRemoteWrites = %q, want disabled", res.GitRemoteWrites)
	}
}

func TestResolveStartupPermissionModeRetriesTransientProfileErrors(t *testing.T) {
	fastStartupRetries(t)
	run, profile := writeProfileRun()

	failures := 2
	c := fake.NewClientBuilder().
		WithScheme(permissionModeScheme(t)).
		WithObjects(run, profile).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey,
				obj client.Object, opts ...client.GetOption,
			) error {
				if _, ok := obj.(*platformv1alpha1.RuntimeProfile); ok && failures > 0 {
					failures--
					return fmt.Errorf("transient API error")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	res := resolveStartupPermissionMode(context.Background(), c, "run-1", "default")
	if res.Mode != agentpolicy.PermissionModeWorkspaceWrite {
		t.Fatalf("mode = %q, want workspace-write after transient failures", res.Mode)
	}
	if res.Run == nil {
		t.Fatal("run = nil, want resolved AgentRun")
	}
	if res.Degraded {
		t.Fatal("Degraded = true, want false after successful resolution")
	}
	if failures != 0 {
		t.Fatalf("failures remaining = %d, want 0 (retries consumed)", failures)
	}
}

// A missing RuntimeProfile is retried: a freshly created run can race the
// creation of its profile, and giving up instantly pinned whole sessions to
// a read-only filesystem.
func TestResolveStartupPermissionModeMissingProfileRetriesThenDegrades(t *testing.T) {
	fastStartupRetries(t)
	run, _ := writeProfileRun()

	profileGets := 0
	c := fake.NewClientBuilder().
		WithScheme(permissionModeScheme(t)).
		WithObjects(run).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey,
				obj client.Object, opts ...client.GetOption,
			) error {
				if _, ok := obj.(*platformv1alpha1.RuntimeProfile); ok {
					profileGets++
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	res := resolveStartupPermissionMode(context.Background(), c, "run-1", "default")
	if res.Mode != agentpolicy.PermissionModeReadOnly {
		t.Fatalf("mode = %q, want read-only for missing profile", res.Mode)
	}
	if !res.Degraded {
		t.Fatal("Degraded = false, want true: a missing profile is a failure/race, not explicit config")
	}
	if res.Reason == "" {
		t.Fatal("Reason = empty, want an explanation for the read-only fallback")
	}
	if profileGets != startupPermissionAttempts {
		t.Fatalf("profile Get attempts = %d, want %d (NotFound must retry)", profileGets, startupPermissionAttempts)
	}
}

// The profile appearing while startup retries are still running must heal
// in place: this is the run-creation race in its benign form.
func TestResolveStartupPermissionModeProfileCreatedLateResolvesWrite(t *testing.T) {
	fastStartupRetries(t)
	run, profile := writeProfileRun()

	notFoundLeft := 2
	gr := schema.GroupResource{Group: "platform.gratefulagents.dev", Resource: "runtimeprofiles"}
	c := fake.NewClientBuilder().
		WithScheme(permissionModeScheme(t)).
		WithObjects(run, profile).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey,
				obj client.Object, opts ...client.GetOption,
			) error {
				if _, ok := obj.(*platformv1alpha1.RuntimeProfile); ok && notFoundLeft > 0 {
					notFoundLeft--
					return apierrors.NewNotFound(gr, key.Name)
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	res := resolveStartupPermissionMode(context.Background(), c, "run-1", "default")
	if res.Mode != agentpolicy.PermissionModeWorkspaceWrite {
		t.Fatalf("mode = %q, want workspace-write once the late profile resolves", res.Mode)
	}
	if res.Degraded {
		t.Fatal("Degraded = true, want false after the late profile resolved")
	}
	if notFoundLeft != 0 {
		t.Fatalf("notFoundLeft = %d, want 0 (NotFound retries consumed)", notFoundLeft)
	}
}

func TestResolveStartupPermissionModePersistentErrorsFallBackReadOnly(t *testing.T) {
	fastStartupRetries(t)
	run, profile := writeProfileRun()

	profileGets := 0
	c := fake.NewClientBuilder().
		WithScheme(permissionModeScheme(t)).
		WithObjects(run, profile).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey,
				obj client.Object, opts ...client.GetOption,
			) error {
				if _, ok := obj.(*platformv1alpha1.RuntimeProfile); ok {
					profileGets++
					return fmt.Errorf("persistent API error")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	res := resolveStartupPermissionMode(context.Background(), c, "run-1", "default")
	if res.Mode != agentpolicy.PermissionModeReadOnly {
		t.Fatalf("mode = %q, want read-only after exhausted retries", res.Mode)
	}
	if !res.Degraded {
		t.Fatal("Degraded = false, want true after exhausted retries")
	}
	if res.Run == nil {
		t.Fatal("run = nil, want resolved AgentRun even when profile is unreadable")
	}
	if profileGets != startupPermissionAttempts {
		t.Fatalf("profile Get attempts = %d, want %d", profileGets, startupPermissionAttempts)
	}
}

func TestResolveStartupPermissionModeRetriesTransientRunReadErrors(t *testing.T) {
	fastStartupRetries(t)
	run, profile := writeProfileRun()

	failures := 2
	c := fake.NewClientBuilder().
		WithScheme(permissionModeScheme(t)).
		WithObjects(run, profile).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey,
				obj client.Object, opts ...client.GetOption,
			) error {
				if _, ok := obj.(*platformv1alpha1.AgentRun); ok && failures > 0 {
					failures--
					return fmt.Errorf("transient API error")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	res := resolveStartupPermissionMode(context.Background(), c, "run-1", "default")
	if res.Mode != agentpolicy.PermissionModeWorkspaceWrite {
		t.Fatalf("mode = %q, want workspace-write after transient run-read failures", res.Mode)
	}
	if res.Run == nil {
		t.Fatal("run = nil, want resolved AgentRun")
	}
	if res.Degraded {
		t.Fatal("Degraded = true, want false after successful resolution")
	}
}

func TestResolveStartupPermissionModeUnreadableRunDegradesReadOnly(t *testing.T) {
	fastStartupRetries(t)
	c := fake.NewClientBuilder().
		WithScheme(permissionModeScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				return fmt.Errorf("persistent API error")
			},
		}).
		Build()

	res := resolveStartupPermissionMode(context.Background(), c, "run-1", "default")
	if res.Mode != agentpolicy.PermissionModeReadOnly {
		t.Fatalf("mode = %q, want read-only when the AgentRun is unreadable", res.Mode)
	}
	if !res.Degraded {
		t.Fatal("Degraded = false, want true when the AgentRun is unreadable")
	}
	if res.Run != nil {
		t.Fatal("run != nil, want nil when unreadable")
	}
}

func TestResolveStartupPermissionModeNoProfileRefIsDegradedReadOnly(t *testing.T) {
	fastStartupRetries(t)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "default"},
	}
	c := fake.NewClientBuilder().WithScheme(permissionModeScheme(t)).WithObjects(run).Build()

	res := resolveStartupPermissionMode(context.Background(), c, "run-1", "default")
	if res.Mode != agentpolicy.PermissionModeReadOnly {
		t.Fatalf("mode = %q, want read-only without RuntimeProfileRef", res.Mode)
	}
	if res.Run == nil {
		t.Fatal("run = nil, want resolved AgentRun")
	}
	if !res.Degraded {
		t.Fatal("Degraded = false, want true so the per-turn re-check can heal a later-added profile")
	}
}

// An explicit read-only grant is configuration, not degradation: the per-turn
// heal must not fight a profile that intends read-only.
func TestResolveStartupPermissionModeExplicitReadOnlyIsNotDegraded(t *testing.T) {
	fastStartupRetries(t)
	run, profile := writeProfileRun()
	profile.Spec.Security.PermissionMode = platformv1alpha1.PermissionModeReadOnly
	c := fake.NewClientBuilder().WithScheme(permissionModeScheme(t)).WithObjects(run, profile).Build()

	res := resolveStartupPermissionMode(context.Background(), c, "run-1", "default")
	if res.Mode != agentpolicy.PermissionModeReadOnly {
		t.Fatalf("mode = %q, want read-only", res.Mode)
	}
	if res.Degraded {
		t.Fatal("Degraded = true, want false for an explicit read-only profile")
	}
	if res.Reason == "" {
		t.Fatal("Reason = empty, want an explanation for the read-only session")
	}
}

func TestResolveStartupPermissionModeProfileWithoutModeIsNotDegraded(t *testing.T) {
	fastStartupRetries(t)
	run, profile := writeProfileRun()
	profile.Spec.Security = nil
	c := fake.NewClientBuilder().WithScheme(permissionModeScheme(t)).WithObjects(run, profile).Build()

	res := resolveStartupPermissionMode(context.Background(), c, "run-1", "default")
	if res.Mode != agentpolicy.PermissionModeReadOnly {
		t.Fatalf("mode = %q, want read-only when the profile grants nothing", res.Mode)
	}
	if res.Degraded {
		t.Fatal("Degraded = true, want false: the profile was read successfully")
	}
}

func TestClampResolvedPermissionMode(t *testing.T) {
	autonomousRun := &platformv1alpha1.AgentRun{
		Status: platformv1alpha1.AgentRunStatus{
			ModeSnapshot: &platformv1alpha1.ModeTemplateSpec{
				Autonomous:     true,
				PermissionMode: platformv1alpha1.PermissionModeReadOnly,
			},
		},
	}
	if got := clampResolvedPermissionMode(agentpolicy.PermissionModeWorkspaceWrite, autonomousRun); got != agentpolicy.PermissionModeReadOnly {
		t.Fatalf("autonomous clamp = %q, want read-only", got)
	}

	interactiveRun := &platformv1alpha1.AgentRun{
		Status: platformv1alpha1.AgentRunStatus{
			ModeSnapshot: &platformv1alpha1.ModeTemplateSpec{
				Autonomous:     false,
				PermissionMode: platformv1alpha1.PermissionModeReadOnly,
			},
		},
	}
	if got := clampResolvedPermissionMode(agentpolicy.PermissionModeWorkspaceWrite, interactiveRun); got != agentpolicy.PermissionModeWorkspaceWrite {
		t.Fatalf("interactive mode clamp = %q, want workspace-write (per-turn enforcement owns it)", got)
	}

	reviewerRun := &platformv1alpha1.AgentRun{
		Status: platformv1alpha1.AgentRunStatus{ModeName: reviewerModeName},
	}
	if got := clampResolvedPermissionMode(agentpolicy.PermissionModeWorkspaceWrite, reviewerRun); got != agentpolicy.PermissionModeReadOnly {
		t.Fatalf("legacy reviewer clamp = %q, want read-only", got)
	}

	if got := clampResolvedPermissionMode(agentpolicy.PermissionModeWorkspaceWrite, nil); got != agentpolicy.PermissionModeWorkspaceWrite {
		t.Fatalf("nil run clamp = %q, want workspace-write", got)
	}
}

func TestHealedWritePermissionMode(t *testing.T) {
	fastStartupRetries(t)
	run, profile := writeProfileRun()

	t.Run("heals when the profile now grants write", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(permissionModeScheme(t)).WithObjects(run, profile).Build()
		mode, ok := healedWritePermissionMode(context.Background(), c, run)
		if !ok {
			t.Fatal("ok = false, want heal when resolution succeeds with write access")
		}
		if mode != agentpolicy.PermissionModeWorkspaceWrite {
			t.Fatalf("mode = %q, want workspace-write", mode)
		}
	})

	t.Run("no heal while the profile is still missing", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(permissionModeScheme(t)).WithObjects(run).Build()
		if _, ok := healedWritePermissionMode(context.Background(), c, run); ok {
			t.Fatal("ok = true, want false while the profile is missing")
		}
	})

	t.Run("no heal for an explicit read-only profile", func(t *testing.T) {
		roProfile := profile.DeepCopy()
		roProfile.Spec.Security.PermissionMode = platformv1alpha1.PermissionModeReadOnly
		c := fake.NewClientBuilder().WithScheme(permissionModeScheme(t)).WithObjects(run, roProfile).Build()
		if _, ok := healedWritePermissionMode(context.Background(), c, run); ok {
			t.Fatal("ok = true, want false for an explicit read-only grant")
		}
	})

	t.Run("no heal when an autonomous template clamps read-only", func(t *testing.T) {
		clampedRun := run.DeepCopy()
		clampedRun.Status.ModeSnapshot = &platformv1alpha1.ModeTemplateSpec{
			Autonomous:     true,
			PermissionMode: platformv1alpha1.PermissionModeReadOnly,
		}
		c := fake.NewClientBuilder().WithScheme(permissionModeScheme(t)).WithObjects(clampedRun, profile).Build()
		if _, ok := healedWritePermissionMode(context.Background(), c, clampedRun); ok {
			t.Fatal("ok = true, want false when the mode template clamps to read-only")
		}
	})

	t.Run("no heal without a run", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(permissionModeScheme(t)).Build()
		if _, ok := healedWritePermissionMode(context.Background(), c, nil); ok {
			t.Fatal("ok = true, want false for nil run")
		}
	})
}

// Guard: the NotFound detection must match real apierrors semantics.
func TestStartupNotFoundDetection(t *testing.T) {
	gr := schema.GroupResource{Group: "platform.gratefulagents.dev", Resource: "runtimeprofiles"}
	err := apierrors.NewNotFound(gr, "profile-1")
	if !apierrors.IsNotFound(err) {
		t.Fatal("expected IsNotFound to hold for NewNotFound error")
	}
}
