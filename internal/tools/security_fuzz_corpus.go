package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/gratefulagents/gratefulagents/internal/securitytoolpacks"
)

// Fuzzing only compounds if the corpus outlives the run. Go keeps its seed
// corpus in `testdata/fuzz/<Target>` and the executor promotes what a campaign
// generated into that directory, so the inputs come back as artifacts. This
// file is the other half: the corpus is stored under a campaign identity that
// is independent of any single scan, restored into the workspace before the
// next campaign is staged, and re-persisted afterwards.
//
// Everything here is bounded on purpose. An unbounded corpus is a slow way to
// exhaust object storage, and a corpus restored from an untrusted path is a
// way to write outside the workspace, so entries are capped and every restored
// path is re-derived rather than trusted.
const (
	fuzzCorpusSchemaVersion = "v1"
	maxFuzzCorpusEntries    = 64
	maxFuzzCorpusBytes      = 1 << 20
	maxFuzzCorpusEntryBytes = 64 << 10
	fuzzCorpusArtifactRoot  = "go-fuzz-corpus/"
)

type fuzzCorpusEntry struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Data   []byte `json:"data"`
}

type fuzzCorpusDocument struct {
	SchemaVersion string            `json:"schema_version"`
	Campaign      string            `json:"campaign"`
	Entries       []fuzzCorpusEntry `json:"entries"`
}

// fuzzCampaignIdentity names the campaign a corpus belongs to. It is derived
// from the target repository and the exact fuzz target, never from the scan or
// the run, so re-running a scan continues the same campaign instead of
// starting a cold one.
func fuzzCampaignIdentity(repository, pkg, target string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{repository, pkg, target}, "\x00")))
	return hex.EncodeToString(sum[:])[:32]
}

func fuzzCorpusObjectKey(namespace, tool, campaign string) string {
	return path.Join("security-tool-corpus", namespace, tool, campaign+".json")
}

// restoreFuzzCorpus writes a persisted corpus into the staged package's seed
// corpus directory. A missing or unreadable corpus is not an error: a campaign
// that has never run simply starts cold, and saying so is more useful than
// failing the run.
func restoreFuzzCorpus(ctx context.Context, blobs SecurityToolRunBlobStore, key, packageDir, target string) int {
	if blobs == nil || packageDir == "" || target == "" {
		return 0
	}
	raw, err := blobs.Get(ctx, key)
	if err != nil || len(raw) == 0 {
		return 0
	}
	var document fuzzCorpusDocument
	if json.Unmarshal(raw, &document) != nil || document.SchemaVersion != fuzzCorpusSchemaVersion {
		return 0
	}
	destination := filepath.Join(packageDir, "testdata", "fuzz", target)
	if err := os.MkdirAll(destination, 0o750); err != nil {
		return 0
	}
	restored := 0
	for _, entry := range document.Entries {
		if restored >= maxFuzzCorpusEntries || len(entry.Data) == 0 || len(entry.Data) > maxFuzzCorpusEntryBytes {
			continue
		}
		// The stored path is never trusted: the file name is re-derived from
		// the content, so a hostile corpus document cannot escape the
		// directory it is being restored into.
		name := fuzzCorpusEntryName(entry.Data)
		if err := os.WriteFile(filepath.Join(destination, name), entry.Data, 0o600); err != nil {
			continue
		}
		restored++
	}
	return restored
}

func fuzzCorpusEntryName(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:32]
}

// persistFuzzCorpus merges the inputs a campaign produced into the stored
// corpus. Entries are deduplicated by digest and the document stays bounded,
// so a long-running campaign cannot grow the object without limit; the oldest
// entries are the ones dropped, because the newest inputs are the ones the
// fuzzer most recently found interesting.
func persistFuzzCorpus(ctx context.Context, blobs SecurityToolRunBlobStore, key, campaign string, fresh []fuzzCorpusEntry) (int, error) {
	if blobs == nil {
		return 0, nil
	}
	document := fuzzCorpusDocument{SchemaVersion: fuzzCorpusSchemaVersion, Campaign: campaign}
	if raw, err := blobs.Get(ctx, key); err == nil && len(raw) > 0 {
		var stored fuzzCorpusDocument
		if json.Unmarshal(raw, &stored) == nil && stored.SchemaVersion == fuzzCorpusSchemaVersion {
			document.Entries = stored.Entries
		}
	}
	known := map[string]bool{}
	for _, entry := range document.Entries {
		known[entry.Digest] = true
	}
	added := 0
	for _, entry := range fresh {
		if entry.Digest == "" || known[entry.Digest] || len(entry.Data) == 0 || len(entry.Data) > maxFuzzCorpusEntryBytes {
			continue
		}
		known[entry.Digest] = true
		document.Entries = append(document.Entries, entry)
		added++
	}
	if added == 0 {
		return 0, nil
	}
	document.Entries = boundFuzzCorpus(document.Entries)
	encoded, err := json.Marshal(document)
	if err != nil {
		return 0, err
	}
	if err := blobs.Put(ctx, key, encoded, "application/json"); err != nil {
		return 0, err
	}
	return added, nil
}

