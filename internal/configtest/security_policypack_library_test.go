/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package configtest

import (
	"os"
	"strings"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

// TestSecurityPolicyPackLibraryAssets validates every shipped policy pack
// beyond the baseline one and pins each default ref to an asset the bootstrap
// actually installs, so a pack can never reference a missing resource.
func TestSecurityPolicyPackLibraryAssets(t *testing.T) {
	t.Parallel()

	packs := []struct {
		name        string
		rankers     []string
		postScripts []string
	}{
		{
			name:        "web-application",
			rankers:     []string{"web-app-impact"},
			postScripts: []string{"false-positive-check", "report-writer"},
		},
		{
			name:    "bug-bounty",
			rankers: []string{"bug-bounty-triage"},
			// prior-art-check runs before any PoC work: a known, already-reported,
			// or bot-findable finding is unpayable, so it must be killed before
			// the pipeline spends proof-of-concept budget on it.
			postScripts: []string{"scope-eligibility-check", "false-positive-check", "prior-art-check", "poc-builder", "poc-validator", "bounty-worthiness-check", "report-writer"},
		},
	}

	for _, tc := range packs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var pack triggersv1alpha1.SecurityPolicyPack
			readBootstrapAsset(t, "securitypolicypacks", tc.name, &pack)

			if pack.Name != tc.name {
				t.Fatalf("metadata.name = %q, want %q", pack.Name, tc.name)
			}
			if strings.TrimSpace(pack.Spec.Description) == "" {
				t.Error("pack must carry a description")
			}
			if errs := triggersv1alpha1.ValidateSecurityPolicyPackSpec(pack.Spec); len(errs) != 0 {
				t.Errorf("spec fails validation: %v", errs)
			}
			if len(pack.Spec.Enforced) != 0 {
				t.Errorf("enforced = %v, want an advisory pack that never silently overrides a scan", pack.Spec.Enforced)
			}
			if tc.name == "bug-bounty" {
				if got := pack.Spec.MinSeverity; got != "low" {
					t.Errorf("effective minSeverity = %q, want low so all in-scope vulnerabilities remain reportable", got)
				}
			}

			assertRefs := func(field, kindDir string, refs []triggersv1alpha1.SecurityResourceRef, want []string) {
				got := make([]string, 0, len(refs))
				for _, ref := range refs {
					got = append(got, ref.Name)
					if _, err := os.Stat(repoPath("configs", kindDir, ref.Name+".yaml")); err != nil {
						t.Errorf("%s references %q with no shipped asset: %v", field, ref.Name, err)
					}
				}
				if strings.Join(got, ",") != strings.Join(want, ",") {
					t.Errorf("%s = %v, want %v", field, got, want)
				}
			}
			assertRefs("defaultRankerRefs", "securityrankers", pack.Spec.DefaultRankerRefs, tc.rankers)
			assertRefs("defaultPostScriptRefs", "securitypostscripts", pack.Spec.DefaultPostScriptRefs, tc.postScripts)
		})
	}
}
