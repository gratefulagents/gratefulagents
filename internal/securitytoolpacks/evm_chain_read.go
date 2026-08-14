package securitytoolpacks

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/gratefulagents/gratefulagents/internal/security"
)

// Chain-state packs read what is deployed, which is where several of the
// highest-paying bug classes live and where source review is structurally
// blind: proxy implementation and admin slots, token decimals, role holders,
// and whether the audited source is the code that is actually running.
//
// The same two rules as the fork packs hold, plus one more:
//
//   - the endpoint is an operator-authorized alias, never a model string;
//   - chain id, block number, and block hash are required, so every read is
//     pinned and replayable;
//   - the RPC surface is a closed allowlist of read-only methods declared in
//     this file. Neither argv nor a typed argument can name an RPC method, so
//     a state-changing call is not merely rejected, it is unreachable.
const (
	// EIP-1967 slots. These are constants of the standard, not configuration.
	eip1967ImplementationSlot = "0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc"
	eip1967AdminSlot          = "0xb53127684a568b3173ae13b9f8a6016e243e63b6e8ee1178d6a717850b5d6103"
	eip1967BeaconSlot         = "0xa3f0ad74e5423aebfd80d3ef4346578335a9a72aeaee59ff6cb3582b35133d50"

	evmZeroAddress = "0x0000000000000000000000000000000000000000"
	evmZeroWord    = "0x0000000000000000000000000000000000000000000000000000000000000000"

	// Initializable markers. OpenZeppelin v5 keeps the initialization counter
	// in an ERC-7201 namespaced slot; v4 keeps it in the low bytes of slot 0.
	// Both are non-zero once a contract has been initialized AND once
	// _disableInitializers() has run, which is exactly what distinguishes a
	// deliberately locked implementation from one nobody ever claimed.
	erc7201InitializableSlot = "0xf0c57e16840df040f15088dc2f81fe391c3923bec73e23a9662efc9c229c6a00"
	legacyInitializableSlot  = evmZeroWord

	// chainReadEndpointFlag marks the argv slot the operator endpoint token is
	// resolved into, so the stage reads a resolved URL it never chose itself.
	chainReadEndpointFlag = "--endpoint"

	maxChainReadAddresses = 16
	maxDeployedCodeBytes  = 1 << 20
)

var (
	evmAddressPattern = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
	evmWordPattern    = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
	// forgeArtifactPattern is `File.sol:Contract`, the standard Foundry
	// fully-qualified artifact name. It is a path fragment, so it is bounded
	// to plain identifiers: nothing here may escape the staged project.
	forgeArtifactPattern = regexp.MustCompile(`^[A-Za-z0-9_$.-]{1,64}\.sol:[A-Za-z_$][A-Za-z0-9_$]{0,63}$`)
)

// chainReadMethods is the complete JSON-RPC surface of the chain packs. Every
// entry is read-only; the list is code, so a run cannot extend it.
var chainReadMethods = []string{
	"eth_chainId",
	"eth_getBlockByNumber",
	"eth_getCode",
	"eth_getStorageAt",
	"eth_getBalance",
	"eth_call",
}

// chainReadSelectors is the closed set of view functions the packs may call.
// Each is a well-known zero-argument selector; a call carries no value, no
// sender, and no calldata beyond these four bytes, so it cannot change state
// even against a node that would allow it.
var chainReadSelectors = map[string]string{
	"decimals":       "0x313ce567",
	"owner":          "0x8da5cb5b",
	"implementation": "0x5c60da1b",
	"admin":          "0xf851a440",
	"totalSupply":    "0x18160ddd",
}

// chainReadPacks are the packs implemented by the in-process chain-state
// stages rather than by an external binary.
var chainReadPacks = []string{"chain-read", "deployed-bytecode-diff"}

// chainReadArguments is the typed contract of the chain-read pack: the pinning
// fields every chain pack shares plus the addresses to observe.
func chainReadArguments() []Argument {
	return append(evmForkPinningArguments(), Argument{Name: "addresses", Type: "evm_address_list", Required: true})
}

