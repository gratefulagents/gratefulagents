package securitytoolpacks

import (
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"net/url"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Registry is immutable after construction and is the only source from which
// an execution request can be built. Callers cannot supply an executable or
// append untyped arguments.
type Registry struct {
	manifest Manifest
	byName   map[string]Tool
}

type ApplicabilityError struct {
	Tool       string
	TargetType string
}

func (e *ApplicabilityError) Error() string {
	return fmt.Sprintf("tool %s does not accept target type %q", e.Tool, e.TargetType)
}

func cloneManifest(in Manifest) Manifest {
	b, _ := json.Marshal(in)
	var out Manifest
	_ = json.Unmarshal(b, &out)
	return out
}

func cloneTool(in Tool) Tool {
	return cloneManifest(Manifest{Tools: []Tool{in}}).Tools[0]
}

func NewRegistry(m Manifest) (*Registry, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	m = cloneManifest(m)
	r := &Registry{manifest: m, byName: make(map[string]Tool, len(m.Tools))}
	for _, t := range m.Tools {
		r.byName[t.Name] = t
	}
	return r, nil
}

func (r *Registry) Manifest() Manifest { return cloneManifest(r.manifest) }
func (r *Registry) Tool(name string) (Tool, bool) {
	t, ok := r.byName[name]
	if !ok {
		return Tool{}, false
	}
	return cloneManifest(Manifest{SchemaVersion: r.manifest.SchemaVersion, Tools: []Tool{t}}).Tools[0], true
}

// Invocation contains an argv vector, never a shell command string.
type Invocation struct {
	Image     string
	Digest    string
	Argv      []string
	Budgets   Budgets
	Network   bool
	Privilege string
}

//nolint:gocyclo // Validation is intentionally centralized before constructing fixed argv.
func (r *Registry) BuildInvocation(cfg RunConfig) (Invocation, Tool, error) {
	stored, ok := r.byName[cfg.Tool]
	if !ok {
		return Invocation{}, Tool{}, fmt.Errorf("unknown registered tool %q", cfg.Tool)
	}
	t := cloneTool(stored)
	if !t.Enabled {
		return Invocation{}, Tool{}, fmt.Errorf("tool %s is disabled: %s", t.Name, t.DisabledReason)
	}
	if !slices.Contains(t.TargetTypes, cfg.Target.Type) {
		return Invocation{}, Tool{}, &ApplicabilityError{Tool: t.Name, TargetType: cfg.Target.Type}
	}
	expectedMedia := map[string]string{
		"aderyn":               "application/vnd.gratefulagents.solidity-project.v1+directory",
		"forge-security-tests": "application/vnd.gratefulagents.foundry-security-project.v1+directory",
	}
	if media := expectedMedia[t.Name]; media != "" && cfg.Target.MediaType != media {
		return Invocation{}, Tool{}, fmt.Errorf("tool %s requires target media type %s", t.Name, media)
	}
	if cfg.Target.Revision == "" || !digestPattern.MatchString(cfg.Target.Digest) {
		return Invocation{}, Tool{}, fmt.Errorf("target revision and immutable sha256 digest are required")
	}
	if t.Requirements.Network && len(cfg.Scope) == 0 {
		return Invocation{}, Tool{}, fmt.Errorf("tool %s requires explicit target scope", t.Name)
	}
	for i, scope := range cfg.Scope {
		if !validScope(scope) {
			return Invocation{}, Tool{}, fmt.Errorf("target scope entry %d is invalid", i)
		}
	}
	if t.Requirements.Network && !scopeAllowsTarget(cfg.Target.Locator, cfg.Scope) {
		return Invocation{}, Tool{}, fmt.Errorf("target %q is outside configured scope", cfg.Target.Locator)
	}
	known := map[string]Argument{}
	for _, a := range t.Arguments {
		known[a.Name] = a
	}
	for k, v := range cfg.Arguments {
		a, ok := known[k]
		if !ok {
			return Invocation{}, Tool{}, fmt.Errorf("tool %s has no argument %q", t.Name, k)
		}
		if err := validateArg(a, v); err != nil {
			return Invocation{}, Tool{}, err
		}
		if strings.Contains(v, "{{") || strings.Contains(v, "}}") {
			return Invocation{}, Tool{}, fmt.Errorf("argument %q contains reserved placeholder syntax", k)
		}
	}
	if rateValue := cfg.Arguments["rate"]; rateValue != "" {
		rate, _ := strconv.Atoi(rateValue)
		if rate < 1 || rate > 1000 {
			return Invocation{}, Tool{}, fmt.Errorf("argument %q must be between 1 and 1000", "rate")
		}
	}
	if t.Name == "naabu" {
		if _, err := netip.ParseAddr(cfg.Target.Locator); err != nil {
			if _, prefixErr := netip.ParsePrefix(cfg.Target.Locator); prefixErr != nil {
				return Invocation{}, Tool{}, fmt.Errorf("naabu target must be an IP address or CIDR prefix")
			}
		}
		portCount, err := validatePortList(cfg.Arguments["ports"])
		if err != nil {
			return Invocation{}, Tool{}, err
		}
		targetCount := 1
		if prefix, prefixErr := netip.ParsePrefix(cfg.Target.Locator); prefixErr == nil {
			hostBits := prefix.Addr().BitLen() - prefix.Bits()
			if hostBits > 20 {
				return Invocation{}, Tool{}, fmt.Errorf("address prefix exceeds request budget")
			}
			targetCount = 1 << hostBits
		}
		if portCount*targetCount > t.Budgets.Requests {
			return Invocation{}, Tool{}, fmt.Errorf("address and port count exceeds request budget")
		}
	}
	if strings.Contains(cfg.Target.Locator, "{{") || strings.Contains(cfg.Target.Locator, "}}") {
		return Invocation{}, Tool{}, fmt.Errorf("target locator contains reserved placeholder syntax")
	}
	for _, a := range t.Arguments {
		if a.Required && cfg.Arguments[a.Name] == "" {
			return Invocation{}, Tool{}, fmt.Errorf("argument %q is required", a.Name)
		}
	}
	keys := []string{"target"}
	values := map[string]string{"target": cfg.Target.Locator}
	for _, a := range t.Arguments {
		if v, ok := cfg.Arguments[a.Name]; ok {
			keys = append(keys, a.Name)
			values[a.Name] = v
		}
	}
	if cfg.Seed != nil {
		keys = append(keys, "seed")
		values["seed"] = strconv.FormatInt(*cfg.Seed, 10)
	} else if t.SeedSupported {
		return Invocation{}, Tool{}, fmt.Errorf("tool %s requires an explicit seed", t.Name)
	}
	argv := make([]string, len(t.Invocation))
	for i, token := range t.Invocation {
		argv[i] = token
		for _, k := range keys {
			argv[i] = strings.ReplaceAll(argv[i], "{{"+k+"}}", values[k])
		}
		if strings.Contains(argv[i], "{{") {
			return Invocation{}, Tool{}, fmt.Errorf("unresolved invocation token %q", argv[i])
		}
	}
	return Invocation{Image: t.Image, Digest: t.ImageDigest, Argv: argv, Budgets: t.Budgets, Network: t.Requirements.Network, Privilege: t.Requirements.Privilege}, t, nil
}

func validScope(scope string) bool {
	if scope == "" || scope != strings.TrimSpace(scope) || strings.ContainsAny(scope, "\x00\r\n\t ") {
		return false
	}
	if net.ParseIP(scope) != nil {
		return true
	}
	if _, _, err := net.ParseCIDR(scope); err == nil {
		return true
	}
	if u, err := url.ParseRequestURI(scope); err == nil && u.IsAbs() && u.Hostname() != "" && u.User == nil {
		return true
	}
	if host, port, err := net.SplitHostPort(scope); err == nil && validScopeHost(host) {
		n, err := strconv.Atoi(port)
		return err == nil && n > 0 && n <= 65535
	}
	return validScopeHost(scope)
}

func scopeAllowsTarget(target string, scopes []string) bool {
	for _, scope := range scopes {
		if target == scope {
			return true
		}
		targetURL, targetURLErr := url.ParseRequestURI(target)
		scopeURL, scopeURLErr := url.ParseRequestURI(scope)
		if targetURLErr == nil && scopeURLErr == nil && targetURL.IsAbs() && scopeURL.IsAbs() &&
			strings.EqualFold(targetURL.Scheme, scopeURL.Scheme) &&
			strings.EqualFold(targetURL.Hostname(), scopeURL.Hostname()) &&
			effectivePort(targetURL) == effectivePort(scopeURL) && pathWithinScope(targetURL.EscapedPath(), scopeURL.EscapedPath()) {
			return true
		}
		targetPrefix, targetPrefixErr := netip.ParsePrefix(target)
		scopePrefix, scopePrefixErr := netip.ParsePrefix(scope)
		if targetPrefixErr == nil && scopePrefixErr == nil && scopePrefix.Bits() <= targetPrefix.Bits() && scopePrefix.Contains(targetPrefix.Addr()) {
			return true
		}
		targetAddr, targetAddrErr := netip.ParseAddr(target)
		if targetAddrErr == nil && scopePrefixErr == nil && scopePrefix.Contains(targetAddr) {
			return true
		}
	}
	return false
}

func effectivePort(value *url.URL) string {
	if value.Port() != "" {
		return value.Port()
	}
	switch strings.ToLower(value.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func pathWithinScope(target, scope string) bool {
	if scope == "" || scope == "/" {
		return true
	}
	scope = strings.TrimSuffix(scope, "/")
	return target == scope || strings.HasPrefix(target, scope+"/")
}

func validScopeHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func validateArg(a Argument, value string) error {
	switch a.Type {
	case "string", "path", "url", "cidr":
	case "ports":
		if _, err := validatePortList(value); err != nil {
			return fmt.Errorf("argument %q: %w", a.Name, err)
		}
	case "integer":
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return fmt.Errorf("argument %q must be an integer", a.Name)
		}
	case "boolean":
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("argument %q must be boolean", a.Name)
		}
	case "enum":
		if !slices.Contains(a.Enum, value) {
			return fmt.Errorf("argument %q must be one of %v", a.Name, a.Enum)
		}
	default:
		return fmt.Errorf("argument %q has unsupported type %q", a.Name, a.Type)
	}
	return nil
}

func validatePortList(value string) (int, error) {
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return 0, fmt.Errorf("ports must be a non-empty comma-separated list")
	}
	count := 0
	for item := range strings.SplitSeq(value, ",") {
		bounds := strings.Split(item, "-")
		if len(bounds) > 2 {
			return 0, fmt.Errorf("invalid port range %q", item)
		}
		start, err := strconv.Atoi(bounds[0])
		if err != nil || start < 1 || start > 65535 {
			return 0, fmt.Errorf("invalid port %q", item)
		}
		end := start
		if len(bounds) == 2 {
			end, err = strconv.Atoi(bounds[1])
			if err != nil || end < start || end > 65535 {
				return 0, fmt.Errorf("invalid port range %q", item)
			}
		}
		count += end - start + 1
		if count > 65535 {
			return 0, fmt.Errorf("port list is too large")
		}
	}
	return count, nil
}

