package dashboard

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	skillTestSubject = "alice-id"
	skillTestName    = "Alice Smith"
)

func newSkillTestServer(t *testing.T, objects ...client.Object) (*Server, client.Client, string) {
	t.Helper()
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return &Server{k8sClient: c, apiReader: c, scheme: scheme}, c, deriveUserNamespaceName(skillTestName, skillTestSubject)
}

func skillMemberContext() context.Context {
	return resourceActorContext(skillTestSubject, "member", skillTestName)
}

func TestSkillMutationsRequireMemberRole(t *testing.T) {
	srv, _, _ := newSkillTestServer(t)
	viewer := resourceActorContext(skillTestSubject, "viewer", skillTestName)

	_, err := srv.UpsertSkill(viewer, &platform.UpsertSkillRequest{Name: "notes", Instructions: "Take notes."})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer UpsertSkill error = %v, want PermissionDenied", err)
	}
	if err := srv.DeleteSkill(viewer, &platform.DeleteSkillRequest{Name: "notes"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer DeleteSkill error = %v, want PermissionDenied", err)
	}
	_, err = srv.InstallSkillFromCatalog(viewer, &platform.InstallSkillFromCatalogRequest{Source: "anthropics/skills", SkillId: "pdf"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer InstallSkillFromCatalog error = %v, want PermissionDenied", err)
	}
	_, err = srv.UpsertMCPServer(viewer, &platform.UpsertMCPServerRequest{Name: "tool", Command: "uvx"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer UpsertMCPServer error = %v, want PermissionDenied", err)
	}
	if err := srv.DeleteMCPServer(viewer, &platform.DeleteMCPServerRequest{Name: "tool"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer DeleteMCPServer error = %v, want PermissionDenied", err)
	}

	member := skillMemberContext()
	if _, err := srv.UpsertSkill(member, &platform.UpsertSkillRequest{Name: "notes", Instructions: "Take notes."}); err != nil {
		t.Fatalf("member UpsertSkill error = %v", err)
	}
	if _, err := srv.UpsertMCPServer(member, &platform.UpsertMCPServerRequest{Name: "tool", Command: "uvx"}); err != nil {
		t.Fatalf("member UpsertMCPServer error = %v", err)
	}
	if err := srv.DeleteSkill(member, &platform.DeleteSkillRequest{Name: "notes"}); err != nil {
		t.Fatalf("member DeleteSkill error = %v", err)
	}
}

func TestUpsertSkillValidation(t *testing.T) {
	srv, c, namespace := newSkillTestServer(t)
	ctx := skillMemberContext()
	if _, err := srv.UpsertSkill(ctx, &platform.UpsertSkillRequest{Name: "seed", Instructions: "seed"}); err != nil {
		t.Fatalf("seed skill: %v", err)
	}
	if err := c.Create(context.Background(), &platformv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "grafana", Namespace: namespace},
		Spec:       platformv1alpha1.MCPServerSpec{MCPServerConfig: &platformv1alpha1.MCPServerConfig{Type: "stdio", Command: "uvx"}},
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		req  *platform.UpsertSkillRequest
		want string
	}{
		{name: "empty name", req: &platform.UpsertSkillRequest{Instructions: "x"}, want: "skill name"},
		{name: "uppercase name", req: &platform.UpsertSkillRequest{Name: "Bad_Name", Instructions: "x"}, want: "skill name"},
		{name: "both sources", req: &platform.UpsertSkillRequest{Name: "both", Instructions: "x", GitUrl: "https://github.com/a/b"}, want: "exactly one"},
		{name: "no source", req: &platform.UpsertSkillRequest{Name: "none"}, want: "exactly one"},
		{name: "non-github host", req: &platform.UpsertSkillRequest{Name: "lab", GitUrl: "https://gitlab.com/a/b"}, want: "github.com"},
		{name: "github without repo", req: &platform.UpsertSkillRequest{Name: "lab", GitUrl: "https://github.com/onlyowner"}, want: "github.com"},
		{name: "inline too large", req: &platform.UpsertSkillRequest{Name: "big", Instructions: strings.Repeat("a", skillMaxInlineInstructionBytes+1)}, want: "max"},
		{name: "missing mcp server", req: &platform.UpsertSkillRequest{Name: "needs", Instructions: "x", McpServerRefs: []string{"ghost"}}, want: `mcp server "ghost" not found`},
		{name: "invalid mcp server name", req: &platform.UpsertSkillRequest{Name: "needs", Instructions: "x", McpServerRefs: []string{"Bad Name"}}, want: "mcp server name"},
		{name: "long description", req: &platform.UpsertSkillRequest{Name: "desc", Instructions: "x", Description: strings.Repeat("d", 1025)}, want: "description"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.UpsertSkill(ctx, tc.req)
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("error = %v, want InvalidArgument", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.want)
			}
		})
	}

	got, err := srv.UpsertSkill(ctx, &platform.UpsertSkillRequest{Name: "needs", Instructions: "x", McpServerRefs: []string{"grafana", " grafana "}})
	if err != nil {
		t.Fatalf("valid mcp ref: %v", err)
	}
	if len(got.McpServerRefs) != 1 || got.McpServerRefs[0] != "grafana" {
		t.Fatalf("McpServerRefs = %v, want deduped [grafana]", got.McpServerRefs)
	}
	tree, err := srv.UpsertSkill(ctx, &platform.UpsertSkillRequest{Name: "pdf", GitUrl: "https://github.com/anthropics/skills/tree/main/document-skills/pdf"})
	if err != nil {
		t.Fatalf("tree link: %v", err)
	}
	if tree.GitUrl != "https://github.com/anthropics/skills" || tree.GitRef != "main" || tree.GitPath != "document-skills/pdf" {
		t.Fatalf("tree link normalized to %+v", tree)
	}
}

