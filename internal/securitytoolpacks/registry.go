package securitytoolpacks

import (
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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
	expectedMedia := map[string]map[string]string{
		"aderyn": {
			"solidity_project": "application/vnd.gratefulagents.solidity-project.v1+directory",
		},
		"forge-security-tests": {
			"foundry_project": "application/vnd.gratefulagents.foundry-security-project.v1+directory",
		},
		"echidna": {
			"solidity_project": "application/vnd.gratefulagents.solidity-project.v1+directory",
		},
		"mythril": {
			"solidity_contract": "application/vnd.gratefulagents.solidity-contract.v1+source",
			"evm_bytecode":      "application/vnd.gratefulagents.evm-bytecode.v1+hex",
		},
		"slither": {
			"solidity_project": "application/vnd.gratefulagents.solidity-project.v1+directory",
		},
		"semgrep": {
			"semgrep_project": "application/vnd.gratefulagents.semgrep-project.v1+directory",
		},
		"halmos": {
			"foundry_project": "application/vnd.gratefulagents.foundry-security-project.v1+directory",
		},
	}
	if media := expectedMedia[t.Name][cfg.Target.Type]; media != "" && cfg.Target.MediaType != media {
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
	scopeTarget := cfg.Target.Locator
	if baseURL := cfg.Arguments["base_url"]; baseURL != "" {
		scopeTarget = baseURL
	}
	if t.Requirements.Network && !scopeAllowsTarget(scopeTarget, cfg.Scope) {
		return Invocation{}, Tool{}, fmt.Errorf("target %q is outside configured scope", scopeTarget)
	}
	if t.Name == "owasp-zap" {
		if err := validateZAPPlan(cfg.Target.Locator, cfg.Arguments["base_url"], cfg.Scope); err != nil {
			return Invocation{}, Tool{}, err
		}
	}
	if t.Name == "schemathesis" {
		if err := validateOpenAPIBudget(cfg.Target.Locator, 100); err != nil {
			return Invocation{}, Tool{}, err
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
	if rateValue := cfg.Arguments["rate"]; rateValue != "" {
		rate, _ := strconv.Atoi(rateValue)
		if rate < 1 || rate > 1000 {
			return Invocation{}, Tool{}, fmt.Errorf("argument %q must be between 1 and 1000", "rate")
		}
	}
	if t.Name == "naabu" || t.Name == "nmap" {
		if _, err := netip.ParseAddr(cfg.Target.Locator); err != nil {
			if _, prefixErr := netip.ParsePrefix(cfg.Target.Locator); prefixErr != nil {
				return Invocation{}, Tool{}, fmt.Errorf("tool %s target must be an IP address or CIDR prefix", t.Name)
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
	if t.Name == "mythril" && cfg.Target.Type == "evm_bytecode" {
		for i, token := range argv {
			if token == cfg.Target.Locator {
				argv = slices.Insert(argv, i, "--codefile")
				break
			}
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
		return safeURLPath(u.EscapedPath())
	}
	if host, port, err := net.SplitHostPort(scope); err == nil && validScopeHost(host) {
		n, err := strconv.Atoi(port)
		return err == nil && n > 0 && n <= 65535
	}
	return validScopeHost(scope)
}

func scopeAllowsTarget(target string, scopes []string) bool {
	for _, scope := range scopes {
		targetURL, targetURLErr := url.ParseRequestURI(target)
		scopeURL, scopeURLErr := url.ParseRequestURI(scope)
		if targetURLErr == nil && scopeURLErr == nil && targetURL.IsAbs() && scopeURL.IsAbs() &&
			strings.EqualFold(targetURL.Scheme, scopeURL.Scheme) &&
			strings.EqualFold(targetURL.Hostname(), scopeURL.Hostname()) &&
			effectivePort(targetURL) == effectivePort(scopeURL) && safeURLPath(targetURL.EscapedPath()) && safeURLPath(scopeURL.EscapedPath()) && pathWithinScope(targetURL.EscapedPath(), scopeURL.EscapedPath()) {
			return true
		}
		if target == scope && (targetURLErr != nil || !targetURL.IsAbs()) {
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

func safeURLPath(escaped string) bool {
	lower := strings.ToLower(escaped)
	if strings.Contains(lower, "%25") || strings.Contains(lower, "%2e") || strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") {
		return false
	}
	decoded, err := url.PathUnescape(escaped)
	if err != nil || strings.Contains(decoded, "\\") {
		return false
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
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

func validateOpenAPIBudget(path string, maxOperations int) error {
	data, err := os.ReadFile(path) // #nosec G304 -- immutable typed target.
	if err != nil {
		return fmt.Errorf("read OpenAPI specification: %w", err)
	}
	if len(data) > 4<<20 {
		return fmt.Errorf("OpenAPI specification exceeds 4 MiB limit")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse OpenAPI specification: %w", err)
	}
	root := &document
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	paths := yamlMappingValue(root, "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		return fmt.Errorf("OpenAPI specification must contain paths")
	}
	methods := map[string]bool{"get": true, "put": true, "post": true, "delete": true, "options": true, "head": true, "patch": true, "trace": true}
	operations := 0
	for i := 0; i+1 < len(paths.Content); i += 2 {
		pathItem := paths.Content[i+1]
		if pathItem.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(pathItem.Content); j += 2 {
			if methods[strings.ToLower(pathItem.Content[j].Value)] {
				operations++
			}
		}
	}
	if operations == 0 || operations > maxOperations {
		return fmt.Errorf("OpenAPI operation count %d is outside deterministic budget 1..%d", operations, maxOperations)
	}
	var rejectExternalRefs func(*yaml.Node) error
	rejectExternalRefs = func(node *yaml.Node) error {
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				if node.Content[i].Value == "$ref" && !strings.HasPrefix(node.Content[i+1].Value, "#") {
					return fmt.Errorf("external OpenAPI references are not allowed")
				}
			}
		}
		for _, child := range node.Content {
			if err := rejectExternalRefs(child); err != nil {
				return err
			}
		}
		return nil
	}
	return rejectExternalRefs(&document)
}

func validateZAPPlan(path, configuredBaseURL string, scopes []string) error {
	data, err := os.ReadFile(path) // #nosec G304 -- the immutable target path is supplied by the typed request.
	if err != nil {
		return fmt.Errorf("read ZAP plan: %w", err)
	}
	if len(data) > 2<<20 {
		return fmt.Errorf("ZAP plan exceeds 2 MiB limit")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse ZAP plan: %w", err)
	}
	if err := validateYAMLStructure(&document); err != nil {
		return fmt.Errorf("ZAP plan structure: %w", err)
	}
	urlCount := 0
	var walk func(*yaml.Node) error
	walk = func(node *yaml.Node) error {
		if node.Kind == yaml.AliasNode {
			return fmt.Errorf("ZAP plan aliases are not allowed")
		}
		if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
			if strings.Contains(node.Value, "${") || strings.Contains(node.Value, "{{") {
				return fmt.Errorf("ZAP plan substitutions are not allowed")
			}
			if parsed, parseErr := url.ParseRequestURI(node.Value); parseErr == nil && parsed.IsAbs() {
				parsed.Scheme = strings.ToLower(parsed.Scheme)
				if parsed.Scheme != "http" && parsed.Scheme != "https" {
					return fmt.Errorf("ZAP plan URI scheme %q is not allowed", parsed.Scheme)
				}
				canonical := parsed.String()
				urlCount++
				if !scopeAllowsTarget(canonical, scopes) {
					return fmt.Errorf("ZAP plan URL %q is outside configured scope", node.Value)
				}
			}
		}
		for _, child := range node.Content {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(&document); err != nil {
		return err
	}
	if urlCount == 0 || !scopeAllowsTarget(configuredBaseURL, scopes) {
		return fmt.Errorf("ZAP plan must contain an in-scope absolute URL")
	}
	root := &document
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	if err := validateZAPEnvironment(root, configuredBaseURL); err != nil {
		return err
	}
	jobs := yamlMappingValue(root, "jobs")
	if jobs == nil || jobs.Kind != yaml.SequenceNode {
		return fmt.Errorf("ZAP plan must declare jobs")
	}
	allowed := map[string]bool{"passiveScan-wait": true, "spider": true, "spiderAjax": true, "openapi": true, "activeScan": true, "report": true}
	reportFound := false
	scanFound := false
	for _, job := range jobs.Content {
		jobType := yamlMappingValue(job, "type")
		if jobType == nil || !allowed[jobType.Value] {
			return fmt.Errorf("ZAP plan job type %q is not allowed", scalarValue(jobType))
		}
		if err := requireYAMLKeys(job, "type", "parameters", "enabled"); err != nil {
			return fmt.Errorf("ZAP %s job: %w", jobType.Value, err)
		}
		if enabled := yamlMappingValue(job, "enabled"); enabled != nil && !strings.EqualFold(enabled.Value, "true") {
			return fmt.Errorf("ZAP scan jobs cannot be disabled")
		}
		if err := validateZAPJobParameters(jobType.Value, yamlMappingValue(job, "parameters")); err != nil {
			return err
		}
		if slices.Contains([]string{"spider", "spiderAjax", "openapi", "activeScan"}, jobType.Value) {
			scanFound = true
		}
		if jobType.Value != "report" {
			continue
		}
		params := yamlMappingValue(job, "parameters")
		if scalarValue(yamlMappingValue(params, "template")) != "traditional-json" || scalarValue(yamlMappingValue(params, "reportDir")) != "/work" || scalarValue(yamlMappingValue(params, "reportFile")) != "zap-report" {
			return fmt.Errorf("ZAP report job must write traditional-json to /work/zap-report.json")
		}
		reportFound = true
	}
	if !scanFound {
		return fmt.Errorf("ZAP plan must contain a request-producing scan job")
	}
	if !reportFound {
		return fmt.Errorf("ZAP plan must contain the required report job")
	}
	return nil
}

func validateYAMLStructure(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode {
		return fmt.Errorf("aliases are not allowed")
	}
	if node.Kind == yaml.MappingNode {
		if len(node.Content)%2 != 0 {
			return fmt.Errorf("mapping has an unmatched key")
		}
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "" {
				return fmt.Errorf("mapping keys must be non-empty strings")
			}
			if seen[key.Value] {
				return fmt.Errorf("duplicate mapping key %q", key.Value)
			}
			seen[key.Value] = true
			if err := validateYAMLStructure(node.Content[i+1]); err != nil {
				return err
			}
		}
		return nil
	}
	for _, child := range node.Content {
		if err := validateYAMLStructure(child); err != nil {
			return err
		}
	}
	return nil
}

func validateZAPEnvironment(root *yaml.Node, configuredBaseURL string) error {
	if err := requireYAMLKeys(root, "env", "jobs"); err != nil {
		return fmt.Errorf("ZAP plan: %w", err)
	}
	env := yamlMappingValue(root, "env")
	if env == nil || env.Kind != yaml.MappingNode {
		return fmt.Errorf("ZAP plan env must be a mapping")
	}
	if err := requireYAMLKeys(env, "contexts"); err != nil {
		return fmt.Errorf("ZAP env: %w", err)
	}
	contexts := yamlMappingValue(env, "contexts")
	if contexts == nil || contexts.Kind != yaml.SequenceNode || len(contexts.Content) == 0 {
		return fmt.Errorf("ZAP plan requires at least one context")
	}
	base, err := url.ParseRequestURI(configuredBaseURL)
	if err != nil || base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("ZAP base URL must not contain query or fragment")
	}
	baseFound := false
	for _, context := range contexts.Content {
		if err := requireYAMLKeys(context, "name", "urls"); err != nil {
			return fmt.Errorf("ZAP context: %w", err)
		}
		if scalarValue(yamlMappingValue(context, "name")) == "" {
			return fmt.Errorf("ZAP context name is required")
		}
		urls := yamlMappingValue(context, "urls")
		if urls == nil || urls.Kind != yaml.SequenceNode || len(urls.Content) == 0 {
			return fmt.Errorf("ZAP context urls are required")
		}
		for _, item := range urls.Content {
			candidate, parseErr := url.ParseRequestURI(item.Value)
			if parseErr != nil || candidate.RawQuery != "" || candidate.Fragment != "" {
				return fmt.Errorf("ZAP context URL is invalid")
			}
			if scopeAllowsTarget(item.Value, []string{configuredBaseURL}) && scopeAllowsTarget(configuredBaseURL, []string{item.Value}) {
				baseFound = true
			}
		}
	}
	if !baseFound {
		return fmt.Errorf("ZAP context must contain the configured base URL")
	}
	return nil
}

func validateZAPJobParameters(jobType string, params *yaml.Node) error {
	if params == nil {
		params = &yaml.Node{Kind: yaml.MappingNode}
	}
	if params.Kind != yaml.MappingNode {
		return fmt.Errorf("ZAP %s parameters must be a mapping", jobType)
	}
	allowed := map[string][]string{
		"spider":           {"context", "maxDuration", "maxDepth", "maxChildren"},
		"spiderAjax":       {"context", "maxDuration", "maxCrawlDepth", "maxCrawlStates"},
		"openapi":          {"context", "apiUrl", "targetUrl"},
		"activeScan":       {"context", "policy", "maxRuleDurationInMins", "maxScanDurationInMins"},
		"passiveScan-wait": {"maxDuration"},
		"report":           {"template", "reportDir", "reportFile", "reportTitle"},
	}
	if err := requireYAMLKeys(params, allowed[jobType]...); err != nil {
		return fmt.Errorf("ZAP %s job: %w", jobType, err)
	}
	for i := 0; i+1 < len(params.Content); i += 2 {
		key, value := params.Content[i].Value, params.Content[i+1].Value
		if strings.HasPrefix(key, "max") {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 1000 {
				return fmt.Errorf("ZAP %s %s must be between 1 and 1000", jobType, key)
			}
		}
	}
	return nil
}

func requireYAMLKeys(node *yaml.Node, allowed ...string) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return fmt.Errorf("value must be a mapping")
	}
	set := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		set[key] = true
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if !set[node.Content[i].Value] {
			return fmt.Errorf("field %q is not allowed", node.Content[i].Value)
		}
	}
	return nil
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func scalarValue(node *yaml.Node) string {
	if node == nil {
		return ""
	}
	return node.Value
}

func validateArg(a Argument, value string) error {
	switch a.Type {
	case "string", "path", "cidr":
	case "url":
		parsed, err := url.ParseRequestURI(value)
		if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("argument %q must be an absolute HTTP(S) URL", a.Name)
		}
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
		return Tool{Name: name, Enabled: true, Domain: domain, Version: version, Image: image, ImageDigest: imageDigest, ToolArtifactDigest: imageDigest, WrapperDigest: imageDigest, Invocation: argv, TargetTypes: targets, Requirements: Requirements{Privilege: "unprivileged"}, Budgets: Budgets{Timeout: 5 * time.Minute, CPU: 1000, Memory: 1 << 30, Requests: 1000, Concurrency: 4, MaxOutputSize: 16 << 20}, ExitCodes: map[int]Status{0: StatusPass, 1: StatusFindings, 2: StatusError}, OutputMediaType: media, Adapter: adapter, RedactionRules: []string{"authorization", "cookie", "private_key", "configured_sensitive_fields"}, Idempotent: true}
	}
	tools := []Tool{
		base("playwright", DomainWeb, "1.52.0", "json-records", "application/json", []string{"base_url", "browser_script"}, []string{"playwright", "test", "{{target}}", "--reporter=json"}),
		base("owasp-zap", DomainWeb, "2.16.1", "zap-json", "application/json", []string{"zap_plan"}, []string{"zap.sh", "-dir", "/work/.ZAP", "-cmd", "-autorun", "{{target}}"}),
		base("schemathesis", DomainWeb, "4.0.16", "junit", "application/junit+xml", []string{"openapi"}, []string{"schemathesis", "run", "{{target}}", "--url", "{{base_url}}", "--report", "junit", "--report-junit-path", "/work/result.xml", "--workers", "1", "--max-examples", "10", "--rate-limit", "10/s"}),
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
		base("nmap", DomainNetwork, "7.95", "nmap-xml", "application/xml", []string{"address_scope"}, []string{"nmap", "-sT", "-n", "-Pn", "-p", "{{ports}}", "--max-rate", "{{rate}}", "-oX", "-", "{{target}}"}),
		base("tshark", DomainNetwork, "4.4.6", "tshark-json", "application/json", []string{"pcap"}, []string{"tshark", "-r", "{{target}}", "-T", "json"}),
		base("zeek", DomainNetwork, "7.2.2", "zeek-jsonl", "application/x-ndjson", []string{"pcap"}, []string{"zeek", "-Cr", "{{target}}", "LogAscii::use_json=T"}),
		base("suricata", DomainNetwork, "7.0.9", "suricata-eve", "application/x-ndjson", []string{"pcap"}, []string{"suricata", "-r", "{{target}}", "-l", "/work", "--set", "outputs.1.eve-log.enabled=yes"}),
		base("scapy", DomainNetwork, "2.6.1", "junit", "application/junit+xml", []string{"packet_assertions"}, []string{"scapy-runner", "--input", "{{target}}", "--junit"}),
		base("boofuzz", DomainNetwork, "0.4.2", "junit", "application/junit+xml", []string{"protocol_fixture"}, []string{"boofuzz-runner", "--fixture", "{{target}}", "--junit"}),
		base("naabu", DomainNetwork, "2.6.1", "naabu-jsonl", "application/x-ndjson", []string{"address_scope"}, []string{"naabu", "-host", "{{target}}", "-p", "{{ports}}", "-rate", "{{rate}}", "-c", "4", "-scan-type", "c", "-retries", "1", "-json", "-silent", "-disable-update-check"}),
		base("aderyn", DomainBlockchain, "0.6.8", "sarif", "application/sarif+json", []string{"solidity_project"}, []string{"aderyn", "{{target}}", "--output", "report.sarif", "--stdout", "--skip-update-check"}),
		base("forge-security-tests", DomainBlockchain, "1.7.1", "junit", "application/junit+xml", []string{"foundry_project"}, []string{"forge", "test", "--root", "{{target}}", "--junit", "--fuzz-seed", "{{seed}}", "--offline", "--threads", "1"}),
		base("slither", DomainBlockchain, "0.11.3", "slither-json", "application/json", []string{"solidity_project"}, []string{"slither", "{{target}}", "--json", "-"}),
		base("mythril", DomainBlockchain, "0.24.8", "mythril-json", "application/json", []string{"evm_bytecode", "solidity_contract"}, []string{"myth", "analyze", "{{target}}", "-o", "json", "--strategy", "bfs", "--max-depth", "64", "--call-depth-limit", "3", "--loop-bound", "3", "--transaction-count", "2", "--execution-timeout", "240", "--solver-timeout", "10000", "--create-timeout", "30", "--no-onchain-data"}),
		base("echidna", DomainBlockchain, "2.3.0", "echidna-json", "application/json", []string{"solidity_project"}, []string{"echidna", "{{target}}", "--format", "json", "--seed", "{{seed}}", "--workers", "1", "--test-limit", "10000", "--seq-len", "32", "--shrink-limit", "5000", "--disable-slither"}),
		base("halmos", DomainBlockchain, "0.3.3", "halmos-json", "application/json", []string{"foundry_project"}, []string{"halmos", "--root", "{{target}}", "--solver", "z3", "--loop", "2", "--width", "64", "--depth", "128", "--json-output", "/work/halmos.json"}),
		base("semgrep", DomainBlockchain, "1.172.0", "sarif", "application/sarif+json", []string{"semgrep_project"}, []string{"semgrep", "scan", "--config", "/tmp/input/.semgrep.yml", "--sarif", "--metrics", "off", "{{target}}"}),
	}
	liveNetwork := []string{"playwright", "owasp-zap", "schemathesis", "restler", "mitmproxy", "nuclei", "tlsfuzzer", "sslyze", "testssl", "nmap", "boofuzz", "naabu"}
	stateful := []string{"playwright", "owasp-zap", "restler", "mitmproxy", "authorization-matrix", "boofuzz"}
	seeded := []string{"schemathesis", "restler", "crypto-differential", "scapy", "boofuzz", "forge-security-tests", "echidna"}
	// Executable entries are either built into ga-security or installed from the
	// checksum-verified runtime lock. Everything else remains catalog-only.
	executable := []string{"authorization-matrix", "wycheproof", "rfc-nist-vectors", "owasp-zap", "schemathesis", "sslyze", "nuclei", "nmap", "zeek", "suricata", "naabu", "aderyn", "forge-security-tests", "echidna", "mythril", "slither", "semgrep", "halmos"}
	knowledgeRequired := []string{"nuclei", "wycheproof", "rfc-nist-vectors", "suricata", "zeek"}
	packagingBlockers := map[string]string{}
	ociTools := map[string]struct{ image, digest, amd64, arm64, root, executable, output string }{
		"owasp-zap":    {"docker.io/zaproxy/zap-stable", "sha256:7840969c7c9fead565bf9734b12f49f6886db90b1d35b1f74d79710bbd081dab", "sha256:65f8bee15a648ca4a0b6a25e1096fc76af6eea42ab2d75f2a9649981225f30b8", "sha256:7d6bc478bd0750a094349b2e9710a4e33b84e003ae4341f2f2ae7245ec1c5065", "owasp-zap", "/zap/zap.sh", "/work/zap-report.json"},
		"schemathesis": {"docker.io/schemathesis/schemathesis", "sha256:153e544c9eefd31c7a0aabc40c7d90bf66c36915e2e4ccba968319da453006b2", "sha256:7f507383fc96256c1de89e8ac2fd9e00525cd46fee0be39d29dac286315fa414", "sha256:99f0b99bb8a44beb22d97fd12643d0990a28b13a8e0dd91d2ace054500373271", "schemathesis", "/usr/local/bin/schemathesis", "/work/result.xml"},
		"sslyze":       {"docker.io/nablac0d3/sslyze", "sha256:e6d59470e380ecb626e831d1c3e006a410f7081266171054c0ce616ee03627d3", "sha256:b2a1cbb8cb716a215ea3e34ab2b8db51a149c8d7a04b8a9f146a70c76d783278", "sha256:5aba89895bc4161df0cdcd8126cb4f3e9c9e4eaf2f0462c5979e4cd38ab80e9f", "sslyze", "/opt/venv/bin/sslyze", ""},
		"nmap":         {"docker.io/instrumentisto/nmap", "sha256:3cca6ece8de5a571c956022ec6c2cf343da8c4416fa36e1891e8c33623cfc845", "sha256:42dc1d797c6f716ef192ac49426a19506bd6d27fc4002b7ca686796452c0b050", "sha256:6b200daa02b7b1a6628df3d815744fba596230137237e270640e788c4a0a65cb", "nmap", "/usr/bin/nmap", ""},
		"zeek":         {"docker.io/zeek/zeek", "sha256:5a4712846e75fab70dbf3c329dbc7191f7057fb7351de157ee18344cf1bad85a", "sha256:c01e13d3bb837fdbccb26cddfab73c0cf8a9f3dba1eb9d181b00f412530bb4f6", "sha256:e53b6b22aaa753010ea356c5a691435a80a9aa0935721dcfd582dc76dd38572b", "zeek", "/usr/local/zeek/bin/zeek", "/work/notice.log"},
		"suricata":     {"docker.io/jasonish/suricata", "sha256:a1b835b83c62c8c5130dcfe4072244ab7fc1bf37ebf472bfb6b2519d98a2e36a", "sha256:559a07fcccae439ffdabd05a4969e1feb74cc43f88ea456cc544a20b9b148123", "sha256:6a0b4d02f9174a74e52c904bbd10d344d024bbebc86283866f92096c09be31b0", "suricata", "/usr/bin/suricata", "/work/eve.json"},
		"mythril":      {"docker.io/mythril/myth", "sha256:49e11758e359d0b410f648df5bbcba28a52e091a78e4772b5c02b9043666b4ff", "sha256:ca947a2a79204667ae2ae93ea6aaaca0cea669f61bc4db6958e7556ea263bd80", "sha256:831577a2cf58deb5df758911e6b2e75b2aeb3a59c8c29f15127c2cedf992617d", "mythril", "/usr/local/bin/myth", ""},
		"slither":      {"ghcr.io/trailofbits/eth-security-toolbox", "sha256:65b53faf87985c6b43a98ac0da9158235715cb767bf1fe68e2e3f94ccb281978", "sha256:28ce0f9b27312f6ed1137495aef70744dc2d6ff8e6d5c9147ec9e31a63ff86a8", "sha256:98b90a826a996507e6b1015a7850b2e8de30a3d80f4ec7deaddbf00e050d5152", "slither", "/home/ethsec/.local/bin/slither", ""},
		"semgrep":      {"docker.io/semgrep/semgrep", "sha256:65dcd4408adda7c183a6b4550cb1e9b19f7f627a6fbb7e0559bd466bedc44d7b", "sha256:a8298d1c09c84b9a0bbc75ec915e37023fc4657360b6dbfa645261d2353a366c", "sha256:318382dd1d95e4e8ae2975be3a52b6847843c73ab49ff21ba29b479cbda8d027", "semgrep", "/usr/local/bin/semgrep", ""},
		"halmos":       {"docker.io/library/python:3.11-slim-bookworm", "sha256:d29f48a31a8b408ed19272ca1e7b10ebae13b240a27e862d3d4217c528e2e0c3", "sha256:77923445c077d8eb971b14b2b114a1d9cd4a87edb4c75654820ca4832ee8cb15", "sha256:ecb0ac954790dd64a0d518d699b9c61a91780c42b0d877c802dbaffd04db66f9", "halmos", "/opt/halmos/bin/halmos", "/work/halmos.json"},
	}
	for i := range tools {
		if digest := lockedToolArtifactDigest(tools[i].Name, runtime.GOARCH); digest != "" {
			tools[i].ToolArtifactDigest = digest
		}
		if oci, ok := ociTools[tools[i].Name]; ok {
			tools[i].Image = oci.image + "@" + oci.digest
			tools[i].ImageDigest = oci.digest
			tools[i].ToolArtifactDigest = oci.digest
			if tools[i].Name == "halmos" {
				tools[i].ToolArtifactDigest = "sha256:7ac9f37f8554d8354a7a924eb81393fe30f1bbe851e07c4c35f33a935f53593f"
			}
			tools[i].PlatformDigests = map[string]string{"amd64": oci.amd64, "arm64": oci.arm64}
			if tools[i].Name == "halmos" {
				tools[i].PlatformDigests = map[string]string{
					"amd64": "sha256:a80b8016e9a409a38d54ff300af5aa37cbb0ae281faaa37afab7fa6a63c87340",
					"arm64": "sha256:32bb55c125446b2aa95ac8bb3968701ee05b740ea0c06ccdd5b73e081d5bce98",
				}
			}
			tools[i].OCIRoot = oci.root
			tools[i].OCIExecutable = oci.executable
			tools[i].OCIOutputPath = oci.output
			tools[i].ExitCodes = map[int]Status{0: StatusPass, 1: StatusError, 2: StatusError, 124: StatusTimeout}
			if tools[i].Name == "schemathesis" || tools[i].Name == "mythril" || tools[i].Name == "semgrep" || tools[i].Name == "halmos" {
				tools[i].ExitCodes[1] = StatusFindings
			}
			if tools[i].Name == "slither" {
				tools[i].ExitCodes[255] = StatusFindings
			}
			if tools[i].Name == "zeek" || tools[i].Name == "suricata" {
				tools[i].KnowledgeDigests = map[string]string{"embedded": oci.digest}
			}
		}
		switch tools[i].Name {
		case "nuclei":
			tools[i].Arguments = []Argument{{Name: "rate", Type: "integer", Required: true}}
		case "owasp-zap", "schemathesis":
			tools[i].Arguments = []Argument{{Name: "base_url", Type: "url", Required: true}}
		case "naabu", "nmap":
			tools[i].Arguments = []Argument{{Name: "rate", Type: "integer", Required: true}, {Name: "ports", Type: "ports", Required: true}}
		}
		if digest, ok := knowledgeDigests[tools[i].Name]; ok {
			tools[i].KnowledgeDigests = map[string]string{"bundle": digest}
		} else if slices.Contains(knowledgeRequired, tools[i].Name) && len(tools[i].KnowledgeDigests) == 0 {
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
		if tools[i].Name == "echidna" {
			tools[i].Budgets.Requests = 10000
			tools[i].Budgets.Concurrency = 1
		}
		if tools[i].Name == "mythril" {
			tools[i].Budgets.Concurrency = 1
		}
		if tools[i].Name == "slither" {
			tools[i].OCIPath = "/home/ethsec/.local/bin:/home/ethsec/.foundry/bin:/usr/local/bin:/usr/bin:/bin"
			tools[i].OCIWritableTarget = true
			tools[i].Budgets.Concurrency = 1
		}
		if tools[i].Name == "halmos" {
			tools[i].OCIPath = "/opt/halmos/bin:/usr/local/bin:/usr/bin:/bin"
			tools[i].OCIWritableTarget = true
			tools[i].Budgets.Concurrency = 1
		}
		if !slices.Contains(executable, tools[i].Name) {
			tools[i].Enabled = false
			tools[i].DisabledReason = packagingBlockers[tools[i].Name]
			if tools[i].DisabledReason == "" {
				tools[i].DisabledReason = "catalog-only: executable wrapper is not implemented"
			}
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
		"echidna":              {"amd64": "sha256:b5db2b36cd95c70b84fde5cde73b004485decc7a07b6bfd65d7d6a6695294cc3", "arm64": "sha256:ede4024e5cdc8112716b726c9951a69c709d428a649d994fa952fc7e38f6f662"},
	}
	return pins[name][arch]
}
