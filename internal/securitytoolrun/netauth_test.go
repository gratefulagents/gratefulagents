/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package securitytoolrun

import (
	"strings"
	"testing"

	"github.com/gratefulagents/gratefulagents/internal/securitytoolpacks"
)

func networkRequest(locator string, scope ...string) Request {
	return Request{RunConfig: securitytoolpacks.RunConfig{
		Target: securitytoolpacks.Target{Type: "base_url", Locator: locator, Revision: "v1", Digest: testDigest},
		Scope:  scope,
	}}
}

func TestSplitAuthorizedNetworkTargets(t *testing.T) {
	got := SplitAuthorizedNetworkTargets(" api.example.test , ,10.0.0.0/8 ")
	if len(got) != 2 || got[0] != "api.example.test" || got[1] != "10.0.0.0/8" {
		t.Fatalf("SplitAuthorizedNetworkTargets() = %#v", got)
	}
	if got := SplitAuthorizedNetworkTargets("   "); len(got) != 0 {
		t.Fatalf("SplitAuthorizedNetworkTargets(blank) = %#v", got)
	}
}

func TestNeedsNetworkAuthorization(t *testing.T) {
	networkTool := securitytoolpacks.Tool{Name: "nuclei", Requirements: securitytoolpacks.Requirements{Network: true}}
	staged := Request{
		RunConfig:       securitytoolpacks.RunConfig{Target: securitytoolpacks.Target{Type: "directory", Locator: "src"}},
		StagedObjectKey: TargetObjectKey("ns", "scan"),
	}
	if !NeedsNetworkAuthorization(networkTool, staged) {
		t.Fatal("a tool that requires network access always needs authorization")
	}
	if NeedsNetworkAuthorization(securitytoolpacks.Tool{Name: "gitleaks"}, staged) {
		t.Fatal("a staged, offline target must not require network authorization")
	}
	if !NeedsNetworkAuthorization(securitytoolpacks.Tool{Name: "sslyze"}, networkRequest("https://api.example.test")) {
		t.Fatal("a live URL target needs authorization even when the tool does not declare network access")
	}
}

func TestAuthorizeNetworkTargets(t *testing.T) {
	cases := []struct {
		name       string
		authorized []string
		request    Request
		wantErr    string
	}{
		{
			name:       "exact host",
			authorized: []string{"api.example.test"},
			request:    networkRequest("https://api.example.test/v1", "api.example.test"),
		},
		{
			name:       "host inside authorized prefix",
			authorized: []string{"192.0.2.0/24"},
			request:    networkRequest("192.0.2.10", "192.0.2.0/25"),
		},
		{
			name:       "url authorization matches port and scheme",
			authorized: []string{"https://api.example.test:8443"},
			request:    networkRequest("https://api.example.test:8443/v1", "api.example.test:8443"),
		},
		{
			name:       "no authorization at all",
			authorized: nil,
			request:    networkRequest("https://api.example.test"),
			wantErr:    "no authorized network targets are configured",
		},
		{
			name:       "different host",
			authorized: []string{"api.example.test"},
			request:    networkRequest("https://metadata.internal"),
			wantErr:    "not covered by the authorized network targets",
		},
		{
			name:       "subdomain is not a wildcard match",
			authorized: []string{"example.test"},
			request:    networkRequest("https://admin.example.test"),
			wantErr:    "not covered",
		},
		{
			name:       "scope entry outside the authorization",
			authorized: []string{"api.example.test"},
			request:    networkRequest("https://api.example.test", "169.254.169.254"),
			wantErr:    `"169.254.169.254" is not covered`,
		},
		{
			name:       "declared port must match",
			authorized: []string{"api.example.test:443"},
			request:    networkRequest("https://api.example.test:8080/v1"),
			wantErr:    "not covered",
		},
		{
			name:       "unported authorization covers any port",
			authorized: []string{"api.example.test"},
			request:    networkRequest("https://api.example.test:8080/v1"),
		},
		{
			name:       "scheme must match",
			authorized: []string{"https://api.example.test"},
			request:    networkRequest("http://api.example.test/v1"),
			wantErr:    "not covered",
		},
		{
			name:       "single host never authorizes a range",
			authorized: []string{"192.0.2.10"},
			request:    networkRequest("192.0.2.0/24"),
			wantErr:    "not covered",
		},
		{
			name:       "address outside the authorized prefix",
			authorized: []string{"192.0.2.0/24"},
			request:    networkRequest("198.51.100.7"),
			wantErr:    "not covered",
		},
		{
			name:       "unparseable target",
			authorized: []string{"api.example.test"},
			request:    networkRequest("file:///etc/passwd"),
			wantErr:    "must be an absolute http or https URL",
		},
		{
			name:       "unparseable authorization entry authorizes nothing",
			authorized: []string{"*.example.test"},
			request:    networkRequest("https://api.example.test"),
			wantErr:    "not covered",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AuthorizeNetworkTargets(tc.authorized, tc.request)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("AuthorizeNetworkTargets() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("AuthorizeNetworkTargets() error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
