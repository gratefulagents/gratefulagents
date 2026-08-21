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
	fuzzCorpusSchemaVersion    = "v2"
	fuzzCorpusLegacyVersion    = "v1"
	maxFuzzCorpusEntries       = 64
	maxFuzzCorpusBytes         = 1 << 20
	maxFuzzCorpusEntryBytes    = 64 << 10
	maxFuzzCorpusDocumentBytes = 2 << 20
	fuzzCorpusArtifactRoot     = "go-fuzz-corpus/"
)

type fuzzCorpusEntry struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Data   []byte `json:"data"`
}

type fuzzCorpusDocument struct {
	SchemaVersion  string            `json:"schema_version"`
	Campaign       string            `json:"campaign"`
	InputFormat    string            `json:"input_format,omitempty"`
	ProducerTool   string            `json:"producer_tool,omitempty"`
	TargetRevision string            `json:"target_revision,omitempty"`
	TargetDigest   string            `json:"target_digest,omitempty"`
	SnapshotDigest string            `json:"snapshot_digest,omitempty"`
	ParentDigest   string            `json:"parent_digest,omitempty"`
	Entries        []fuzzCorpusEntry `json:"entries"`
}

func canonicalFuzzCorpusEntries(entries []fuzzCorpusEntry) ([]fuzzCorpusEntry, error) {
	if len(entries) > maxFuzzCorpusEntries {
		return nil, fmt.Errorf("fuzz corpus has more than %d entries", maxFuzzCorpusEntries)
	}
	canonical := make([]fuzzCorpusEntry, 0, len(entries))
	seen := map[string]bool{}
	total := 0
	for _, entry := range entries {
		if len(entry.Data) == 0 || len(entry.Data) > maxFuzzCorpusEntryBytes || total+len(entry.Data) > maxFuzzCorpusBytes {
			return nil, fmt.Errorf("fuzz corpus entry exceeds storage bounds")
		}
		sum := sha256.Sum256(entry.Data)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		if seen[digest] {
			continue
		}
		seen[digest] = true
		total += len(entry.Data)
		canonical = append(canonical, fuzzCorpusEntry{Path: fuzzCorpusEntryName(entry.Data), Digest: digest, Data: entry.Data})
	}
	return canonical, nil
}

func fuzzCorpusSnapshotDigest(document fuzzCorpusDocument) (string, error) {
	document.SnapshotDigest = ""
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func decodeFuzzCorpusDocument(raw []byte, campaign string, requireLineage bool) (fuzzCorpusDocument, error) {
	if len(raw) == 0 || len(raw) > maxFuzzCorpusDocumentBytes {
		return fuzzCorpusDocument{}, fmt.Errorf("fuzz corpus document exceeds storage bounds")
	}
	var document fuzzCorpusDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return fuzzCorpusDocument{}, err
	}
	if document.SchemaVersion != fuzzCorpusSchemaVersion && document.SchemaVersion != fuzzCorpusLegacyVersion {
		return fuzzCorpusDocument{}, fmt.Errorf("unsupported fuzz corpus schema %q", document.SchemaVersion)
	}
	if campaign != "" && document.Campaign != campaign {
		return fuzzCorpusDocument{}, fmt.Errorf("fuzz corpus campaign mismatch")
	}
	entries, err := canonicalFuzzCorpusEntries(document.Entries)
	if err != nil {
		return fuzzCorpusDocument{}, err
	}
	if document.SchemaVersion == fuzzCorpusSchemaVersion {
		if len(entries) != len(document.Entries) {
			return fuzzCorpusDocument{}, fmt.Errorf("fuzz corpus contains duplicate entries")
		}
		for i := range entries {
			if document.Entries[i].Digest != entries[i].Digest || document.Entries[i].Path != entries[i].Path {
				return fuzzCorpusDocument{}, fmt.Errorf("fuzz corpus entry integrity mismatch")
			}
		}
		if requireLineage && (document.InputFormat != "bytes" || document.ProducerTool == "" || document.TargetRevision == "" || document.TargetDigest == "") {
			return fuzzCorpusDocument{}, fmt.Errorf("Rust fuzz corpus lineage is incomplete")
		}
		want, err := fuzzCorpusSnapshotDigest(document)
		if err != nil || document.SnapshotDigest != want {
			return fuzzCorpusDocument{}, fmt.Errorf("fuzz corpus snapshot digest mismatch")
		}
	} else if requireLineage {
		return fuzzCorpusDocument{}, fmt.Errorf("legacy Rust fuzz corpus has no authenticated lineage")
	}
	document.Entries = entries
	return document, nil
}