// DefaultManifest builds the reviewed v1 wrapper set from release provenance.
// imageDigest identifies the immutable wrapper/image executing the registry.
// The reviewed Nuclei knowledge pin is compiled in; other optional knowledge
// sources must be provided by trusted controller configuration.
func DefaultManifest(imageDigest string, knowledgeDigests map[string]string) Manifest {
	configuredKnowledge := maps.Clone(knowledgeDigests)
	if configuredKnowledge == nil {
		configuredKnowledge = map[string]string{}
	}
	configuredKnowledge["nuclei"] = "sha256:0a3671c37c329bc28fdeeac93140a31d3caa3615a6c259054ec03c517d66d732"
	knowledgeDigests = configuredKnowledge
	const image = "ghcr.io/gratefulagents/security-toolpack"
	base := func(name string, domain Domain, version, adapter, media string, targets, argv []string) Tool {
		return Tool{Name: name, Enabled: true, Domain: domain, Version: version, Image: image, ImageDigest: imageDigest, ToolArtifactDigest: imageDigest, Invocation: argv, TargetTypes: targets, Requirements: Requirements{Privilege: "unprivileged"}, Budgets: Budgets{Timeout: 5 * time.Minute, CPU: 1000, Memory: 1 << 30, Requests: 1000, Concurrency: 4, MaxOutputSize: 16 << 20}, ExitCodes: map[int]Status{0: StatusPass, 1: StatusFindings, 2: StatusError}, OutputMediaType: media, Adapter: adapter, RedactionRules: []string{"authorization", "cookie", "private_key", "configured_sensitive_fields"}, Idempotent: true}
	}
	tools := []Tool{
		base("playwright", DomainWeb, "1.52.0", "json-records", "application/json", []string{"base_url", "browser_script"}, []string{"playwright", "test", "{{target}}", "--reporter=json"}),
		base("owasp-zap", DomainWeb, "2.16.1", "zap-json", "application/json", []string{"base_url", "openapi"}, []string{"zap.sh", "-cmd", "-autorun", "{{target}}"}),
		base("schemathesis", DomainWeb, "4.0.16", "schemathesis-json", "application/json", []string{"openapi"}, []string{"schemathesis", "run", "{{target}}", "--report=json"}),
		base("restler", DomainWeb, "9.2.4", "restler-json", "application/json", []string{"openapi"}, []string{"restler", "fuzz-lean", "--grammar_file", "{{target}}"}),
		base("mitmproxy", DomainWeb, "12.0.0", "har", "application/json", []string{"har", "base_url"}, []string{"mitmdump", "--set", "hardump={{target}}"}),
		base("nuclei", DomainWeb, "3.11.1", "nuclei-jsonl", "application/x-ndjson", []string{"base_url"}, []string{"nuclei", "-u", "{{target}}", "-templates", "@operator/nuclei-reviewed.yaml", "-rate-limit", "{{rate}}", "-concurrency", "1", "-bulk-size", "1", "-jsonl", "-silent", "-disable-update-check", "-no-interactsh"}),
		base("authorization-matrix", DomainWeb, "1.0.0", "authorization-matrix", "application/json", []string{"authorization_matrix"}, []string{"authz-matrix", "--input", "{{target}}", "--json"}),
		base("wycheproof", DomainCrypto, "0.9", "crypto-vectors", "application/json", []string{"crypto_vectors"}, []string{"wycheproof-runner", "--vectors", "{{target}}", "--json"}),
		base("rfc-nist-vectors", DomainCrypto, "2025.1", "crypto-vectors", "application/json", []string{"crypto_vectors"}, []string{"vector-runner", "--vectors", "{{target}}", "--json"}),
		base("dudect", DomainCrypto, "0.7.0", "junit", "application/junit+xml", []string{"crypto_binary"}, []string{"dudect-runner", "{{target}}", "--output=junit"}),
		base("ctgrind", DomainCrypto, "3.25.1", "junit", "application/junit+xml", []string{"crypto_binary"}, []string{"ctgrind", "--xml=yes", "{{target}}"}),
		base("tlsfuzzer", DomainCrypto, "0.0.1", "junit", "application/junit+xml", []string{"tls_service"}, []string{"tlsfuzzer", "--target", "{{target}}", "--junit"}),
		base("crypto-differential", DomainCrypto, "1.0.0", "crypto-vectors", "application/json", []string{"crypto_vectors"}, []string{"crypto-differential", "--spec", "{{target}}", "--json"}),
		base("tamarin", DomainCrypto, "1.10.0", "json-records", "application/json", []string{"protocol_model"}, []string{"tamarin-prover", "{{target}}", "--output=json"}),
		base("proverif", DomainCrypto, "2.05", "json-records", "application/json", []string{"protocol_model"}, []string{"proverif", "-out", "json", "{{target}}"}),
		base("verifpal", DomainCrypto, "0.27.0", "json-records", "application/json", []string{"protocol_model"}, []string{"verifpal", "verify", "{{target}}", "--format=json"}),
		base("openssl-inspect", DomainCrypto, "3.5.0", "openssl-json", "application/json", []string{"key", "certificate", "asn1"}, []string{"openssl-inspect", "--input", "{{target}}", "--json"}),
		base("sslyze", DomainWeb, "6.1.0", "sslyze-json", "application/json", []string{"base_url", "tls_service"}, []string{"sslyze", "--json_out=-", "{{target}}"}),
		base("testssl", DomainNetwork, "3.2.1", "testssl-json", "application/json", []string{"tls_service"}, []string{"testssl.sh", "--jsonfile-pretty=-", "{{target}}"}),
		base("nmap", DomainNetwork, "7.95", "nmap-xml", "application/xml", []string{"address_scope"}, []string{"nmap", "-oX", "-", "{{target}}"}),
		base("tshark", DomainNetwork, "4.4.6", "tshark-json", "application/json", []string{"pcap"}, []string{"tshark", "-r", "{{target}}", "-T", "json"}),
		base("zeek", DomainNetwork, "7.2.2", "zeek-jsonl", "application/x-ndjson", []string{"pcap"}, []string{"zeek", "-Cr", "{{target}}", "LogAscii::use_json=T"}),
		base("suricata", DomainNetwork, "7.0.9", "suricata-eve", "application/x-ndjson", []string{"pcap"}, []string{"suricata", "-r", "{{target}}", "--set", "outputs.1.eve-log.enabled=yes"}),
		base("scapy", DomainNetwork, "2.6.1", "junit", "application/junit+xml", []string{"packet_assertions"}, []string{"scapy-runner", "--input", "{{target}}", "--junit"}),
		base("boofuzz", DomainNetwork, "0.4.2", "junit", "application/junit+xml", []string{"protocol_fixture"}, []string{"boofuzz-runner", "--fixture", "{{target}}", "--junit"}),
		base("naabu", DomainNetwork, "2.6.1", "naabu-jsonl", "application/x-ndjson", []string{"address_scope"}, []string{"naabu", "-host", "{{target}}", "-p", "{{ports}}", "-rate", "{{rate}}", "-c", "4", "-scan-type", "c", "-retries", "1", "-json", "-silent", "-disable-update-check"}),
		base("aderyn", DomainBlockchain, "0.6.8", "sarif", "application/sarif+json", []string{"solidity_project"}, []string{"aderyn", "{{target}}", "--output", "report.sarif", "--stdout", "--skip-update-check"}),
		base("forge-security-tests", DomainBlockchain, "1.7.1", "junit", "application/junit+xml", []string{"foundry_project"}, []string{"forge", "test", "--root", "{{target}}", "--junit", "--fuzz-seed", "{{seed}}", "--offline", "--threads", "1"}),
		base("slither", DomainBlockchain, "0.11.3", "json-records", "application/json", []string{"solidity_project"}, []string{"slither", "{{target}}", "--json", "-"}),
		base("mythril", DomainBlockchain, "0.24.8", "json-records", "application/json", []string{"evm_bytecode", "solidity_contract"}, []string{"myth", "analyze", "{{target}}", "-o", "json"}),
		base("echidna", DomainBlockchain, "2.3.0", "json-records", "application/x-ndjson", []string{"solidity_project"}, []string{"echidna", "{{target}}", "--format", "json", "--seed", "{{seed}}"}),
	}
	liveNetwork := []string{"playwright", "owasp-zap", "schemathesis", "restler", "mitmproxy", "nuclei", "tlsfuzzer", "sslyze", "testssl", "nmap", "boofuzz", "naabu"}
	stateful := []string{"playwright", "owasp-zap", "restler", "mitmproxy", "authorization-matrix", "boofuzz"}
	seeded := []string{"schemathesis", "restler", "crypto-differential", "scapy", "boofuzz", "forge-security-tests", "echidna"}
	// Executable entries are either built into ga-security or installed from the
	// checksum-verified runtime lock. Everything else remains catalog-only.
	executable := []string{"authorization-matrix", "wycheproof", "rfc-nist-vectors", "nuclei", "naabu", "aderyn", "forge-security-tests"}
	knowledgeRequired := []string{"nuclei", "wycheproof", "rfc-nist-vectors", "suricata", "zeek"}
	for i := range tools {
		if digest := lockedToolArtifactDigest(tools[i].Name, runtime.GOARCH); digest != "" {
			tools[i].ToolArtifactDigest = digest
		}
		switch tools[i].Name {
		case "nuclei":
			tools[i].Arguments = []Argument{{Name: "rate", Type: "integer", Required: true}}
		case "naabu":
			tools[i].Arguments = []Argument{{Name: "rate", Type: "integer", Required: true}, {Name: "ports", Type: "ports", Required: true}}
		}
		if digest, ok := knowledgeDigests[tools[i].Name]; ok {
			tools[i].KnowledgeDigests = map[string]string{"bundle": digest}
		} else if slices.Contains(knowledgeRequired, tools[i].Name) {
			tools[i].Enabled = false
			tools[i].DisabledReason = "catalog-only: required knowledge bundle digest was not configured"
		}
		tools[i].Requirements.Network = slices.Contains(liveNetwork, tools[i].Name)
		if slices.Contains(stateful, tools[i].Name) {
			tools[i].Idempotent = false
		}
		if slices.Contains(seeded, tools[i].Name) {
			tools[i].SeedSupported = true
			if !slices.Contains(tools[i].Invocation, "{{seed}}") {
				tools[i].Invocation = append(tools[i].Invocation, "--seed", "{{seed}}")
			}
		}
		if !slices.Contains(executable, tools[i].Name) {
			tools[i].Enabled = false
			tools[i].DisabledReason = "catalog-only: executable wrapper is not implemented"
		}
	}
	return Manifest{SchemaVersion: "security-tool-registry/v1", Tools: tools}
}