func TestUpsertSkillCreatesUpdatesAndDetachesCatalogProvenance(t *testing.T) {
	srv, c, namespace := newSkillTestServer(t)
	ctx := skillMemberContext()

	created, err := srv.UpsertSkill(ctx, &platform.UpsertSkillRequest{Name: "Astro", Version: "1.0", Description: " Astro guidance ", Instructions: "Use islands."})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Name != "astro" || created.Version != "1.0" || created.Description != "Astro guidance" || created.Instructions != "Use islands." {
		t.Fatalf("created = %+v", created)
	}

	stored := &platformv1alpha1.Skill{}
	key := client.ObjectKey{Namespace: namespace, Name: "astro"}
	if err := c.Get(context.Background(), key, stored); err != nil {
		t.Fatal(err)
	}
	stored.Annotations = map[string]string{
		skillsShSourceAnnotation: "astrolicious/agent-skills",
		skillsShIDAnnotation:     "astro",
		skillsShURLAnnotation:    "https://skills.sh/astrolicious/agent-skills/astro",
		skillsShHashAnnotation:   strings.Repeat("a", 64),
	}
	if err := c.Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}

	// Editing metadata only keeps the catalog provenance.
	updated, err := srv.UpsertSkill(ctx, &platform.UpsertSkillRequest{Name: "astro", Version: "1.1", Instructions: "Use islands."})
	if err != nil {
		t.Fatalf("update metadata: %v", err)
	}
	if updated.Version != "1.1" || updated.CatalogSource != "astrolicious/agent-skills" || updated.CatalogHash == "" {
		t.Fatalf("metadata update lost provenance: %+v", updated)
	}

	// Switching the source to git detaches it, even though no inline text changed.
	switched, err := srv.UpsertSkill(ctx, &platform.UpsertSkillRequest{Name: "astro", GitUrl: "https://github.com/astrolicious/agent-skills/tree/main/astro"})
	if err != nil {
		t.Fatalf("switch to git: %v", err)
	}
	if switched.CatalogSource != "" || switched.CatalogSkillId != "" || switched.CatalogUrl != "" || switched.CatalogHash != "" {
		t.Fatalf("git switch kept provenance: %+v", switched)
	}
	if switched.GitUrl != "https://github.com/astrolicious/agent-skills" || switched.GitRef != "main" || switched.GitPath != "astro" || switched.Instructions != "" {
		t.Fatalf("git switch = %+v", switched)
	}
	if err := c.Get(context.Background(), key, stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.Source.Inline != nil || stored.Spec.Source.Git == nil || stored.Annotations[skillsShSourceAnnotation] != "" {
		t.Fatalf("stored after git switch = %+v", stored)
	}

	// Editing inline instructions of a catalog skill also detaches it.
	stored.Spec.Source = platformv1alpha1.SkillSource{Inline: &platformv1alpha1.SkillInlineSource{Instructions: "catalog text"}}
	if stored.Annotations == nil {
		stored.Annotations = map[string]string{}
	}
	stored.Annotations[skillsShSourceAnnotation] = "astrolicious/agent-skills"
	if err := c.Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	edited, err := srv.UpsertSkill(ctx, &platform.UpsertSkillRequest{Name: "astro", Instructions: "my own text"})
	if err != nil {
		t.Fatalf("edit instructions: %v", err)
	}
	if edited.CatalogSource != "" {
		t.Fatalf("instruction edit kept provenance: %+v", edited)
	}
}

func TestListSkillsScopedToCallerNamespace(t *testing.T) {
	namespace := deriveUserNamespaceName(skillTestName, skillTestSubject)
	inline := func(ns, name string) *platformv1alpha1.Skill {
		return &platformv1alpha1.Skill{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{Inline: &platformv1alpha1.SkillInlineSource{Instructions: name}}},
		}
	}
	srv, _, _ := newSkillTestServer(t, inline(namespace, "zeta"), inline(namespace, "alpha"), inline("someone-else", "beta"))
	resp, err := srv.ListSkills(skillMemberContext(), &platform.ListSkillsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Namespace != namespace {
		t.Fatalf("namespace = %q, want %q", resp.Namespace, namespace)
	}
	names := make([]string, 0, len(resp.Skills))
	for _, skill := range resp.Skills {
		names = append(names, skill.Name)
	}
	if strings.Join(names, ",") != "alpha,zeta" {
		t.Fatalf("skills = %v, want only the caller's, sorted", names)
	}
	if _, err := srv.ListSkills(context.Background(), &platform.ListSkillsRequest{}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous ListSkills error = %v, want Unauthenticated", err)
	}
}

func TestDeleteSkillValidatesNameAndIgnoresMissing(t *testing.T) {
	srv, c, namespace := newSkillTestServer(t)
	ctx := skillMemberContext()
	if err := srv.DeleteSkill(ctx, &platform.DeleteSkillRequest{Name: "Not Valid"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid name error = %v, want InvalidArgument", err)
	}
	if err := srv.DeleteSkill(ctx, &platform.DeleteSkillRequest{Name: "missing"}); err != nil {
		t.Fatalf("missing skill delete error = %v, want nil", err)
	}
	if _, err := srv.UpsertSkill(ctx, &platform.UpsertSkillRequest{Name: "temp", Instructions: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.DeleteSkill(ctx, &platform.DeleteSkillRequest{Name: " TEMP "}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "temp"}, &platformv1alpha1.Skill{}); err == nil {
		t.Fatal("skill still exists after delete")
	}
}