// deployedBytecodeDiffArguments pins one deployed address against one compiled
// artifact of the audited revision.
func deployedBytecodeDiffArguments() []Argument {
	return append(evmForkPinningArguments(),
		Argument{Name: "address", Type: "evm_address", Required: true},
		Argument{Name: "artifact", Type: "solidity_artifact", Required: true},
	)
}

// validateEVMAddressList keeps the observed address set a bounded list of
// canonical lowercase 20-byte addresses.
func validateEVMAddressList(value string) error {
	if value == "" {
		return fmt.Errorf("argument %q is required", "addresses")
	}
	count := 0
	for address := range strings.SplitSeq(value, ",") {
		count++
		if count > maxChainReadAddresses {
			return fmt.Errorf("argument %q lists more than %d addresses", "addresses", maxChainReadAddresses)
		}
		if !evmAddressPattern.MatchString(address) {
			return fmt.Errorf("argument %q must be a comma-separated list of 0x-prefixed lowercase 20-byte addresses", "addresses")
		}
	}
	return nil
}

// chainReadAccount is the observed state of one deployed address at the pinned
// block. Absent facts are empty rather than zero-valued: a contract that has
// no decimals() is not a token with zero decimals.
type chainReadAccount struct {
	Address                string `json:"address"`
	CodeSize               int    `json:"code_size"`
	CodeDigest             string `json:"code_digest,omitempty"`
	ProxyImplementation    string `json:"proxy_implementation,omitempty"`
	ImplementationCodeSize int    `json:"implementation_code_size,omitempty"`
	ImplementationOwner    string `json:"implementation_owner,omitempty"`
	ProxyAdmin             string `json:"proxy_admin,omitempty"`
	ProxyBeacon            string `json:"proxy_beacon,omitempty"`
	ProxySlotsSet          bool   `json:"proxy_slots_set"`
	Owner                  string `json:"owner,omitempty"`
	Decimals               *int64 `json:"decimals,omitempty"`
	// ImplementationInitializer is the observed Initializable marker of the
	// implementation: "zero" only when both the ERC-7201 and the legacy slot
	// are zero, which is the sole state that evidences an implementation
	// nobody has claimed and nobody has locked.
	ImplementationInitializer string `json:"implementation_initializer,omitempty"`
}

// chainReadReport is the replay record of a chain-read run: the pin, the
// endpoint alias (never its URL), and the observed accounts.
type chainReadReport struct {
	ChainID       int64              `json:"chain_id"`
	BlockNumber   int64              `json:"block_number"`
	BlockHash     string             `json:"block_hash"`
	EndpointAlias string             `json:"endpoint_alias"`
	Accounts      []chainReadAccount `json:"accounts"`
}

// bytecodeDiffReport records whether the audited artifact is the code running
// at the pinned address and block.
type bytecodeDiffReport struct {
	ChainID          int64  `json:"chain_id"`
	BlockNumber      int64  `json:"block_number"`
	BlockHash        string `json:"block_hash"`
	EndpointAlias    string `json:"endpoint_alias"`
	Address          string `json:"address"`
	Artifact         string `json:"artifact"`
	DeployedSize     int    `json:"deployed_size"`
	ArtifactSize     int    `json:"artifact_size"`
	DeployedDigest   string `json:"deployed_digest"`
	ArtifactDigest   string `json:"artifact_digest"`
	NormalizedDigest string `json:"normalized_digest,omitempty"`
	Match            string `json:"match"`
}

// Match kinds. "exact" and "normalized" both prove the audited revision is the
// deployed one; "mismatch" proves the audit was performed against code that is
// not running, which invalidates every finding derived from it.
const (
	bytecodeMatchExact      = "exact"
	bytecodeMatchNormalized = "normalized"
	bytecodeMatchMismatch   = "mismatch"
)

type chainReadAdapter struct{}