// fuzzCampaignIdentity names the campaign a corpus belongs to. It is derived
// from the repository, staged target root, package, and exact fuzz target,
// never from the scan or run, so re-running the same target continues the
// campaign without contaminating a sibling project in the same repository.
func fuzzCampaignIdentity(repository, targetRoot, pkg, target string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{repository, targetRoot, pkg, target}, "\x00")))
	return hex.EncodeToString(sum[:])[:32]
}

func fuzzCampaignTargetRoot(locator string) string {
	return path.Clean(filepath.ToSlash(strings.TrimSpace(locator)))
}

func fuzzCorpusObjectKey(namespace, tool, campaign string) string {
	return path.Join("security-tool-corpus", namespace, tool, campaign+".json")
}

// fuzzCorpusForArchive resolves a persisted corpus into the archive entries a
// campaign should start from. It deliberately returns content instead of
// writing files: restoring into the user's checkout would leave untracked
// inputs behind, and a cleanup step that runs "usually" is not a guarantee.
// A missing or unreadable corpus is not an error either — a campaign that has
// never run simply starts cold, and saying so is more useful than failing.
func fuzzCorpusForArchive(ctx context.Context, blobs SecurityToolRunBlobStore, key, packageRelative, target string) map[string][]byte {
	if blobs == nil || target == "" {
		return nil
	}
	raw, err := blobs.Get(ctx, key)
	if err != nil || len(raw) == 0 {
		return nil
	}
	document, err := decodeFuzzCorpusDocument(raw, "", false)
	if err != nil {
		return nil
	}
	directory := path.Join(packageRelative, "testdata", "fuzz", target)
	return fuzzCorpusEntriesForDirectory(document.Entries, directory)
}

func fuzzCorpusEntriesForDirectory(entries []fuzzCorpusEntry, directory string) map[string][]byte {
	out := make(map[string][]byte, len(entries))
	total := 0
	for _, entry := range entries {
		if len(out) >= maxFuzzCorpusEntries || len(entry.Data) == 0 || len(entry.Data) > maxFuzzCorpusEntryBytes {
			continue
		}
		if total+len(entry.Data) > maxFuzzCorpusBytes {
			break
		}
		total += len(entry.Data)
		// The stored path is never trusted: the entry name is re-derived from
		// the content, so a hostile corpus document cannot escape this directory.
		out[path.Join(directory, fuzzCorpusEntryName(entry.Data))] = entry.Data
	}
	return out
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
type fuzzCorpusLineage struct {
	InputFormat    string
	ProducerTool   string
	TargetRevision string
	TargetDigest   string
}

func persistFuzzCorpus(ctx context.Context, blobs SecurityToolRunBlobStore, key, campaign string, fresh []fuzzCorpusEntry) (int, error) {
	return persistFuzzCorpusWithLineage(ctx, blobs, key, campaign, fuzzCorpusLineage{}, fresh)
}

func persistFuzzCorpusWithLineage(ctx context.Context, blobs SecurityToolRunBlobStore, key, campaign string, lineage fuzzCorpusLineage, fresh []fuzzCorpusEntry) (int, error) {
	if blobs == nil {
		return 0, nil
	}
	document := fuzzCorpusDocument{SchemaVersion: fuzzCorpusSchemaVersion, Campaign: campaign, InputFormat: lineage.InputFormat, ProducerTool: lineage.ProducerTool, TargetRevision: lineage.TargetRevision, TargetDigest: lineage.TargetDigest}
	if raw, getErr := blobs.Get(ctx, key); getErr == nil && len(raw) > 0 {
		stored, decodeErr := decodeFuzzCorpusDocument(raw, campaign, false)
		if decodeErr != nil {
			return 0, fmt.Errorf("decode stored fuzz corpus: %w", decodeErr)
		}
		document.Entries = stored.Entries
		if stored.SchemaVersion == fuzzCorpusSchemaVersion {
			document.ParentDigest = stored.SnapshotDigest
		}
	}
	known := map[string]bool{}
	for _, entry := range document.Entries {
		known[entry.Digest] = true
	}
	added := 0
	for _, entry := range fresh {
		if len(entry.Data) == 0 || len(entry.Data) > maxFuzzCorpusEntryBytes {
			continue
		}
		sum := sha256.Sum256(entry.Data)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		if known[digest] {
			continue
		}
		known[digest] = true
		document.Entries = append(document.Entries, fuzzCorpusEntry{Path: fuzzCorpusEntryName(entry.Data), Digest: digest, Data: entry.Data})
		added++
	}
	if added == 0 {
		return 0, nil
	}
	document.Entries = boundFuzzCorpus(document.Entries)
	canonical, err := canonicalFuzzCorpusEntries(document.Entries)
	if err != nil {
		return 0, err
	}
	document.Entries = canonical
	document.SnapshotDigest, err = fuzzCorpusSnapshotDigest(document)
	if err != nil {
		return 0, err
	}
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
	if restored == 0 {
		return fmt.Sprintf("fuzz corpus: cold start, persisted %d new input(s) for the next campaign", persisted)
	}
	return fmt.Sprintf("fuzz corpus: restored %d seed input(s), persisted %d new input(s) for the next campaign", restored, persisted)
}

// goFuzzCorpusForArchive resolves this campaign's persisted corpus into archive
// entries, keyed by their path inside the staged target.
func (t *runSecurityToolTool) goFuzzCorpusForArchive(ctx context.Context, in runSecurityToolInput, local string) map[string][]byte {
	if in.Tool != "go-fuzz-tests" {
		return nil
	}
	pkg, target := in.Arguments["package"], securitytoolpacks.GoFuzzTargetName(in.Arguments["fuzz"])
	if pkg == "" || target == "" {
		return nil
	}
	packageDir := securitytoolpacks.GoFuzzPackageDir(local, pkg)
	relative, err := filepath.Rel(local, packageDir)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		relative = ""
	}
	campaign := fuzzCampaignIdentity(t.state.scanCtx.Repository, fuzzCampaignTargetRoot(in.Target.Locator), pkg, target)
	key := fuzzCorpusObjectKey(t.deps.Namespace, in.Tool, campaign)
	return fuzzCorpusForArchive(ctx, t.deps.Blobs, key, filepath.ToSlash(relative), target)
}

