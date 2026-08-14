package securitytoolpacks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const forkEndpointAPIKey = "s3cr3t-fork-api-key"

func configureForkEndpoint(t *testing.T, aliases, alias, endpoint string) {
	t.Helper()
	t.Setenv(evmForkEndpointsEnv, aliases)
	if alias != "" {
		t.Setenv(operatorConfigurationName(evmForkEndpointURLEnvPrefix, alias), endpoint)
	}
}

func TestForkEndpointResolvesFromOperatorConfigurationOnly(t *testing.T) {
	endpoint := "https://rpc.example.test/v1/" + forkEndpointAPIKey
	configureForkEndpoint(t, "mainnet-archive,base-archive", "mainnet-archive", endpoint)

	resolved, err := resolveForkEndpoint("mainnet-archive")
	if err != nil || resolved != endpoint {
		t.Fatalf("configured alias resolved to %q (%v)", resolved, err)
	}
	// Authorized but unconfigured, and unauthorized, must both fail the run
	// rather than fall through to the literal token.
	for name, alias := range map[string]string{"unconfigured alias": "base-archive", "unknown alias": "attacker-archive"} {
		if _, err := resolveForkEndpoint(alias); err == nil {
			t.Errorf("%s %q was accepted", name, alias)
		}
	}
	for _, configured := range []string{"", "   ", "not-a-url", "ftp://rpc.example.test", "/local/path", "https://rpc.example.test/#fragment"} {
		t.Setenv(operatorConfigurationName(evmForkEndpointURLEnvPrefix, "mainnet-archive"), configured)
		if _, err := resolveForkEndpoint("mainnet-archive"); err == nil {
			t.Errorf("malformed operator endpoint %q was accepted", configured)
		} else if strings.Contains(err.Error(), configured) && strings.TrimSpace(configured) != "" {
			t.Errorf("error echoed the configured endpoint: %v", err)
		}
	}
}

func TestForkEndpointTokenResolutionNeverLeaksIntoResults(t *testing.T) {
	endpoint := "https://rpc.example.test/v1/" + forkEndpointAPIKey
	configureForkEndpoint(t, "mainnet-archive", "mainnet-archive", endpoint)

	secrets := &operatorSecret{}
	argv := []string{"anvil", "--fork-url", operatorForkEndpointToken + "mainnet-archive"}
	if err := resolveOperatorEVMTokens(t.Context(), argv, "", secrets); err != nil {
		t.Fatalf("resolve operator tokens: %v", err)
	}
	if argv[2] != endpoint {
		t.Fatalf("argv = %q, want the resolved operator endpoint", argv)
	}

	result := secrets.scrub(NativeResult{
		Output: []byte("forking from " + endpoint + "\n"),
		Err:    fmt.Errorf("dial %s: connection refused (key %s)", endpoint, forkEndpointAPIKey),
	})
	for _, emitted := range []string{string(result.Output), result.Err.Error()} {
		if strings.Contains(emitted, forkEndpointAPIKey) || strings.Contains(emitted, endpoint) {
			t.Errorf("emitted result carries the operator endpoint: %q", emitted)
		}
	}
	if !strings.Contains(string(result.Output), operatorForkEndpointToken+"mainnet-archive") {
		t.Errorf("redacted output lost the endpoint alias: %q", result.Output)
	}
	// The timeout path is recognized by error identity, so it survives scrubbing.
	if timedOut := secrets.scrub(NativeResult{Err: context.DeadlineExceeded}); !errors.Is(timedOut.Err, context.DeadlineExceeded) {
		t.Errorf("scrubbing rewrote the timeout error: %v", timedOut.Err)
	}

	unknown := []string{"anvil", "--fork-url", operatorForkEndpointToken + "attacker-archive"}
	if err := resolveOperatorEVMTokens(t.Context(), unknown, "", secrets); err == nil {
		t.Fatal("unauthorized alias was resolved")
	}
	if unknown[2] != operatorForkEndpointToken+"attacker-archive" {
		t.Fatalf("failed resolution rewrote argv: %q", unknown)
	}
}

