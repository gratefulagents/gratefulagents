package securitytoolpacks

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gratefulagents/gratefulagents/internal/security"
)

type fixtureSandbox struct {
	native NativeResult
	got    ExecutionRequest
}

func (f *fixtureSandbox) Execute(_ context.Context, req ExecutionRequest) NativeResult {
	f.got = req
	return f.native
}

func fixtureTarget(kind, locator string) Target {
	return Target{Type: kind, Locator: locator, Revision: "fixture-v1", Digest: sha256Digest([]byte(locator))}
}

func readFixture(t *testing.T, parts ...string) []byte {
	t.Helper()
	path := filepath.Join(append([]string{"..", "..", "test", "fixtures", "security-toolpacks"}, parts...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func fixtureManifest() Manifest {
	pin := sha256Digest([]byte("fixture-toolpack-image"))
	manifest := DefaultManifest(pin, map[string]string{"nuclei": pin, "wycheproof": pin, "rfc-nist-vectors": pin, "suricata": pin, "zeek": pin})
	// Offline fixture executors stand in for verified external runtime binaries.
	for i := range manifest.Tools {
		if slices.Contains([]string{"nmap", "zeek", "suricata"}, manifest.Tools[i].Name) {
			manifest.Tools[i].Enabled = true
			manifest.Tools[i].DisabledReason = ""
		}
	}
	return manifest
}

func runnerFor(t *testing.T, native NativeResult) *Runner {
	t.Helper()
	reg, err := NewRegistry(fixtureManifest())
	if err != nil {
		t.Fatal(err)
	}
	return NewRunner(reg, &fixtureSandbox{native: native})
}

func TestDefaultManifestDeclaresInitialToolPacks(t *testing.T) {
	m := fixtureManifest()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"playwright", "owasp-zap", "schemathesis", "restler", "mitmproxy", "sslyze", "testssl", "nuclei", "authorization-matrix", "wycheproof", "rfc-nist-vectors", "dudect", "ctgrind", "tlsfuzzer", "crypto-differential", "tamarin", "proverif", "verifpal", "openssl-inspect", "nmap", "tshark", "zeek", "suricata", "scapy", "boofuzz"} {
		found := false
		for _, tool := range m.Tools {
			if tool.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %s", name)
		}
	}
}

func TestTypedInvocationUsesFixedArgv(t *testing.T) {
	reg, _ := NewRegistry(fixtureManifest())
	target := fixtureTarget("address_scope", "192.0.2.0/24")
	inv, _, err := reg.BuildInvocation(RunConfig{Tool: "nmap", Target: target, Arguments: map[string]string{"ports": "80,443", "rate": "10"}, Scope: []string{"192.0.2.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Argv) != 11 || inv.Argv[10] != target.Locator {
		t.Fatalf("argv=%q", inv.Argv)
	}
}

func TestAuthorizationMatrixFindingFlowsThroughSecurityPipeline(t *testing.T) {
	native := NativeResult{ExitCode: 0, Examined: []string{"anonymous-own", "alice-own", "bob-cross-tenant"}, Output: readFixture(t, "web", "authorization-matrix.json")}
	r := runnerFor(t, native)
	res := r.Run(context.Background(), RunConfig{Tool: "authorization-matrix", Target: fixtureTarget("authorization_matrix", "fixtures/web/matrix.json"), Scope: []string{"http://fixture.invalid"}})
	if res.Status != StatusFindings || len(res.Findings) != 1 {
		t.Fatalf("status=%s findings=%+v errors=%v", res.Status, res.Findings, res.Errors)
	}
	rec := res.Findings[0]
	if rec.Category != "authz" || rec.CWE != "CWE-639" {
		t.Fatalf("record=%+v", rec)
	}
	if strings.Contains(rec.RawEvidence, "fixture credential") || strings.Contains(rec.RawEvidence, "fixture session") {
		t.Fatalf("evidence leaked credentials: %s", rec.RawEvidence)
	}
	finding, err := security.NormalizeScannerRecord(rec, "fixture-repository", "fixture-revision")
	if err != nil {
		t.Fatal(err)
	}
	ranked := security.Rank([]security.Finding{finding}, security.RankRules{})
	if len(ranked) != 1 {
		t.Fatal("finding did not enter rank pipeline")
	}
	if !strings.Contains(security.RenderMarkdown(security.ReportInput{Repository: "fixture-repository", Revision: "fixture-revision", Ranked: ranked}), "Authorization matrix") {
		t.Fatal("finding absent from report")
	}
}

func TestReviewedNucleiFixtureNormalizesAndRedacts(t *testing.T) {
	output := readFixture(t, "web", "nuclei.jsonl")
	result := runnerFor(t, NativeResult{ExitCode: 0, Examined: []string{"http://127.0.0.1:18084"}, Output: output}).Run(context.Background(), RunConfig{
		Tool: "nuclei", Target: fixtureTarget("base_url", "http://127.0.0.1:18084"),
		Arguments: map[string]string{"rate": "1"}, Scope: []string{"http://127.0.0.1:18084"},
	})
	if result.Status != StatusFindings || len(result.Findings) != 1 || result.Findings[0].Category != "misconfiguration" {
		t.Fatalf("nuclei result=%+v", result)
	}
	encoded, _ := canonicalJSON(result.Findings)
	if strings.Contains(string(encoded), "fixture-secret") {
		t.Fatal("Nuclei evidence leaked credentials")
	}
}

func TestCryptoKnownAnswerPassAndFailure(t *testing.T) {
	target := fixtureTarget("crypto_vectors", "fixtures/crypto/rfc4231.json")
	pass := runnerFor(t, NativeResult{ExitCode: 0, Examined: []string{"rfc4231-1"}, Output: readFixture(t, "crypto", "pass.json")}).Run(context.Background(), RunConfig{Tool: "rfc-nist-vectors", Target: target})
	if pass.Status != StatusPass || len(pass.Findings) != 0 {
		t.Fatalf("pass result=%+v", pass)
	}
	fail := runnerFor(t, NativeResult{ExitCode: 1, Examined: []string{"rfc4231-1"}, Output: readFixture(t, "crypto", "fail.json")}).Run(context.Background(), RunConfig{Tool: "rfc-nist-vectors", Target: target})
	if fail.Status != StatusFindings || len(fail.Findings) != 1 || fail.Findings[0].RuleID != "CRYPTO-KAT-MISMATCH" {
		t.Fatalf("failure result=%+v", fail)
	}
}

func TestNetworkFixturesNmapAndZeek(t *testing.T) {
	nmapXML := readFixture(t, "network", "nmap.xml")
	nmap := runnerFor(t, NativeResult{ExitCode: 0, Examined: []string{"192.0.2.10"}, Skipped: []string{"198.51.100.7"}, Output: nmapXML}).Run(context.Background(), RunConfig{Tool: "nmap", Target: fixtureTarget("address_scope", "192.0.2.0/24"), Arguments: map[string]string{"ports": "80,443", "rate": "10"}, Scope: []string{"192.0.2.0/24"}})
	if nmap.Status != StatusPartial || len(nmap.Findings) != 1 || !strings.Contains(nmap.Findings[0].Message, "telnet") {
		t.Fatalf("nmap=%+v", nmap)
	}
	zeekJSON := readFixture(t, "network", "zeek-notice.jsonl")
	pcap := readFixture(t, "network", "capture.pcap")
	pcapTarget := Target{Type: "pcap", Locator: "test/fixtures/security-toolpacks/network/capture.pcap", Revision: "fixture-v1", Digest: sha256Digest(pcap)}
	zeek := runnerFor(t, NativeResult{ExitCode: 0, Examined: []string{"capture.pcap"}, Output: zeekJSON}).Run(context.Background(), RunConfig{Tool: "zeek", Target: pcapTarget})
	if zeek.Status != StatusFindings || len(zeek.Findings) != 1 || zeek.Artifacts[0].Digest == "" {
		t.Fatalf("zeek=%+v", zeek)
	}
}

func TestNetworkNaabuAndBlockchainForgeFixtures(t *testing.T) {
	naabuOutput := readFixture(t, "network", "naabu.jsonl")
	naabu := runnerFor(t, NativeResult{ExitCode: 0, Examined: []string{"service.example.test"}, Output: naabuOutput}).Run(context.Background(), RunConfig{
		Tool: "naabu", Target: fixtureTarget("address_scope", "192.0.2.10"), Scope: []string{"192.0.2.10"},
		Arguments: map[string]string{"rate": "25", "ports": "80,443"},
	})
	if naabu.Status != StatusFindings || len(naabu.Findings) != 1 || naabu.Findings[0].RuleID != "NAABU-OPEN-PORT" {
		t.Fatalf("naabu result=%+v", naabu)
	}

	aderynTarget := fixtureTarget("solidity_project", "fixtures/blockchain/vault")
	aderynTarget.MediaType = "application/vnd.gratefulagents.solidity-project.v1+directory"
	aderynOutput := readFixture(t, "blockchain", "aderyn.sarif")
	aderyn := runnerFor(t, NativeResult{ExitCode: 0, Examined: []string{"src/Vault.sol"}, Output: aderynOutput}).Run(context.Background(), RunConfig{Tool: "aderyn", Target: aderynTarget})
	if aderyn.Status != StatusFindings || len(aderyn.Findings) != 1 || aderyn.Findings[0].RuleID != "reentrancy-state-change" {
		t.Fatalf("aderyn result=%+v", aderyn)
	}

	seed := int64(42)
	forgeOutput := readFixture(t, "blockchain", "forge-junit.xml")
	forgeTarget := fixtureTarget("foundry_project", "fixtures/blockchain/vault")
	forgeTarget.MediaType = "application/vnd.gratefulagents.foundry-security-project.v1+directory"
	forge := runnerFor(t, NativeResult{ExitCode: 1, Examined: []string{"test/InvariantVault.t.sol"}, Output: forgeOutput}).Run(context.Background(), RunConfig{
		Tool: "forge-security-tests", Target: forgeTarget, Seed: &seed,
	})
	if forge.Status != StatusFindings || len(forge.Findings) != 1 || forge.Findings[0].RuleID != "JUNIT-FAILURE" {
		t.Fatalf("forge result=%+v", forge)
	}
	if forge.Replay.Seed == nil || *forge.Replay.Seed != seed {
		t.Fatal("forge replay omitted fuzz seed")
	}
	registry, err := NewRegistry(DefaultManifest(sha256Digest([]byte("image")), nil))
	if err != nil {
		t.Fatal(err)
	}
	invocation, _, err := registry.BuildInvocation(RunConfig{Tool: "forge-security-tests", Target: forgeTarget, Seed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	fuzzSeedFlags := 0
	for _, argument := range invocation.Argv {
		if argument == "--fuzz-seed" {
			fuzzSeedFlags++
		}
	}
	if fuzzSeedFlags != 1 || slices.Contains(invocation.Argv, "--seed") {
		t.Fatalf("forge seed argv is not fixed: %q", invocation.Argv)
	}
}

func TestFailureStatusesAndCanonicalReplay(t *testing.T) {
	cfg := RunConfig{Tool: "zeek", Target: fixtureTarget("pcap", "fixture.pcapng")}
	timed := runnerFor(t, NativeResult{TimedOut: true, Err: context.DeadlineExceeded}).Run(context.Background(), cfg)
	if timed.Status != StatusTimeout {
		t.Fatal(timed.Status)
	}
	errResult := runnerFor(t, NativeResult{ExitCode: 2, Err: context.Canceled}).Run(context.Background(), cfg)
	if errResult.Status != StatusError {
		t.Fatal(errResult.Status)
	}
	partial := runnerFor(t, NativeResult{ExitCode: 0, Uncovered: []string{"encrypted-payload"}, Output: []byte{}}).Run(context.Background(), cfg)
	if partial.Status != StatusPartial {
		t.Fatal(partial.Status)
	}
	a := runnerFor(t, NativeResult{ExitCode: 0, Examined: []string{"b", "a"}, Output: []byte{}}).Run(context.Background(), cfg)
	b := runnerFor(t, NativeResult{ExitCode: 0, Examined: []string{"a", "b"}, Output: []byte{}}).Run(context.Background(), cfg)
	ja, _ := MarshalCanonical(a)
	jb, _ := MarshalCanonical(b)
	if string(ja) != string(jb) {
		t.Fatalf("non-deterministic:\n%s\n%s", ja, jb)
	}
}
