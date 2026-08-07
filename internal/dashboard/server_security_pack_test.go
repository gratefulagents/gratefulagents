package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func seedSecurityPackFixtures(t *testing.T, c client.Client, ns string) {
	t.Helper()
	fixtures := []client.Object{
		&triggersv1alpha1.SecurityWorkflow{
			ObjectMeta: metav1.ObjectMeta{Name: "wf", Namespace: ns},
			Spec: triggersv1alpha1.SecurityWorkflowSpec{
				Description: "workflow",
				Tasks:       []triggersv1alpha1.SecurityScanTask{{Name: "recon", Objective: "map the code"}},
			},
		},
		&triggersv1alpha1.SecurityRanker{
			ObjectMeta: metav1.ObjectMeta{Name: "ranker", Namespace: ns},
			Spec:       triggersv1alpha1.SecurityRankerSpec{Rules: []string{"raise sqli to critical"}},
		},
		&triggersv1alpha1.SecurityPostScript{
			ObjectMeta: metav1.ObjectMeta{Name: "ps", Namespace: ns},
			Spec:       triggersv1alpha1.SecurityPostScriptSpec{Prompt: "write a repro", RunOn: "all"},
		},
		&triggersv1alpha1.SecurityPolicyPack{
			ObjectMeta: metav1.ObjectMeta{Name: "pp", Namespace: ns},
			Spec: triggersv1alpha1.SecurityPolicyPackSpec{
				Description:    "org floors",
				MinSeverity:    "medium",
				FailOnSeverity: "high",
				Enforced:       []string{"minSeverity"},
				Suppressions: []triggersv1alpha1.SecurityPolicySuppression{{
					Name:    "legacy-md5",
					Reason:  "accepted risk",
					Owner:   "secteam",
					Matcher: triggersv1alpha1.SecuritySuppressionMatcher{Category: "weak-crypto"},
				}},
			},
		},
		&triggersv1alpha1.SecurityScan{
			ObjectMeta: metav1.ObjectMeta{Name: "scan", Namespace: ns},
			Spec: triggersv1alpha1.SecurityScanSpec{
				RepoURL: "https://github.com/acme/app.git",
				Defaults: triggersv1alpha1.AgentRunDefaults{
					Model: "gpt-5.2",
					Secrets: triggersv1alpha1.AgentRunSecrets{
						OpenAIOAuthSecret: "oauth-secret",
						ProviderKeys: []platformv1alpha1.ProviderKeyRef{
							{Provider: "openai", SecretName: "openai-key", SecretKey: "key"},
						},
					},
					KubernetesAdmin:       true,
					DisableCommandSandbox: true,
				},
			},
		},
	}
	for _, obj := range fixtures {
		if err := c.Create(context.Background(), obj); err != nil {
			t.Fatalf("seed %T: %v", obj, err)
		}
	}
}

func TestExportSecurityPackStripsSecrets(t *testing.T) {
	srv, c := newCronTestServer(t)
	ns := testUserNS()
	ctx := projectActorCtx()
	seedSecurityPackFixtures(t, c, ns)

	resp, err := srv.ExportSecurityPack(ctx, &platform.ExportSecurityPackRequest{
		Workflows:   []string{"wf"},
		Rankers:     []string{"ranker"},
		PostScripts: []string{"ps"},
		ScanConfigs: []string{"scan"},
		PolicyPacks: []string{"pp"},
	})
	if err != nil {
		t.Fatalf("ExportSecurityPack() error = %v", err)
	}
	if resp.ItemCount != 5 || !strings.HasSuffix(resp.Filename, ".json") {
		t.Fatalf("resp = count %d filename %q", resp.ItemCount, resp.Filename)
	}
	for _, banned := range []string{"oauth-secret", "openai-key", "kubernetesAdmin", "disableCommandSandbox"} {
		if bytes.Contains(resp.Data, []byte(banned)) {
			t.Fatalf("exported pack leaks %q:\n%s", banned, resp.Data)
		}
	}
	var doc securityPackDocument
	if err := json.Unmarshal(resp.Data, &doc); err != nil {
		t.Fatalf("pack is not valid JSON: %v", err)
	}
	if doc.SchemaVersion != securityPackSchemaVersion || doc.SourceNamespace != ns || len(doc.Items) != 5 {
		t.Fatalf("doc = %+v", doc)
	}
	kinds := map[string]bool{}
	for _, item := range doc.Items {
		kinds[item.Kind] = true
	}
	if !kinds[securityPackKindPolicyPack] {
		t.Fatalf("pack missing SecurityPolicyPack item: %+v", doc.Items)
	}
}

