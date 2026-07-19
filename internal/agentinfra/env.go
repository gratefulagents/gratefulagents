package agentinfra

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/template"
)

// MustEnv returns the value of the given environment variable, or an error if it is not set.
func MustEnv(key string) (string, error) {
	val := os.Getenv(key)
	if val == "" {
		return "", fmt.Errorf("required env var %s is not set", key)
	}
	return val, nil
}

// EnvOrDefault returns the value of the given environment variable, or def if it is not set.
func EnvOrDefault(key, def string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return def
}

// RenderPrompt executes a text/template against the provided data map.
func RenderPrompt(tmplStr string, data map[string]string) (string, error) {
	t := template.Must(template.New("prompt").Option("missingkey=error").Parse(tmplStr))
	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to render prompt template: %w", err)
	}
	return buf.String(), nil
}

// ParseRepoURL extracts owner/name from a GitHub URL.
// Supports: https://github.com/owner/repo.git, https://github.com/owner/repo, git@github.com:owner/repo.git
func ParseRepoURL(url string) (string, string, error) {
	url = strings.TrimSuffix(url, ".git")

	if strings.Contains(url, "github.com/") {
		parts := strings.Split(url, "github.com/")
		if len(parts) == 2 {
			segments := strings.SplitN(parts[1], "/", 2)
			if len(segments) == 2 {
				return segments[0], segments[1], nil
			}
		}
	}

	if strings.Contains(url, "github.com:") {
		parts := strings.Split(url, "github.com:")
		if len(parts) == 2 {
			segments := strings.SplitN(parts[1], "/", 2)
			if len(segments) == 2 {
				return segments[0], segments[1], nil
			}
		}
	}

	return "", "", fmt.Errorf("cannot parse owner/repo from URL: %s", url)
}

// repoDirNamePattern restricts derived clone directory names to a single safe
// path segment.
var repoDirNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// DeriveRepoDirName extracts a safe single-segment directory name from a git URL
// (e.g. https://github.com/owner/my-repo.git -> "my-repo").
func DeriveRepoDirName(repoURL string) (string, error) {
	name := strings.TrimSpace(repoURL)
	name = strings.TrimSuffix(name, ".git")
	name = strings.TrimRight(name, "/")
	if i := strings.LastIndexAny(name, "/:"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || name == "." || name == ".." || !repoDirNamePattern.MatchString(name) {
		return "", fmt.Errorf("could not derive a valid directory name from %q", repoURL)
	}
	return name, nil
}
