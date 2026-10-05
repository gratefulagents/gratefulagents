package dashboard

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

func bootstrapMCPServer(name, command string) *platformv1alpha1.MCPServer {
	return &platformv1alpha1.MCPServer{
		ObjectMeta: bootstrapMeta(name),
		Spec: platformv1alpha1.MCPServerSpec{
			Version:         "0.1.0",
			MCPServerConfig: &platformv1alpha1.MCPServerConfig{Type: "stdio", Command: command},
		},
	}
}

// Seeded skills that require an MCP server must be able to attach it, so the
// sync copies exactly the bootstrap servers those skills name and leaves the
// rest of the shipped catalog, and any user-authored server, alone.
func TestBootstrapSeedsMCPServersRequiredBySkills(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	grafanaSkill := &platformv1alpha1.Skill{
		ObjectMeta: bootstrapMeta("grafana"),
		Spec: platformv1alpha1.SkillSpec{
			Requires: &platformv1alpha1.SkillRequires{MCPServers: []platformv1alpha1.NamedRef{{Name: "grafana"}, {Name: "postgres-mcp"}}},
			Source:   platformv1alpha1.SkillSource{Inline: &platformv1alpha1.SkillInlineSource{Instructions: "query grafana"}},
		},
	}
	userPostgres := &platformv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres-mcp", Namespace: "alice"},
		Spec:       platformv1alpha1.MCPServerSpec{MCPServerConfig: &platformv1alpha1.MCPServerConfig{Type: "stdio", Command: "my-own-postgres"}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		bootstrapReadyMarker("v1"), grafanaSkill,
		bootstrapMCPServer("grafana", "uvx"),
		bootstrapMCPServer("postgres-mcp", "npx"),
		bootstrapMCPServer("fetch", "uvx"),
		userPostgres,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alice"}},
	).Build()
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme}
	ctx := context.Background()
	if err := srv.syncBootstrapResources(ctx, "alice"); err != nil {
		t.Fatal(err)
	}

	seeded := &platformv1alpha1.MCPServer{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "alice", Name: "grafana"}, seeded); err != nil {
		t.Fatalf("required MCPServer not seeded: %v", err)
	}
	if seeded.Spec.MCPServerConfig.Command != "uvx" || seeded.Annotations[bootstrapDefaultAnnotation] != "true" ||
		seeded.Annotations[bootstrapSourceAnnotation] != "system" || seeded.Annotations["helm.sh/hook"] != "" {
		t.Fatalf("seeded MCPServer = %+v", seeded)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "alice", Name: "fetch"}, &platformv1alpha1.MCPServer{}); err == nil {
		t.Fatal("unrelated bootstrap MCPServer was seeded")
	}
	existing := &platformv1alpha1.MCPServer{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "alice", Name: "postgres-mcp"}, existing); err != nil {
		t.Fatal(err)
	}
	if existing.Spec.MCPServerConfig.Command != "my-own-postgres" {
		t.Fatalf("user MCPServer overwritten: %+v", existing.Spec)
	}

	// A second sync at the same bundle version is a no-op, and a bundle bump
	// refreshes an untouched seeded server.
	source := &platformv1alpha1.MCPServer{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "system", Name: "grafana"}, source); err != nil {
		t.Fatal(err)
	}
	source.Spec.MCPServerConfig.Command = "uvx-next"
	if err := c.Update(ctx, source); err != nil {
		t.Fatal(err)
	}
	marker := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "system", Name: "bootstrap-ready"}, marker); err != nil {
		t.Fatal(err)
	}
	marker.Data[bootstrapBundleVersionKey] = "v2"
	if err := c.Update(ctx, marker); err != nil {
		t.Fatal(err)
	}
	if err := srv.syncBootstrapResources(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "alice", Name: "grafana"}, seeded); err != nil {
		t.Fatal(err)
	}
	if seeded.Spec.MCPServerConfig.Command != "uvx-next" {
		t.Fatalf("seeded MCPServer not refreshed: %+v", seeded.Spec)
	}
}