// countInjectedGoFuzzInputs excludes persisted entries that are already
// checked into the target and therefore will not be injected into its archive.
func countInjectedGoFuzzInputs(local string, injected map[string][]byte) int {
	count := 0
	for name := range injected {
		if _, err := os.Lstat(filepath.Join(local, filepath.FromSlash(name))); os.IsNotExist(err) {
			count++
		}
	}
	return count
}

// addGoFuzzCampaignMetadata carries the trusted restore count into the
// executor. Its path is reserved so target content cannot forge provenance.
func addGoFuzzCampaignMetadata(local string, restored int, injected map[string][]byte) (map[string][]byte, error) {
	reserved := filepath.Join(local, filepath.FromSlash(securitytoolpacks.GoFuzzCampaignMetadataPath))
	if _, err := os.Lstat(reserved); err == nil {
		return nil, fmt.Errorf("target contains reserved campaign metadata path %s", securitytoolpacks.GoFuzzCampaignMetadataPath)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect reserved campaign metadata path: %w", err)
	}
	metadata, err := securitytoolpacks.EncodeGoFuzzCampaignMetadata(restored)
	if err != nil {
		return nil, err
	}
	if injected == nil {
		injected = map[string][]byte{}
	}
	injected[securitytoolpacks.GoFuzzCampaignMetadataPath] = metadata
	return injected, nil
}

// persistGoFuzzCorpus stores what this campaign produced so the next one
// starts from it, and returns the note describing corpus provenance.
func (t *runSecurityToolTool) persistGoFuzzCorpus(ctx context.Context, in runSecurityToolInput, restored int, artifacts []runSecurityToolArtifact) string {
	if in.Tool != "go-fuzz-tests" {
		return ""
	}
	pkg, target := in.Arguments["package"], securitytoolpacks.GoFuzzTargetName(in.Arguments["fuzz"])
	if pkg == "" || target == "" {
		return ""
	}
	campaign := fuzzCampaignIdentity(t.state.scanCtx.Repository, fuzzCampaignTargetRoot(in.Target.Locator), pkg, target)
	key := fuzzCorpusObjectKey(t.deps.Namespace, in.Tool, campaign)
	persisted, err := persistFuzzCorpus(ctx, t.deps.Blobs, key, campaign, goFuzzCorpusArtifacts(ctx, t.deps.Blobs, artifacts))
	if err != nil {
		return "fuzz corpus could not be persisted: " + err.Error()
	}
	return goFuzzCampaignNote(restored, persisted)
}

const rustFuzzCorpusFamily = "rust-libfuzzer-v1"

func rustFuzzCampaignNote(restored, persisted int) string {
	if restored == 0 {
		return fmt.Sprintf("Rust fuzz corpus: cold start, persisted %d new input(s) for the next compatible campaign", persisted)
	}
	return fmt.Sprintf("Rust fuzz corpus: restored %d input(s), persisted %d new input(s) for the next compatible campaign", restored, persisted)
}

func (t *runSecurityToolTool) rustFuzzCorpusCoordinates(in runSecurityToolInput) (string, string) {
	target := in.Arguments["fuzz_target"]
	campaign := fuzzCampaignIdentity(t.state.scanCtx.Repository, fuzzCampaignTargetRoot(in.Target.Locator), rustFuzzCorpusFamily, target)
	return campaign, fuzzCorpusObjectKey(t.deps.Namespace, rustFuzzCorpusFamily, campaign)
}

