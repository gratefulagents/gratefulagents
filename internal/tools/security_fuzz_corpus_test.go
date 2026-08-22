package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gratefulagents/gratefulagents/internal/securitytoolpacks"
)

type recordingCorpusStore struct {
	objects map[string][]byte
}

func (s *recordingCorpusStore) Get(_ context.Context, key string) ([]byte, error) {
	data, ok := s.objects[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func (s *recordingCorpusStore) Put(_ context.Context, key string, content []byte, _ string) error {
	if s.objects == nil {
		s.objects = map[string][]byte{}
	}
	s.objects[key] = content
	return nil
}

func archiveNames(t *testing.T, archive []byte) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	names := []string{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
	slices.Sort(names)
	return names
}

// A restored corpus must reach the execution Job without ever touching the
// user's checkout: writing seed inputs into the workspace leaves untracked
// files behind for later agent work to trip over or commit by accident.
func TestFuzzCorpusIsInjectedIntoTheArchiveNotTheWorkspace(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "parser"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "parser", "parser.go"), []byte("package parser"), 0o600); err != nil {
		t.Fatal(err)
	}

	document, err := json.Marshal(fuzzCorpusDocument{
		SchemaVersion: fuzzCorpusLegacyVersion, Campaign: "c1",
		Entries: []fuzzCorpusEntry{{Path: "parser/testdata/fuzz/FuzzDecode/old", Digest: "sha256:x", Data: []byte("seed-input")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	blobs := &recordingCorpusStore{objects: map[string][]byte{"corpus-key": document}}

	injected := fuzzCorpusForArchive(context.Background(), blobs, "corpus-key", "parser", "FuzzDecode")
	if len(injected) != 1 {
		t.Fatalf("resolved %d corpus entries, want 1", len(injected))
	}

	archive, entries, _, err := archiveWorkspaceTargetWithInjected(workspace, injected)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	names := archiveNames(t, archive)
	if !slices.ContainsFunc(names, func(name string) bool {
		return len(name) > len("parser/testdata/fuzz/FuzzDecode/") && name[:len("parser/testdata/fuzz/FuzzDecode/")] == "parser/testdata/fuzz/FuzzDecode/"
	}) {
		t.Fatalf("the archive does not carry the restored corpus: %v", names)
	}
	if entries < 2 {
		t.Fatalf("archive entry count = %d, want the project plus the injected input", entries)
	}

	// The workspace is exactly as the user left it.
	if _, err := os.Stat(filepath.Join(workspace, "parser", "testdata")); !os.IsNotExist(err) {
		t.Fatalf("restoring the corpus wrote into the workspace: %v", err)
	}
}

// A hostile corpus document cannot place a file outside the target's own seed
// corpus, because the entry name is re-derived from its content.
func TestFuzzCorpusEntryPathsAreDerivedNotTrusted(t *testing.T) {
	document, err := json.Marshal(fuzzCorpusDocument{
		SchemaVersion: fuzzCorpusLegacyVersion,
		Entries: []fuzzCorpusEntry{
			{Path: "../../../etc/passwd", Digest: "sha256:a", Data: []byte("evil")},
			{Path: "/absolute", Digest: "sha256:b", Data: []byte("also evil")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	blobs := &recordingCorpusStore{objects: map[string][]byte{"k": document}}
	for name := range fuzzCorpusForArchive(context.Background(), blobs, "k", "parser", "FuzzDecode") {
		if !slices.Contains([]string{"parser/testdata/fuzz/FuzzDecode"}, filepath.ToSlash(filepath.Dir(name))) {
			t.Fatalf("corpus entry escaped its seed directory: %q", name)
		}
	}
}

func TestPersistFuzzCorpusStaysBounded(t *testing.T) {
	blobs := &recordingCorpusStore{}
	fresh := make([]fuzzCorpusEntry, 0, maxFuzzCorpusEntries+8)
	for i := range maxFuzzCorpusEntries + 8 {
		fresh = append(fresh, fuzzCorpusEntry{Digest: string(rune('a'+i%26)) + string(rune('0'+i/26)), Data: []byte{byte(i)}})
	}
	if _, err := persistFuzzCorpus(context.Background(), blobs, "k", "c1", fresh); err != nil {
		t.Fatalf("persist: %v", err)
	}
	var stored fuzzCorpusDocument
	if err := json.Unmarshal(blobs.objects["k"], &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Entries) > maxFuzzCorpusEntries {
		t.Fatalf("stored %d entries, want at most %d", len(stored.Entries), maxFuzzCorpusEntries)
	}
}

// A corpus input persisted by an earlier campaign can later be committed to the
// repository at the same content-derived path. Two tar headers for one path
// abort extraction in the Job (files are created with O_EXCL), which would
// break every later campaign, so the workspace copy has to win.
func TestInjectedCorpusNeverDuplicatesAWorkspaceEntry(t *testing.T) {
	workspace := t.TempDir()
	seedDir := filepath.Join(workspace, "parser", "testdata", "fuzz", "FuzzDecode")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	committed := []byte("seed-input")
	name := fuzzCorpusEntryName(committed)
	if err := os.WriteFile(filepath.Join(seedDir, name), committed, 0o600); err != nil {
		t.Fatal(err)
	}

	injected := map[string][]byte{
		"parser/testdata/fuzz/FuzzDecode/" + name: committed,
		"parser/testdata/fuzz/FuzzDecode/fresh":   []byte("new-input"),
	}
	archive, _, _, err := archiveWorkspaceTargetWithInjected(workspace, injected)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	names := archiveNames(t, archive)
	seen := map[string]int{}
	for _, entry := range names {
		seen[entry]++
	}
	for entry, count := range seen {
		if count > 1 {
			t.Fatalf("archive carries %d headers for %q", count, entry)
		}
	}
	if seen["parser/testdata/fuzz/FuzzDecode/fresh"] != 1 {
		t.Fatalf("a corpus input absent from the workspace was not injected: %v", names)
	}
}

func TestGoFuzzCampaignNoteDistinguishesColdAndRestored(t *testing.T) {
	if got := goFuzzCampaignNote(0, 1); got != "fuzz corpus: cold start, persisted 1 new input(s) for the next campaign" {
		t.Fatalf("cold note = %q", got)
	}
	if got := goFuzzCampaignNote(2, 1); got != "fuzz corpus: restored 2 seed input(s), persisted 1 new input(s) for the next campaign" {
		t.Fatalf("warm note = %q", got)
	}
}

func TestFuzzCampaignIdentityIsStableAndScoped(t *testing.T) {
	base := fuzzCampaignIdentity("https://example.test/repo", "service-a", "./parser", "FuzzDecode")
	if base != fuzzCampaignIdentity("https://example.test/repo", "service-a", "./parser", "FuzzDecode") {
		t.Fatal("identical campaign coordinates produced different identities")
	}
	for _, different := range []string{
		fuzzCampaignIdentity("https://example.test/other", "service-a", "./parser", "FuzzDecode"),
		fuzzCampaignIdentity("https://example.test/repo", "service-b", "./parser", "FuzzDecode"),
		fuzzCampaignIdentity("https://example.test/repo", "service-a", "./codec", "FuzzDecode"),
		fuzzCampaignIdentity("https://example.test/repo", "service-a", "./parser", "FuzzEncode"),
	} {
		if different == base {
			t.Fatal("a different repository, package, or target reused the campaign identity")
		}
	}
}

func TestGoFuzzSecondCampaignRestoresFirstCampaignCorpus(t *testing.T) {
	blobs := &recordingCorpusStore{}
	repository, pkg, target := "https://example.test/repo", "./parser", "FuzzDecode"
	campaign := fuzzCampaignIdentity(repository, "service-a", pkg, target)
	key := fuzzCorpusObjectKey("security", "go-fuzz-tests", campaign)
	seed := []byte("go test fuzz v1\n[]byte(\"learned\")\n")
	digest := fuzzCorpusEntryName(seed)

	added, err := persistFuzzCorpus(context.Background(), blobs, key, campaign, []fuzzCorpusEntry{{
		Path: "go-fuzz-corpus/parser/testdata/fuzz/FuzzDecode/crash", Digest: "sha256:" + digest, Data: seed,
	}})
	if err != nil || added != 1 {
		t.Fatalf("first campaign persist = %d, %v", added, err)
	}

	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "parser"), 0o755); err != nil {
		t.Fatal(err)
	}
	injected := fuzzCorpusForArchive(context.Background(), blobs, key, "parser", target)
	if len(injected) != 1 || string(injected["parser/testdata/fuzz/FuzzDecode/"+digest]) != string(seed) {
		t.Fatalf("second campaign inputs = %#v", injected)
	}
	injected, err = addGoFuzzCampaignMetadata(workspace, len(injected), injected)
	if err != nil {
		t.Fatal(err)
	}
	archive, _, _, err := archiveWorkspaceTargetWithInjected(workspace, injected)
	if err != nil {
		t.Fatal(err)
	}
	names := archiveNames(t, archive)
	if !slices.Contains(names, "parser/testdata/fuzz/FuzzDecode/"+digest) || !slices.Contains(names, ".gratefulagents/go-fuzz-campaign.json") {
		t.Fatalf("warm campaign archive is missing corpus or provenance: %v", names)
	}
	if _, err := os.Stat(filepath.Join(workspace, "parser", "testdata")); !os.IsNotExist(err) {
		t.Fatalf("restoring the corpus modified the workspace: %v", err)
	}
}

func TestCargoFuzzSecondCampaignRestoresFirstCorpusWithoutTouchingWorkspace(t *testing.T) {
	blobs := &recordingCorpusStore{}
	tool := &runSecurityToolTool{state: &securityScanState{scanCtx: SecurityScanContext{Repository: "https://example.test/repo"}}, deps: SecurityToolRunDeps{Namespace: "security", Blobs: blobs}}
	in := runSecurityToolInput{Tool: "cargo-fuzz", Target: runSecurityToolTarget{Locator: "client", Revision: "rev-1"}, Arguments: map[string]string{"fuzz_target": "decode"}}
	campaign, key := tool.rustFuzzCorpusCoordinates(in)
	seed := []byte("learned-rust-input")
	added, err := persistFuzzCorpusWithLineage(context.Background(), blobs, key, campaign, fuzzCorpusLineage{InputFormat: "bytes", ProducerTool: "cargo-fuzz", TargetRevision: "rev-1", TargetDigest: "sha256:" + strings.Repeat("a", 64)}, []fuzzCorpusEntry{{Digest: "sha256:" + fuzzCorpusEntryName(seed), Data: seed}})
	if err != nil || added != 1 {
		t.Fatalf("persist = %d, %v", added, err)
	}
	workspace := t.TempDir()
	injected := tool.rustFuzzCorpusForArchive(context.Background(), in)
	if len(injected) != 1 {
		t.Fatalf("restored %d inputs", len(injected))
	}
	injected, err = addRustFuzzCampaignMetadata(workspace, len(injected), injected)
	if err != nil {
		t.Fatal(err)
	}
	archive, _, _, err := archiveWorkspaceTargetWithInjected(workspace, injected)
	if err != nil {
		t.Fatal(err)
	}
	names := archiveNames(t, archive)
	want := "fuzz/corpus/decode/" + fuzzCorpusEntryName(seed)
	if !slices.Contains(names, want) || !slices.Contains(names, securitytoolpacks.RustFuzzCampaignMetadataPath) {
		t.Fatalf("archive missing Rust corpus/provenance: %v", names)
	}
	if _, err := os.Stat(filepath.Join(workspace, "fuzz")); !os.IsNotExist(err) {
		t.Fatalf("restore modified workspace: %v", err)
	}
	var stored fuzzCorpusDocument
	if err := json.Unmarshal(blobs.objects[key], &stored); err != nil {
		t.Fatal(err)
	}
	if stored.InputFormat != "bytes" || stored.ProducerTool != "cargo-fuzz" || stored.SnapshotDigest == "" {
		t.Fatalf("lineage missing: %+v", stored)
	}
}

func TestCargoFuzzCorpusIsIsolatedAndCrashArtifactsAreExcluded(t *testing.T) {
	corpus, crash := []byte("corpus"), []byte("crash")
	corpusSum, crashSum := sha256.Sum256(corpus), sha256.Sum256(crash)
	corpusDigest := "sha256:" + hex.EncodeToString(corpusSum[:])
	crashDigest := "sha256:" + hex.EncodeToString(crashSum[:])
	blobs := &recordingCorpusStore{objects: map[string][]byte{"corpus": corpus, "crash": crash}}
	entries, err := rustFuzzCorpusArtifacts(context.Background(), blobs, []runSecurityToolArtifact{
		{Name: "cargo-fuzz-corpus/" + strings.TrimPrefix(corpusDigest, "sha256:")[:32], MediaType: "application/octet-stream", ObjectKey: "corpus", Digest: corpusDigest, Size: int64(len(corpus))},
		{Name: "cargo-fuzz-crash/crash-a", MediaType: "application/octet-stream", ObjectKey: "crash", Digest: crashDigest, Size: int64(len(crash))},
	})
	if err != nil || len(entries) != 1 || string(entries[0].Data) != "corpus" {
		t.Fatalf("durable entries = %+v, err=%v", entries, err)
	}
	base := fuzzCampaignIdentity("repo-a", "root", rustFuzzCorpusFamily, "decode")
	for _, other := range []string{
		fuzzCampaignIdentity("repo-b", "root", rustFuzzCorpusFamily, "decode"),
		fuzzCampaignIdentity("repo-a", "other", rustFuzzCorpusFamily, "decode"),
		fuzzCampaignIdentity("repo-a", "root", rustFuzzCorpusFamily, "encode"),
	} {
		if other == base {
			t.Fatal("Rust corpus lineage was not isolated")
		}
	}
}

func TestCargoFuzzCorpusRejectsTamperedSnapshotAndCrashDuplicate(t *testing.T) {
	blobs := &recordingCorpusStore{}
	seed := []byte("same-input")
	sum := sha256.Sum256(seed)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	name := "cargo-fuzz-corpus/" + strings.TrimPrefix(digest, "sha256:")[:32]
	entries, err := rustFuzzCorpusArtifacts(context.Background(), &recordingCorpusStore{objects: map[string][]byte{"corpus": seed}}, []runSecurityToolArtifact{
		{Name: name, MediaType: "application/octet-stream", ObjectKey: "corpus", Digest: digest, Size: int64(len(seed))},
		{Name: "cargo-fuzz-crash/crash", MediaType: "application/octet-stream", ObjectKey: "crash", Digest: digest, Size: int64(len(seed))},
	})
	if err != nil || len(entries) != 0 {
		t.Fatalf("crashing input was retained: %+v, err=%v", entries, err)
	}
	campaign := "campaign"
	lineage := fuzzCorpusLineage{InputFormat: "bytes", ProducerTool: "cargo-fuzz", TargetRevision: "rev", TargetDigest: "sha256:" + strings.Repeat("a", 64)}
	if _, err := persistFuzzCorpusWithLineage(context.Background(), blobs, "key", campaign, lineage, []fuzzCorpusEntry{{Data: seed}}); err != nil {
		t.Fatal(err)
	}
	var document fuzzCorpusDocument
	if err := json.Unmarshal(blobs.objects["key"], &document); err != nil {
		t.Fatal(err)
	}
	document.TargetRevision = "forged"
	tampered, _ := json.Marshal(document)
	if _, err := decodeFuzzCorpusDocument(tampered, campaign, true); err == nil {
		t.Fatal("tampered lineage was accepted")
	}
}

func TestRustFuzzCampaignMetadataPathIsReserved(t *testing.T) {
	workspace := t.TempDir()
	reserved := filepath.Join(workspace, filepath.FromSlash(securitytoolpacks.RustFuzzCampaignMetadataPath))
	if err := os.MkdirAll(filepath.Dir(reserved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reserved, []byte(`{"restored_inputs":999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := addRustFuzzCampaignMetadata(workspace, 0, nil); err == nil {
		t.Fatal("target-authored Rust campaign provenance was accepted")
	}
}

func TestGoFuzzCampaignMetadataPathIsReserved(t *testing.T) {
	workspace := t.TempDir()
	reserved := filepath.Join(workspace, ".gratefulagents", "go-fuzz-campaign.json")
	if err := os.MkdirAll(filepath.Dir(reserved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reserved, []byte(`{"restored_inputs":999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := addGoFuzzCampaignMetadata(workspace, 0, nil); err == nil {
		t.Fatal("target-authored campaign provenance was accepted")
	}
}