//nolint:gocyclo // One auditable pass from observed chain state to the deployed-state classes it evidences.
func (chainReadAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var report chainReadReport
	if err := json.Unmarshal(native, &report); err != nil {
		return nil, fmt.Errorf("chain-read report: %w", err)
	}
	if report.ChainID < 1 || report.BlockNumber < 0 || !evmBlockHashPattern.MatchString(report.BlockHash) {
		return nil, fmt.Errorf("chain-read report must pin chain id, block number, and block hash")
	}
	if len(report.Accounts) == 0 {
		return nil, fmt.Errorf("chain-read report observed no address")
	}
	out := make([]securityRecord, 0, len(report.Accounts))
	finding := func(account chainReadAccount, rule, name, severity, category, message string, extra map[string]string) securityRecord {
		asset := chainAsset(report, account.Address)
		return securityRecord{Asset: asset, Record: fromPipelineRecord(security.ScannerRecord{
			Tool: tool.Name, ToolVersion: tool.Version, RuleID: rule, RuleName: name,
			Message: r.Text(message), Severity: severity, Category: category, FilePath: asset,
			Symbol: account.Address, RawEvidence: r.Text(message), Extra: extra,
		})}
	}
	for _, account := range report.Accounts {
		asset := chainAsset(report, account.Address)
		if account.CodeSize == 0 {
			out = append(out, finding(account, "deployed-address-has-no-code", "Declared address holds no deployed code", "high", "chain_state",
				fmt.Sprintf("%s has no code at block %d: the audited asset is not deployed at this address on chain %d", account.Address, report.BlockNumber, report.ChainID), nil))
			continue
		}
		out = append(out, securityRecord{Asset: asset, Examined: true})
		switch {
		case account.ProxySlotsSet && account.ProxyImplementation == "":
			out = append(out, finding(account, "proxy-implementation-unset", "Proxy has no implementation set", "critical", "chain_state",
				fmt.Sprintf("%s carries EIP-1967 proxy slots but its implementation slot is zero at block %d: the proxy is uninitialized", account.Address, report.BlockNumber), nil))
		case account.ProxyImplementation != "" && account.ImplementationCodeSize == 0:
			out = append(out, finding(account, "proxy-implementation-missing-code", "Proxy points at an address with no code", "critical", "chain_state",
				fmt.Sprintf("%s delegates to %s, which holds no code at block %d", account.Address, account.ProxyImplementation, report.BlockNumber),
				map[string]string{"implementation": account.ProxyImplementation}))
		case account.ProxyImplementation != "" && account.ImplementationInitializer == "zero" && account.ImplementationOwner == evmZeroAddress:
			// Both Initializable markers zero rules out the two states that
			// look identical from owner() alone: an implementation that ran
			// _disableInitializers() (marker set to its maximum) and one whose
			// owner was deliberately renounced after initialization (marker
			// set to the version it initialized at).
			out = append(out, finding(account, "implementation-contract-uninitialized", "Implementation contract behind the proxy is uninitialized", "high", "chain_state",
				fmt.Sprintf("implementation %s behind proxy %s has a zero Initializable marker in both the ERC-7201 and the legacy slot and reports owner() == %s at block %d: it was never initialized and its initializers were never disabled",
					account.ProxyImplementation, account.Address, evmZeroAddress, report.BlockNumber),
				map[string]string{"implementation": account.ProxyImplementation, "initializer_marker": account.ImplementationInitializer}))
		}
		if account.Decimals != nil && *account.Decimals != 18 {
			out = append(out, finding(account, "token-decimals-nonstandard", "Token does not use 18 decimals", "low", "accounting",
				fmt.Sprintf("%s reports decimals() == %d at block %d: every scaling path that assumes 18 decimals is wrong for this asset", account.Address, *account.Decimals, report.BlockNumber),
				map[string]string{"decimals": strconv.FormatInt(*account.Decimals, 10)}))
		}
	}
	return out, nil
}

func chainAsset(report chainReadReport, address string) string {
	return fmt.Sprintf("chain:%d@%d/%s", report.ChainID, report.BlockNumber, address)
}

type bytecodeDiffAdapter struct{}

