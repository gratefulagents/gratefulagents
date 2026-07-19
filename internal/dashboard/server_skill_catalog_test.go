package dashboard

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func catalogResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestListSkillCatalog(t *testing.T) {
	var requested string
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requested = req.URL.String()
		return catalogResponse(http.StatusOK, `{"skills":[{"source":"anthropics/skills","skillId":"pdf","name":"pdf","installs":42,"isOfficial":true}],"total":9592,"hasMore":true,"page":2}`), nil
	})}
	srv := &Server{skillsHTTP: httpClient, skillsCatalogURL: "https://catalog.test"}
	got, err := srv.ListSkillCatalog(context.Background(), &platform.ListSkillCatalogRequest{Page: 2})
	if err != nil {
		t.Fatalf("ListSkillCatalog() error = %v", err)
	}
	if requested != "https://catalog.test/api/skills/all-time/2" || got.Total != 9592 || !got.HasMore || len(got.Skills) != 1 {
		t.Fatalf("unexpected request/response: %q %+v", requested, got)
	}
	entry := got.Skills[0]
	if entry.Source != "anthropics/skills" || entry.SkillId != "pdf" || entry.Installs != 42 || !entry.IsOfficial || entry.CatalogUrl != "https://skills.sh/anthropics/skills/pdf" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestListSkillCatalogSearchEscapesQuery(t *testing.T) {
	var requested string
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requested = req.URL.String()
		return catalogResponse(http.StatusOK, `{"skills":[],"count":0}`), nil
	})}
	srv := &Server{skillsHTTP: httpClient, skillsCatalogURL: "https://catalog.test"}
	if _, err := srv.ListSkillCatalog(context.Background(), &platform.ListSkillCatalogRequest{Query: "react native", Page: 8}); err != nil {
		t.Fatal(err)
	}
	if requested != "https://catalog.test/api/search?q=react+native&limit=100" {
		t.Fatalf("requested %q", requested)
	}
}

func TestInstallDomainSkillFromCatalogCreatesCRD(t *testing.T) {
	const skillMD = "---\nname: lark-approval\ndescription: Handle approval workflows\n---\nUse the lark approval commands.\n"
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Host == "catalog.test" && req.URL.Path == "/site/open.feishu.cn/lark-approval":
			return catalogResponse(http.StatusOK, "<html>catalog entry</html>"), nil
		case req.URL.Host == "open.feishu.cn" && req.URL.Path == "/.well-known/skills/lark-approval/SKILL.md":
			return catalogResponse(http.StatusOK, skillMD), nil
		default:
			t.Fatalf("unexpected request %s", req.URL)
			return nil, nil
		}
	})}
	scheme := testProjectScheme(t)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewServer(k8sClient, scheme, nil, nil, false, WithSkillsCatalogHTTPClient(httpClient), WithSkillsCatalogURL("https://catalog.test"))
	ctx := resourceActorContext("alice-id", "member", "Alice Smith")
	got, err := srv.InstallSkillFromCatalog(ctx, &platform.InstallSkillFromCatalogRequest{Source: "open.feishu.cn", SkillId: "lark-approval"})
	if err != nil {
		t.Fatalf("InstallSkillFromCatalog() error = %v", err)
	}
	if got.Name != "lark-approval" || got.CatalogSource != "open.feishu.cn" || got.CatalogSkillId != "lark-approval" {
		t.Fatalf("unexpected SkillInfo: %+v", got)
	}
	namespace := deriveUserNamespaceName("Alice Smith", "alice-id")
	var saved platformv1alpha1.Skill
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: "lark-approval"}, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Spec.Source.Inline == nil || saved.Spec.Source.Inline.Instructions != "Use the lark approval commands." || len(saved.Annotations[skillsShHashAnnotation]) != 64 {
		t.Fatalf("unexpected saved skill: %+v", saved)
	}
	_, err = srv.InstallSkillFromCatalog(ctx, &platform.InstallSkillFromCatalogRequest{Source: "open.feishu.cn", SkillId: "lark-approval"})
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("duplicate install: %v", err)
	}
}

func TestDownloadCatalogSkillUsesSkillsShSnapshot(t *testing.T) {
	const skillMD = "---\nname: vercel-react-best-practices\ndescription: React guidance\n---\nUse React well.\n"
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/download/vercel-labs/agent-skills/vercel-react-best-practices" {
			t.Fatalf("unexpected request %s", req.URL)
		}
		return catalogResponse(http.StatusOK, `{"files":[{"path":"SKILL.md","contents":"---\nname: vercel-react-best-practices\ndescription: React guidance\n---\nUse React well.\n"}],"hash":"ca7b0c0c6e5f2750043f7f0cd72d16ac4e2abc48f9b5500d047a4b77a2506212"}`), nil
	})}
	srv := &Server{skillsHTTP: httpClient, skillsCatalogURL: "https://catalog.test"}
	got, hash, err := srv.downloadCatalogSkill(context.Background(), "vercel-labs/agent-skills", "vercel-react-best-practices")
	if err != nil || got != skillMD || len(hash) != 64 {
		t.Fatalf("snapshot = %q %q, err %v", got, hash, err)
	}
}

func TestCatalogResourceNameSupportsDuplicateIDs(t *testing.T) {
	scheme := testProjectScheme(t)
	existing := &platformv1alpha1.Skill{ObjectMeta: metav1.ObjectMeta{Name: "pdf", Namespace: "user-test", Annotations: map[string]string{skillsShSourceAnnotation: "anthropics/skills", skillsShIDAnnotation: "pdf"}}, Spec: platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{Inline: &platformv1alpha1.SkillInlineSource{Instructions: "x"}}}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	srv := &Server{k8sClient: k8sClient}
	name, err := srv.catalogResourceName(context.Background(), "user-test", "openai/skills", "pdf")
	if err != nil || name == "pdf" || !strings.HasPrefix(name, "pdf-") {
		t.Fatalf("collision-safe name = %q, err %v", name, err)
	}
	_, err = srv.catalogResourceName(context.Background(), "user-test", "anthropics/skills", "pdf")
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("same coordinate: %v", err)
	}
}

func TestPublicCatalogIPValidation(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::1", "fd00::1"} {
		if isPublicCatalogIP(net.ParseIP(raw)) {
			t.Errorf("isPublicCatalogIP(%s) = true", raw)
		}
	}
	if !isPublicCatalogIP(net.ParseIP("8.8.8.8")) {
		t.Error("public address was rejected")
	}
}
