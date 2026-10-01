package platform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestControllerRecoveryEnvtest(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Skip("set KUBEBUILDER_ASSETS to run real API-server regressions")
	}
	moduleDir, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "sigs.k8s.io/agent-sandbox").Output()
	if err != nil {
		t.Fatal(err)
	}
	env := &envtest.Environment{
		BinaryAssetsDirectory: assets,
		CRDDirectoryPaths: []string{
			"../../../config/crd/bases/platform.gratefulagents.dev_agentruns.yaml",
			filepath.Join(strings.TrimSpace(string(moduleDir)), "k8s/crds/extensions.agents.x-k8s.io_sandboxtemplates.yaml"),
		},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	c, err := client.NewWithWatch(cfg, client.Options{Scheme: newReconcilerTestScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "controller-recovery"}}
	if err := c.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	run := provisioningRun("real-api", time.Now())
	run.Namespace = ns.Name
	run.UID = ""
	run.Spec.Trigger = platformv1alpha1.TriggerRef{Kind: "Manual", Name: "test"}
	status := run.Status.DeepCopy()
	if err := c.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	run.Status = *status
	if err := c.Status().Update(ctx, run); err != nil {
		t.Fatal(err)
	}

	t.Run("optimistic-status-guard", func(t *testing.T) {
		conflicted := false
		wrapped := interceptor.NewClient(c, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, base client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if !conflicted {
				conflicted = true
				fresh := &platformv1alpha1.AgentRun{}
				if err := base.Get(ctx, client.ObjectKeyFromObject(run), fresh); err != nil {
					return err
				}
				fresh.Status.Phase = platformv1alpha1.AgentRunPhaseQuestion
				if err := base.Status().Update(ctx, fresh); err != nil {
					return err
				}
			}
			return base.SubResource(sub).Patch(ctx, obj, patch, opts...)
		}})
		if err := (&AgentRunReconciler{Client: wrapped}).patchRunning(ctx, run); err != nil {
			t.Fatal(err)
		}
		if got := getRun(t, c, run); !conflicted || got.Status.Phase != platformv1alpha1.AgentRunPhaseQuestion {
			t.Fatalf("concurrent worker status overwritten: %s", got.Status.Phase)
		}
	})

	t.Run("template-refresh-idempotence", func(t *testing.T) {
		name, err := ensureRunSandboxTemplate(ctx, c, run, nil, "worker-sa", "")
		if err != nil {
			t.Fatal(err)
		}
		run.Spec.Image = "worker:updated"
		if _, err := ensureRunSandboxTemplate(ctx, c, run, nil, "worker-sa", ""); err != nil {
			t.Fatal(err)
		}
		tpl := &extensionsv1alpha1.SandboxTemplate{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, tpl); err != nil {
			t.Fatal(err)
		}
		if tpl.Spec.PodTemplate.Spec.Containers[0].Image != run.Spec.Image {
			t.Fatal("template was not refreshed")
		}
		rv := tpl.ResourceVersion
		if _, err := ensureRunSandboxTemplate(ctx, c, run, nil, "worker-sa", ""); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(ctx, client.ObjectKeyFromObject(tpl), tpl); err != nil {
			t.Fatal(err)
		}
		if tpl.ResourceVersion != rv {
			t.Fatal("unchanged rendered template was rewritten")
		}
	})

	t.Run("namespace-isolated-rbac", func(t *testing.T) {
		other := run.DeepCopy()
		other.Namespace = "another-namespace"
		other.Spec.KubernetesAdmin = false
		run.Spec.KubernetesAdmin = true
		if err := ensureClusterScopedRBAC(ctx, c, run, "worker-sa"); err != nil {
			t.Fatal(err)
		}
		if err := ensureClusterScopedRBAC(ctx, c, other, "worker-sa"); err != nil {
			t.Fatal(err)
		}
		if err := cleanupClusterRoleBindings(ctx, c, other); err != nil {
			t.Fatal(err)
		}
		own := &rbacv1.ClusterRoleBinding{}
		if err := c.Get(ctx, client.ObjectKey{Name: clusterRoleBindingName(run, "worker-sa", "admin-binding")}, own); err != nil {
			t.Fatal(err)
		}
		if own.Subjects[0].Namespace != run.Namespace {
			t.Fatal("foreign run rewrote admin binding")
		}
	})
}
