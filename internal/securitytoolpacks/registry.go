package securitytoolpacks

import (
	"encoding/json"
	"fmt"
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

type notApplicableError struct{ message string }

func (e notApplicableError) Error() string { return e.message }

func cloneManifest(in Manifest) Manifest {
	b, _ := json.Marshal(in)
	var out Manifest
	_ = json.Unmarshal(b, &out)
	return out
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

func (r *Registry) BuildInvocation(cfg RunConfig) (Invocation, Tool, error) {
	t, ok := r.byName[cfg.Tool]
	if !ok {
		return Invocation{}, Tool{}, fmt.Errorf("unknown registered tool %q", cfg.Tool)
	}
	if !slices.Contains(t.TargetTypes, cfg.Target.Type) {
		return Invocation{}, Tool{}, notApplicableError{fmt.Sprintf("tool %s does not accept target type %q", t.Name, cfg.Target.Type)}
	}
	if cfg.Target.Revision == "" || !digestPattern.MatchString(cfg.Target.Digest) {
		return Invocation{}, Tool{}, fmt.Errorf("target revision and immutable sha256 digest are required")
	}
	if t.Requirements.Network && len(cfg.Scope) == 0 {
		return Invocation{}, Tool{}, fmt.Errorf("tool %s requires explicit target scope", t.Name)
	}
	for _, scope := range cfg.Scope {
		if strings.TrimSpace(scope) == "" {
			return Invocation{}, Tool{}, fmt.Errorf("target scope entries must not be empty")
		}
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

func validateArg(a Argument, value string) error {
	switch a.Type {
	case "string", "path", "url", "cidr", "ports":
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

// DefaultManifest builds the reviewed v1 wrapper set from release provenance.
// imageDigest must be the digest reported by the immutable tool-pack OCI image;
// knowledgeDigests must contain the reviewed bundle digests named below. The
// function intentionally does not carry placeholder pins: deployment or release
// configuration must supply verifiable content digests.
func DefaultManifest(imageDigest string, knowledgeDigests map[string]string) Manifest {
	const image = "ghcr.io/gratefulagents/security-toolpack"
	base := func(name string, domain Domain, version, adapter, media string, targets, argv []string) Tool {
		return Tool{Name: name, Domain: domain, Version: version, Image: image, ImageDigest: imageDigest, Invocation: argv, TargetTypes: targets, Requirements: Requirements{Privilege: "unprivileged"}, Budgets: Budgets{Timeout: 5 * time.Minute, CPU: 1000, Memory: 1 << 30, Requests: 1000, Concurrency: 4, MaxOutputSize: 16 << 20}, ExitCodes: map[int]Status{0: StatusPass, 1: StatusFindings, 2: StatusError}, OutputMediaType: media, Adapter: adapter, RedactionRules: []string{"authorization", "cookie", "private_key", "configured_sensitive_fields"}, Idempotent: true}
	}
	tools := []Tool{
		base("playwright", DomainWeb, "1.52.0", "json-records", "application/json", []string{"base_url", "browser_script"}, []string{"playwright", "test", "{{target}}", "--reporter=json"}),
		base("owasp-zap", DomainWeb, "2.16.1", "zap-json", "application/json", []string{"base_url", "openapi"}, []string{"zap.sh", "-cmd", "-autorun", "{{target}}"}),
		base("schemathesis", DomainWeb, "4.0.16", "schemathesis-json", "application/json", []string{"openapi"}, []string{"schemathesis", "run", "{{target}}", "--report=json"}),
		base("restler", DomainWeb, "9.2.4", "restler-json", "application/json", []string{"openapi"}, []string{"restler", "fuzz-lean", "--grammar_file", "{{target}}"}),
		base("mitmproxy", DomainWeb, "12.0.0", "har", "application/json", []string{"har", "base_url"}, []string{"mitmdump", "--set", "hardump={{target}}"}),
		base("nuclei", DomainWeb, "3.4.3", "nuclei-jsonl", "application/x-ndjson", []string{"base_url"}, []string{"nuclei", "-u", "{{target}}", "-jsonl"}),
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
	}
	liveNetwork := []string{"playwright", "owasp-zap", "schemathesis", "restler", "mitmproxy", "nuclei", "authorization-matrix", "tlsfuzzer", "sslyze", "testssl", "nmap", "boofuzz"}
	stateful := []string{"playwright", "owasp-zap", "restler", "mitmproxy", "authorization-matrix", "boofuzz"}
	seeded := []string{"schemathesis", "restler", "crypto-differential", "scapy", "boofuzz"}
	for i := range tools {
		if digest, ok := knowledgeDigests[tools[i].Name]; ok {
			tools[i].KnowledgeDigests = map[string]string{"bundle": digest}
		}
		tools[i].Requirements.Network = slices.Contains(liveNetwork, tools[i].Name)
		if slices.Contains(stateful, tools[i].Name) {
			tools[i].Idempotent = false
		}
		if slices.Contains(seeded, tools[i].Name) {
			tools[i].SeedSupported = true
			tools[i].Invocation = append(tools[i].Invocation, "--seed", "{{seed}}")
		}
	}
	return Manifest{SchemaVersion: "security-tool-registry/v1", Tools: tools}
}