func (bytecodeDiffAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var report bytecodeDiffReport
	if err := json.Unmarshal(native, &report); err != nil {
		return nil, fmt.Errorf("deployed-bytecode-diff report: %w", err)
	}
	if report.ChainID < 1 || !evmBlockHashPattern.MatchString(report.BlockHash) || !evmAddressPattern.MatchString(report.Address) {
		return nil, fmt.Errorf("deployed-bytecode-diff report must pin chain id, block hash, and address")
	}
	asset := fmt.Sprintf("chain:%d@%d/%s", report.ChainID, report.BlockNumber, report.Address)
	extra := map[string]string{
		"artifact": report.Artifact, "match": report.Match,
		"deployed_digest": report.DeployedDigest, "artifact_digest": report.ArtifactDigest,
		"deployed_size": strconv.Itoa(report.DeployedSize), "artifact_size": strconv.Itoa(report.ArtifactSize),
	}
	switch report.Match {
	case bytecodeMatchExact, bytecodeMatchNormalized:
		return []securityRecord{{Asset: asset, Examined: true}}, nil
	case bytecodeMatchMismatch:
		message := fmt.Sprintf("%s at block %d does not run artifact %s: deployed %s (%d bytes) differs from the audited artifact %s (%d bytes)",
			report.Address, report.BlockNumber, report.Artifact, report.DeployedDigest, report.DeployedSize, report.ArtifactDigest, report.ArtifactSize)
		return []securityRecord{{Asset: asset, Record: fromPipelineRecord(security.ScannerRecord{
			Tool: tool.Name, ToolVersion: tool.Version, RuleID: "deployed-bytecode-mismatch", RuleName: "Deployed bytecode is not the audited artifact",
			Message: r.Text(message), Severity: "high", Category: "deployed_state", FilePath: asset, Symbol: report.Artifact,
			RawEvidence: r.Text(message), Extra: extra,
		})}}, nil
	default:
		return nil, fmt.Errorf("deployed-bytecode-diff report has unknown match kind %q", report.Match)
	}
}

// chainReader issues the closed set of read-only calls against a resolved
// operator endpoint, pinned to one block.
type chainReader struct {
	client   *http.Client
	endpoint string
	// block is the state selector every read carries. It is an EIP-1898
	// {"blockHash": ...} object when the endpoint accepts one, so a reorg
	// between the pin check and the reads cannot silently move the state a
	// record claims to describe, and the plain block number otherwise.
	block any
}

// call is the single chokepoint for every chain read. A method outside the
// allowlist is rejected here, in the executor, so it cannot be reached even if
// a caller is wrong; a model never supplies a method at all.
func (c chainReader) call(ctx context.Context, method string, params []any, out any) error {
	if !slices.Contains(chainReadMethods, method) {
		return fmt.Errorf("chain read refused non-allowlisted JSON-RPC method %q", method)
	}
	return jsonRPCCall(ctx, c.client, c.endpoint, "chain read", method, params, out)
}

// staticCall invokes one allowlisted zero-argument view function at the pinned
// block. A revert or a malformed word is an observation — "this contract is
// not a token" — and returns ok=false. A transport failure, timeout, or rate
// limit is not: it is returned as an error, because a run that quietly treats
// a broken endpoint as "the contract has no owner()" reports a clean audit it
// never performed.
func (c chainReader) staticCall(ctx context.Context, address, function string) (string, bool, error) {
	selector, known := chainReadSelectors[function]
	if !known {
		return "", false, fmt.Errorf("chain read refused non-allowlisted view function %q", function)
	}
	var result string
	if err := c.call(ctx, "eth_call", []any{map[string]string{"to": address, "data": selector}, c.block}, &result); err != nil {
		var rejected nodeRejection
		if errors.As(err, &rejected) {
			return "", false, nil
		}
		return "", false, err
	}
	word, ok := strings.CutPrefix(strings.ToLower(result), "0x")
	if !ok || len(word) < 64 {
		return "", false, nil
	}
	return "0x" + word[len(word)-64:], true, nil
}

