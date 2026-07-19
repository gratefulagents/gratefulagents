package dashboard

import (
	"fmt"
	"path/filepath"
	"strings"
)

// normalizeAdditionalRepoURLs validates and dedupes the extra repository URLs
// attached to a project or run. Each URL must be a recognised git transport
// and must yield a unique clone directory name (extra repos are cloned side by
// side under repos/<name> in the run's sandbox). URLs equal to the primary
// repository and exact duplicates are dropped rather than rejected.
func normalizeAdditionalRepoURLs(urls []string, primaryURL string) ([]string, error) {
	primary := strings.TrimSpace(primaryURL)
	seenURLs := make(map[string]struct{}, len(urls))
	nameOwners := make(map[string]string, len(urls))
	out := make([]string, 0, len(urls))
	for _, raw := range urls {
		repoURL := strings.TrimSpace(raw)
		if repoURL == "" || repoURL == primary {
			continue
		}
		if _, ok := seenURLs[repoURL]; ok {
			continue
		}
		if err := validateCloneURL(repoURL); err != nil {
			return nil, fmt.Errorf("additional repository %q: %w", repoURL, err)
		}
		name, err := deriveRepoDirName(repoURL)
		if err != nil {
			return nil, fmt.Errorf("additional repository %q: %w", repoURL, err)
		}
		if name == filepath.Base(repoDir) {
			return nil, fmt.Errorf("additional repository %q: directory name %q is reserved for the run's primary repository", repoURL, name)
		}
		if owner, ok := nameOwners[name]; ok {
			return nil, fmt.Errorf("additional repositories %q and %q would both clone to %q", owner, repoURL, name)
		}
		seenURLs[repoURL] = struct{}{}
		nameOwners[name] = repoURL
		out = append(out, repoURL)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
