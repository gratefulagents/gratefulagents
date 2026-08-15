/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package securitytoolrun

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/securitytoolpacks"
)

// validationImageDigest is a well-formed placeholder: the control plane never
// executes a tool, so it only needs a registry whose enabled/disabled set and
// typed argument contract match the Job's. The Job rebuilds the registry from
// its own executable digest and re-validates the RunConfig before running
// anything, so the digest recorded for replay always comes from the executor.
const validationImageDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// DefaultRegistry builds the compiled tool registry used for control-plane
// validation of SecurityToolRun requests.
func DefaultRegistry() (*securitytoolpacks.Registry, error) {
	// Medusa is an amd64-only execution pack. The controller pins its Job to an
	// amd64 node; validation must therefore describe that worker architecture,
	// not the architecture running the controller process.
	return securitytoolpacks.NewRegistry(
		securitytoolpacks.DefaultManifestForArchitecture(validationImageDigest, nil, "amd64"),
	)
}

// RunConfigFor converts an immutable SecurityToolRun spec into the typed
// Request consumed by the Job. It never produces command-line tokens: argv
// is derived inside the Job from the pinned registry.
func RunConfigFor(spec platformv1alpha1.SecurityToolRunSpec) (Request, error) {
	cfg := securitytoolpacks.RunConfig{
		Tool: strings.TrimSpace(spec.Tool),
		Target: securitytoolpacks.Target{
			Type:      strings.TrimSpace(spec.Target.Type),
			Locator:   spec.Target.Locator,
			Revision:  strings.TrimSpace(spec.Target.Revision),
			Digest:    strings.TrimSpace(spec.Target.Digest),
			MediaType: strings.TrimSpace(spec.Target.MediaType),
		},
		Seed:      spec.Seed,
		Scope:     slices.Clone(spec.Scope),
		Sensitive: slices.Clone(spec.SensitiveFields),
	}
	for _, argument := range spec.Arguments {
		name := strings.TrimSpace(argument.Name)
		if name == "" {
			return Request{}, errors.New("argument names must not be empty")
		}
		if _, exists := cfg.Arguments[name]; exists {
			return Request{}, fmt.Errorf("argument %q is specified more than once", name)
		}
		if cfg.Arguments == nil {
			cfg.Arguments = map[string]string{}
		}
		cfg.Arguments[name] = argument.Value
	}
	return Request{RunConfig: cfg, StagedObjectKey: strings.TrimSpace(spec.Target.StagedObjectKey)}, nil
}

// Request is one execution request: the typed RunConfig plus the staged-target
// assertion. The staged key decides whether the target digest pins anything —
// without it the locator itself must be verifiable.
type Request struct {
	securitytoolpacks.RunConfig
	StagedObjectKey string `json:"-"`
}

// Validate checks a request against the compiled registry before a Job is
// created. It covers everything the control plane can decide without the
// materialized target; the Job re-validates through BuildInvocation, which
// additionally inspects the staged content.
func Validate(registry *securitytoolpacks.Registry, req Request) (securitytoolpacks.Tool, error) {
	cfg := req.RunConfig
	if registry == nil {
		return securitytoolpacks.Tool{}, errors.New("security tool registry is not configured")
	}
	tool, ok := registry.Tool(cfg.Tool)
	if !ok {
		return securitytoolpacks.Tool{}, fmt.Errorf("unknown registered tool %q", cfg.Tool)
	}
	if !tool.Enabled {
		return tool, fmt.Errorf("tool %s is disabled: %s", tool.Name, tool.DisabledReason)
	}
	if !slices.Contains(tool.TargetTypes, cfg.Target.Type) {
		return tool, &securitytoolpacks.ApplicabilityError{Tool: tool.Name, TargetType: cfg.Target.Type}
	}
	if strings.TrimSpace(cfg.Target.Locator) == "" {
		return tool, errors.New("target locator is required")
	}
	if cfg.Target.Revision == "" || !digestPattern.MatchString(cfg.Target.Digest) {
		return tool, errors.New("target revision and immutable sha256 digest are required")
	}
	if hasPlaceholder(cfg.Target.Locator) {
		return tool, errors.New("target locator contains reserved placeholder syntax")
	}
	if err := validateTarget(cfg.Target, req.StagedObjectKey); err != nil {
		return tool, err
	}
	known := make(map[string]securitytoolpacks.Argument, len(tool.Arguments))
	for _, argument := range tool.Arguments {
		known[argument.Name] = argument
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Arguments)) {
		value := cfg.Arguments[name]
		argument, ok := known[name]
		if !ok {
			return tool, fmt.Errorf("tool %s has no argument %q", tool.Name, name)
		}
		if hasPlaceholder(value) {
			return tool, fmt.Errorf("argument %q contains reserved placeholder syntax", name)
		}
		if len(argument.Enum) > 0 && !slices.Contains(argument.Enum, value) {
			return tool, fmt.Errorf("argument %q must be one of %s", name, strings.Join(argument.Enum, ", "))
		}
	}
	for _, argument := range tool.Arguments {
		if argument.Required && cfg.Arguments[argument.Name] == "" {
			return tool, fmt.Errorf("argument %q is required", argument.Name)
		}
	}
	if tool.Requirements.Network && !securitytoolpacks.IsStagedBuildTool(tool.Name) && len(cfg.Scope) == 0 {
		return tool, fmt.Errorf("tool %s requires explicit target scope", tool.Name)
	}
	if tool.SeedSupported && cfg.Seed == nil {
		return tool, fmt.Errorf("tool %s requires an explicit seed", tool.Name)
	}
	return tool, nil
}