func TestExportSecurityPackUnknownNameFails(t *testing.T) {
	srv, _ := newCronTestServer(t)
	ctx := projectActorCtx()
	_, err := srv.ExportSecurityPack(ctx, &platform.ExportSecurityPackRequest{Workflows: []string{"missing"}})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("err = %v, want NotFound", err)
	}
	_, err = srv.ExportSecurityPack(ctx, &platform.ExportSecurityPackRequest{})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty selection err = %v, want InvalidArgument", err)
	}
}

func exportedTestPack(t *testing.T, srv *Server, ctx context.Context) []byte {
	t.Helper()
	resp, err := srv.ExportSecurityPack(ctx, &platform.ExportSecurityPackRequest{
		Workflows:   []string{"wf"},
		Rankers:     []string{"ranker"},
		PostScripts: []string{"ps"},
		ScanConfigs: []string{"scan"},
		PolicyPacks: []string{"pp"},
	})
	if err != nil {
		t.Fatalf("ExportSecurityPack() error = %v", err)
	}
	return resp.Data
}

func TestImportSecurityPackRoundTrip(t *testing.T) {
	src, c := newCronTestServer(t)
	ns := testUserNS()
	ctx := projectActorCtx()
	seedSecurityPackFixtures(t, c, ns)
	data := exportedTestPack(t, src, ctx)

	// Import into a fresh server (empty cluster).
	dst, dc := newCronTestServer(t)

	dry, err := dst.ImportSecurityPack(ctx, &platform.ImportSecurityPackRequest{Data: data})
	if err != nil {
		t.Fatalf("ImportSecurityPack(dry) error = %v", err)
	}
	if dry.Applied || len(dry.Items) != 5 {
		t.Fatalf("dry = %+v", dry)
	}
	for _, item := range dry.Items {
		if item.Action != "would-create" {
			t.Fatalf("dry item = %+v", item)
		}
	}
	// Dry run must not create anything.
	wfList := &triggersv1alpha1.SecurityWorkflowList{}
	if err := dc.List(context.Background(), wfList); err != nil || len(wfList.Items) != 0 {
		t.Fatalf("dry run created workflows: %v %d", err, len(wfList.Items))
	}

	applied, err := dst.ImportSecurityPack(ctx, &platform.ImportSecurityPackRequest{Data: data, Apply: true})
	if err != nil {
		t.Fatalf("ImportSecurityPack(apply) error = %v", err)
	}
	for _, item := range applied.Items {
		if item.Action != "created" {
			t.Fatalf("apply item = %+v", item)
		}
	}
	scan := &triggersv1alpha1.SecurityScan{}
	if err := dc.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "scan"}, scan); err != nil {
		t.Fatalf("imported scan missing: %v", err)
	}
	if scan.Spec.Defaults.Secrets.OpenAIOAuthSecret != "" || len(scan.Spec.Defaults.Secrets.ProviderKeys) != 0 ||
		scan.Spec.Defaults.KubernetesAdmin || scan.Spec.Defaults.DisableCommandSandbox {
		t.Fatalf("imported scan kept stripped fields: %+v", scan.Spec.Defaults)
	}
	pack := &triggersv1alpha1.SecurityPolicyPack{}
	if err := dc.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "pp"}, pack); err != nil {
		t.Fatalf("imported policy pack missing: %v", err)
	}
	if pack.Spec.MinSeverity != "medium" || len(pack.Spec.Suppressions) != 1 ||
		pack.Spec.Suppressions[0].Name != "legacy-md5" {
		t.Fatalf("imported policy pack spec = %+v", pack.Spec)
	}
}

func TestImportSecurityPackCollisionPolicies(t *testing.T) {
	src, c := newCronTestServer(t)
	ns := testUserNS()
	ctx := projectActorCtx()
	seedSecurityPackFixtures(t, c, ns)
	data := exportedTestPack(t, src, ctx)

	// Importing into the same populated cluster collides on every item.
	fail, err := src.ImportSecurityPack(ctx, &platform.ImportSecurityPackRequest{Data: data, Apply: true})
	if err != nil {
		t.Fatalf("ImportSecurityPack(FAIL) error = %v", err)
	}
	for _, item := range fail.Items {
		if item.Action != "failed" || !strings.Contains(item.Error, "already exists") {
			t.Fatalf("FAIL item = %+v", item)
		}
	}

	skip, err := src.ImportSecurityPack(ctx, &platform.ImportSecurityPackRequest{
		Data: data, Apply: true,
		CollisionPolicy: platform.SecurityPackCollisionPolicy_SECURITY_PACK_COLLISION_POLICY_SKIP,
	})
	if err != nil {
		t.Fatalf("ImportSecurityPack(SKIP) error = %v", err)
	}
	for _, item := range skip.Items {
		if item.Action != "skipped" {
			t.Fatalf("SKIP item = %+v", item)
		}
	}

	rename, err := src.ImportSecurityPack(ctx, &platform.ImportSecurityPackRequest{
		Data: data, Apply: true,
		CollisionPolicy: platform.SecurityPackCollisionPolicy_SECURITY_PACK_COLLISION_POLICY_RENAME,
	})
	if err != nil {
		t.Fatalf("ImportSecurityPack(RENAME) error = %v", err)
	}
	for _, item := range rename.Items {
		if item.Action != "renamed" || item.FinalName != item.Name+"-2" {
			t.Fatalf("RENAME item = %+v", item)
		}
	}
	wf := &triggersv1alpha1.SecurityWorkflow{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "wf-2"}, wf); err != nil {
		t.Fatalf("renamed workflow missing: %v", err)
	}
}