func (c chainReader) code(ctx context.Context, address string) ([]byte, error) {
	var value string
	if err := c.call(ctx, "eth_getCode", []any{address, c.block}, &value); err != nil {
		return nil, err
	}
	digits, ok := strings.CutPrefix(strings.ToLower(value), "0x")
	if !ok {
		return nil, fmt.Errorf("eth_getCode returned malformed data for %s", address)
	}
	if len(digits)/2 > maxDeployedCodeBytes {
		return nil, fmt.Errorf("deployed code at %s exceeds the %d-byte read budget", address, maxDeployedCodeBytes)
	}
	decoded, err := hex.DecodeString(digits)
	if err != nil {
		return nil, fmt.Errorf("eth_getCode returned malformed data for %s", address)
	}
	return decoded, nil
}

func (c chainReader) storageAddress(ctx context.Context, address, slot string) (string, error) {
	word, err := c.storageWord(ctx, address, slot)
	if err != nil {
		return "", err
	}
	return wordAddress(word), nil
}

func (c chainReader) storageWord(ctx context.Context, address, slot string) (string, error) {
	var value string
	if err := c.call(ctx, "eth_getStorageAt", []any{address, slot, c.block}, &value); err != nil {
		return "", err
	}
	return strings.ToLower(value), nil
}

// wordAddress reads the low 20 bytes of a storage word as an address and
// returns "" for the zero word, which is how an unset slot must read.
func wordAddress(word string) string {
	lowered := strings.ToLower(word)
	if !evmWordPattern.MatchString(lowered) {
		return ""
	}
	address := "0x" + lowered[26:]
	if address == evmZeroAddress {
		return ""
	}
	return address
}

// verifyPin proves the endpoint is serving the chain and block the run pinned
// before a single fact is read from it.
func (c chainReader) verifyPin(ctx context.Context, chainID, blockNumber int64, blockHash string) error {
	var quantity string
	if err := c.call(ctx, "eth_chainId", []any{}, &quantity); err != nil {
		return err
	}
	reported, err := parseHexQuantity(quantity)
	if err != nil {
		return err
	}
	if reported != chainID {
		return fmt.Errorf("endpoint serves chain id %d, but the run pinned %d", reported, chainID)
	}
	var block struct {
		Number string `json:"number"`
		Hash   string `json:"hash"`
	}
	if err := c.call(ctx, "eth_getBlockByNumber", []any{"0x" + strconv.FormatInt(blockNumber, 16), false}, &block); err != nil {
		return err
	}
	number, err := parseHexQuantity(block.Number)
	if err != nil {
		return err
	}
	if number != blockNumber || strings.ToLower(block.Hash) != blockHash {
		return fmt.Errorf("endpoint returned block %d (%s) for the pinned block %d (%s)", number, strings.ToLower(block.Hash), blockNumber, blockHash)
	}
	return nil
}

// chainPin is the validated pinning contract of one chain-pack run.
type chainPin struct {
	ChainID     int64
	BlockNumber int64
	BlockHash   string
	Alias       string
	Endpoint    string
}

func chainPinFromRequest(arguments map[string]string, argv []string) (chainPin, error) {
	chainID, err := strconv.ParseInt(arguments["chain_id"], 10, 64)
	if err != nil || chainID < 1 {
		return chainPin{}, fmt.Errorf("chain read is missing its pinned chain id")
	}
	blockNumber, err := strconv.ParseInt(arguments["fork_block_number"], 10, 64)
	if err != nil || blockNumber < 0 {
		return chainPin{}, fmt.Errorf("chain read is missing its pinned block number")
	}
	blockHash := strings.ToLower(arguments["fork_block_hash"])
	if !evmBlockHashPattern.MatchString(blockHash) {
		return chainPin{}, fmt.Errorf("chain read is missing its pinned block hash")
	}
	endpoint := ""
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == chainReadEndpointFlag {
			endpoint = argv[i+1]
		}
	}
	if strings.HasPrefix(endpoint, operatorForkEndpointToken) || endpoint == "" {
		return chainPin{}, fmt.Errorf("chain read has no resolved operator endpoint")
	}
	return chainPin{ChainID: chainID, BlockNumber: blockNumber, BlockHash: blockHash, Alias: arguments["fork_endpoint"], Endpoint: endpoint}, nil
}

func (p chainPin) reader(client *http.Client) chainReader {
	return chainReader{client: client, endpoint: p.Endpoint, block: "0x" + strconv.FormatInt(p.BlockNumber, 16)}
}

