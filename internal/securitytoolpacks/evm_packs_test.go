package securitytoolpacks

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// evmPackNames are the packs added by the EVM verification toolchain.
var evmPackNames = []string{"anvil-fork", "forge-fork-test", "medusa", "forge-coverage-mutation", "upstream-fork-diff"}

func evmTestManifest(t *testing.T, enable ...string) Manifest {
	t.Helper()
	manifest := DefaultManifest(sha256Digest([]byte("evm-pack-fixture")), nil)
	for i := range manifest.Tools {
		if slices.Contains(enable, manifest.Tools[i].Name) {
			manifest.Tools[i].Enabled = true
			manifest.Tools[i].DisabledReason = ""
		}
	}
	return manifest
}

func evmTestRegistry(t *testing.T, enable ...string) *Registry {
	t.Helper()
	registry, err := NewRegistry(evmTestManifest(t, enable...))
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	return registry
}

func evmPack(t *testing.T, name string) Tool {
	t.Helper()
	registry := evmTestRegistry(t)
	tool, ok := registry.Tool(name)
	if !ok {
		t.Fatalf("pack %q is not registered", name)
	}
	return tool
}

// The EVM packs are reviewed contracts whose executor stage is not wired yet.
// They must stay catalog-only with a reason that names the missing capability,
// so no caller can be told a run would work.
func TestEVMPacksAreRegisteredAsReviewedCatalogEntries(t *testing.T) {
	adapters := DefaultAdapters()
	for _, name := range evmPackNames {
		tool := evmPack(t, name)
		if tool.Domain != DomainBlockchain {
			t.Errorf("%s domain = %q, want %q", name, tool.Domain, DomainBlockchain)
		}
		if tool.Enabled {
			t.Errorf("%s must remain catalog-only until its executor stage exists", name)
		}
		if !strings.HasPrefix(tool.DisabledReason, "catalog-only: ") {
			t.Errorf("%s disabled reason must name the missing capability, got %q", name, tool.DisabledReason)
		}
		if _, ok := adapters[tool.Adapter]; !ok {
			t.Errorf("%s adapter %q is not registered", name, tool.Adapter)
		}
		if tool.OutputMediaType == "" || len(tool.TargetTypes) == 0 || len(tool.Invocation) == 0 {
			t.Errorf("%s has an incomplete execution contract: %+v", name, tool)
		}
		if tool.Requirements.Privilege != "unprivileged" {
			t.Errorf("%s privilege = %q, want unprivileged", name, tool.Requirements.Privilege)
		}
		if tool.Budgets.Concurrency != 1 || tool.Budgets.Timeout <= 0 || tool.Budgets.MaxOutputSize <= 0 {
			t.Errorf("%s budgets are not bounded to a single deterministic run: %+v", name, tool.Budgets)
		}
	}
	// A disabled pack must not be reachable even with a fully valid request.
	registry := evmTestRegistry(t)
	pin := sha256Digest([]byte("target"))
	_, _, err := registry.BuildInvocation(RunConfig{
		Tool:   "anvil-fork",
		Target: Target{Type: "foundry_project", Locator: "project", Revision: "v1", Digest: pin, MediaType: "application/vnd.gratefulagents.foundry-security-project.v1+directory"},
	})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled pack built an invocation: %v", err)
	}
}

func TestEVMPackTargetTypesAndAdapters(t *testing.T) {
	want := map[string]struct{ target, adapter, media string }{
		"anvil-fork":              {"foundry_project", "evm-fork-record", "application/json"},
		"forge-fork-test":         {"foundry_project", "forge-json", "application/json"},
		"medusa":                  {"solidity_project", "medusa-console", "text/plain"},
		"forge-coverage-mutation": {"foundry_project", "forge-mutation-json", "application/json"},
		"upstream-fork-diff":      {"git_repository", "git-divergence", "text/plain"},
	}
	for name, expected := range want {
		tool := evmPack(t, name)
		if !slices.Equal(tool.TargetTypes, []string{expected.target}) {
			t.Errorf("%s target types = %v, want [%s]", name, tool.TargetTypes, expected.target)
		}
		if tool.Adapter != expected.adapter || tool.OutputMediaType != expected.media {
			t.Errorf("%s adapter/media = %s/%s, want %s/%s", name, tool.Adapter, tool.OutputMediaType, expected.adapter, expected.media)
		}
	}
}