func TestImportSecurityPackRejectsBadDocuments(t *testing.T) {
	srv, _ := newCronTestServer(t)
	ctx := projectActorCtx()

	cases := map[string][]byte{
		"empty":          nil,
		"not json":       []byte("nope"),
		"wrong version":  []byte(`{"schemaVersion":"security-pack/v999","sourceNamespace":"x","items":[{"kind":"SecurityRanker","name":"r","spec":{}}]}`),
		"no items":       []byte(`{"schemaVersion":"security-pack/v1","sourceNamespace":"x","items":[]}`),
		"unknown fields": []byte(`{"schemaVersion":"security-pack/v1","sourceNamespace":"x","items":[],"extra":1}`),
	}
	for name, data := range cases {
		if _, err := srv.ImportSecurityPack(ctx, &platform.ImportSecurityPackRequest{Data: data}); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%s: err = %v, want InvalidArgument", name, err)
		}
	}
	if _, err := srv.ImportSecurityPack(ctx, &platform.ImportSecurityPackRequest{Data: bytes.Repeat([]byte("a"), securityPackMaxBytes+1)}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("oversized: want InvalidArgument")
	}
}

func TestImportSecurityPackValidationErrorsReported(t *testing.T) {
	srv, _ := newCronTestServer(t)
	ctx := projectActorCtx()

	doc := securityPackDocument{
		SchemaVersion:   securityPackSchemaVersion,
		SourceNamespace: "elsewhere",
		Items: []securityPackItem{
			{Kind: securityPackKindWorkflow, Name: "bad-wf", Spec: json.RawMessage(`{"tasks":[{"name":"a","objective":"x","dependsOn":["a"]}]}`)},
			{Kind: "Nonsense", Name: "weird", Spec: json.RawMessage(`{}`)},
			{Kind: securityPackKindScan, Name: "bad-scan", Spec: json.RawMessage(`{"repoURL":"","schedule":"bogus"}`)},
			{Kind: securityPackKindPolicyPack, Name: "bad-pack", Spec: json.RawMessage(`{"minSeverity":"severe","enforced":["nonsense"]}`)},
		},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.ImportSecurityPack(ctx, &platform.ImportSecurityPackRequest{Data: data, Apply: true})
	if err != nil {
		t.Fatalf("ImportSecurityPack() error = %v", err)
	}
	if len(resp.Items) != 4 {
		t.Fatalf("items = %+v", resp.Items)
	}
	if resp.Items[0].Action != "failed" || len(resp.Items[0].ValidationErrors) == 0 {
		t.Fatalf("workflow item = %+v", resp.Items[0])
	}
	if resp.Items[1].Action != "failed" || !strings.Contains(resp.Items[1].Error, "unsupported item kind") {
		t.Fatalf("kind item = %+v", resp.Items[1])
	}
	if resp.Items[2].Action != "failed" || len(resp.Items[2].ValidationErrors) < 2 {
		t.Fatalf("scan item = %+v", resp.Items[2])
	}
	if resp.Items[3].Action != "failed" || len(resp.Items[3].ValidationErrors) < 2 {
		t.Fatalf("policy pack item = %+v", resp.Items[3])
	}
}

func TestSecurityPackCrossNamespaceDenied(t *testing.T) {
	srv, _ := newCronTestServer(t)
	ctx := projectActorCtx()

	if _, err := srv.ExportSecurityPack(ctx, &platform.ExportSecurityPackRequest{
		Namespace: "someone-else", Workflows: []string{"wf"},
	}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("export err = %v, want PermissionDenied", err)
	}
	if _, err := srv.ImportSecurityPack(ctx, &platform.ImportSecurityPackRequest{
		Namespace: "someone-else", Data: []byte(`{}`),
	}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("import err = %v, want PermissionDenied", err)
	}
}