// pinReader verifies the endpoint is serving the pinned chain and block, then
// selects the strongest available state selector: an EIP-1898 block-hash
// object when the endpoint supports one, so a reorg cannot move the state out
// from under the reads, and the block number otherwise.
func pinReader(ctx context.Context, client *http.Client, pin chainPin) (chainReader, error) {
	reader := pin.reader(client)
	if err := reader.verifyPin(ctx, pin.ChainID, pin.BlockNumber, pin.BlockHash); err != nil {
		return chainReader{}, err
	}
	hashSelector := map[string]any{"blockHash": pin.BlockHash, "requireCanonical": true}
	probe := chainReader{client: client, endpoint: pin.Endpoint, block: hashSelector}
	var code string
	if err := probe.call(ctx, "eth_getCode", []any{evmZeroAddress, hashSelector}, &code); err == nil {
		return probe, nil
	} else if !errors.As(err, &nodeRejection{}) {
		return chainReader{}, err
	}
	return reader, nil
}

// confirmPinStillHolds re-reads the pinned block after the reads complete. It
// is the fallback path's protection: an endpoint that only accepts block
// numbers can reorganize mid-run, and a record that claims a block hash it no
// longer matches is not replayable.
func (c chainReader) confirmPinStillHolds(ctx context.Context, pin chainPin) error {
	if _, hashPinned := c.block.(map[string]any); hashPinned {
		return nil
	}
	return c.verifyPin(ctx, pin.ChainID, pin.BlockNumber, pin.BlockHash)
}

// readChainState observes every requested address at the pinned block and
// returns the replay record the chain-read adapter consumes.
func readChainState(ctx context.Context, client *http.Client, pin chainPin, addresses []string) ([]byte, error) {
	reader, err := pinReader(ctx, client, pin)
	if err != nil {
		return nil, err
	}
	report := chainReadReport{ChainID: pin.ChainID, BlockNumber: pin.BlockNumber, BlockHash: pin.BlockHash, EndpointAlias: pin.Alias}
	for _, address := range addresses {
		account, accountErr := readAccount(ctx, reader, address)
		if accountErr != nil {
			return nil, accountErr
		}
		report.Accounts = append(report.Accounts, account)
	}
	if err := reader.confirmPinStillHolds(ctx, pin); err != nil {
		return nil, err
	}
	return json.Marshal(report)
}

func readAccount(ctx context.Context, reader chainReader, address string) (chainReadAccount, error) {
	code, err := reader.code(ctx, address)
	if err != nil {
		return chainReadAccount{}, err
	}
	account := chainReadAccount{Address: address, CodeSize: len(code)}
	if len(code) == 0 {
		return account, nil
	}
	account.CodeDigest = sha256Digest(code)
	for slot, field := range map[string]*string{
		eip1967ImplementationSlot: &account.ProxyImplementation,
		eip1967AdminSlot:          &account.ProxyAdmin,
		eip1967BeaconSlot:         &account.ProxyBeacon,
	} {
		value, storageErr := reader.storageAddress(ctx, address, slot)
		if storageErr != nil {
			return chainReadAccount{}, storageErr
		}
		*field = value
	}
	// A beacon proxy legitimately leaves its implementation slot empty: the
	// implementation lives on the beacon. Resolving it here is what keeps a
	// correct beacon proxy from being reported as an unset proxy, and is also
	// the only way its implementation is examined at all.
	if account.ProxyImplementation == "" && account.ProxyBeacon != "" {
		resolved, resolveErr := readImplementationPointer(ctx, reader, account.ProxyBeacon)
		if resolveErr != nil {
			return chainReadAccount{}, resolveErr
		}
		account.ProxyImplementation = resolved
	}
	// A contract that answers implementation() is a proxy even when it keeps
	// the pointer outside the EIP-1967 slot.
	if account.ProxyImplementation == "" {
		resolved, resolveErr := readImplementationPointer(ctx, reader, address)
		if resolveErr != nil {
			return chainReadAccount{}, resolveErr
		}
		account.ProxyImplementation = resolved
	}
	account.ProxySlotsSet = account.ProxyImplementation != "" || account.ProxyAdmin != "" || account.ProxyBeacon != ""
	owner, err := readAddressView(ctx, reader, address, "owner")
	if err != nil {
		return chainReadAccount{}, err
	}
	account.Owner = owner
	word, ok, err := reader.staticCall(ctx, address, "decimals")
	if err != nil {
		return chainReadAccount{}, err
	}
	if ok {
		if decimals, parsed := new(big.Int).SetString(strings.TrimPrefix(word, "0x"), 16); parsed && decimals.IsInt64() && decimals.Int64() <= 77 {
			value := decimals.Int64()
			account.Decimals = &value
		}
	}
	if account.ProxyImplementation != "" {
		implementation, codeErr := reader.code(ctx, account.ProxyImplementation)
		if codeErr != nil {
			return chainReadAccount{}, codeErr
		}
		account.ImplementationCodeSize = len(implementation)
		implementationOwner, ownerErr := readAddressView(ctx, reader, account.ProxyImplementation, "owner")
		if ownerErr != nil {
			return chainReadAccount{}, ownerErr
		}
		account.ImplementationOwner = implementationOwner
		marker, markerErr := readInitializableMarker(ctx, reader, account.ProxyImplementation)
		if markerErr != nil {
			return chainReadAccount{}, markerErr
		}
		account.ImplementationInitializer = marker
	}
	return account, nil
}

