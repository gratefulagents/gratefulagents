package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
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
		SchemaVersion: fuzzCorpusSchemaVersion, Campaign: "c1",
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
		SchemaVersion: fuzzCorpusSchemaVersion,
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
