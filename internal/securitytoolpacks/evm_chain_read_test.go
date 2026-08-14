package securitytoolpacks

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

const (
	pinnedBlockHash = "0x00000000000000000000000000000000000000000000000000000000000000aa"
	proxyAddress    = "0x1111111111111111111111111111111111111111"
	tokenAddress    = "0x2222222222222222222222222222222222222222"
	implAddress     = "0x3333333333333333333333333333333333333333"
)

// chainNode is a JSON-RPC fixture that answers only the read methods the packs
// are allowed to issue and records everything it was asked, so a test can
// assert on the exact RPC surface a run touched.
type chainNode struct {
	mu      sync.Mutex
	methods []string
	chainID int64
	number  int64
	hash    string
	code    map[string]string
	storage map[string]map[string]string
	calls   map[string]map[string]string
}

func newChainNode() *chainNode {
	return &chainNode{
		chainID: 1, number: 21000000, hash: pinnedBlockHash,
		code:    map[string]string{},
		storage: map[string]map[string]string{},
		calls:   map[string]map[string]string{},
	}
}

func (n *chainNode) seen(method string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Contains(n.methods, method)
}

func word(value string) string {
	return "0x" + strings.Repeat("0", 64-len(strings.TrimPrefix(value, "0x"))) + strings.TrimPrefix(value, "0x")
}