// boundFuzzCorpus keeps the newest entries within both the count and the byte
// budget.
func boundFuzzCorpus(entries []fuzzCorpusEntry) []fuzzCorpusEntry {
	if len(entries) > maxFuzzCorpusEntries {
		entries = entries[len(entries)-maxFuzzCorpusEntries:]
	}
	total := 0
	for i, entry := range slices.Backward(entries) {
		total += len(entry.Data)
		if total > maxFuzzCorpusBytes {
			return entries[i+1:]
		}
	}
	return entries
}

// goFuzzCorpusArtifacts turns the run's returned artifacts into corpus
// entries. Only artifacts the Go pack itself produced are considered, and each
// is re-hashed from its bytes rather than trusted.
func goFuzzCorpusArtifacts(ctx context.Context, blobs SecurityToolRunBlobStore, artifacts []runSecurityToolArtifact) []fuzzCorpusEntry {
	if blobs == nil {
		return nil
	}
	entries := make([]fuzzCorpusEntry, 0, len(artifacts))
	for _, artifact := range artifacts {
		if !strings.HasPrefix(artifact.Name, fuzzCorpusArtifactRoot) || artifact.ObjectKey == "" {
			continue
		}
		data, err := blobs.Get(ctx, artifact.ObjectKey)
		if err != nil || len(data) == 0 || len(data) > maxFuzzCorpusEntryBytes {
			continue
		}
		sum := sha256.Sum256(data)
		entries = append(entries, fuzzCorpusEntry{
			Path:   strings.TrimPrefix(artifact.Name, fuzzCorpusArtifactRoot),
			Digest: "sha256:" + hex.EncodeToString(sum[:]),
			Data:   data,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Digest < entries[j].Digest })
	return entries
}

// goFuzzCampaignNote is what the agent is told about corpus provenance, so a
// clean result is never read as "nothing is there" when it was simply a cold
// first campaign.
func goFuzzCampaignNote(restored, persisted int) string {
	if restored == 0 && persisted == 0 {
		return "fuzz corpus: cold start, no new inputs persisted"
	}
	return fmt.Sprintf("fuzz corpus: restored %d seed input(s), persisted %d new input(s) for the next campaign", restored, persisted)
}

// restoreGoFuzzCorpus restores the persisted corpus for this campaign into the
// staged workspace copy. It is best-effort by design: a cold start is a normal
// state, and refusing to fuzz because no corpus exists yet would make the
// first campaign impossible.
func (t *runSecurityToolTool) restoreGoFuzzCorpus(ctx context.Context, in runSecurityToolInput, local string) int {
	if in.Tool != "go-fuzz-tests" {
		return 0
	}
	pkg, target := in.Arguments["package"], securitytoolpacks.GoFuzzTargetName(in.Arguments["fuzz"])
	if pkg == "" || target == "" {
		return 0
	}
	campaign := fuzzCampaignIdentity(t.state.scanCtx.Repository, pkg, target)
	key := fuzzCorpusObjectKey(t.deps.Namespace, in.Tool, campaign)
	return restoreFuzzCorpus(ctx, t.deps.Blobs, key, securitytoolpacks.GoFuzzPackageDir(local, pkg), target)
}

// persistGoFuzzCorpus stores what this campaign produced so the next one
// starts from it, and returns the note describing corpus provenance.
func (t *runSecurityToolTool) persistGoFuzzCorpus(ctx context.Context, in runSecurityToolInput, artifacts []runSecurityToolArtifact) string {
	if in.Tool != "go-fuzz-tests" {
		return ""
	}
	pkg, target := in.Arguments["package"], securitytoolpacks.GoFuzzTargetName(in.Arguments["fuzz"])
	if pkg == "" || target == "" {
		return ""
	}
	campaign := fuzzCampaignIdentity(t.state.scanCtx.Repository, pkg, target)
	key := fuzzCorpusObjectKey(t.deps.Namespace, in.Tool, campaign)
	persisted, err := persistFuzzCorpus(ctx, t.deps.Blobs, key, campaign, goFuzzCorpusArtifacts(ctx, t.deps.Blobs, artifacts))
	if err != nil {
		return "fuzz corpus could not be persisted: " + err.Error()
	}
	return goFuzzCampaignNote(t.restoredFuzzInputs, persisted)
}