// Every fork run must be replayable from its own record, so chain id, fork
// block number, and fork block hash are required typed arguments.
func TestEVMForkPacksRequirePinnedChainState(t *testing.T) {
	t.Setenv(evmForkEndpointsEnv, "mainnet-archive,base-archive")
	wantTypes := map[string]string{
		"fork_endpoint":     "enum",
		"chain_id":          "integer",
		"fork_block_number": "integer",
		"fork_block_hash":   "evm_block_hash",
	}
	for _, name := range evmForkPacks {
		tool := evmPack(t, name)
		seen := map[string]Argument{}
		for _, argument := range tool.Arguments {
			seen[argument.Name] = argument
		}
		for argumentName, argumentType := range wantTypes {
			argument, ok := seen[argumentName]
			if !ok {
				t.Fatalf("%s is missing pinning argument %q", name, argumentName)
			}
			if !argument.Required {
				t.Errorf("%s argument %q must be required", name, argumentName)
			}
			if argument.Type != argumentType {
				t.Errorf("%s argument %q type = %q, want %q", name, argumentName, argument.Type, argumentType)
			}
		}
		if !slices.Equal(seen["fork_endpoint"].Enum, []string{"base-archive", "mainnet-archive"}) {
			t.Errorf("%s fork endpoint enum = %v, want the operator-authorized aliases", name, seen["fork_endpoint"].Enum)
		}
		argv := strings.Join(tool.Invocation, " ")
		for _, token := range []string{operatorForkEndpointToken + "{{fork_endpoint}}", "{{fork_block_number}}", "{{chain_id}}"} {
			if !strings.Contains(argv, token) {
				t.Errorf("%s argv %q does not carry %q", name, argv, token)
			}
		}
	}
}

func TestEVMForkEndpointComesFromOperatorConfigurationOnly(t *testing.T) {
	t.Setenv(evmForkEndpointsEnv, "")
	tool := evmPack(t, "forge-fork-test")
	for _, argument := range tool.Arguments {
		if argument.Name == "fork_endpoint" && len(argument.Enum) != 0 {
			t.Fatalf("unconfigured operator endpoints produced aliases %v", argument.Enum)
		}
	}
	registry := evmTestRegistry(t, "forge-fork-test")
	seed := int64(7)
	_, _, err := registry.BuildInvocation(forkRunConfig(map[string]string{
		"fork_endpoint":     "mainnet-archive",
		"chain_id":          "1",
		"fork_block_number": "21000000",
		"fork_block_hash":   "0x" + strings.Repeat("ab", 32),
	}, &seed))
	if err == nil || !strings.Contains(err.Error(), "must be one of") {
		t.Fatalf("unconfigured endpoint accepted an alias: %v", err)
	}
}

func forkRunConfig(arguments map[string]string, seed *int64) RunConfig {
	return RunConfig{
		Tool:      "forge-fork-test",
		Target:    Target{Type: "foundry_project", Locator: "project", Revision: "v1", Digest: sha256Digest([]byte("target")), MediaType: "application/vnd.gratefulagents.foundry-security-project.v1+directory"},
		Arguments: arguments,
		Seed:      seed,
	}
}

