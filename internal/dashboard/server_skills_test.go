package dashboard

import "testing"

func TestNormalizeGitHubSkillLink(t *testing.T) {
	cases := []struct {
		in                string
		ref, path         string
		wantURL           string
		wantRef, wantPath string
	}{
		{
			in:      "https://github.com/anthropics/skills/tree/main/document-skills/pdf",
			wantURL: "https://github.com/anthropics/skills", wantRef: "main", wantPath: "document-skills/pdf",
		},
		{
			in:      "https://github.com/anthropics/skills/blob/main/document-skills/pdf/SKILL.md",
			wantURL: "https://github.com/anthropics/skills", wantRef: "main", wantPath: "document-skills/pdf",
		},
		{
			in:      "https://github.com/owner/repo",
			wantURL: "https://github.com/owner/repo", wantRef: "", wantPath: "",
		},
		{
			// Explicit values win over link-derived ones.
			in: "https://github.com/anthropics/skills/tree/main/document-skills/pdf", ref: "v2", path: "other/pdf",
			wantURL: "https://github.com/anthropics/skills", wantRef: "v2", wantPath: "other/pdf",
		},
	}
	for _, tc := range cases {
		url, ref, path := normalizeGitHubSkillLink(tc.in, tc.ref, tc.path)
		if url != tc.wantURL || ref != tc.wantRef || path != tc.wantPath {
			t.Fatalf("normalizeGitHubSkillLink(%q, %q, %q) = (%q, %q, %q), want (%q, %q, %q)",
				tc.in, tc.ref, tc.path, url, ref, path, tc.wantURL, tc.wantRef, tc.wantPath)
		}
	}
}
