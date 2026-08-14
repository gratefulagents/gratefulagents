package securitytoolpacks

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/gratefulagents/gratefulagents/internal/security"
)

// EVM verification packs observe deployed and forked chain state instead of
// source alone. Two rules hold for every pack in this file:
//
//   - A fork endpoint is never a model-supplied string. The typed argument is
//     an operator-authorized alias; argv carries only
//     operatorForkEndpointToken+alias, which the execution layer resolves from
//     operator configuration. A run whose operator configured no alias cannot
//     name an endpoint at all.
//   - Chain id, fork block number, and fork block hash are required typed
//     arguments, so the recorded RunConfig alone is enough to replay the run
//     against the same chain state.
const (
	operatorForkEndpointToken = "@operator/evm-fork-endpoint/"
	operatorUpstreamToken     = "@operator/evm-upstream/"

	// evmForkEndpointsEnv is set by the operator (never by a model or a scan
	// request) to the comma-separated aliases a fork pack may use.
	evmForkEndpointsEnv = "GA_SECURITY_EVM_FORK_ENDPOINTS"
)

var (
	evmForkEndpointAliasPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	evmBlockHashPattern         = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
	gitRevisionPattern          = regexp.MustCompile(`^[0-9a-f]{40}$`)
	solidityIdentifierPattern   = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]{0,127}$`)
	ansiEscape                  = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
)

// evmForkPacks are the packs whose execution reaches an operator-authorized
// fork endpoint and therefore must pin the forked chain state.
var evmForkPacks = []string{"anvil-fork", "forge-fork-test", "chain-read", "deployed-bytecode-diff"}

// evmVerificationPacks are all packs defined in this file. They operate on
// staged EVM content, so their egress is either the operator-authorized fork
// endpoint or compiler/dependency resolution, never a model-chosen target.
var evmVerificationPacks = []string{"anvil-fork", "forge-fork-test", "medusa", "forge-coverage-mutation", "upstream-fork-diff", "chain-read", "deployed-bytecode-diff"}

// evmUpstreams are the reviewed upstream projects a fork target may be
// compared against. The value is an identifier, never a URL: the execution
// layer maps it to the reviewed upstream mirror.
var evmUpstreams = []string{"go-ethereum", "reth", "openzeppelin-contracts", "op-stack"}

// evmForkEndpointAliases returns the operator-authorized endpoint aliases.
// An unset or malformed configuration authorizes nothing, which leaves every
// fork pack without a usable endpoint value.
func evmForkEndpointAliases() []string {
	aliases := []string{}
	for entry := range strings.SplitSeq(os.Getenv(evmForkEndpointsEnv), ",") {
		alias := strings.TrimSpace(entry)
		if evmForkEndpointAliasPattern.MatchString(alias) && !slices.Contains(aliases, alias) {
			aliases = append(aliases, alias)
		}
	}
	slices.Sort(aliases)
	return aliases
}

// evmForkPinningArguments is the typed pinning contract shared by every pack
// that forks chain state.
func evmForkPinningArguments() []Argument {
	return []Argument{
		{Name: "fork_endpoint", Type: "enum", Required: true, Enum: evmForkEndpointAliases()},
		{Name: "chain_id", Type: "integer", Required: true},
		{Name: "fork_block_number", Type: "integer", Required: true},
		{Name: "fork_block_hash", Type: "evm_block_hash", Required: true},
	}
}

// validateEVMPackArguments applies the per-pack bounds that the generic typed
// argument check cannot express.
func validateEVMPackArguments(tool Tool, cfg RunConfig) error {
	if slices.Contains(evmForkPacks, tool.Name) {
		chainID, err := strconv.ParseInt(cfg.Arguments["chain_id"], 10, 64)
		if err != nil || chainID < 1 {
			return fmt.Errorf("argument %q must be a positive EIP-155 chain id", "chain_id")
		}
		block, err := strconv.ParseInt(cfg.Arguments["fork_block_number"], 10, 64)
		if err != nil || block < 0 {
			return fmt.Errorf("argument %q must be a non-negative block height", "fork_block_number")
		}
		if len(tool.Arguments) > 0 && len(argumentEnum(tool, "fork_endpoint")) == 0 {
			return fmt.Errorf("tool %s has no operator-authorized fork endpoint configured", tool.Name)
		}
	}
	if tool.Name == "chain-read" {
		if err := validateEVMAddressList(cfg.Arguments["addresses"]); err != nil {
			return err
		}
	}
	if tool.Name == "medusa" {
		if err := validateSolidityContractList(cfg.Arguments["target_contracts"]); err != nil {
			return err
		}
	}
	return nil
}

func argumentEnum(tool Tool, name string) []string {
	for _, argument := range tool.Arguments {
		if argument.Name == name {
			return argument.Enum
		}
	}
	return nil
}

// validateSolidityContractList keeps the fuzzer's target selection a list of
// Solidity identifiers rather than a free-form flag payload.
func validateSolidityContractList(value string) error {
	if value == "" {
		return fmt.Errorf("argument %q is required", "target_contracts")
	}
	count := 0
	for contract := range strings.SplitSeq(value, ",") {
		count++
		if count > 32 {
			return fmt.Errorf("argument %q lists more than 32 contracts", "target_contracts")
		}
		if !solidityIdentifierPattern.MatchString(contract) {
			return fmt.Errorf("argument %q must be a comma-separated list of Solidity contract names", "target_contracts")
		}
	}
	return nil
}

// evmForkRecord is the replay record a fork devnet must emit: without all
// three pinning fields the run cannot be replayed and is treated as an error.
type evmForkRecord struct {
	ChainID         int64  `json:"chain_id"`
	ForkBlockNumber int64  `json:"fork_block_number"`
	ForkBlockHash   string `json:"fork_block_hash"`
	EndpointAlias   string `json:"endpoint_alias"`
	ListenURL       string `json:"listen_url"`
}

type evmForkRecordAdapter struct{}

func (evmForkRecordAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var record evmForkRecord
	if err := json.Unmarshal(native, &record); err != nil {
		return nil, fmt.Errorf("fork record: %w", err)
	}
	if record.ChainID < 1 || record.ForkBlockNumber < 0 || !evmBlockHashPattern.MatchString(record.ForkBlockHash) {
		return nil, fmt.Errorf("fork record must pin chain id, fork block number, and fork block hash")
	}
	asset := fmt.Sprintf("chain:%d@%d/%s", record.ChainID, record.ForkBlockNumber, record.ForkBlockHash)
	out := []securityRecord{{Asset: asset, Examined: true}}
	// A devnet that is not bound to loopback is an escape from the local fork
	// into a reachable network, which is the one thing this pack must not do.
	if !isLoopbackDevnet(record.ListenURL) {
		message := r.Text(fmt.Sprintf("fork devnet listened on %q instead of loopback", record.ListenURL))
		out = append(out, securityRecord{Asset: asset, Record: fromPipelineRecord(security.ScannerRecord{
			Tool: tool.Name, ToolVersion: tool.Version, RuleID: "fork-devnet-not-local", RuleName: "Fork devnet was not confined to loopback",
			Message: message, Severity: "critical", Category: "sandboxing", FilePath: target.Locator, RawEvidence: message,
		})})
	}
	return out, nil
}

func isLoopbackDevnet(listenURL string) bool {
	return strings.HasPrefix(listenURL, "http://127.0.0.1:") || strings.HasPrefix(listenURL, "http://[::1]:")
}

// forgeTestSuite mirrors the shape of `forge test --json`: a map of test suite
// to per-test results. Per-assertion status comes from this document, never
// from scraped console output.
type forgeTestSuite struct {
	TestResults map[string]struct {
		Status         string          `json:"status"`
		Reason         *string         `json:"reason"`
		Counterexample json.RawMessage `json:"counterexample"`
		DecodedLogs    []string        `json:"decoded_logs"`
	} `json:"test_results"`
}

type forgeJSONAdapter struct{}

func (forgeJSONAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var suites map[string]forgeTestSuite
	if err := json.Unmarshal(native, &suites); err != nil {
		return nil, fmt.Errorf("forge test JSON output: %w", err)
	}
	out := make([]securityRecord, 0, len(suites))
	for _, suite := range slices.Sorted(maps.Keys(suites)) {
		for _, test := range slices.Sorted(maps.Keys(suites[suite].TestResults)) {
			result := suites[suite].TestResults[test]
			asset := suite + ":" + test
			switch strings.ToLower(result.Status) {
			case "success":
				out = append(out, securityRecord{Asset: asset, Examined: true})
			case "skipped":
				out = append(out, securityRecord{Asset: asset, Skipped: true})
			default:
				message := "assertion failed"
				if result.Reason != nil && *result.Reason != "" {
					message = *result.Reason
				}
				evidence := message
				if len(result.Counterexample) > 0 && string(result.Counterexample) != "null" {
					evidence = message + "\ncounterexample: " + string(result.Counterexample)
				}
				if len(result.DecodedLogs) > 0 {
					evidence += "\n" + strings.Join(result.DecodedLogs, "\n")
				}
				out = append(out, securityRecord{Asset: asset, Record: fromPipelineRecord(security.ScannerRecord{
					Tool: tool.Name, ToolVersion: tool.Version, RuleID: "forge-fork-assertion-failed", RuleName: "Forked-state assertion failed",
					Message: r.Text(message), Severity: "high", Category: "chain_state", FilePath: suite, Symbol: test, RawEvidence: r.Text(evidence),
				})})
			}
		}
	}
	return out, nil
}

// forgeCoverageMutation is the combined coverage and mutation document. A
// surviving mutant is the evidence that an assertion cannot fail, so it is
// reported as a finding against the harness rather than the contract.
type forgeCoverageMutation struct {
	Coverage []struct {
		File       string `json:"file"`
		LinesTotal int    `json:"lines_total"`
		LinesHit   int    `json:"lines_hit"`
	} `json:"coverage"`
	Mutants []struct {
		File     string `json:"file"`
		Line     int    `json:"line"`
		Operator string `json:"operator"`
		Status   string `json:"status"`
	} `json:"mutants"`
}

type forgeCoverageMutationAdapter struct{}

func (forgeCoverageMutationAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var document forgeCoverageMutation
	if err := json.Unmarshal(native, &document); err != nil {
		return nil, fmt.Errorf("forge coverage/mutation output: %w", err)
	}
	out := make([]securityRecord, 0, len(document.Coverage)+len(document.Mutants))
	for _, file := range document.Coverage {
		if file.File == "" {
			continue
		}
		if file.LinesHit == 0 && file.LinesTotal > 0 {
			out = append(out, securityRecord{Asset: file.File, Uncovered: true})
			continue
		}
		out = append(out, securityRecord{Asset: file.File, Examined: true})
	}
	for _, mutant := range document.Mutants {
		if !strings.EqualFold(mutant.Status, "survived") {
			continue
		}
		message := fmt.Sprintf("mutation %s at %s:%d survived the test suite: the assertions covering it cannot fail", mutant.Operator, mutant.File, mutant.Line)
		out = append(out, securityRecord{Asset: mutant.File, Record: fromPipelineRecord(security.ScannerRecord{
			Tool: tool.Name, ToolVersion: tool.Version, RuleID: "mutation-survived", RuleName: "Mutant survived the harness",
			Message: r.Text(message), Severity: "medium", Category: "test_quality", FilePath: mutant.File, StartLine: mutant.Line, RawEvidence: r.Text(message),
		})})
	}
	return out, nil
}

type gitDivergenceAdapter struct{}

// Normalize consumes `git diff --numstat` between the reviewed upstream
// revision and the fork. Every diverged file is a candidate for the
// fork-versus-upstream bug class, so it is reported for review.
func (gitDivergenceAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	out := []securityRecord{{Asset: target.Locator, Examined: true}}
	for line := range strings.SplitSeq(string(native), "\n") {
		fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(fields) != 3 || fields[2] == "" {
			continue
		}
		added, deleted, path := fields[0], fields[1], fields[2]
		if !isNumstatCount(added) || !isNumstatCount(deleted) {
			return nil, fmt.Errorf("git numstat line %q is malformed", line)
		}
		message := fmt.Sprintf("%s diverges from the declared upstream (+%s/-%s)", path, added, deleted)
		out = append(out, securityRecord{Asset: path, Record: fromPipelineRecord(security.ScannerRecord{
			Tool: tool.Name, ToolVersion: tool.Version, RuleID: "upstream-divergence", RuleName: "Fork diverges from declared upstream",
			Message: r.Text(message), Severity: "medium", Category: "fork_divergence", FilePath: path, RawEvidence: r.Text(message),
			Extra: map[string]string{"added_lines": added, "deleted_lines": deleted},
		})})
	}
	return out, nil
}

func isNumstatCount(value string) bool {
	if value == "-" {
		return true
	}
	_, err := strconv.Atoi(value)
	return err == nil
}

var medusaResultLine = regexp.MustCompile(`^\[(FAILED|PASSED)\]\s+(Assertion Test|Property Test|Optimization Test):\s+(\S+)`)

type medusaConsoleAdapter struct{}

// Normalize reads medusa's deterministic per-test result lines. Medusa has no
// machine-readable result document upstream, so the reviewed contract is the
// exact `[STATUS] <kind>: <contract>.<signature>` line it prints per test.
func (medusaConsoleAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	out := []securityRecord{}
	for raw := range strings.SplitSeq(string(native), "\n") {
		line := strings.TrimSpace(ansiEscape.ReplaceAllString(raw, ""))
		match := medusaResultLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		status, kind, name := match[1], match[2], match[3]
		if status == "PASSED" {
			out = append(out, securityRecord{Asset: name, Examined: true})
			continue
		}
		message := fmt.Sprintf("%s %s failed under stateful fuzzing", kind, name)
		out = append(out, securityRecord{Asset: name, Record: fromPipelineRecord(security.ScannerRecord{
			Tool: tool.Name, ToolVersion: tool.Version, RuleID: "medusa-property-violated", RuleName: "Stateful property violated",
			Message: r.Text(message), Severity: "high", Category: "fuzzing", FilePath: target.Locator, Symbol: name, RawEvidence: r.Text(line),
		})})
	}
	return out, nil
}