func (t *runSecurityToolTool) rustFuzzCorpusForArchive(ctx context.Context, in runSecurityToolInput) map[string][]byte {
	if in.Tool != "cargo-fuzz" || in.Arguments["fuzz_target"] == "" || t.deps.Blobs == nil {
		return nil
	}
	campaign, key := t.rustFuzzCorpusCoordinates(in)
	raw, err := t.deps.Blobs.Get(ctx, key)
	if err != nil || len(raw) == 0 {
		return nil
	}
	document, err := decodeFuzzCorpusDocument(raw, campaign, true)
	if err != nil {
		return nil
	}
	return fuzzCorpusEntriesForDirectory(document.Entries, path.Join("fuzz", "corpus", in.Arguments["fuzz_target"]))
}

func addRustFuzzCampaignMetadata(local string, restored int, injected map[string][]byte) (map[string][]byte, error) {
	reserved := filepath.Join(local, filepath.FromSlash(securitytoolpacks.RustFuzzCampaignMetadataPath))
	if _, err := os.Lstat(reserved); err == nil {
		return nil, fmt.Errorf("target contains reserved campaign metadata path %s", securitytoolpacks.RustFuzzCampaignMetadataPath)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect reserved campaign metadata path: %w", err)
	}
	metadata, err := securitytoolpacks.EncodeRustFuzzCampaignMetadata(restored)
	if err != nil {
		return nil, err
	}
	if injected == nil {
		injected = map[string][]byte{}
	}
	injected[securitytoolpacks.RustFuzzCampaignMetadataPath] = metadata
	return injected, nil
}

func rustFuzzCorpusArtifacts(ctx context.Context, blobs SecurityToolRunBlobStore, artifacts []runSecurityToolArtifact) ([]fuzzCorpusEntry, error) {
	if blobs == nil {
		return nil, nil
	}
	crashes := map[string]bool{}
	for _, artifact := range artifacts {
		if strings.HasPrefix(artifact.Name, "cargo-fuzz-crash/") {
			crashes[artifact.Digest] = true
		}
	}
	entries := make([]fuzzCorpusEntry, 0, min(len(artifacts), maxFuzzCorpusEntries))
	seenKeys := map[string]bool{}
	total := int64(0)
	for _, artifact := range artifacts {
		if !strings.HasPrefix(artifact.Name, "cargo-fuzz-corpus/") || artifact.ObjectKey == "" {
			continue
		}
		if len(entries) >= maxFuzzCorpusEntries || seenKeys[artifact.ObjectKey] {
			continue
		}
		seenKeys[artifact.ObjectKey] = true
		if artifact.MediaType != "application/octet-stream" || artifact.Size <= 0 || artifact.Size > maxFuzzCorpusEntryBytes || total+artifact.Size > maxFuzzCorpusBytes {
			return nil, fmt.Errorf("cargo-fuzz corpus artifact %q violates storage bounds", artifact.Name)
		}
		data, err := blobs.Get(ctx, artifact.ObjectKey)
		if err != nil {
			return nil, err
		}
		if int64(len(data)) != artifact.Size {
			return nil, fmt.Errorf("cargo-fuzz corpus artifact %q size mismatch", artifact.Name)
		}
		sum := sha256.Sum256(data)
		digest := "sha256:" + hex.EncodeToString(sum[:])
		canonicalName := "cargo-fuzz-corpus/" + strings.TrimPrefix(digest, "sha256:")[:32]
		if digest != artifact.Digest || artifact.Name != canonicalName {
			return nil, fmt.Errorf("cargo-fuzz corpus artifact %q failed integrity validation", artifact.Name)
		}
		if crashes[digest] {
			continue
		}
		total += artifact.Size
		entries = append(entries, fuzzCorpusEntry{Path: canonicalName, Digest: digest, Data: data})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Digest < entries[j].Digest })
	return entries, nil
}

func (t *runSecurityToolTool) persistRustFuzzCorpus(ctx context.Context, in runSecurityToolInput, targetRevision, targetDigest string, restored int, artifacts []runSecurityToolArtifact) string {
	if in.Tool != "cargo-fuzz" || in.Arguments["fuzz_target"] == "" {
		return ""
	}
	entries, err := rustFuzzCorpusArtifacts(ctx, t.deps.Blobs, artifacts)
	if err != nil {
		return "Rust fuzz corpus could not be verified: " + err.Error()
	}
	campaign, key := t.rustFuzzCorpusCoordinates(in)
	lineage := fuzzCorpusLineage{InputFormat: "bytes", ProducerTool: in.Tool, TargetRevision: targetRevision, TargetDigest: targetDigest}
	persisted, err := persistFuzzCorpusWithLineage(ctx, t.deps.Blobs, key, campaign, lineage, entries)
	if err != nil {
		return "Rust fuzz corpus could not be persisted: " + err.Error()
	}
	return rustFuzzCampaignNote(restored, persisted)
}
