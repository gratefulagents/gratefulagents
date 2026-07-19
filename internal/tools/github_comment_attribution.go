package tools

import "strings"

const githubAppAuthorizationFooter = "_Authorized by the [gratefulagents GitHub App](https://github.com/apps/gratefulagents)._"

// attributeGitHubComment adds visible GitHub App authorization to every
// comment-like body posted by the built-in GitHub tools.
func attributeGitHubComment(body string) string {
	body = strings.TrimRight(body, " \t\r\n")
	if strings.HasSuffix(body, githubAppAuthorizationFooter) {
		return body
	}
	if body == "" {
		return githubAppAuthorizationFooter
	}
	return body + "\n\n" + githubAppAuthorizationFooter
}