// readImplementationPointer calls implementation() and returns the address it
// points at, or "" when the contract does not answer the call.
func readImplementationPointer(ctx context.Context, reader chainReader, address string) (string, error) {
	word, ok, err := reader.staticCall(ctx, address, "implementation")
	if err != nil || !ok {
		return "", err
	}
	return wordAddress(word), nil
}

// readAddressView returns the low 20 bytes of an allowlisted view function's
// result, including the zero address when that is genuinely what it returned,
// and "" when the contract does not answer the call at all.
func readAddressView(ctx context.Context, reader chainReader, address, function string) (string, error) {
	word, ok, err := reader.staticCall(ctx, address, function)
	if err != nil || !ok {
		return "", err
	}
	return "0x" + strings.ToLower(word)[26:], nil
}

// readInitializableMarker reports whether the contract's OpenZeppelin
// Initializable state has ever been written: "zero" only when both the
// ERC-7201 namespaced slot and the legacy slot-0 marker are zero, "set"
// otherwise. An implementation that was initialized, and one that ran
// _disableInitializers(), both read as "set"; only a contract that nobody
// claimed and nobody locked reads as "zero".
func readInitializableMarker(ctx context.Context, reader chainReader, address string) (string, error) {
	for _, slot := range []string{erc7201InitializableSlot, legacyInitializableSlot} {
		word, err := reader.storageWord(ctx, address, slot)
		if err != nil {
			return "", err
		}
		if !evmWordPattern.MatchString(word) {
			return "", fmt.Errorf("eth_getStorageAt returned a malformed word for %s", address)
		}
		if word != evmZeroWord {
			return "set", nil
		}
	}
	return "zero", nil
}

// diffDeployedBytecode compares the deployed runtime code at the pinned block
// with the compiled artifact of the audited revision. Metadata and immutable
// bytes are normalized only after an exact comparison fails, so the record
// always states which comparison established the match.
func diffDeployedBytecode(ctx context.Context, client *http.Client, pin chainPin, address, artifactName, projectRoot string) ([]byte, error) {
	artifact, immutables, err := loadForgeRuntimeArtifact(projectRoot, artifactName)
	if err != nil {
		return nil, err
	}
	reader, err := pinReader(ctx, client, pin)
	if err != nil {
		return nil, err
	}
	deployed, err := reader.code(ctx, address)
	if err != nil {
		return nil, err
	}
	if err := reader.confirmPinStillHolds(ctx, pin); err != nil {
		return nil, err
	}
	report := bytecodeDiffReport{
		ChainID: pin.ChainID, BlockNumber: pin.BlockNumber, BlockHash: pin.BlockHash, EndpointAlias: pin.Alias,
		Address: address, Artifact: artifactName,
		DeployedSize: len(deployed), ArtifactSize: len(artifact),
		DeployedDigest: sha256Digest(deployed), ArtifactDigest: sha256Digest(artifact),
	}
	switch {
	case len(deployed) == 0:
		report.Match = bytecodeMatchMismatch
	case report.DeployedDigest == report.ArtifactDigest:
		report.Match = bytecodeMatchExact
	default:
		normalizedDeployed := normalizeRuntimeBytecode(deployed, immutables)
		normalizedArtifact := normalizeRuntimeBytecode(artifact, immutables)
		report.NormalizedDigest = sha256Digest(normalizedDeployed)
		report.Match = bytecodeMatchMismatch
		if len(normalizedDeployed) > 0 && report.NormalizedDigest == sha256Digest(normalizedArtifact) {
			report.Match = bytecodeMatchNormalized
		}
	}
	return json.Marshal(report)
}

