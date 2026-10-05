package dashboard

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

func parseGitHubRepositoryURL(repoURL string) (string, string, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(repoURL))
	if err != nil {
		return "", "", err
	}
	if parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return "", "", fmt.Errorf("unsupported GitHub repository URL")
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("expected exactly an owner and repository")
	}
	owner := parts[0]
	repo := strings.TrimSuffix(parts[1], ".git")
	if !githubOwnerPattern.MatchString(owner) || !githubRepoPattern.MatchString(repo) {
		return "", "", fmt.Errorf("invalid GitHub owner or repository name")
	}
	return owner, repo, nil
}

var (
	githubOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	githubRepoPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)