//nolint:gocyclo // A fixture node: one flat switch over the read methods under test.
func (n *chainNode) start(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		var call struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &call)
		n.mu.Lock()
		n.methods = append(n.methods, call.Method)
		n.mu.Unlock()
		text := func(index int) string {
			var value string
			if index < len(call.Params) {
				_ = json.Unmarshal(call.Params[index], &value)
			}
			return strings.ToLower(value)
		}
		w.Header().Set("Content-Type", "application/json")
		switch call.Method {
		case "eth_chainId":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":"0x%x"}`, n.chainID)
		case "eth_getBlockByNumber":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"number":"0x%x","hash":%q}}`, n.number, n.hash)
		case "eth_getCode":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%q}`, "0x"+strings.TrimPrefix(n.code[text(0)], "0x"))
		case "eth_getStorageAt":
			value := n.storage[text(0)][text(1)]
			if value == "" {
				value = word("0x0")
			}
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%q}`, value)
		case "eth_call":
			var request struct {
				To   string `json:"to"`
				Data string `json:"data"`
			}
			if len(call.Params) > 0 {
				_ = json.Unmarshal(call.Params[0], &request)
			}
			result := n.calls[strings.ToLower(request.To)][strings.ToLower(request.Data)]
			if result == "" {
				_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"message":"execution reverted"}}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%q}`, result)
		default:
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"message":"unsupported method"}}`)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func chainPinFor(endpoint string) chainPin {
	return chainPin{ChainID: 1, BlockNumber: 21000000, BlockHash: pinnedBlockHash, Alias: "mainnet-archive", Endpoint: endpoint}
}

func normalizeChainRead(t *testing.T, native []byte) []securityRecord {
	t.Helper()
	records, err := chainReadAdapter{}.Normalize(Tool{Name: "chain-read", Version: "1.0.0"}, Target{Locator: "/staged"}, native, Redactor{})
	if err != nil {
		t.Fatalf("normalize chain-read report: %v", err)
	}
	return records
}

func ruleIDs(records []securityRecord) []string {
	ids := []string{}
	for _, record := range records {
		if record.Record.RuleID != "" {
			ids = append(ids, record.Record.RuleID)
		}
	}
	return ids
}

// TestChainReadRPCSurfaceIsAClosedReadOnlyAllowlist is the pack's core
// guarantee: a state-changing method is rejected in the executor, and no typed
// argument or argv token can name a method at all.
func TestChainReadRPCSurfaceIsAClosedReadOnlyAllowlist(t *testing.T) {
	reader := chainReader{client: http.DefaultClient, endpoint: "http://127.0.0.1:1", block: "0x1"}
	for _, method := range []string{
		"eth_sendTransaction", "eth_sendRawTransaction", "eth_sign", "eth_signTransaction",
		"personal_unlockAccount", "anvil_setBalance", "hardhat_impersonateAccount", "evm_mine", "debug_traceCall", "admin_addPeer",
	} {
		err := reader.call(t.Context(), method, []any{}, new(string))
		if err == nil || !strings.Contains(err.Error(), "non-allowlisted") {
			t.Errorf("method %q was not refused by the allowlist: %v", method, err)
		}
	}
	for _, method := range chainReadMethods {
		if strings.HasPrefix(method, "eth_send") || strings.Contains(method, "sign") ||
			strings.HasPrefix(method, "anvil_") || strings.HasPrefix(method, "hardhat_") || strings.HasPrefix(method, "evm_") {
			t.Errorf("allowlisted method %q can change state", method)
		}
	}
	manifest := DefaultManifest("sha256:"+strings.Repeat("a", 64), nil)
	for _, name := range chainReadPacks {
		index := slices.IndexFunc(manifest.Tools, func(tool Tool) bool { return tool.Name == name })
		if index < 0 {
			t.Fatalf("pack %s is missing from the registry", name)
		}
		tool := manifest.Tools[index]
		if !tool.Enabled {
			t.Errorf("pack %s is not executable: %s", name, tool.DisabledReason)
		}
		for _, token := range tool.Invocation {
			if strings.Contains(token, "eth_") || strings.Contains(token, "http://") || strings.Contains(token, "https://") {
				t.Errorf("pack %s argv token %q carries an RPC method or URL", name, token)
			}
		}
		for _, required := range []string{"chain_id", "fork_block_number", "fork_block_hash", "fork_endpoint"} {
			if !slices.ContainsFunc(tool.Arguments, func(a Argument) bool { return a.Name == required && a.Required }) {
				t.Errorf("pack %s does not require %s", name, required)
			}
		}
	}
}

func TestChainReadArgumentsRejectUnboundedOrMalformedInput(t *testing.T) {
	for _, value := range []string{
		"", "0xnothex", "0x1111", strings.ToUpper(proxyAddress),
		strings.TrimSuffix(strings.Repeat(proxyAddress+",", maxChainReadAddresses+1), ","),
	} {
		if err := validateArg(Argument{Name: "addresses", Type: "evm_address_list"}, value); err == nil {
			t.Errorf("address list %q was accepted", value)
		}
	}
	if err := validateArg(Argument{Name: "addresses", Type: "evm_address_list"}, proxyAddress+","+tokenAddress); err != nil {
		t.Errorf("valid address list rejected: %v", err)
	}
	for _, value := range []string{"", "Vault", "../../etc/passwd", "Vault.sol:", "/abs.sol:Vault", "Vault.sol:Vault/../x"} {
		if err := validateArg(Argument{Name: "artifact", Type: "solidity_artifact"}, value); err == nil {
			t.Errorf("artifact name %q was accepted", value)
		}
	}
	if err := validateArg(Argument{Name: "artifact", Type: "solidity_artifact"}, "Vault.sol:Vault"); err != nil {
		t.Errorf("valid artifact name rejected: %v", err)
	}
}

// TestChainReadDetectsUninitializedProxyAgainstPinnedBlock is the acceptance
// case for the class behind the Wormhole payout: the proxy carries EIP-1967
// slots, and the implementation it delegates to was never initialized.
func TestChainReadDetectsUninitializedProxyAgainstPinnedBlock(t *testing.T) {
	node := newChainNode()
	node.code[proxyAddress] = "0x60016002"
	node.code[implAddress] = "0x60036004"
	node.storage[proxyAddress] = map[string]string{
		eip1967ImplementationSlot: word(implAddress),
		eip1967AdminSlot:          word("0x4444444444444444444444444444444444444444"),
	}
	node.calls[implAddress] = map[string]string{chainReadSelectors["owner"]: word("0x0")}
	endpoint := node.start(t)

	native, err := readChainState(t.Context(), http.DefaultClient, chainPinFor(endpoint), []string{proxyAddress})
	if err != nil {
		t.Fatalf("read chain state: %v", err)
	}
	var report chainReadReport
	if err := json.Unmarshal(native, &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.BlockNumber != 21000000 || report.BlockHash != pinnedBlockHash || report.EndpointAlias != "mainnet-archive" {
		t.Fatalf("report did not record the pin and alias: %+v", report)
	}
	if len(report.Accounts) != 1 || report.Accounts[0].ProxyImplementation != implAddress || report.Accounts[0].ImplementationCodeSize == 0 {
		t.Fatalf("proxy relationship was not recorded: %+v", report.Accounts)
	}
	if got := ruleIDs(normalizeChainRead(t, native)); !slices.Contains(got, "implementation-contract-uninitialized") {
		t.Fatalf("uninitialized implementation was not reported: %v", got)
	}
	// The report is the replay record, so it must never carry the endpoint URL.
	if strings.Contains(string(native), endpoint) {
		t.Error("chain-read report leaked the operator endpoint URL")
	}
}

func TestChainReadReportsMissingImplementationAndNonStandardDecimals(t *testing.T) {
	node := newChainNode()
	node.code[proxyAddress] = "0x6001"
	node.code[tokenAddress] = "0x6002"
	// An EIP-1967 proxy whose implementation slot is zero is the uninitialized
	// proxy itself; the token reports 2 decimals, the DFX assimilator class.
	node.storage[proxyAddress] = map[string]string{eip1967AdminSlot: word("0x4444444444444444444444444444444444444444")}
	node.calls[tokenAddress] = map[string]string{chainReadSelectors["decimals"]: word("0x2")}
	endpoint := node.start(t)

	native, err := readChainState(t.Context(), http.DefaultClient, chainPinFor(endpoint), []string{proxyAddress, tokenAddress, implAddress})
	if err != nil {
		t.Fatalf("read chain state: %v", err)
	}
	got := ruleIDs(normalizeChainRead(t, native))
	for _, rule := range []string{"proxy-implementation-unset", "token-decimals-nonstandard", "deployed-address-has-no-code"} {
		if !slices.Contains(got, rule) {
			t.Errorf("rule %s was not reported: %v", rule, got)
		}
	}
	if node.seen("eth_sendRawTransaction") {
		t.Error("the run issued a state-changing method")
	}
}

func TestChainReadFailsWhenTheEndpointIsNotServingThePinnedState(t *testing.T) {
	for name, mutate := range map[string]func(*chainNode){
		"wrong chain":      func(n *chainNode) { n.chainID = 8453 },
		"wrong block":      func(n *chainNode) { n.number = 20999999 },
		"wrong block hash": func(n *chainNode) { n.hash = strings.Replace(pinnedBlockHash, "aa", "bb", 1) },
	} {
		node := newChainNode()
		node.code[proxyAddress] = "0x6001"
		mutate(node)
		if _, err := readChainState(t.Context(), http.DefaultClient, chainPinFor(node.start(t)), []string{proxyAddress}); err == nil {
			t.Errorf("%s was accepted as the pinned state", name)
		}
	}
}

func TestChainPinRequiresAResolvedOperatorEndpoint(t *testing.T) {
	arguments := map[string]string{"chain_id": "1", "fork_block_number": "21000000", "fork_block_hash": pinnedBlockHash, "fork_endpoint": "mainnet-archive"}
	if _, err := chainPinFromRequest(arguments, []string{"ga-chain-read", chainReadEndpointFlag, operatorForkEndpointToken + "mainnet-archive"}); err == nil {
		t.Error("an unresolved endpoint token was accepted as an endpoint")
	}
	if _, err := chainPinFromRequest(arguments, []string{"ga-chain-read"}); err == nil {
		t.Error("a run with no endpoint at all was accepted")
	}
	for name, broken := range map[string]string{"chain_id": "0", "fork_block_number": "-1", "fork_block_hash": "0xdeadbeef"} {
		mutated := maps.Clone(arguments)
		mutated[name] = broken
		if _, err := chainPinFromRequest(mutated, []string{"ga-chain-read", chainReadEndpointFlag, "http://127.0.0.1:1"}); err == nil {
			t.Errorf("unpinned %s was accepted", name)
		}
	}
}

func stageFoundryArtifact(t *testing.T, runtimeObject string, immutables string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "out", "Vault.sol"), 0o755); err != nil {
		t.Fatal(err)
	}
	references := "{}"
	if immutables != "" {
		references = immutables
	}
	artifact := fmt.Sprintf(`{"deployedBytecode":{"object":%q,"immutableReferences":%s}}`, runtimeObject, references)
	if err := os.WriteFile(filepath.Join(root, "out", "Vault.sol", "Vault.json"), []byte(artifact), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func decodeBytecodeDiff(t *testing.T, native []byte) bytecodeDiffReport {
	t.Helper()
	var report bytecodeDiffReport
	if err := json.Unmarshal(native, &report); err != nil {
		t.Fatalf("decode bytecode diff report: %v", err)
	}
	return report
}

func TestDeployedBytecodeDiffProvesTheAuditedArtifactIsRunning(t *testing.T) {
	code := "0x60016002600360046005"
	node := newChainNode()
	node.code[proxyAddress] = code
	endpoint := node.start(t)
	root := stageFoundryArtifact(t, code, "")

	native, err := diffDeployedBytecode(t.Context(), http.DefaultClient, chainPinFor(endpoint), proxyAddress, "Vault.sol:Vault", root)
	if err != nil {
		t.Fatalf("diff deployed bytecode: %v", err)
	}
	report := decodeBytecodeDiff(t, native)
	if report.Match != bytecodeMatchExact || report.DeployedDigest != report.ArtifactDigest {
		t.Fatalf("identical code was not an exact match: %+v", report)
	}
	records, err := bytecodeDiffAdapter{}.Normalize(Tool{Name: "deployed-bytecode-diff"}, Target{Locator: root}, native, Redactor{})
	if err != nil || len(records) != 1 || !records[0].Examined {
		t.Fatalf("a matching artifact produced %v (%v)", records, err)
	}
}

func TestDeployedBytecodeDiffFlagsCodeThatIsNotTheAuditedArtifact(t *testing.T) {
	node := newChainNode()
	node.code[proxyAddress] = "0x60016002600360046005"
	endpoint := node.start(t)
	root := stageFoundryArtifact(t, "0x70017002700370047005", "")

	native, err := diffDeployedBytecode(t.Context(), http.DefaultClient, chainPinFor(endpoint), proxyAddress, "Vault.sol:Vault", root)
	if err != nil {
		t.Fatalf("diff deployed bytecode: %v", err)
	}
	if report := decodeBytecodeDiff(t, native); report.Match != bytecodeMatchMismatch {
		t.Fatalf("different code was not a mismatch: %+v", report)
	}
	records, err := bytecodeDiffAdapter{}.Normalize(Tool{Name: "deployed-bytecode-diff"}, Target{Locator: root}, native, Redactor{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got := ruleIDs(records); !slices.Contains(got, "deployed-bytecode-mismatch") {
		t.Fatalf("mismatch was not reported: %v", got)
	}
}

// A contract with immutables differs from its artifact exactly in the spans the
// compiler declares, so those spans and the metadata trailer are normalized
// before the fallback comparison — and the record still says which comparison
// established the match.
func TestDeployedBytecodeDiffNormalizesImmutablesAndMetadata(t *testing.T) {
	body := "6001600260036004"
	artifactCode := "0x" + body + "0000000000000000000000000000000000000000000000000000000000000000" + "aabb0002"
	deployedCode := "0x" + body + "00000000000000000000000000000000000000000000000000000000000000ff" + "ccdd0002"
	node := newChainNode()
	node.code[proxyAddress] = deployedCode
	endpoint := node.start(t)
	immutables := fmt.Sprintf(`{"7":[{"start":%d,"length":32}]}`, len(body)/2)
	root := stageFoundryArtifact(t, artifactCode, immutables)

	native, err := diffDeployedBytecode(t.Context(), http.DefaultClient, chainPinFor(endpoint), proxyAddress, "Vault.sol:Vault", root)
	if err != nil {
		t.Fatalf("diff deployed bytecode: %v", err)
	}
	if report := decodeBytecodeDiff(t, native); report.Match != bytecodeMatchNormalized {
		t.Fatalf("immutable-only difference was not a normalized match: %+v", report)
	}
}

func TestDeployedBytecodeDiffFailsWithoutAStagedArtifact(t *testing.T) {
	node := newChainNode()
	node.code[proxyAddress] = "0x6001"
	endpoint := node.start(t)
	root := stageFoundryArtifact(t, "0x6001", "")

	for name, artifact := range map[string]string{
		"missing artifact":  "Other.sol:Other",
		"path traversal":    "../../../etc/passwd.sol:Passwd",
		"unqualified name":  "Vault",
		"empty artifact id": "",
	} {
		if _, err := diffDeployedBytecode(t.Context(), http.DefaultClient, chainPinFor(endpoint), proxyAddress, artifact, root); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestDeployedBytecodeDiffTreatsAnEmptyAccountAsAMismatch(t *testing.T) {
	node := newChainNode()
	endpoint := node.start(t)
	root := stageFoundryArtifact(t, "0x6001", "")

	native, err := diffDeployedBytecode(t.Context(), http.DefaultClient, chainPinFor(endpoint), proxyAddress, "Vault.sol:Vault", root)
	if err != nil {
		t.Fatalf("diff deployed bytecode: %v", err)
	}
	if report := decodeBytecodeDiff(t, native); report.Match != bytecodeMatchMismatch || report.DeployedSize != 0 {
		t.Fatalf("an empty account was not a mismatch: %+v", report)
	}
}

func TestNormalizeRuntimeBytecodeBlanksImmutablesAndDropsMetadata(t *testing.T) {
	code, err := hex.DecodeString("6001600260036004aabb0002")
	if err != nil {
		t.Fatal(err)
	}
	normalized := normalizeRuntimeBytecode(code, []immutableRange{{start: 2, length: 2}})
	if got := hex.EncodeToString(normalized); got != "6001000060036004" {
		t.Fatalf("normalized bytecode = %s", got)
	}
	// An out-of-range span never panics and never truncates the code.
	if got := normalizeRuntimeBytecode([]byte{0x60, 0x01}, []immutableRange{{start: 100, length: 4}}); len(got) != 2 {
		t.Fatalf("out-of-range immutable span rewrote the code: %x", got)
	}
}

// The chain packs execute in-process, so the executor stage — not a fallthrough
// to exec — must handle them.
func TestExecuteEVMStageHandlesTheChainPacks(t *testing.T) {
	node := newChainNode()
	node.code[proxyAddress] = "0x6001"
	endpoint := node.start(t)
	for _, name := range chainReadPacks {
		arguments := map[string]string{
			"chain_id": "1", "fork_block_number": "21000000", "fork_block_hash": pinnedBlockHash,
			"fork_endpoint": "mainnet-archive", "addresses": proxyAddress,
			"address": proxyAddress, "artifact": "Vault.sol:Vault",
		}
		request := ExecutionRequest{
			Tool:       Tool{Name: name, Version: "1.0.0"},
			Config:     RunConfig{Tool: name, Arguments: arguments},
			Invocation: Invocation{Budgets: Budgets{MaxOutputSize: 1 << 20}},
		}
		argv := []string{"ga-" + name, chainReadEndpointFlag, endpoint}
		result, handled := executeEVMStage(t.Context(), request, argv, stageFoundryArtifact(t, "0x6001", ""), nil, nil)
		if !handled {
			t.Fatalf("pack %s fell through to the generic executor", name)
		}
		if result.ExitCode != 0 || result.Err != nil {
			t.Fatalf("pack %s failed: exit %d (%v)", name, result.ExitCode, result.Err)
		}
		if len(result.Output) == 0 {
			t.Fatalf("pack %s produced no record", name)
		}
	}
}