// lockedToolArtifactDigest mirrors the extracted-binary checksums in the
// reviewed runtime lock so every replay cites the exact executed artifact.
func lockedToolArtifactDigest(name, arch string) string {
	pins := map[string]map[string]string{
		"nuclei":               {"amd64": "sha256:c49588140f357cbdddd5436dec11201953a4c5390faeec90777f9ee2cfd70251", "arm64": "sha256:f27098e0be0cc370af52274611608ad61896d7f0a024e35b136327d39e725477"},
		"naabu":                {"amd64": "sha256:6c0aac4253aebe95bbc13d4712a5f8caf7db9c9b62d6bc1fb4c56594cfa45165", "arm64": "sha256:635d93e16b2e6423434b361c017d8fd12f354eaebbf04bcf062c97a9d4e2addc"},
		"aderyn":               {"amd64": "sha256:a268d616826901e17717b1bc6368d8b2c063045a46fb99a0c0f657f102d977ca", "arm64": "sha256:773033830116d7628c01f105a4bd0691d1034fc285f37652e8868c8dc14d97e0"},
		"forge-security-tests": {"amd64": "sha256:4f77da0810de94325734855d0ad58d70640aa8a5b2a837608ddf8c26da34355c", "arm64": "sha256:a93076d85e013a45b7050c21b26cf05627f1d64f40b99cf0524fa5facf4d3988"},
	}
	return pins[name][arch]
}