func hasPlaceholder(value string) bool {
	return strings.Contains(value, "{{") || strings.Contains(value, "}}")
}

// hostTargetTypes are the registry target types naming a live host rather than
// content: their locator is an address, prefix, or host[:port].
var hostTargetTypes = []string{"address_scope", "tls_service"}

// urlTargetTypes are the registry target types naming a live http endpoint.
var urlTargetTypes = []string{"base_url"}

// ociReferencePattern matches an image reference pinned by digest; the digest
// is derived from the locator, so it needs no staged archive to be verifiable.
var ociReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*@(sha256:[0-9a-f]{64})$`)

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)

// validateTarget decides whether the target digest pins anything. A staged
// archive is verified against it inside the Job, and an image reference
// carries it; otherwise the digest is an unverified caller assertion and the
// locator must name a network endpoint, never a path the Job would bind-mount
// or hand to the scanner as a file.
func validateTarget(target securitytoolpacks.Target, stagedObjectKey string) error {
	locator := strings.TrimSpace(target.Locator)
	if stagedObjectKey != "" {
		if strings.HasPrefix(locator, "/") || strings.HasPrefix(locator, `\`) || strings.HasPrefix(locator, "~") ||
			slices.Contains(strings.Split(locator, "/"), "..") {
			return fmt.Errorf("target locator %q must be a relative path inside the staged archive", locator)
		}
		return nil
	}
	if match := ociReferencePattern.FindStringSubmatch(locator); match != nil {
		if match[1] != target.Digest {
			return fmt.Errorf("target digest %q does not match the digest pinned by locator %q", target.Digest, locator)
		}
		return nil
	}
	switch {
	case slices.Contains(urlTargetTypes, target.Type):
		if err := validateNetworkURL(locator); err != nil {
			return err
		}
		return nil
	case slices.Contains(hostTargetTypes, target.Type):
		if !isHostLocator(locator) {
			return fmt.Errorf("target locator %q must be an address, CIDR prefix, or host[:port]", locator)
		}
		return nil
	}
	return fmt.Errorf("target locator %q is not verifiable: nothing pins digest %s. "+
		"Stage the target archive (spec.target.stagedObjectKey), pin an image with @sha256:, "+
		"or use a network locator for a network target type", locator, target.Digest)
}

func validateNetworkURL(locator string) error {
	parsed, err := url.Parse(locator)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("target locator %q must be an absolute http or https URL", locator)
	}
	return nil
}

func isHostLocator(locator string) bool {
	if _, err := netip.ParseAddr(locator); err == nil {
		return true
	}
	if _, err := netip.ParsePrefix(locator); err == nil {
		return true
	}
	host := locator
	if splitHost, port, err := net.SplitHostPort(locator); err == nil {
		number, convErr := strconv.Atoi(port)
		if convErr != nil || number < 1 || number > 65535 {
			return false
		}
		host = splitHost
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	return hostnamePattern.MatchString(host)
}