// fakeDevnet answers the two JSON-RPC calls the supervisor makes, so the
// supervisor's readiness and pinning logic is testable without anvil.
func fakeDevnet(t *testing.T, chainID, blockNumber int64, blockHash string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		var call struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &call)
		w.Header().Set("Content-Type", "application/json")
		switch call.Method {
		case "eth_chainId":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":"0x%x"}`, chainID)
		case "eth_getBlockByNumber":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x%x","hash":%q}}`, blockNumber, blockHash)
		default:
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"message":"unsupported method"}}`)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func devnetRequest(listenURL string, blockHash string) forkDevnetRequest {
	return forkDevnetRequest{
		Argv: []string{"/bin/sleep", "30"}, Alias: "mainnet-archive",
		ChainID: 1, BlockNumber: 21000000, BlockHash: blockHash, ListenURL: listenURL,
		Readiness: 5 * time.Second, MaxLog: 4096,
	}
}

func TestForkDevnetEmitsThePinnedReplayRecordAndTerminates(t *testing.T) {
	hash := "0x" + strings.Repeat("ab", 32)
	devnet := fakeDevnet(t, 1, 21000000, hash)
	started := time.Now()
	output, err := superviseForkDevnet(t.Context(), devnetRequest(devnet.URL, hash))
	if err != nil {
		t.Fatalf("supervise fork devnet: %v", err)
	}
	// The devnet argv sleeps for 30s: returning promptly is the evidence that
	// the supervisor terminated and reaped it instead of waiting it out.
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("devnet was not terminated: supervisor took %s", elapsed)
	}
	var record evmForkRecord
	if err := json.Unmarshal(output, &record); err != nil {
		t.Fatalf("fork record: %v", err)
	}
	if record.ChainID != 1 || record.ForkBlockNumber != 21000000 || record.ForkBlockHash != hash {
		t.Fatalf("fork record does not pin the replayed chain state: %+v", record)
	}
	if record.EndpointAlias != "mainnet-archive" || !isLoopbackDevnet(record.ListenURL) {
		t.Fatalf("fork record must record the alias and the loopback listener: %+v", record)
	}
	if _, err := (evmForkRecordAdapter{}).Normalize(Tool{Name: "anvil-fork"}, Target{Locator: "project"}, output, NewRedactor()); err != nil {
		t.Fatalf("emitted record is not consumable by the fork-record adapter: %v", err)
	}
}

func TestForkDevnetReadinessTimeoutIsAnError(t *testing.T) {
	argv, listenURL, err := loopbackDevnetArgv([]string{"anvil", "--host", "127.0.0.1", "--port", "8545"})
	if err != nil {
		t.Fatalf("loopback devnet argv: %v", err)
	}
	if argv[4] == "8545" || !strings.HasPrefix(listenURL, "http://127.0.0.1:") {
		t.Fatalf("argv %q was not pinned to a free loopback port (%s)", argv, listenURL)
	}
	if _, _, err := loopbackDevnetArgv([]string{"anvil", "--host", "0.0.0.0", "--port", "8545"}); err == nil {
		t.Fatal("a devnet bound outside loopback was accepted")
	}

	request := devnetRequest(listenURL, "0x"+strings.Repeat("ab", 32))
	request.Readiness = 300 * time.Millisecond
	started := time.Now()
	if _, err := superviseForkDevnet(t.Context(), request); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("a devnet that never became ready must be an error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("readiness wait was not bounded: %s", elapsed)
	}
}

func TestForkDevnetChainOrBlockMismatchIsAnError(t *testing.T) {
	pinned := "0x" + strings.Repeat("ab", 32)
	for name, devnet := range map[string]*httptest.Server{
		"chain id":   fakeDevnet(t, 8453, 21000000, pinned),
		"block":      fakeDevnet(t, 1, 20999999, pinned),
		"block hash": fakeDevnet(t, 1, 21000000, "0x"+strings.Repeat("cd", 32)),
	} {
		_, err := superviseForkDevnet(t.Context(), devnetRequest(devnet.URL, pinned))
		if err == nil {
			t.Errorf("%s mismatch was reported as a pass", name)
			continue
		}
		if !strings.Contains(err.Error(), "pinned") {
			t.Errorf("%s mismatch error must name the pinned request: %v", name, err)
		}
	}
}

func TestForkDevnetFailureRedactsTheOperatorEndpoint(t *testing.T) {
	endpoint := "https://rpc.example.test/v1/" + forkEndpointAPIKey
	secrets := &operatorSecret{}
	secrets.hide(endpoint, operatorForkEndpointToken+"mainnet-archive")
	_, listenURL, err := loopbackDevnetArgv([]string{"anvil", "--host", "127.0.0.1", "--port", "8545"})
	if err != nil {
		t.Fatal(err)
	}
	request := devnetRequest(listenURL, "0x"+strings.Repeat("ab", 32))
	// A devnet that echoes its fork URL and then never serves RPC: the failure
	// is reported, but the operator's credential is not.
	request.Argv = []string{"/bin/sh", "-c", "echo forking from " + endpoint + " >&2; sleep 30"}
	request.Readiness = 500 * time.Millisecond
	request.Secrets = secrets
	_, err = superviseForkDevnet(t.Context(), request)
	if err == nil {
		t.Fatal("expected a readiness failure")
	}
	if strings.Contains(err.Error(), forkEndpointAPIKey) || strings.Contains(err.Error(), endpoint) {
		t.Fatalf("devnet failure echoed the operator endpoint: %v", err)
	}
}

func gitOrSkip(t *testing.T, repository string, arguments ...string) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed in this environment")
	}
	output, err := runGit(t.Context(), git, repository, arguments...)
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return output
}

func TestUpstreamRevisionMustBeTheExactPinnedCommit(t *testing.T) {
	mirror, repository := t.TempDir(), t.TempDir()
	gitOrSkip(t, mirror, "init", "--quiet", "--initial-branch", "main")
	gitOrSkip(t, mirror, "config", "user.email", "operator@example.test")
	gitOrSkip(t, mirror, "config", "user.name", "operator")
	if err := os.WriteFile(filepath.Join(mirror, "evm.go"), []byte("package vm\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOrSkip(t, mirror, "add", "evm.go")
	gitOrSkip(t, mirror, "commit", "--quiet", "-m", "upstream")
	revision := gitOrSkip(t, mirror, "rev-parse", "HEAD")
	gitOrSkip(t, repository, "init", "--quiet", "--initial-branch", "main")

	t.Setenv(operatorConfigurationName(evmUpstreamMirrorEnvPrefix, "go-ethereum"), mirror)
	resolved, err := materializeUpstreamRevision(t.Context(), "go-ethereum@"+revision, repository)
	if err != nil || resolved != revision {
		t.Fatalf("pinned upstream revision resolved to %q (%v)", resolved, err)
	}
	if got := gitOrSkip(t, repository, "cat-file", "-t", revision); got != "commit" {
		t.Fatalf("mirror fetch did not materialize the commit locally: %q", got)
	}

	// A revision the mirror cannot produce must fail closed, never diff against
	// whatever ref happened to be fetched.
	for name, reference := range map[string]string{
		"revision mismatch":  "go-ethereum@" + strings.Repeat("ab", 20),
		"unreviewed name":    "attacker-fork@" + revision,
		"unpinned revision":  "go-ethereum@main",
		"missing separator":  "go-ethereum",
		"unconfigured chain": "reth@" + revision,
	} {
		if _, err := materializeUpstreamRevision(t.Context(), reference, repository); err == nil {
			t.Errorf("%s (%q) was accepted", name, reference)
		}
	}
	t.Setenv(operatorConfigurationName(evmUpstreamMirrorEnvPrefix, "go-ethereum"), "http://mirror.example.test/geth.git")
	if _, err := materializeUpstreamRevision(t.Context(), "go-ethereum@"+revision, repository); err == nil {
		t.Error("a plaintext mirror URL was accepted")
	}
}

const mutationFixture = `// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

