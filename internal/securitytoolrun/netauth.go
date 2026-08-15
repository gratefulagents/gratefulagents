/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package securitytoolrun

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/gratefulagents/gratefulagents/internal/securitytoolpacks"
)

// SplitAuthorizedNetworkTargets parses the comma-separated authorization list
// the platform stamps onto a scan run into its trimmed entries.
func SplitAuthorizedNetworkTargets(raw string) []string {
	targets := make([]string, 0, strings.Count(raw, ",")+1)
	for entry := range strings.SplitSeq(raw, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			targets = append(targets, trimmed)
		}
	}
	return targets
}

// NeedsNetworkAuthorization reports whether an execution reaches the network,
// and therefore may only run against operator-authorized targets. A tool that
// declares a network requirement always does; so does any request whose target
// is a live network locator rather than staged content.
func NeedsNetworkAuthorization(tool securitytoolpacks.Tool, req Request) bool {
	// Local EVM build/test tools may resolve compiler and project dependencies.
	// Their target remains staged content rather than a network probe, so there
	// is no remote target to compare with the scan's authorization allowlist.
	if strings.TrimSpace(req.StagedObjectKey) != "" && securitytoolpacks.IsStagedBuildTool(tool.Name) {
		return false
	}
	if tool.Requirements.Network {
		return true
	}
	if strings.TrimSpace(req.StagedObjectKey) != "" {
		return false
	}
	if !slices.Contains(urlTargetTypes, req.Target.Type) && !slices.Contains(hostTargetTypes, req.Target.Type) {
		return false
	}
	_, err := parseNetworkLocator(req.Target.Locator)
	return err == nil
}

// AuthorizeNetworkTargets checks the target locator and every scope entry of a
// request against the operator-declared authorization list. Matching is
// deliberately conservative: a host matches by exact name, an explicit
// wildcard domain matches only that DNS suffix, or an address must be inside a
// declared CIDR. A declared port and URL scheme must match. An empty
// authorization list authorizes nothing.
func AuthorizeNetworkTargets(authorized []string, req Request) error {
	if len(authorized) == 0 {
		return fmt.Errorf("no authorized network targets are configured for this run; " +
			"network security tools may only run against operator-declared targets")
	}
	matchers := make([]networkLocator, 0, len(authorized))
	for _, entry := range authorized {
		// An unparseable authorization entry cannot authorize anything, so it
		// is dropped rather than widened into a match.
		if parsed, err := parseNetworkLocator(entry); err == nil {
			matchers = append(matchers, parsed)
		}
	}
	candidates := append([]string{req.Target.Locator}, req.Scope...)
	for _, candidate := range candidates {
		locator, err := parseNetworkLocator(candidate)
		if err != nil {
			return fmt.Errorf("%w; only authorized network targets may be probed", err)
		}
		if !coveredBy(matchers, locator) {
			return fmt.Errorf("network target %q is not covered by the authorized network targets [%s]",
				strings.TrimSpace(candidate), strings.Join(authorized, ", "))
		}
	}
	return nil
}

func coveredBy(matchers []networkLocator, candidate networkLocator) bool {
	for _, matcher := range matchers {
		if matcher.covers(candidate) {
			return true
		}
	}
	return false
}

// networkLocator is one parsed host, host:port, CIDR prefix, or http(s) URL.
type networkLocator struct {
	scheme   string
	host     string
	port     string
	prefix   netip.Prefix
	wildcard bool
}

func (l networkLocator) isPrefix() bool { return l.prefix.IsValid() }

// covers reports whether this authorization entry permits the candidate.
func (l networkLocator) covers(candidate networkLocator) bool {
	if l.isPrefix() {
		if candidate.isPrefix() {
			return l.prefix.Addr().BitLen() == candidate.prefix.Addr().BitLen() &&
				candidate.prefix.Bits() >= l.prefix.Bits() && l.prefix.Contains(candidate.prefix.Addr())
		}
		address, err := netip.ParseAddr(candidate.host)
		return err == nil && l.prefix.Contains(address)
	}
	// A single authorized host never authorizes a whole address range.
	if candidate.isPrefix() {
		return false
	}
	if l.wildcard {
		candidateHost := strings.ToLower(candidate.host)
		if candidateHost != l.host && !strings.HasSuffix(candidateHost, "."+l.host) {
			return false
		}
	} else if !strings.EqualFold(l.host, candidate.host) {
		return false
	}
	// A port-qualified authorization permits exactly that port; a candidate
	// that names no port could reach any of them.
	if l.port != "" && l.port != candidate.port {
		return false
	}
	if l.scheme != "" && candidate.scheme != "" && !strings.EqualFold(l.scheme, candidate.scheme) {
		return false
	}
	return true
}

// parseNetworkLocator accepts the forms an authorization entry and a probed
// target may take: an absolute http(s) URL, an IP address, a CIDR prefix, or a
// hostname with an optional port.
func parseNetworkLocator(raw string) (networkLocator, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return networkLocator{}, fmt.Errorf("network target is empty")
	}
	if host, ok := strings.CutPrefix(value, "*."); ok {
		if !hostnamePattern.MatchString(host) {
			return networkLocator{}, fmt.Errorf("network target %q has an invalid wildcard domain", value)
		}
		return networkLocator{host: strings.ToLower(host), wildcard: true}, nil
	}
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return networkLocator{}, fmt.Errorf("network target %q must be an absolute http or https URL", value)
		}
		return networkLocator{scheme: parsed.Scheme, host: strings.ToLower(parsed.Hostname()), port: parsed.Port()}, nil
	}
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return networkLocator{prefix: prefix.Masked()}, nil
	}
	if address, err := netip.ParseAddr(value); err == nil {
		return networkLocator{host: address.String()}, nil
	}
	host, port := value, ""
	if splitHost, splitPort, err := net.SplitHostPort(value); err == nil {
		number, convErr := strconv.Atoi(splitPort)
		if convErr != nil || number < 1 || number > 65535 {
			return networkLocator{}, fmt.Errorf("network target %q has an invalid port", value)
		}
		host, port = splitHost, splitPort
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return networkLocator{host: address.String(), port: port}, nil
	}
	if !hostnamePattern.MatchString(host) {
		return networkLocator{}, fmt.Errorf("network target %q is not a host, host:port, CIDR prefix, or http(s) URL", value)
	}
	return networkLocator{host: strings.ToLower(host), port: port}, nil
}
