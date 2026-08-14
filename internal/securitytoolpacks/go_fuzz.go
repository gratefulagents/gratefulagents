package securitytoolpacks

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// Go fuzzing is the one language lane that already executes, and until now it
// ran for thirty seconds from whatever corpus happened to be checked in. That
// is a smoke test wearing a fuzzer's name: a bounded clean result from it is
// technically honest and practically worthless.
//
// Two things make it a campaign instead:
//
//   - the campaign length is a reviewed, bounded argument, and the run's
//     wall-clock budget is derived from it rather than fixed;
//   - the corpus Go generates during the run is promoted into the package's
//     seed corpus, so it leaves the sandbox as artifacts and seeds the next
//     campaign instead of being discarded with the build cache.
const (
	defaultFuzzCampaign = 2 * time.Minute
	minFuzzCampaign     = 30 * time.Second
	maxFuzzCampaign     = 15 * time.Minute

	// fuzzCampaignOverhead covers build, seed replay and minimization around
	// the campaign itself, so the executor's deadline never fires before the
	// fuzzer has had the time the run asked for.
	fuzzCampaignOverhead = 90 * time.Second

	// FuzzCampaignOverhead and RustFuzzBuildAllowance are exported so the
	// control plane can derive its wait from the same numbers the executor
	// budgets with; a caller that waits less than the campaign orphans the run.
	FuzzCampaignOverhead   = fuzzCampaignOverhead
	RustFuzzBuildAllowance = rustFuzzBuildAllowance

	// Promotion bounds. A campaign can generate thousands of inputs; the seed
	// corpus we carry forward is deliberately a bounded sample.
	maxPromotedCorpusFiles = 64
	maxPromotedCorpusBytes = 1 << 20

	// rustFuzzBuildAllowance covers compiling the instrumented target before
	// the campaign starts; Rust builds are the slow part of a cargo-fuzz run.
	rustFuzzBuildAllowance = 10 * time.Minute
)

// ParseFuzzCampaign validates the campaign length. An empty value is the
// registry default, which BuildInvocation has already substituted; parsing it
// again here keeps the bound enforced for callers that construct a config by
// hand.
func ParseFuzzCampaign(value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return defaultFuzzCampaign, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("argument %q must be a Go duration such as 2m", "fuzztime")
	}
	if parsed < minFuzzCampaign || parsed > maxFuzzCampaign {
		return 0, fmt.Errorf("argument %q must be between %s and %s", "fuzztime", minFuzzCampaign, maxFuzzCampaign)
	}
	return parsed, nil
}

// fuzzCampaignBudget derives the run's wall-clock budget from the campaign it
// was asked to run. A fixed budget either truncates long campaigns or lets
// short ones hold a slot for far longer than they need.
func fuzzCampaignBudget(base Budgets, campaign time.Duration) Budgets {
	out := base
	if deadline := campaign + fuzzCampaignOverhead; deadline > out.Timeout {
		out.Timeout = deadline
	}
	return out
}

// GoFuzzPackageDir resolves the Go package selector to a directory inside the
// staged project. Anything that escapes the root resolves to the root itself,
// which is where a single-package project keeps its testdata anyway.
func GoFuzzPackageDir(root, selector string) string {
	relative := strings.TrimSuffix(strings.TrimPrefix(selector, "./"), "/...")
	relative = strings.TrimSuffix(relative, "...")
	if relative == "" || relative == "." || strings.Contains(relative, "..") {
		return root
	}
	candidate := filepath.Join(root, filepath.FromSlash(relative))
	if !strings.HasPrefix(candidate, filepath.Clean(root)+string(filepath.Separator)) {
		return root
	}
	if info, err := os.Stat(candidate); err != nil || !info.IsDir() {
		return root
	}
	return candidate
}

// GoFuzzTargetName turns the `^FuzzXxx$` selector into the bare target name Go
// uses for its corpus directory.
func GoFuzzTargetName(selector string) string {
	return strings.TrimSuffix(strings.TrimPrefix(selector, "^"), "$")
}

// promoteGeneratedCorpus copies the inputs Go kept in its build cache during
// the campaign into the package's seed corpus. Go only persists minimized
// crashers to testdata; everything it learned otherwise lives in GOCACHE and
// dies with the sandbox. Promoting a bounded sample is what lets the next
// campaign start where this one stopped, and it is also what makes the
// generated corpus visible as run artifacts.
//
// It returns how many inputs were promoted. A cache with nothing in it is not
// an error: a campaign that generated no new input is a fact about the
// campaign, not a failure of the run.
func promoteGeneratedCorpus(cacheRoot, packageDir, target string) (int, error) {
	if cacheRoot == "" || packageDir == "" || target == "" {
		return 0, nil
	}
	generated, err := generatedCorpusFiles(cacheRoot, target)
	if err != nil || len(generated) == 0 {
		return 0, err
	}
	destination := filepath.Join(packageDir, "testdata", "fuzz", target)
	if err := os.MkdirAll(destination, 0o750); err != nil {
		return 0, err
	}
	promoted, total := 0, int64(0)
	for _, source := range generated {
		if promoted >= maxPromotedCorpusFiles {
			break
		}
		data, readErr := os.ReadFile(source) // #nosec G304 -- path is rooted in the executor's own sandbox work directory.
		if readErr != nil {
			return promoted, readErr
		}
		total += int64(len(data))
		if total > maxPromotedCorpusBytes {
			break
		}
		name := sha256Digest(data)
		name = strings.TrimPrefix(name, "sha256:")
		path := filepath.Join(destination, name[:32])
		if _, statErr := os.Stat(path); statErr == nil {
			continue
		}
		if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
			return promoted, writeErr
		}
		promoted++
	}
	return promoted, nil
}

// generatedCorpusFiles finds the campaign's corpus inside the Go build cache.
// The layout is $GOCACHE/fuzz/<package path>/<TargetName>/<hash>, and the
// executor owns the cache directory, so the walk is bounded to its own work
// tree.
func generatedCorpusFiles(cacheRoot, target string) ([]string, error) {
	files := []string{}
	err := filepath.WalkDir(cacheRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A cache directory that disappeared mid-walk is not a run failure.
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(path), "/")
		if !slices.Contains(parts, "fuzz") || len(parts) < 2 {
			return nil
		}
		if parts[len(parts)-2] != target {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, err
}

// goFuzzBoundedScope states what the campaign actually covered. "corpus" names
// the provenance a reader needs to judge a clean result: a cold thirty-second
// run and a restored fifteen-minute run are not the same claim.
func goFuzzBoundedScope(pkg, target string, campaign time.Duration, seeded, promoted int) *BoundedScope {
	provenance := "cold"
	if seeded > 0 {
		provenance = "restored"
	}
	return &BoundedScope{
		Harness: pkg + " " + target,
		Corpus:  fmt.Sprintf("seed inputs in=%d (%s), promoted out=%d", seeded, provenance, promoted),
		Bounds:  "fuzztime=" + campaign.String(),
	}
}

// countSeedCorpus counts the seed inputs the campaign started from.
func countSeedCorpus(packageDir, target string) int {
	entries, err := os.ReadDir(filepath.Join(packageDir, "testdata", "fuzz", target))
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			count++
		}
	}
	return count
}
