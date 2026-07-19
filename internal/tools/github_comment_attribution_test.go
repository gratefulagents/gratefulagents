package tools

import "testing"

func TestAttributeGitHubComment(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "comment", body: "Decision: dispatch", want: "Decision: dispatch\n\n" + githubAppAuthorizationFooter},
		{name: "trailing whitespace", body: "Fixed  \n", want: "Fixed\n\n" + githubAppAuthorizationFooter},
		{name: "already attributed", body: "LGTM\n\n" + githubAppAuthorizationFooter, want: "LGTM\n\n" + githubAppAuthorizationFooter},
		{name: "empty review body", body: "", want: githubAppAuthorizationFooter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := attributeGitHubComment(tc.body); got != tc.want {
				t.Fatalf("attributeGitHubComment(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}