// immutableRange is a byte span the compiler fills at deployment time. The
// deployed code necessarily differs there, so both sides are blanked before
// the normalized comparison.
type immutableRange struct{ start, length int }

// loadForgeRuntimeArtifact reads one Foundry artifact from the staged project
// and returns its runtime bytecode and immutable ranges. The artifact name is
// resolved inside the staged root only.
func loadForgeRuntimeArtifact(projectRoot, name string) ([]byte, []immutableRange, error) {
	if !forgeArtifactPattern.MatchString(name) {
		return nil, nil, fmt.Errorf("artifact %q must be a fully-qualified Foundry name such as Vault.sol:Vault", name)
	}
	file, contract, _ := strings.Cut(name, ":")
	path := filepath.Join(projectRoot, "out", file, contract+".json")
	if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(projectRoot)+string(filepath.Separator)) {
		return nil, nil, fmt.Errorf("artifact %q resolves outside the staged project", name)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- staged project root joined with a pattern-validated artifact name.
	if err != nil {
		return nil, nil, fmt.Errorf("compiled artifact for %s was not found in the staged project: build it before diffing deployed code", name)
	}
	var artifact struct {
		DeployedBytecode struct {
			Object              string `json:"object"`
			ImmutableReferences map[string][]struct {
				Start  int `json:"start"`
				Length int `json:"length"`
			} `json:"immutableReferences"`
		} `json:"deployedBytecode"`
	}
	if err := json.Unmarshal(data, &artifact); err != nil {
		return nil, nil, fmt.Errorf("compiled artifact for %s is not valid JSON: %w", name, err)
	}
	object, ok := strings.CutPrefix(artifact.DeployedBytecode.Object, "0x")
	if !ok || object == "" {
		return nil, nil, fmt.Errorf("compiled artifact for %s carries no deployed bytecode", name)
	}
	runtime, err := hex.DecodeString(strings.ToLower(object))
	if err != nil {
		return nil, nil, fmt.Errorf("compiled artifact for %s carries malformed deployed bytecode", name)
	}
	ranges := []immutableRange{}
	for _, references := range artifact.DeployedBytecode.ImmutableReferences {
		for _, reference := range references {
			if reference.Length > 0 && reference.Start >= 0 {
				ranges = append(ranges, immutableRange{start: reference.Start, length: reference.Length})
			}
		}
	}
	slices.SortFunc(ranges, func(a, b immutableRange) int { return a.start - b.start })
	return runtime, ranges, nil
}

// normalizeRuntimeBytecode blanks immutable spans and drops the trailing CBOR
// metadata block, which encodes source paths and compiler settings rather than
// behavior. It is only ever used for the fallback comparison.
func normalizeRuntimeBytecode(code []byte, immutables []immutableRange) []byte {
	out := slices.Clone(code)
	for _, span := range immutables {
		if span.start+span.length > len(out) {
			continue
		}
		for i := span.start; i < span.start+span.length; i++ {
			out[i] = 0
		}
	}
	if len(out) < 2 {
		return out
	}
	length := int(out[len(out)-2])<<8 | int(out[len(out)-1])
	if length > 0 && length+2 <= len(out) {
		out = out[:len(out)-length-2]
	}
	return out
}
