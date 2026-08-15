package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/security"
	"github.com/gratefulagents/gratefulagents/internal/securitytoolpacks"
	"github.com/gratefulagents/gratefulagents/internal/securitytoolrun"
)

// fakeSecurityToolBlobStore is an in-memory object store; failGet simulates an
// unreadable result document.
type fakeSecurityToolBlobStore struct {
	objects map[string][]byte
	media   map[string]string
	failGet error
}

func newFakeSecurityToolBlobStore() *fakeSecurityToolBlobStore {
	return &fakeSecurityToolBlobStore{objects: map[string][]byte{}, media: map[string]string{}}
}

func (s *fakeSecurityToolBlobStore) Put(_ context.Context, key string, content []byte, mediaType string) error {
	s.objects[key] = append([]byte(nil), content...)
	s.media[key] = mediaType
	return nil
}

func (s *fakeSecurityToolBlobStore) Get(_ context.Context, key string) ([]byte, error) {
	if s.failGet != nil {
		return nil, s.failGet
	}
	data, ok := s.objects[key]
	if !ok {
		return nil, fmt.Errorf("object %q not found", key)
	}
	return append([]byte(nil), data...), nil
}

// reconcilingSecurityToolRunClient stands in for the controller: the status a
// test wants is applied as soon as the SecurityToolRun is created.
type reconcilingSecurityToolRunClient struct {
	client.Client
	status  platformv1alpha1.SecurityToolRunStatus
	created *platformv1alpha1.SecurityToolRun
}

func (c *reconcilingSecurityToolRunClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := c.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}
	run, ok := obj.(*platformv1alpha1.SecurityToolRun)
	if !ok {
		return nil
	}
	c.created = run.DeepCopy()
	fresh := &platformv1alpha1.SecurityToolRun{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(run), fresh); err != nil {
		return err
	}
	fresh.Status = *c.status.DeepCopy()
	return c.Client.Status().Update(ctx, fresh)
}

// vanishingSecurityToolRunClient serves the created SecurityToolRun once and
// then reports it gone, standing in for a run deleted or garbage-collected
// while the tool waits for a verdict.
type vanishingSecurityToolRunClient struct {
	*reconcilingSecurityToolRunClient
	gets int
}

func (c *vanishingSecurityToolRunClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	c.gets++
	if c.gets > 1 {
		return apierrors.NewNotFound(schema.GroupResource{Group: platformv1alpha1.GroupVersion.Group, Resource: "securitytoolruns"}, key.Name)
	}
	return c.reconcilingSecurityToolRunClient.Get(ctx, key, obj, opts...)
}

func newSecurityToolRunTestClient(t *testing.T, status platformv1alpha1.SecurityToolRunStatus) *reconcilingSecurityToolRunClient {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	base := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.SecurityToolRun{}).
		Build()
	return &reconcilingSecurityToolRunClient{Client: base, status: status}
}

type securityToolRunFixture struct {
	tool         *runSecurityToolTool
	client       *reconcilingSecurityToolRunClient
	blobs        *fakeSecurityToolBlobStore
	findingStore *fakeSecurityFindingStore
	workspace    string
}

func newSecurityToolRunFixture(t *testing.T, status platformv1alpha1.SecurityToolRunStatus) *securityToolRunFixture {
	t.Helper()
	findingStore := newFakeSecurityFindingStore()
	registry := &Registry{tools: map[string]Tool{}}
	state := RegisterSecurityScanTools(registry, findingStore, nil, testScanContext())
	crdClient := newSecurityToolRunTestClient(t, status)
	blobs := newFakeSecurityToolBlobStore()
	workspace := t.TempDir()
	RegisterSecurityToolRunTool(registry, state, SecurityToolRunDeps{
		Client:       crdClient,
		Blobs:        blobs,
		Namespace:    "default",
		RunName:      "nightly-scan-run-1",
		RunUID:       "run-uid-1",
		WorkspaceDir: workspace,
	})
	tool, ok := registry.Get("run_security_tool").(*runSecurityToolTool)
	if !ok {
		t.Fatal("run_security_tool is not registered")
	}
	tool.pollInterval = time.Millisecond
	return &securityToolRunFixture{
		tool:         tool,
		client:       crdClient,
		blobs:        blobs,
		findingStore: findingStore,
		workspace:    workspace,
	}
}

func (f *securityToolRunFixture) exec(t *testing.T, input string) (Result, runSecurityToolSummary) {
	t.Helper()
	result, err := f.tool.Execute(context.Background(), json.RawMessage(input), "")
	if err != nil {
		t.Fatalf("run_security_tool returned a Go error: %v", err)
	}
	var summary runSecurityToolSummary
	if !result.IsError || strings.HasPrefix(strings.TrimSpace(result.Content), "{") {
		if err := json.Unmarshal([]byte(result.Content), &summary); err != nil {
			t.Fatalf("summary is not JSON: %v (%s)", err, result.Content)
		}
	}
	return result, summary
}