func TestForkInvocationResolvesOnlyOperatorEndpointAliases(t *testing.T) {
	t.Setenv(evmForkEndpointsEnv, "mainnet-archive")
	registry := evmTestRegistry(t, "forge-fork-test")
	seed := int64(11)
	valid := map[string]string{
		"fork_endpoint":     "mainnet-archive",
		"chain_id":          "1",
		"fork_block_number": "21000000",
		"fork_block_hash":   "0x" + strings.Repeat("ab", 32),
	}
	invocation, _, err := registry.BuildInvocation(forkRunConfig(valid, &seed))
	if err != nil {
		t.Fatalf("pinned fork request: %v", err)
	}
	if !slices.Contains(invocation.Argv, operatorForkEndpointToken+"mainnet-archive") {
		t.Fatalf("argv %v does not carry the operator endpoint alias", invocation.Argv)
	}
	for _, token := range invocation.Argv {
		if strings.Contains(token, "://") {
			t.Fatalf("argv token %q carries a URL", token)
		}
	}
	if !slices.Contains(invocation.Argv, "21000000") || !slices.Contains(invocation.Argv, "1") {
		t.Fatalf("argv %v does not pin the forked chain state", invocation.Argv)
	}

	rejected := map[string]map[string]string{
		"model-supplied endpoint URL": {"fork_endpoint": "https://mainnet.example.test"},
		"unpinned chain id":           {"chain_id": ""},
		"zero chain id":               {"chain_id": "0"},
		"negative block":              {"fork_block_number": "-1"},
		"short block hash":            {"fork_block_hash": "0xdeadbeef"},
		"uppercase block hash":        {"fork_block_hash": "0x" + strings.Repeat("AB", 32)},
	}
	for name, override := range rejected {
		arguments := maps.Clone(valid)
		for key, value := range override {
			if value == "" {
				delete(arguments, key)
				continue
			}
			arguments[key] = value
		}
		if _, _, err := registry.BuildInvocation(forkRunConfig(arguments, &seed)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, _, err := registry.BuildInvocation(forkRunConfig(valid, nil)); err == nil {
		t.Error("fork test without an explicit seed was accepted")
	}
}

func TestEVMPackExitCodeMapsAreComplete(t *testing.T) {
	want := map[string]map[int]Status{
		"anvil-fork":              {0: StatusPass, 1: StatusError, 2: StatusError, 124: StatusTimeout},
		"forge-fork-test":         {0: StatusPass, 1: StatusFindings, 2: StatusError, 124: StatusTimeout},
		"medusa":                  {0: StatusPass, 1: StatusFindings, 2: StatusError, 124: StatusTimeout},
		"forge-coverage-mutation": {0: StatusPass, 1: StatusFindings, 2: StatusError, 124: StatusTimeout},
		"upstream-fork-diff":      {0: StatusPass, 1: StatusFindings, 124: StatusTimeout, 128: StatusError, 129: StatusError},
	}
	for name, expected := range want {
		tool := evmPack(t, name)
		if len(tool.ExitCodes) != len(expected) {
			t.Errorf("%s exit codes = %v, want %v", name, tool.ExitCodes, expected)
			continue
		}
		for code, status := range expected {
			if tool.ExitCodes[code] != status {
				t.Errorf("%s exit code %d = %q, want %q", name, code, tool.ExitCodes[code], status)
			}
		}
	}
}

// Fork and build egress is either the operator-authorized endpoint or compiler
// resolution, never a model-chosen remote target, so these packs declare a
// network requirement and are exempt from model-supplied scope.
func TestEVMPacksDeclareNetworkRequirementsWithoutModelScope(t *testing.T) {
	for _, name := range evmPackNames {
		tool := evmPack(t, name)
		if !tool.Requirements.Network {
			t.Errorf("%s must declare its outbound requirement", name)
		}
		if !IsEVMBuildTool(name) {
			t.Errorf("%s must be treated as a staged-content EVM tool so no model-supplied scope authorizes it", name)
		}
	}
	// medusa is a stateful fuzzer without an upstream seed flag, so it must not
	// claim idempotency.
	if evmPack(t, "medusa").Idempotent {
		t.Error("medusa must not be marked idempotent")
	}
	for _, name := range []string{"forge-fork-test", "forge-coverage-mutation"} {
		if !evmPack(t, name).SeedSupported {
			t.Errorf("%s must require an explicit seed", name)
		}
	}
}

// No pack argument may carry a free-form command, path, or URL: every argument
// is an enum, a bounded number, or a validated identifier list.
func TestEVMPackArgumentsRejectFreeFormCommandsAndURLs(t *testing.T) {
	t.Setenv(evmForkEndpointsEnv, "mainnet-archive")
	allowed := []string{"enum", "integer", "evm_block_hash", "git_revision", "string"}
	for _, name := range evmPackNames {
		tool := evmPack(t, name)
		for _, argument := range tool.Arguments {
			if !slices.Contains(allowed, argument.Type) {
				t.Errorf("%s argument %q has free-form type %q", name, argument.Name, argument.Type)
			}
			// Only the fork endpoint enum may be empty: its values come from
			// operator configuration rather than from the compiled catalog.
			if argument.Type == "enum" && len(argument.Enum) == 0 && argument.Name != "fork_endpoint" {
				t.Errorf("%s enum argument %q has no reviewed values", name, argument.Name)
			}
		}
		for _, token := range tool.Invocation {
			if strings.Contains(token, "://") {
				t.Errorf("%s argv token %q embeds a URL", name, token)
			}
			if strings.HasPrefix(token, "@") && !strings.HasPrefix(token, operatorForkEndpointToken) && !strings.HasPrefix(token, operatorUpstreamToken) {
				t.Errorf("%s argv token %q is not an operator-resolved reference", name, token)
			}
		}
	}

	registry := evmTestRegistry(t, "medusa", "upstream-fork-diff")
	medusaTarget := Target{Type: "solidity_project", Locator: "project", Revision: "v1", Digest: sha256Digest([]byte("target")), MediaType: "application/vnd.gratefulagents.solidity-project.v1+directory"}
	for _, value := range []string{"https://example.test/x", "Vault; rm -rf /", "../../etc/passwd", "Vault --config /etc/shadow"} {
		cfg := RunConfig{Tool: "medusa", Target: medusaTarget, Arguments: map[string]string{"target_contracts": value}}
		if _, _, err := registry.BuildInvocation(cfg); err == nil {
			t.Errorf("medusa accepted free-form target_contracts %q", value)
		}
	}
	valid := RunConfig{Tool: "medusa", Target: medusaTarget, Arguments: map[string]string{"target_contracts": "VaultHarness,TokenHarness"}}
	if _, _, err := registry.BuildInvocation(valid); err != nil {
		t.Fatalf("valid medusa contract list: %v", err)
	}

	gitTarget := Target{Type: "git_repository", Locator: "repo", Revision: "v1", Digest: sha256Digest([]byte("target")), MediaType: "application/vnd.gratefulagents.git-repository.v1+directory"}
	revision := strings.Repeat("a1b2c3d4", 5)
	invocation, _, err := registry.BuildInvocation(RunConfig{Tool: "upstream-fork-diff", Target: gitTarget, Arguments: map[string]string{"upstream": "go-ethereum", "upstream_revision": revision}})
	if err != nil {
		t.Fatalf("valid upstream diff: %v", err)
	}
	if !slices.Contains(invocation.Argv, operatorUpstreamToken+"go-ethereum@"+revision) {
		t.Fatalf("argv %v does not pin the reviewed upstream revision", invocation.Argv)
	}
	for _, arguments := range []map[string]string{
		{"upstream": "https://github.com/attacker/geth", "upstream_revision": revision},
		{"upstream": "go-ethereum", "upstream_revision": "main"},
		{"upstream": "go-ethereum", "upstream_revision": strings.ToUpper(revision)},
		{"upstream": "go-ethereum"},
	} {
		if _, _, err := registry.BuildInvocation(RunConfig{Tool: "upstream-fork-diff", Target: gitTarget, Arguments: arguments}); err == nil {
			t.Errorf("upstream diff accepted %v", arguments)
		}
	}
}

func TestForkRecordAdapterRequiresReplayablePinning(t *testing.T) {
	tool := evmPack(t, "anvil-fork")
	target := Target{Locator: "project"}
	record := `{"chain_id":1,"fork_block_number":21000000,"fork_block_hash":"0x` + strings.Repeat("ab", 32) + `","endpoint_alias":"mainnet-archive","listen_url":"http://127.0.0.1:8545"}`
	records, err := (evmForkRecordAdapter{}).Normalize(tool, target, []byte(record), NewRedactor())
	if err != nil {
		t.Fatalf("fork record: %v", err)
	}
	if len(records) != 1 || !records[0].Examined || !strings.HasPrefix(records[0].Asset, "chain:1@21000000/0x") {
		t.Fatalf("fork record coverage = %+v", records)
	}
	for _, incomplete := range []string{
		`{"fork_block_number":21000000,"fork_block_hash":"0x` + strings.Repeat("ab", 32) + `"}`,
		`{"chain_id":1,"fork_block_hash":"0x` + strings.Repeat("ab", 32) + `","fork_block_number":-1}`,
		`{"chain_id":1,"fork_block_number":21000000,"fork_block_hash":"0xdead"}`,
	} {
		if _, err := (evmForkRecordAdapter{}).Normalize(tool, target, []byte(incomplete), NewRedactor()); err == nil {
			t.Errorf("unpinned fork record accepted: %s", incomplete)
		}
	}
	exposed := strings.Replace(record, "http://127.0.0.1:8545", "http://0.0.0.0:8545", 1)
	records, err = (evmForkRecordAdapter{}).Normalize(tool, target, []byte(exposed), NewRedactor())
	if err != nil {
		t.Fatalf("exposed devnet: %v", err)
	}
	if len(records) != 2 || records[1].Record.RuleID != "fork-devnet-not-local" || records[1].Record.Severity != "critical" {
		t.Fatalf("a non-loopback devnet must be reported: %+v", records)
	}
}

func TestForgeJSONAdapterEmitsPerAssertionResults(t *testing.T) {
	tool := evmPack(t, "forge-fork-test")
	native := `{
	  "test/Vault.t.sol:VaultTest": {
	    "test_results": {
	      "testWithdrawSolvent()": {"status": "Success", "reason": null, "decoded_logs": []},
	      "testOracleFresh()": {"status": "Failure", "reason": "assertion failed: stale oracle", "counterexample": {"calldata": "0x1234"}, "decoded_logs": ["price 0"]},
	      "testSkipped()": {"status": "Skipped", "reason": null, "decoded_logs": []}
	    }
	  }
	}`
	records, err := (forgeJSONAdapter{}).Normalize(tool, Target{Locator: "project"}, []byte(native), NewRedactor())
	if err != nil {
		t.Fatalf("forge json: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %+v", records)
	}
	var findings, examined, skipped int
	for _, record := range records {
		switch {
		case record.Examined:
			examined++
		case record.Skipped:
			skipped++
		default:
			findings++
			if record.Record.RuleID != "forge-fork-assertion-failed" || !strings.Contains(record.Record.RawEvidence, "0x1234") {
				t.Errorf("failure record = %+v", record.Record)
			}
			if record.Record.Symbol != "testOracleFresh()" {
				t.Errorf("failure must name the assertion, got %q", record.Record.Symbol)
			}
		}
	}
	if findings != 1 || examined != 1 || skipped != 1 {
		t.Fatalf("findings=%d examined=%d skipped=%d", findings, examined, skipped)
	}
	if _, err := (forgeJSONAdapter{}).Normalize(tool, Target{}, []byte("not json"), NewRedactor()); err == nil {
		t.Error("malformed forge output was accepted")
	}
}

func TestCoverageMutationAdapterReportsSurvivingMutants(t *testing.T) {
	tool := evmPack(t, "forge-coverage-mutation")
	native := `{
	  "coverage": [
	    {"file": "src/Vault.sol", "lines_total": 40, "lines_hit": 38},
	    {"file": "src/Oracle.sol", "lines_total": 12, "lines_hit": 0}
	  ],
	  "mutants": [
	    {"file": "src/Vault.sol", "line": 88, "operator": "assertion-negation", "status": "survived"},
	    {"file": "src/Vault.sol", "line": 91, "operator": "assertion-negation", "status": "killed"}
	  ]
	}`
	records, err := (forgeCoverageMutationAdapter{}).Normalize(tool, Target{Locator: "project"}, []byte(native), NewRedactor())
	if err != nil {
		t.Fatalf("coverage/mutation: %v", err)
	}
	var uncovered, examined int
	findings := []securityRecord{}
	for _, record := range records {
		switch {
		case record.Uncovered:
			uncovered++
		case record.Examined:
			examined++
		default:
			findings = append(findings, record)
		}
	}
	if uncovered != 1 || examined != 1 || len(findings) != 1 {
		t.Fatalf("uncovered=%d examined=%d findings=%+v", uncovered, examined, findings)
	}
	if findings[0].Record.RuleID != "mutation-survived" || findings[0].Record.StartLine != 88 {
		t.Fatalf("surviving mutant record = %+v", findings[0].Record)
	}
}

func TestGitDivergenceAdapterEmitsDivergenceSet(t *testing.T) {
	tool := evmPack(t, "upstream-fork-diff")
	native := "12\t3\tcore/vm/evm.go\n0\t7\tcore/state_transition.go\n-\t-\tbuild/logo.png\n"
	records, err := (gitDivergenceAdapter{}).Normalize(tool, Target{Locator: "repo"}, []byte(native), NewRedactor())
	if err != nil {
		t.Fatalf("git divergence: %v", err)
	}
	if len(records) != 4 || !records[0].Examined || records[0].Asset != "repo" {
		t.Fatalf("records = %+v", records)
	}
	if records[1].Record.RuleID != "upstream-divergence" || records[1].Record.FilePath != "core/vm/evm.go" ||
		records[1].Record.Extra["added_lines"] != "12" || records[1].Record.Extra["deleted_lines"] != "3" {
		t.Fatalf("divergence record = %+v", records[1].Record)
	}
	if _, err := (gitDivergenceAdapter{}).Normalize(tool, Target{Locator: "repo"}, []byte("x\ty\tz\n"), NewRedactor()); err == nil {
		t.Error("malformed numstat output was accepted")
	}
}

func TestMedusaAdapterReportsViolatedProperties(t *testing.T) {
	tool := evmPack(t, "medusa")
	native := "\x1b[31m[FAILED] \x1b[0mAssertion Test: VaultHarness.assertSolvency()\n" +
		"Test for method \"VaultHarness.assertSolvency()\" resulted in an assertion failure after the following call sequence:\n" +
		"[PASSED] Assertion Test: VaultHarness.assertNoFreeMint()\n"
	records, err := (medusaConsoleAdapter{}).Normalize(tool, Target{Locator: "project"}, []byte(native), NewRedactor())
	if err != nil {
		t.Fatalf("medusa console: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %+v", records)
	}
	if records[0].Record.RuleID != "medusa-property-violated" || records[0].Record.Symbol != "VaultHarness.assertSolvency()" {
		t.Fatalf("violation record = %+v", records[0].Record)
	}
	if !records[1].Examined || records[1].Asset != "VaultHarness.assertNoFreeMint()" {
		t.Fatalf("passing property must be coverage: %+v", records[1])
	}
}