contract Vault {
    address owner;

    function withdraw(uint256 amount) external {
        require(msg.sender == owner);
        require(amount == 0);
    }
}
`

func stageMutationProject(t *testing.T) string {
	t.Helper()
	staged := t.TempDir()
	for path, content := range map[string]string{
		"src/Vault.sol":     mutationFixture,
		"test/Vault.t.sol":  "contract VaultTest { function t() public { require(1 == 1); } }\n",
		"lib/forge/Std.sol": "contract Std { function s() public { require(2 == 2); } }\n",
	} {
		full := filepath.Join(staged, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return staged
}

// mutationCampaignFixture runs one campaign over a staged project whose
// harness checks ownership but never the amount, so exactly one mutant
// survives: the assertion that cannot fail.
func mutationCampaignFixture(t *testing.T) (staged string, document []byte, survivors int, roots []string) {
	t.Helper()
	staged = stageMutationProject(t)
	lcov := "TN:\nSF:" + filepath.Join(staged, "src", "Vault.sol") + "\nDA:8,3\nDA:9,0\nend_of_record\n"
	run := func(_ context.Context, root string) (int, []byte, error) {
		roots = append(roots, root)
		if root == staged {
			return 0, []byte(lcov), nil
		}
		source, err := os.ReadFile(filepath.Join(root, "src", "Vault.sol"))
		if err != nil {
			return 0, nil, err
		}
		// The harness checks ownership but never the amount, so only the
		// ownership mutant is killed.
		if strings.Contains(string(source), "msg.sender != owner") {
			return 1, nil, nil
		}
		return 0, nil, nil
	}

	document, survivors, err := runMutationCampaign(t.Context(), staged, "assertion-negation", run)
	if err != nil {
		t.Fatalf("mutation campaign: %v", err)
	}
	return staged, document, survivors, roots
}

func TestMutationCampaignRunsEveryMutantOutsideTheStagedTree(t *testing.T) {
	staged, _, survivors, roots := mutationCampaignFixture(t)
	if survivors != 1 {
		t.Fatalf("survivors = %d, want 1", survivors)
	}
	if len(roots) != 3 || roots[0] != staged {
		t.Fatalf("campaign ran %v, want one baseline plus one run per mutant", roots)
	}
	for _, root := range roots[1:] {
		if root == staged {
			t.Fatal("a mutant was run against the staged original")
		}
	}
	if source, err := os.ReadFile(filepath.Join(staged, "src", "Vault.sol")); err != nil || string(source) != mutationFixture {
		t.Fatalf("staged sources were mutated in place: %q (%v)", source, err)
	}
}

func TestMutationCampaignReportsSurvivorsAsUnfailableAssertions(t *testing.T) {
	staged, document, _, _ := mutationCampaignFixture(t)

	var report mutationReport
	if err := json.Unmarshal(document, &report); err != nil {
		t.Fatalf("mutation document: %v", err)
	}
	if len(report.Coverage) != 1 || report.Coverage[0].File != "src/Vault.sol" || report.Coverage[0].LinesTotal != 2 || report.Coverage[0].LinesHit != 1 {
		t.Fatalf("coverage = %+v", report.Coverage)
	}
	if len(report.Mutants) != 2 {
		t.Fatalf("mutants = %+v", report.Mutants)
	}
	statuses := map[int]string{}
	for _, mutant := range report.Mutants {
		if mutant.File != "src/Vault.sol" || mutant.Operator != "assertion-negation" {
			t.Fatalf("mutant %+v is not attributed to the mutated contract", mutant)
		}
		statuses[mutant.Line] = mutant.Status
	}
	if statuses[8] != "killed" || statuses[9] != "survived" {
		t.Fatalf("per-mutant verdicts = %v", statuses)
	}

	// The document must raise the "assertions cannot fail" finding downstream.
	records, err := (forgeCoverageMutationAdapter{}).Normalize(Tool{Name: "forge-coverage-mutation"}, Target{Locator: staged}, document, NewRedactor())
	if err != nil {
		t.Fatalf("normalize mutation document: %v", err)
	}
	findings := 0
	for _, record := range records {
		if !record.Examined && !record.Skipped && !record.Uncovered {
			findings++
			if record.Record.RuleID != "mutation-survived" || record.Record.StartLine != 9 {
				t.Fatalf("survivor finding = %+v", record.Record)
			}
		}
	}
	if findings != 1 {
		t.Fatalf("findings = %d, want exactly the surviving mutant", findings)
	}
}

func TestMutationCampaignRefusesAFailingBaselineAndUnusableOperators(t *testing.T) {
	staged := stageMutationProject(t)
	failing := func(_ context.Context, _ string) (int, []byte, error) { return 1, nil, nil }
	if _, _, err := runMutationCampaign(t.Context(), staged, "assertion-negation", failing); err == nil {
		t.Fatal("mutants were judged against a failing baseline")
	}
	clean := func(_ context.Context, _ string) (int, []byte, error) { return 0, nil, nil }
	if _, _, err := runMutationCampaign(t.Context(), staged, "return-value-swap", clean); err == nil {
		t.Fatal("an operator with no applicable site reported a clean campaign")
	}
}

func TestMutationSitesCoverOnlyProjectContracts(t *testing.T) {
	staged := stageMutationProject(t)
	for operator, want := range map[string]int{"assertion-negation": 2, "require-removal": 2, "boundary-shift": 0, "return-value-swap": 0} {
		sites, err := mutationSites(staged, operator, maxMutants)
		if err != nil {
			t.Fatalf("%s: %v", operator, err)
		}
		if len(sites) != want {
			t.Errorf("%s produced %d sites, want %d: %+v", operator, len(sites), want, sites)
		}
		for _, site := range sites {
			if !strings.HasPrefix(site.File, "src/") {
				t.Errorf("%s mutated %s: only the project's own contracts may be mutated", operator, site.File)
			}
		}
	}
	if sites, err := mutationSites(staged, "assertion-negation", 1); err != nil || len(sites) != 1 {
		t.Fatalf("mutant budget was not enforced: %+v (%v)", sites, err)
	}
	// pragma and comment lines carry comparison operators that are not code.
	for _, line := range []string{"pragma solidity >=0.8.20;", "// require(a == b);", "    * require(a == b);"} {
		if mutated, ok := mutateSolidityLine("boundary-shift", line); ok {
			t.Errorf("non-code line %q was mutated to %q", line, mutated)
		}
		if mutated, ok := mutateSolidityLine("assertion-negation", line); ok {
			t.Errorf("non-code line %q was mutated to %q", line, mutated)
		}
	}
}

// The executor itself must resolve the operator token and scrub the resolved
// endpoint, not just the helpers it is built from.
func TestProcessSandboxResolvesAndRedactsTheForkEndpoint(t *testing.T) {
	endpoint := "https://rpc.example.test/v1/" + forkEndpointAPIKey
	configureForkEndpoint(t, "mainnet-archive", "mainnet-archive", endpoint)
	request := ExecutionRequest{
		Invocation: Invocation{
			Argv:    []string{"/usr/bin/printf", "%s", operatorForkEndpointToken + "mainnet-archive"},
			Budgets: Budgets{Timeout: 5 * time.Second, MaxOutputSize: 4096},
		},
		Config: RunConfig{Target: Target{Locator: "fixture"}},
	}
	result := (ProcessSandbox{}).Execute(t.Context(), request)
	if result.Err != nil {
		t.Fatalf("result = %+v", result)
	}
	if strings.Contains(string(result.Output), forkEndpointAPIKey) || strings.Contains(string(result.Output), endpoint) {
		t.Fatalf("executor emitted the operator endpoint: %q", result.Output)
	}
	if string(result.Output) != operatorForkEndpointToken+"mainnet-archive" {
		t.Fatalf("output = %q, want the endpoint alias in place of the resolved URL", result.Output)
	}

	request.Invocation.Argv[2] = operatorForkEndpointToken + "attacker-archive"
	if failed := (ProcessSandbox{}).Execute(t.Context(), request); failed.Err == nil || failed.ExitCode != -1 {
		t.Fatalf("unauthorized alias ran anyway: %+v", failed)
	}
}