func (f *securityToolRunFixture) writeProject(t *testing.T) {
	t.Helper()
	dir := filepath.Join(f.workspace, "repo", "contracts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Token.sol"), []byte("contract Token {}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// publishResult stores a result document under the key the controller
// recorded, optionally with a deliberately wrong digest.
func securityToolRunStatusForResult(t *testing.T, blobs *fakeSecurityToolBlobStore,
	document securitytoolpacks.Result, digestOverride string,
) platformv1alpha1.SecurityToolRunStatus {
	t.Helper()
	key := testResultObjectKey
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := blobs.Put(context.Background(), key, raw, "application/json"); err != nil {
		t.Fatalf("put result: %v", err)
	}
	digest := securityToolRunDigest(raw)
	if digestOverride != "" {
		digest = digestOverride
	}
	return platformv1alpha1.SecurityToolRunStatus{
		Phase: platformv1alpha1.SecurityToolRunPhaseSucceeded,
		Result: &platformv1alpha1.SecurityToolRunResult{
			Status:          string(document.Status),
			FindingCount:    int32(len(document.Findings)),
			ResultObjectKey: key,
			ResultDigest:    digest,
			Artifacts: []platformv1alpha1.SecurityToolRunArtifact{
				{Name: "raw-00", MediaType: "application/json", ObjectKey: key + "-raw", Digest: digest},
			},
		},
	}
}

func sampleSecurityToolResult() securitytoolpacks.Result {
	return securitytoolpacks.Result{
		Status: securitytoolpacks.StatusFindings,
		Findings: []security.ScannerRecord{{
			Tool:        "slither",
			ToolVersion: "0.11.3",
			RuleID:      "reentrancy",
			Message:     "state change after external call",
			Severity:    "HIGH",
			FilePath:    "contracts/Token.sol",
			StartLine:   12,
		}},
		Coverage: securitytoolpacks.Coverage{Examined: []string{"contracts/Token.sol"}, Skipped: []string{"test/"}},
		Replay: securitytoolpacks.Replay{
			ToolVersion:     "0.11.3",
			ImageDigest:     "sha256:" + strings.Repeat("a", 64),
			ConfigurationID: "sha256:" + strings.Repeat("b", 64),
		},
	}
}

const slitherRequest = `{"tool":"slither","target":{"type":"solidity_project","locator":"repo/contracts","revision":"abc1234"},"timeout_seconds":60}`

// The staged-target key embeds the SecurityToolRun name, so the result key can
// only be predicted once the object exists. Tests that need a result document
// therefore stage it under a fixed key the fake status points at.
const testResultObjectKey = "security-tool-runs/default/fixture/output/result.json"

func TestRunSecurityToolStagesTargetAndRecordsTypedRequest(t *testing.T) {
	fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{})
	fixture.writeProject(t)
	fixture.client.status = securityToolRunStatusForResult(t, fixture.blobs, sampleSecurityToolResult(), "")

	result, summary := fixture.exec(t, slitherRequest)
	if result.IsError {
		t.Fatalf("unexpected error result: %s", result.Content)
	}
	created := fixture.client.created
	if created == nil {
		t.Fatal("no SecurityToolRun was created")
	}
	assertStagedSlitherSpec(t, fixture, created)
	assertSpecCarriesTypedFieldsOnly(t, created)
	assertSlitherSummary(t, fixture, summary)
}

func TestRunSecurityToolStagesDirectoryTools(t *testing.T) {
	for name, request := range map[string]string{
		"slither":       `{"tool":"slither","target":{"type":"solidity_project","locator":"repo/contracts","revision":"abc1234"}}`,
		"halmos":        `{"tool":"halmos","target":{"type":"foundry_project","locator":"repo/contracts","revision":"abc1234"}}`,
		"go-fuzz-tests": `{"tool":"go-fuzz-tests","target":{"type":"go_fuzz_project","locator":"repo/contracts","revision":"abc1234"},"arguments":{"package":"./...","fuzz":"FuzzTarget"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{})
			fixture.writeProject(t)
			fixture.client.status = securityToolRunStatusForResult(t, fixture.blobs, sampleSecurityToolResult(), "")
			result, _ := fixture.exec(t, request)
			if result.IsError || fixture.client.created == nil {
				t.Fatalf("staged %s request failed: %+v", name, result)
			}
			target := fixture.client.created.Spec.Target
			if target.StagedObjectKey == "" || target.MediaType != stagedTargetMediaType {
				t.Fatalf("%s target was not staged as an archive: %+v", name, target)
			}
		})
	}
}

func assertStagedSlitherSpec(t *testing.T, fixture *securityToolRunFixture, created *platformv1alpha1.SecurityToolRun) {
	t.Helper()
	if created.Namespace != "default" || created.Spec.Tool != "slither" ||
		created.Spec.RequestedBy != "nightly-scan-run-1" {
		t.Fatalf("unexpected spec: %+v", created.Spec)
	}
	if len(created.OwnerReferences) != 1 || created.OwnerReferences[0].Kind != "AgentRun" ||
		string(created.OwnerReferences[0].UID) != "run-uid-1" {
		t.Fatalf("SecurityToolRun must be owned by the AgentRun: %+v", created.OwnerReferences)
	}
	wantKey := securitytoolrun.TargetObjectKey("default", created.Name)
	if created.Spec.Target.StagedObjectKey != wantKey {
		t.Fatalf("staged object key = %q, want %q", created.Spec.Target.StagedObjectKey, wantKey)
	}
	if created.Spec.Target.MediaType != stagedTargetMediaType {
		t.Fatalf("staged media type = %q", created.Spec.Target.MediaType)
	}
	archive, ok := fixture.blobs.objects[wantKey]
	if !ok {
		t.Fatal("staged archive was not uploaded")
	}
	if created.Spec.Target.Digest != securityToolRunDigest(archive) {
		t.Fatalf("staged digest %q does not match the uploaded archive", created.Spec.Target.Digest)
	}
	if created.Spec.Target.Locator != "repo/contracts" {
		t.Fatalf("locator = %q, want the workspace-relative path", created.Spec.Target.Locator)
	}
	if names := archiveEntryNames(t, archive); len(names) == 0 || !contains(names, "Token.sol") {
		t.Fatalf("archive entries = %v, want the target contents", names)
	}
}

// assertSpecCarriesTypedFieldsOnly proves the request holds no image, command,
// or raw scanner flags.
func assertSpecCarriesTypedFieldsOnly(t *testing.T, created *platformv1alpha1.SecurityToolRun) {
	t.Helper()
	encoded, err := json.Marshal(created.Spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	for _, forbidden := range []string{"image", "command", "args", "flags"} {
		if strings.Contains(string(encoded), `"`+forbidden+`"`) {
			t.Fatalf("spec must not carry %q: %s", forbidden, encoded)
		}
	}
}

func assertSlitherSummary(t *testing.T, fixture *securityToolRunFixture, summary runSecurityToolSummary) {
	t.Helper()
	if summary.Status != "findings" || summary.Findings.Reported != 1 || summary.Findings.IngestedNew != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if summary.Replay == nil || summary.Replay.ConfigDigest == "" || summary.Replay.ImageDigest == "" {
		t.Fatalf("summary must carry replay metadata: %+v", summary.Replay)
	}
	if len(summary.Artifacts) != 1 || summary.Artifacts[0].ObjectKey == "" {
		t.Fatalf("summary must list raw artifact keys: %+v", summary.Artifacts)
	}
	if summary.Coverage == nil || len(summary.Coverage.Examined) != 1 {
		t.Fatalf("summary must carry coverage: %+v", summary.Coverage)
	}
	if len(fixture.findingStore.findings) != 1 {
		t.Fatalf("finding store holds %d findings, want 1", len(fixture.findingStore.findings))
	}
	stored := fixture.findingStore.findings[0]
	if stored.Tool != "slither" || stored.Repository != "github.com/acme/widget" || stored.Revision != "abc1234" {
		t.Fatalf("finding did not go through the scan pipeline: %+v", stored)
	}
}

// gitCheckoutCommit is the commit the fake workspace checkout has at HEAD.
const gitCheckoutCommit = "9f1a2b3c4d5e6f708192a3b4c5d6e7f809a1b2c3"

// writeGitCheckout lays down the plumbing a git checkout keeps: HEAD naming a
// branch whose commit lives either in a loose ref file or in packed-refs.
func writeGitCheckout(t *testing.T, root string, packed bool) {
	t.Helper()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(filepath.Join(gitDir, "HEAD"), "ref: refs/heads/main\n")
	if packed {
		write(filepath.Join(gitDir, "packed-refs"),
			"# pack-refs with: peeled fully-peeled sorted\n"+gitCheckoutCommit+" refs/heads/main\n")
		return
	}
	write(filepath.Join(gitDir, "refs", "heads", "main"), gitCheckoutCommit+"\n")
}

// TestRunSecurityToolStagedTargetRevision covers the fallback chain that keeps
// a staged request valid when a scan runs against an unpinned branch head.
func TestRunSecurityToolStagedTargetRevision(t *testing.T) {
	tests := []struct {
		name           string
		callerRevision string
		scanRevision   string
		git            string
		want           string
	}{
		{name: "caller revision wins", callerRevision: "cafe1234", scanRevision: "abc1234", git: "loose", want: "cafe1234"},
		{name: "scan context revision is used", scanRevision: "abc1234", git: "loose", want: "abc1234"},
		{name: "git HEAD is derived from a loose ref", git: "loose", want: gitCheckoutCommit},
		{name: "git HEAD is derived from packed-refs", git: "packed", want: gitCheckoutCommit},
		{name: "staged content digest is the last resort"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{})
			fixture.writeProject(t)
			fixture.tool.state.scanCtx.Revision = tc.scanRevision
			if tc.git != "" {
				writeGitCheckout(t, filepath.Join(fixture.workspace, "repo"), tc.git == "packed")
			}

			in := runSecurityToolInput{
				Tool: "slither",
				Target: runSecurityToolTarget{
					Type:     "solidity_project",
					Locator:  "repo/contracts",
					Revision: tc.callerRevision,
				},
			}
			spec, staged, failure := fixture.tool.buildSpec(context.Background(), in, "fixture-run")
			if failure != nil {
				t.Fatalf("buildSpec failed: %s", failure.Content)
			}
			want := tc.want
			if want == "" {
				want = "staged:" + staged.Digest
				if !strings.HasPrefix(want, "staged:sha256:") {
					t.Fatalf("staged digest %q is not a sha256 digest", staged.Digest)
				}
			}
			if spec.Target.Revision != want {
				t.Fatalf("revision = %q, want %q", spec.Target.Revision, want)
			}

			// The whole point of a derived revision is that the control plane
			// accepts the request.
			config, err := securitytoolrun.RunConfigFor(spec)
			if err != nil {
				t.Fatalf("RunConfigFor: %v", err)
			}
			registry, err := securitytoolrun.DefaultRegistry()
			if err != nil {
				t.Fatalf("DefaultRegistry: %v", err)
			}
			if _, err := securitytoolrun.Validate(registry, config); err != nil {
				t.Fatalf("staged request must validate: %v", err)
			}
		})
	}
}

// TestRunSecurityToolCallerCannotAssertFilesystemDigest keeps the digest
// provenance rule: only staged content or a digest-pinned locator may set it.
func TestRunSecurityToolCallerCannotAssertFilesystemDigest(t *testing.T) {
	fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{})
	fixture.writeProject(t)
	asserted := "sha256:" + strings.Repeat("d", 64)

	in := runSecurityToolInput{
		Tool:   "slither",
		Target: runSecurityToolTarget{Type: "solidity_project", Locator: "repo/contracts", Revision: asserted},
	}
	spec, staged, failure := fixture.tool.buildSpec(context.Background(), in, "fixture-run")
	if failure != nil {
		t.Fatalf("buildSpec failed: %s", failure.Content)
	}
	if spec.Target.Digest != staged.Digest || spec.Target.Digest == asserted {
		t.Fatalf("digest = %q, want the staged archive digest %q", spec.Target.Digest, staged.Digest)
	}
	if spec.Target.Revision != asserted {
		t.Fatalf("revision = %q, want the caller value recorded as a revision", spec.Target.Revision)
	}
}

func TestRunSecurityToolRejectsResultDigestMismatch(t *testing.T) {
	fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{})
	fixture.writeProject(t)
	fixture.client.status = securityToolRunStatusForResult(t, fixture.blobs,
		sampleSecurityToolResult(), "sha256:"+strings.Repeat("c", 64))

	result, summary := fixture.exec(t, slitherRequest)
	if !result.IsError {
		t.Fatalf("digest mismatch must never pass: %s", result.Content)
	}
	if summary.Status != string(securitytoolpacks.StatusError) {
		t.Fatalf("status = %q, want error", summary.Status)
	}
	if !strings.Contains(result.Content, "digest mismatch") {
		t.Fatalf("summary must explain the mismatch: %s", result.Content)
	}
	if len(fixture.findingStore.findings) != 0 {
		t.Fatalf("untrusted result must ingest nothing, got %d findings", len(fixture.findingStore.findings))
	}
}

func TestRunSecurityToolUnreadableResultIsNeverAPass(t *testing.T) {
	fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{})
	fixture.writeProject(t)
	fixture.client.status = securityToolRunStatusForResult(t, fixture.blobs, sampleSecurityToolResult(), "")
	fixture.blobs.failGet = errors.New("bucket unavailable")

	result, summary := fixture.exec(t, slitherRequest)
	if !result.IsError || summary.Status != string(securitytoolpacks.StatusError) {
		t.Fatalf("unreadable result must be an error: %+v", result)
	}
	if len(fixture.findingStore.findings) != 0 {
		t.Fatalf("nothing may be ingested from an unreadable result")
	}
}

func TestRunSecurityToolTerminalStatusMapping(t *testing.T) {
	pass := sampleSecurityToolResult()
	// The retired verdict, spelled literally so the test outlives the constant.
	pass.Status = securitytoolpacks.Status("pass")
	pass.Findings = nil

	tests := []struct {
		name       string
		status     func(t *testing.T, blobs *fakeSecurityToolBlobStore) platformv1alpha1.SecurityToolRunStatus
		wantStatus string
		wantError  bool
	}{
		{
			name: "succeeded with findings",
			status: func(t *testing.T, blobs *fakeSecurityToolBlobStore) platformv1alpha1.SecurityToolRunStatus {
				return securityToolRunStatusForResult(t, blobs, sampleSecurityToolResult(), "")
			},
			wantStatus: "findings",
		},
		{
			name: "succeeded clean",
			status: func(t *testing.T, blobs *fakeSecurityToolBlobStore) platformv1alpha1.SecurityToolRunStatus {
				return securityToolRunStatusForResult(t, blobs, pass, "")
			},
			wantStatus: "pass",
		},
		{
			name: "failed with timeout verdict",
			status: func(*testing.T, *fakeSecurityToolBlobStore) platformv1alpha1.SecurityToolRunStatus {
				return platformv1alpha1.SecurityToolRunStatus{
					Phase:   platformv1alpha1.SecurityToolRunPhaseFailed,
					Message: "execution Job exceeded its deadline",
					Result:  &platformv1alpha1.SecurityToolRunResult{Status: "timeout"},
				}
			},
			wantStatus: "timeout",
			wantError:  true,
		},
		{
			name: "failed with error verdict",
			status: func(*testing.T, *fakeSecurityToolBlobStore) platformv1alpha1.SecurityToolRunStatus {
				return platformv1alpha1.SecurityToolRunStatus{
					Phase:  platformv1alpha1.SecurityToolRunPhaseFailed,
					Result: &platformv1alpha1.SecurityToolRunResult{Status: "error", Errors: []string{"image pull failed"}},
				}
			},
			wantStatus: "error",
			wantError:  true,
		},
		{
			name: "failed run claiming a pass is rewritten",
			status: func(*testing.T, *fakeSecurityToolBlobStore) platformv1alpha1.SecurityToolRunStatus {
				return platformv1alpha1.SecurityToolRunStatus{
					Phase:  platformv1alpha1.SecurityToolRunPhaseFailed,
					Result: &platformv1alpha1.SecurityToolRunResult{Status: "pass"},
				}
			},
			wantStatus: "error",
			wantError:  true,
		},
		{
			name: "succeeded without a result document",
			status: func(*testing.T, *fakeSecurityToolBlobStore) platformv1alpha1.SecurityToolRunStatus {
				return platformv1alpha1.SecurityToolRunStatus{Phase: platformv1alpha1.SecurityToolRunPhaseSucceeded}
			},
			wantStatus: "error",
			wantError:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{})
			fixture.writeProject(t)
			fixture.client.status = tc.status(t, fixture.blobs)

			result, summary := fixture.exec(t, slitherRequest)
			if summary.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (%s)", summary.Status, tc.wantStatus, result.Content)
			}
			if result.IsError != tc.wantError {
				t.Fatalf("IsError = %t, want %t (%s)", result.IsError, tc.wantError, result.Content)
			}
		})
	}
}

func TestRunSecurityToolWaitTimeoutNamesTheRun(t *testing.T) {
	fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{
		Phase: platformv1alpha1.SecurityToolRunPhaseRunning,
	})
	fixture.writeProject(t)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result, err := fixture.tool.Execute(ctx,
		json.RawMessage(`{"tool":"slither","target":{"type":"solidity_project","locator":"repo/contracts","revision":"abc1234"},"timeout_seconds":5}`), "")
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("a run that never finishes must not pass: %s", result.Content)
	}
	if fixture.client.created == nil || !strings.Contains(result.Content, fixture.client.created.Name) {
		t.Fatalf("timeout must name the SecurityToolRun for follow-up: %s", result.Content)
	}
	if len(fixture.findingStore.findings) != 0 {
		t.Fatal("a timed-out run must ingest nothing")
	}
}

func TestRunSecurityToolRejectsInvalidRequestsLocally(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{
			name:    "unknown tool",
			input:   `{"tool":"definitely-not-a-tool","target":{"type":"solidity_project","locator":"repo/contracts","revision":"abc1234"}}`,
			wantErr: "unknown security tool",
		},
		{
			name:    "catalog-only tool",
			input:   `{"tool":"playwright","target":{"type":"browser_script","locator":"repo/browser","revision":"abc1234"}}`,
			wantErr: "not executable",
		},
		{
			name:    "wrong target type",
			input:   `{"tool":"slither","target":{"type":"base_url","locator":"https://example.com","revision":"abc1234"}}`,
			wantErr: "does not accept target type",
		},
		{
			name:    "unknown argument",
			input:   `{"tool":"slither","target":{"type":"solidity_project","locator":"repo/contracts","revision":"abc1234"},"arguments":{"depth":"9"}}`,
			wantErr: `has no argument "depth"`,
		},
		{
			name:    "missing scope for a network tool",
			input:   `{"tool":"nuclei","target":{"type":"base_url","locator":"https://example.com","revision":"sha256:` + strings.Repeat("d", 64) + `"},"arguments":{"rate":"10"}}`,
			wantErr: "requires explicit target scope",
		},
		{
			name:    "network target without a pinned digest",
			input:   `{"tool":"nuclei","target":{"type":"base_url","locator":"https://example.com","revision":"latest"},"arguments":{"rate":"10"},"scope":["example.com"]}`,
			wantErr: "immutable sha256 digest",
		},
		{
			name:    "unknown input field",
			input:   `{"tool":"slither","target":{"type":"solidity_project","locator":"repo/contracts"},"image":"evil:latest"}`,
			wantErr: "invalid input",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{})
			fixture.writeProject(t)
			result, err := fixture.tool.Execute(context.Background(), json.RawMessage(tc.input), "")
			if err != nil {
				t.Fatalf("validation must not return a Go error: %v", err)
			}
			if !result.IsError || !strings.Contains(result.Content, tc.wantErr) {
				t.Fatalf("result = %+v, want an error containing %q", result, tc.wantErr)
			}
			if fixture.client.created != nil {
				t.Fatalf("invalid request must not create a SecurityToolRun: %+v", fixture.client.created.Spec)
			}
		})
	}
}

func TestRunSecurityToolNetworkTargetIsNotStaged(t *testing.T) {
	fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{})
	digest := "sha256:" + strings.Repeat("e", 64)
	fixture.client.status = securityToolRunStatusForResult(t, fixture.blobs, sampleSecurityToolResult(), "")

	result, _ := fixture.exec(t, `{"tool":"nuclei","target":{"type":"base_url","locator":"https://example.com","revision":"`+digest+`"},"arguments":{"rate":"10"},"scope":["example.com"],"timeout_seconds":60}`)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	created := fixture.client.created
	if created.Spec.Target.StagedObjectKey != "" || created.Spec.Target.MediaType != "" {
		t.Fatalf("network targets must not stage content: %+v", created.Spec.Target)
	}
	if created.Spec.Target.Digest != digest || created.Spec.Target.Locator != "https://example.com" {
		t.Fatalf("network target lost its pin: %+v", created.Spec.Target)
	}
	if len(created.Spec.Arguments) != 1 || created.Spec.Arguments[0].Name != "rate" || created.Spec.Arguments[0].Value != "10" {
		t.Fatalf("typed arguments were not recorded: %+v", created.Spec.Arguments)
	}
}

func TestRunSecurityToolRejectsLocatorsOutsideTheWorkspace(t *testing.T) {
	tests := []struct {
		name    string
		locator string
		wantErr string
	}{
		{name: "absolute host path", locator: "/proc/1/environ", wantErr: "outside the run workspace"},
		{name: "traversal", locator: "../../etc/passwd", wantErr: "outside the run workspace"},
		{name: "symlink out of the workspace", locator: "escape/passwd", wantErr: "outside the run workspace"},
		{name: "home relative", locator: "~/.ssh", wantErr: "target locator"},
		{name: "runtime config", locator: "/ga/config/run.json", wantErr: "outside the run workspace"},
		{name: "missing workspace path", locator: "repo/does-not-exist", wantErr: "does not exist in the run workspace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{})
			fixture.writeProject(t)
			if err := os.Symlink("/etc", filepath.Join(fixture.workspace, "escape")); err != nil {
				t.Fatalf("symlink: %v", err)
			}
			input := fmt.Sprintf(`{"tool":"slither","target":{"type":"solidity_project","locator":%q,"revision":"sha256:%s"}}`,
				tc.locator, strings.Repeat("f", 64))

			result, err := fixture.tool.Execute(context.Background(), json.RawMessage(input), "")
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if !result.IsError || !strings.Contains(result.Content, tc.wantErr) {
				t.Fatalf("result = %+v, want an error containing %q", result, tc.wantErr)
			}
			if fixture.client.created != nil {
				t.Fatalf("a rejected locator must not create a SecurityToolRun: %+v", fixture.client.created.Spec)
			}
			if len(fixture.blobs.objects) != 0 {
				t.Fatalf("a rejected locator must not stage anything: %v", fixture.blobs.objects)
			}
		})
	}
}

func TestRunSecurityToolDigestIsNeverTakenFromTheCallerForStagedContent(t *testing.T) {
	fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{})
	fixture.writeProject(t)
	fixture.client.status = securityToolRunStatusForResult(t, fixture.blobs, sampleSecurityToolResult(), "")
	fabricated := "sha256:" + strings.Repeat("9", 64)

	result, _ := fixture.exec(t, fmt.Sprintf(
		`{"tool":"slither","target":{"type":"solidity_project","locator":"repo/contracts","revision":%q},"timeout_seconds":60}`, fabricated))
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	created := fixture.client.created
	archive := fixture.blobs.objects[created.Spec.Target.StagedObjectKey]
	if created.Spec.Target.Digest == fabricated {
		t.Fatal("the caller's revision must never become the target digest")
	}
	if created.Spec.Target.Digest != securityToolRunDigest(archive) {
		t.Fatalf("digest %q must be the digest of the archive this tool built", created.Spec.Target.Digest)
	}
}

func TestBuildSpecDigestSources(t *testing.T) {
	fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{})
	fixture.writeProject(t)
	locatorDigest := "sha256:" + strings.Repeat("1", 64)
	fabricated := "sha256:" + strings.Repeat("9", 64)

	tests := []struct {
		name    string
		locator string
		want    string
	}{
		{
			name:    "image locator pins itself",
			locator: "ghcr.io/acme/widget@" + locatorDigest,
			want:    locatorDigest,
		},
		{
			name:    "network endpoint carries the caller's unverified pin",
			locator: "https://example.com",
			want:    fabricated,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := runSecurityToolInput{
				Tool:   "nuclei",
				Target: runSecurityToolTarget{Type: "base_url", Locator: tc.locator, Revision: fabricated},
			}
			spec, staging, failure := fixture.tool.buildSpec(context.Background(), in, "run-1")
			if failure != nil {
				t.Fatalf("unexpected failure: %s", failure.Content)
			}
			if spec.Target.Digest != tc.want {
				t.Fatalf("digest = %q, want %q", spec.Target.Digest, tc.want)
			}
			if spec.Target.StagedObjectKey != "" || staging.ObjectKey != "" {
				t.Fatalf("network targets must not stage content: %+v", spec.Target)
			}
		})
	}
}

func TestRunSecurityToolRejectsDirectoryToolsWithoutStagedContent(t *testing.T) {
	fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{})

	result, err := fixture.tool.Execute(context.Background(), json.RawMessage(
		`{"tool":"slither","target":{"type":"solidity_project","locator":"https://example.com/acme/widget.git","revision":"abc1234"}}`), "")
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "solidity-project.v1+directory") {
		t.Fatalf("result = %+v, want the required directory media type explained", result)
	}
	if fixture.client.created != nil {
		t.Fatal("a request the Job can never satisfy must not create a SecurityToolRun")
	}
}

func TestRunSecurityToolReportsADisappearedRunDistinctly(t *testing.T) {
	fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{
		Phase: platformv1alpha1.SecurityToolRunPhaseRunning,
	})
	fixture.writeProject(t)
	fixture.tool.deps.Client = &vanishingSecurityToolRunClient{reconcilingSecurityToolRunClient: fixture.client}

	result, err := fixture.tool.Execute(context.Background(), json.RawMessage(slitherRequest), "")
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("a run that vanished must not pass: %s", result.Content)
	}
	if !strings.Contains(result.Content, "no longer exists") || strings.Contains(result.Content, "keeps running") {
		t.Fatalf("a deleted run must be reported distinctly from a still-running one: %s", result.Content)
	}
	if len(fixture.findingStore.findings) != 0 {
		t.Fatal("a run that vanished must ingest nothing")
	}
}

func TestRunSecurityToolReportsTheResultSizeCap(t *testing.T) {
	fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{})
	fixture.writeProject(t)
	fixture.client.status = securityToolRunStatusForResult(t, fixture.blobs, sampleSecurityToolResult(), "")
	fixture.blobs.failGet = fmt.Errorf("project asset object %q exceeds the %d-byte limit", testResultObjectKey, 25<<20)

	result, summary := fixture.exec(t, slitherRequest)
	if !result.IsError || summary.Status != string(securitytoolpacks.StatusError) {
		t.Fatalf("an unreadable result must be an error: %+v", result)
	}
	if !strings.Contains(result.Content, "object-read cap") || !strings.Contains(result.Content, "25 MiB") {
		t.Fatalf("the size cap must be reported explicitly: %s", result.Content)
	}
	if !strings.Contains(result.Content, "intact in object storage") {
		t.Fatalf("the summary must say the document still exists: %s", result.Content)
	}
}

func TestSecurityToolRunNameSuffixIsCollisionResistant(t *testing.T) {
	seen := map[string]bool{}
	for range 64 {
		name, err := securityToolRunName("nightly-scan-run-1", "slither")
		if err != nil {
			t.Fatalf("securityToolRunName: %v", err)
		}
		suffix := name[strings.LastIndex(name, "-")+1:]
		if len(suffix) < 16 {
			t.Fatalf("suffix %q carries less than 8 bytes of entropy", suffix)
		}
		if seen[name] {
			t.Fatalf("name %q was generated twice", name)
		}
		seen[name] = true
	}
}

func TestRunSecurityToolMetadata(t *testing.T) {
	fixture := newSecurityToolRunFixture(t, platformv1alpha1.SecurityToolRunStatus{})
	if !fixture.tool.IsReadOnly() {
		t.Error("run_security_tool only records platform state and must be read-only")
	}
	if fixture.tool.NeedsApproval() {
		t.Error("run_security_tool must not need approval")
	}
	if fixture.tool.TimeoutSeconds() <= securityToolRunDefaultTimeout {
		t.Errorf("tool timeout %d must exceed the default wait", fixture.tool.TimeoutSeconds())
	}
	description := fixture.tool.Description()
	for _, want := range []string{"cannot", "image", "outbound network"} {
		if !strings.Contains(description, want) {
			t.Errorf("description must mention %q: %s", want, description)
		}
	}
	var inputSchema map[string]any
	if err := json.Unmarshal(fixture.tool.InputSchema(), &inputSchema); err != nil {
		t.Fatalf("input schema is not valid JSON: %v", err)
	}
	properties, _ := inputSchema["properties"].(map[string]any)
	for _, forbidden := range []string{"image", "command", "argv", "flags"} {
		if _, exists := properties[forbidden]; exists {
			t.Errorf("input schema must not expose %q", forbidden)
		}
	}
	for _, required := range []string{"tool", "target", "arguments", "scope", "seed", "sensitive_fields", "timeout_seconds"} {
		if _, exists := properties[required]; !exists {
			t.Errorf("input schema is missing %q", required)
		}
	}
}

func TestArchiveWorkspaceTargetIsDeterministicAndSkipsSpecialFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../b.txt", filepath.Join(root, "src", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	first, entries, skipped, err := archiveWorkspaceTarget(root)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	second, _, _, err := archiveWorkspaceTarget(root)
	if err != nil {
		t.Fatalf("archive again: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("archiving the same tree twice must produce identical bytes")
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want the escaping symlink skipped", skipped)
	}
	names := archiveEntryNames(t, first)
	if entries != len(names) {
		t.Fatalf("entry count %d does not match archive contents %v", entries, names)
	}
	if contains(names, "escape") {
		t.Fatalf("escaping symlink must not be staged: %v", names)
	}
	for _, want := range []string{"src", "src/a.go", "src/link", "b.txt"} {
		if !contains(names, want) {
			t.Fatalf("archive %v is missing %q", names, want)
		}
	}
}

func archiveEntryNames(t *testing.T, archive []byte) []string {
	t.Helper()
	stream, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer func() { _ = stream.Close() }()
	reader := tar.NewReader(stream)
	var names []string
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		if !header.ModTime.Equal(time.Unix(0, 0).UTC()) || header.Uid != 0 || header.Gid != 0 {
			t.Fatalf("archive entry %q is not normalized: %+v", header.Name, header)
		}
		names = append(names, header.Name)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want || filepath.Base(value) == want {
			return true
		}
	}
	return false
}

// A campaign the control plane will not wait out is worse than a shorter one:
// the call returns "unfinished", so the findings are never ingested and the
// corpus is never persisted even when the Job goes on to succeed.
func TestFuzzCampaignsDriveTheControlPlaneWait(t *testing.T) {
	goCampaign := runSecurityToolInput{Tool: "go-fuzz-tests", Arguments: map[string]string{"fuzztime": "15m"}}
	if got := goCampaign.timeout(); got <= securityToolRunDefaultTimeout {
		t.Fatalf("15m Go campaign waits %ds, want more than the %ds default", got, securityToolRunDefaultTimeout)
	}
	if goCampaign.campaignExceedsWaitBudget() {
		t.Fatal("a 15m Go campaign fits the maximum wait and must not be rejected")
	}

	// A caller who set an explicit wait keeps it: the platform does not
	// silently spend more of their budget than they asked for.
	explicit := runSecurityToolInput{Tool: "go-fuzz-tests", TimeoutSeconds: 120, Arguments: map[string]string{"fuzztime": "15m"}}
	if got := explicit.timeout(); got != 120 {
		t.Fatalf("explicit timeout = %ds, want 120", got)
	}

	// Rust campaigns carry a build allowance on top of the campaign, and the
	// longest one the registry accepts must still fit the platform's maximum
	// wait — otherwise the pack would advertise a campaign whose result could
	// never be ingested. This pins that relationship rather than assuming it.
	rustCampaign := runSecurityToolInput{Tool: "cargo-fuzz", Arguments: map[string]string{"max_total_time": "15m"}}
	if rustCampaign.campaignExceedsWaitBudget() {
		t.Fatalf("the longest Rust campaign needs %ds, past the %ds maximum wait: it could never be ingested", rustCampaign.campaignWait(), securityToolRunMaxTimeout)
	}
	if got := rustCampaign.timeout(); got != securityToolRunMaxTimeout && got < rustCampaign.campaignWait() {
		t.Fatalf("15m Rust campaign waits %ds, want at least its %ds campaign budget", got, rustCampaign.campaignWait())
	}
	// The guard itself still bites when a campaign genuinely cannot be waited
	// out, which is what protects a future budget change from orphaning runs.
	overBudget := runSecurityToolInput{Tool: "cargo-fuzz", Arguments: map[string]string{"max_total_time": "15m"}}
	if !overBudget.exceedsWait(overBudget.campaignWait() - 1) {
		t.Fatalf("a campaign needing %ds was accepted against a shorter wait", overBudget.campaignWait())
	}
	if overBudget.exceedsWait(overBudget.campaignWait()) {
		t.Fatal("a campaign that exactly fits its wait was rejected")
	}
	short := runSecurityToolInput{Tool: "cargo-fuzz", Arguments: map[string]string{"max_total_time": "2m"}}
	if short.campaignExceedsWaitBudget() {
		t.Fatal("a 2m Rust campaign fits the maximum wait and must not be rejected")
	}
	// A short campaign keeps the platform default, which already exceeds what
	// it needs; the invariant that matters is that the wait is never shorter
	// than the campaign plus its allowances.
	if got := short.timeout(); got < short.campaignWait() {
		t.Fatalf("2m Rust campaign waits %ds, less than its %ds campaign budget", got, short.campaignWait())
	}

	// A tool without a campaign keeps the platform default.
	plain := runSecurityToolInput{Tool: "slither"}
	if got := plain.timeout(); got != securityToolRunDefaultTimeout {
		t.Fatalf("non-campaign tool waits %ds, want the %ds default", got, securityToolRunDefaultTimeout)
	}
}
