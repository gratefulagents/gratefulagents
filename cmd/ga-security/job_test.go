package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gratefulagents/gratefulagents/internal/security"
	"github.com/gratefulagents/gratefulagents/internal/securitytoolpacks"
	"github.com/gratefulagents/gratefulagents/internal/securitytoolrun"
)

type putRecord struct {
	key       string
	content   []byte
	mediaType string
}

type fakeStore struct {
	objects map[string][]byte
	getErr  error
	puts    []putRecord
	putErr  map[string]error
}

func newFakeStore() *fakeStore {
	return &fakeStore{objects: map[string][]byte{}, putErr: map[string]error{}}
}

func (f *fakeStore) Get(_ context.Context, key string) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	data, ok := f.objects[key]
	if !ok {
		return nil, errors.New("no such key")
	}
	return data, nil
}

func (f *fakeStore) Put(_ context.Context, key string, content []byte, mediaType string) error {
	if err := f.putErr[key]; err != nil {
		return err
	}
	f.puts = append(f.puts, putRecord{key: key, content: append([]byte(nil), content...), mediaType: mediaType})
	f.objects[key] = append([]byte(nil), content...)
	return nil
}

func (f *fakeStore) keys() []string {
	keys := make([]string, 0, len(f.puts))
	for _, put := range f.puts {
		keys = append(keys, put.key)
	}
	return keys
}

type tarEntry struct {
	name     string
	body     string
	typeflag byte
	linkname string
	size     int64
}

func buildTarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		flag := entry.typeflag
		if flag == 0 {
			flag = tar.TypeReg
		}
		size := int64(len(entry.body))
		if entry.size != 0 {
			size = entry.size
		}
		if flag != tar.TypeReg {
			size = 0
		}
		header := &tar.Header{Name: entry.name, Mode: 0o600, Size: size, Typeflag: flag, Linkname: entry.linkname}
		if flag == tar.TypeDir {
			header.Mode = 0o700
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if flag == tar.TypeReg {
			body := entry.body
			if entry.size != 0 {
				body = strings.Repeat("a", int(entry.size))
			}
			if _, err := tarWriter.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestExtractTarGzWritesRegularFilesAndDirectories(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "target")
	archive := buildTarGz(t,
		tarEntry{name: "repo/", typeflag: tar.TypeDir},
		tarEntry{name: "repo/main.go", body: "package main\n"},
		tarEntry{name: "repo/link", typeflag: tar.TypeSymlink, linkname: "main.go"},
	)
	if err := extractTarGz(archive, dest, defaultTarLimits); err != nil {
		t.Fatalf("extractTarGz() = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "repo", "main.go"))
	if err != nil || string(data) != "package main\n" {
		t.Fatalf("extracted file = %q, %v", data, err)
	}
	info, err := os.Lstat(filepath.Join(dest, "repo", "link"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("in-root symlink not extracted: %v %v", info, err)
	}
}

func TestExtractTarGzRejectsUnsafeEntries(t *testing.T) {
	cases := map[string]struct {
		entries []tarEntry
		limits  tarLimits
		want    string
	}{
		"parent traversal": {
			entries: []tarEntry{{name: "../escape.txt", body: "x"}},
			want:    "traverses outside",
		},
		"nested traversal": {
			entries: []tarEntry{{name: "repo/../../escape.txt", body: "x"}},
			want:    "traverses outside",
		},
		"absolute path": {
			entries: []tarEntry{{name: "/etc/passwd", body: "x"}},
			want:    "absolute path",
		},
		"escaping symlink": {
			entries: []tarEntry{{name: "escape", typeflag: tar.TypeSymlink, linkname: "../../etc"}},
			want:    "outside the extraction root",
		},
		"absolute symlink": {
			entries: []tarEntry{{name: "escape", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}},
			want:    "absolute path",
		},
		"escaping hardlink": {
			entries: []tarEntry{{name: "escape", typeflag: tar.TypeLink, linkname: "../outside"}},
			want:    "outside the extraction root",
		},
		"write through symlinked directory": {
			entries: []tarEntry{
				{name: "dir", typeflag: tar.TypeSymlink, linkname: "."},
				{name: "dir/payload", body: "x"},
			},
			want: "traverses a symlink",
		},
		"oversized entry": {
			entries: []tarEntry{{name: "big", size: 4096}},
			limits:  tarLimits{maxEntries: 10, maxEntryBytes: 512, maxTotalBytes: 1 << 20},
			want:    "over the 512-byte entry limit",
		},
		"total size budget": {
			entries: []tarEntry{{name: "a", size: 400}, {name: "b", size: 400}},
			limits:  tarLimits{maxEntries: 10, maxEntryBytes: 1024, maxTotalBytes: 600},
			want:    "extraction size limit",
		},
		"entry count budget": {
			entries: []tarEntry{{name: "a", body: "x"}, {name: "b", body: "x"}},
			limits:  tarLimits{maxEntries: 1, maxEntryBytes: 1024, maxTotalBytes: 1 << 20},
			want:    "more than 1 entries",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			limits := testCase.limits
			if limits.maxEntries == 0 {
				limits = defaultTarLimits
			}
			dest := filepath.Join(t.TempDir(), "target")
			err := extractTarGz(buildTarGz(t, testCase.entries...), dest, limits)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("extractTarGz() = %v, want error containing %q", err, testCase.want)
			}
			if _, statErr := os.Stat(filepath.Join(filepath.Dir(dest), "escape.txt")); statErr == nil {
				t.Fatal("archive escaped the extraction root")
			}
		})
	}
}

// jobFixture wires executeJob to a temp workdir, a fake store, and a fake
// runner so no test touches S3 or executes a scanner.
type jobFixture struct {
	env     map[string]string
	store   *fakeStore
	workdir string
	result  securitytoolpacks.Result
	runErr  error
	runs    int
	stdout  bytes.Buffer
	stderr  bytes.Buffer
	config  securitytoolpacks.RunConfig
}

func newJobFixture(t *testing.T, config securitytoolpacks.RunConfig) *jobFixture {
	t.Helper()
	workdir := t.TempDir()
	configPath := filepath.Join(workdir, "run.json")
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return &jobFixture{
		env: map[string]string{
			"GA_JOB_CONFIG":        configPath,
			"GA_JOB_WORKDIR":       workdir,
			"GA_JOB_OUTPUT_PREFIX": "runs/scan-1",
		},
		store:   newFakeStore(),
		workdir: workdir,
		result:  securitytoolpacks.Result{Status: securitytoolpacks.StatusPass},
	}
}

func (f *jobFixture) run(t *testing.T) int {
	t.Helper()
	return executeJob(context.Background(), jobDeps{
		env:   func(key string) string { return f.env[key] },
		store: f.store,
		run: func(_ context.Context, config securitytoolpacks.RunConfig) (securitytoolpacks.Result, error) {
			f.runs++
			f.config = config
			return f.result, f.runErr
		},
		stdout: &f.stdout,
		stderr: &f.stderr,
	})
}

func TestJobUploadsManifestLastWithExpectedShape(t *testing.T) {
	fixture := newJobFixture(t, securitytoolpacks.RunConfig{Tool: "authorization-matrix"})
	fixture.result = securitytoolpacks.Result{
		Status: securitytoolpacks.StatusFindings,
		Findings: []security.ScannerRecord{
			{Tool: "authorization-matrix", RuleID: "R1", Message: "m", Severity: "high", FilePath: "a"},
			{Tool: "authorization-matrix", RuleID: "R2", Message: "m", Severity: "low", FilePath: "b"},
		},
		Artifacts: []securitytoolpacks.Artifact{
			{MediaType: "application/json", Digest: digestBytes([]byte("one")), Size: 3, Data: []byte("one")},
			{MediaType: "text/plain", Digest: digestBytes([]byte("two")), Size: 3, Data: []byte("two")},
		},
		Errors: []string{"partial coverage"},
	}
	if code := fixture.run(t); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, fixture.stderr.String())
	}

	wantKeys := []string{
		"runs/scan-1/result.json", "runs/scan-1/raw-00", "runs/scan-1/raw-01", "runs/scan-1/manifest.json",
	}
	if got := fixture.store.keys(); !slicesEqual(got, wantKeys) {
		t.Fatalf("upload order = %v, want %v", got, wantKeys)
	}

	var manifest map[string]any
	if err := json.Unmarshal(fixture.store.objects["runs/scan-1/manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	resultBytes := fixture.store.objects["runs/scan-1/result.json"]
	want := map[string]any{
		"schema_version":    "security-tool-job-manifest/v1",
		"tool":              "authorization-matrix",
		"status":            "findings",
		"finding_count":     float64(2),
		"result_object_key": "runs/scan-1/result.json",
		"result_digest":     digestBytes(resultBytes),
	}
	for key, value := range want {
		if manifest[key] != value {
			t.Fatalf("manifest[%q] = %v, want %v", key, manifest[key], value)
		}
	}
	if errs, _ := manifest["errors"].([]any); len(errs) != 1 || errs[0] != "partial coverage" {
		t.Fatalf("manifest errors = %v", manifest["errors"])
	}
	artifacts, _ := manifest["artifacts"].([]any)
	if len(artifacts) != 2 {
		t.Fatalf("manifest artifacts = %v", manifest["artifacts"])
	}
	first, _ := artifacts[0].(map[string]any)
	if first["name"] != "raw-00" || first["object_key"] != "runs/scan-1/raw-00" ||
		first["media_type"] != "application/json" || first["digest"] != digestBytes([]byte("one")) ||
		first["size"] != float64(3) {
		t.Fatalf("manifest artifacts[0] = %v", first)
	}
	if got := fixture.store.objects["runs/scan-1/raw-01"]; string(got) != "two" {
		t.Fatalf("raw-01 = %q", got)
	}
	if _, err := os.Stat(filepath.Join(fixture.workdir, "out", "result.json")); err != nil {
		t.Fatal(err)
	}
}

func TestJobExitsZeroWhenScannerReportsErrorOrTimeout(t *testing.T) {
	statuses := []securitytoolpacks.Status{
		securitytoolpacks.StatusError, securitytoolpacks.StatusTimeout, securitytoolpacks.StatusPartial,
	}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			fixture := newJobFixture(t, securitytoolpacks.RunConfig{Tool: "authorization-matrix"})
			fixture.result = securitytoolpacks.Result{Status: status, Errors: []string{"boom"}}
			if code := fixture.run(t); code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, fixture.stderr.String())
			}
			var manifest securitytoolrun.Manifest
			if err := json.Unmarshal(fixture.store.objects["runs/scan-1/manifest.json"], &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.Status != string(status) || manifest.FindingCount != 0 {
				t.Fatalf("manifest = %+v", manifest)
			}
		})
	}
}

func TestJobFailsWhenStagingOrUploadFails(t *testing.T) {
	archive := buildTarGz(t, tarEntry{name: "repo/main.go", body: "package main\n"})

	t.Run("digest mismatch", func(t *testing.T) {
		fixture := newJobFixture(t, securitytoolpacks.RunConfig{Tool: "authorization-matrix"})
		fixture.store.objects["staged/target.tar.gz"] = archive
		fixture.env["GA_JOB_TARGET_KEY"] = "staged/target.tar.gz"
		fixture.env["GA_JOB_TARGET_DIGEST"] = digestBytes([]byte("something else"))
		if code := fixture.run(t); code == 0 {
			t.Fatal("exit=0, want non-zero for a digest mismatch")
		}
		if fixture.runs != 0 {
			t.Fatal("scanner ran despite a digest mismatch")
		}
		if len(fixture.store.puts) != 0 {
			t.Fatalf("uploaded %v despite a digest mismatch", fixture.store.keys())
		}
		if !strings.Contains(fixture.stderr.String(), "digest mismatch") {
			t.Fatalf("stderr = %q", fixture.stderr.String())
		}
	})

	t.Run("missing digest", func(t *testing.T) {
		fixture := newJobFixture(t, securitytoolpacks.RunConfig{Tool: "authorization-matrix"})
		fixture.store.objects["staged/target.tar.gz"] = archive
		fixture.env["GA_JOB_TARGET_KEY"] = "staged/target.tar.gz"
		if code := fixture.run(t); code == 0 || fixture.runs != 0 {
			t.Fatalf("exit=%d runs=%d, want non-zero without execution", code, fixture.runs)
		}
		if !strings.Contains(fixture.stderr.String(), "GA_JOB_TARGET_DIGEST") {
			t.Fatalf("stderr = %q", fixture.stderr.String())
		}
	})

	t.Run("missing output prefix", func(t *testing.T) {
		fixture := newJobFixture(t, securitytoolpacks.RunConfig{Tool: "authorization-matrix"})
		delete(fixture.env, "GA_JOB_OUTPUT_PREFIX")
		if code := fixture.run(t); code == 0 || !strings.Contains(fixture.stderr.String(), "GA_JOB_OUTPUT_PREFIX") {
			t.Fatalf("exit=%d stderr=%q", code, fixture.stderr.String())
		}
	})

	t.Run("unreadable config", func(t *testing.T) {
		fixture := newJobFixture(t, securitytoolpacks.RunConfig{Tool: "authorization-matrix"})
		fixture.env["GA_JOB_CONFIG"] = filepath.Join(fixture.workdir, "missing.json")
		if code := fixture.run(t); code == 0 || !strings.Contains(fixture.stderr.String(), "read config") {
			t.Fatalf("exit=%d stderr=%q", code, fixture.stderr.String())
		}
	})

	t.Run("manifest the controller could not trust", func(t *testing.T) {
		fixture := newJobFixture(t, securitytoolpacks.RunConfig{})
		if code := fixture.run(t); code == 0 || !strings.Contains(fixture.stderr.String(), "manifest tool is required") {
			t.Fatalf("exit=%d stderr=%q", code, fixture.stderr.String())
		}
		if _, uploaded := fixture.store.objects["runs/scan-1/manifest.json"]; uploaded {
			t.Fatal("uploaded a manifest the controller would reject")
		}
	})

	t.Run("manifest upload failure", func(t *testing.T) {
		fixture := newJobFixture(t, securitytoolpacks.RunConfig{Tool: "authorization-matrix"})
		fixture.store.putErr["runs/scan-1/manifest.json"] = errors.New("s3 down")
		code := fixture.run(t)
		if code == 0 || !strings.Contains(fixture.stderr.String(), "upload runs/scan-1/manifest.json") {
			t.Fatalf("exit=%d stderr=%q", code, fixture.stderr.String())
		}
	})
}

func TestJobStagesVerifiedTargetAndRewritesLocator(t *testing.T) {
	archive := buildTarGz(t, tarEntry{name: "repo/main.go", body: "package main\n"})
	fixture := newJobFixture(t, securitytoolpacks.RunConfig{
		Tool:   "authorization-matrix",
		Target: securitytoolpacks.Target{Type: "repository", Locator: "staged/target.tar.gz", Digest: digestBytes(archive)},
	})
	fixture.store.objects["staged/target.tar.gz"] = archive
	fixture.env["GA_JOB_TARGET_KEY"] = "staged/target.tar.gz"
	fixture.env["GA_JOB_TARGET_DIGEST"] = digestBytes(archive)

	if code := fixture.run(t); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, fixture.stderr.String())
	}
	wantLocator := filepath.Join(fixture.workdir, "target")
	if fixture.runs != 1 || fixture.config.Target.Locator != wantLocator {
		t.Fatalf("runs=%d locator=%q, want %q", fixture.runs, fixture.config.Target.Locator, wantLocator)
	}
	data, err := os.ReadFile(filepath.Join(wantLocator, "repo", "main.go"))
	if err != nil || string(data) != "package main\n" {
		t.Fatalf("staged target file = %q, %v", data, err)
	}
	extractedDigest, exists, err := securitytoolpacks.DigestPath(wantLocator)
	if err != nil || !exists {
		t.Fatalf("digest staged target: exists=%t err=%v", exists, err)
	}
	if fixture.config.Target.Digest != extractedDigest || fixture.config.Target.Digest == digestBytes(archive) {
		t.Fatalf("runtime digest = %q, want extracted content %q (not archive digest)", fixture.config.Target.Digest, extractedDigest)
	}
}

func TestJobSetsTheMediaTypeTheRegistryExpectsForExtractedTargets(t *testing.T) {
	archive := buildTarGz(t, tarEntry{name: "src/Token.sol", body: "contract Token {}\n"})
	tests := map[string]struct {
		tool       string
		targetType string
		media      string
		want       string
	}{
		"solidity project": {
			tool:       "aderyn",
			targetType: "solidity_project",
			media:      "application/gzip",
			want:       "application/vnd.gratefulagents.solidity-project.v1+directory",
		},
		"foundry project": {
			tool:       "forge-security-tests",
			targetType: "foundry_project",
			media:      "application/gzip",
			want:       "application/vnd.gratefulagents.foundry-security-project.v1+directory",
		},
		"echidna solidity project": {
			tool:       "echidna",
			targetType: "solidity_project",
			media:      "application/gzip",
			want:       "application/vnd.gratefulagents.solidity-project.v1+directory",
		},
		"tool without a directory requirement keeps the staged media type": {
			tool:       "authorization-matrix",
			targetType: "authorization_matrix",
			media:      "application/gzip",
			want:       "application/gzip",
		},
	}
	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newJobFixture(t, securitytoolpacks.RunConfig{
				Tool: testCase.tool,
				Target: securitytoolpacks.Target{
					Type: testCase.targetType, Locator: "staged/target.tar.gz", Digest: digestBytes(archive), MediaType: testCase.media,
				},
			})
			fixture.store.objects["staged/target.tar.gz"] = archive
			fixture.env["GA_JOB_TARGET_KEY"] = "staged/target.tar.gz"
			fixture.env["GA_JOB_TARGET_DIGEST"] = digestBytes(archive)

			if code := fixture.run(t); code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, fixture.stderr.String())
			}
			if fixture.config.Target.MediaType != testCase.want {
				t.Fatalf("media type = %q, want %q", fixture.config.Target.MediaType, testCase.want)
			}
			if fixture.config.Target.Locator != filepath.Join(fixture.workdir, "target") {
				t.Fatalf("locator = %q, want the extraction directory", fixture.config.Target.Locator)
			}
		})
	}
}

func TestJobResolvesStagedSingleFileTargets(t *testing.T) {
	tests := map[string]struct {
		targetType string
		fileName   string
		body       string
		mediaType  string
	}{
		"solidity contract": {
			targetType: "solidity_contract",
			fileName:   "Token.sol",
			body:       "contract Token {}\n",
			mediaType:  "application/vnd.gratefulagents.solidity-contract.v1+source",
		},
		"EVM bytecode": {
			targetType: "evm_bytecode",
			fileName:   "Token.hex",
			body:       "60006000f3\n",
			mediaType:  "application/vnd.gratefulagents.evm-bytecode.v1+hex",
		},
	}
	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			archive := buildTarGz(t, tarEntry{name: testCase.fileName, body: testCase.body})
			fixture := newJobFixture(t, securitytoolpacks.RunConfig{
				Tool: "mythril",
				Target: securitytoolpacks.Target{
					Type: testCase.targetType, Locator: "staged/target.tar.gz", Digest: digestBytes(archive), MediaType: "application/gzip",
				},
			})
			fixture.store.objects["staged/target.tar.gz"] = archive
			fixture.env["GA_JOB_TARGET_KEY"] = "staged/target.tar.gz"
			fixture.env["GA_JOB_TARGET_DIGEST"] = digestBytes(archive)

			if code := fixture.run(t); code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, fixture.stderr.String())
			}
			wantLocator := filepath.Join(fixture.workdir, "target", testCase.fileName)
			if fixture.config.Target.Locator != wantLocator || fixture.config.Target.MediaType != testCase.mediaType {
				t.Fatalf("target = %+v, want locator %q and media %q", fixture.config.Target, wantLocator, testCase.mediaType)
			}
		})
	}
}

func TestJobRejectsMultiFileArchiveForSingleFileTarget(t *testing.T) {
	archive := buildTarGz(t,
		tarEntry{name: "Token.sol", body: "contract Token {}\n"},
		tarEntry{name: "Other.sol", body: "contract Other {}\n"},
	)
	fixture := newJobFixture(t, securitytoolpacks.RunConfig{
		Tool:   "mythril",
		Target: securitytoolpacks.Target{Type: "solidity_contract", Locator: "staged/target.tar.gz", Digest: digestBytes(archive)},
	})
	fixture.store.objects["staged/target.tar.gz"] = archive
	fixture.env["GA_JOB_TARGET_KEY"] = "staged/target.tar.gz"
	fixture.env["GA_JOB_TARGET_DIGEST"] = digestBytes(archive)

	if code := fixture.run(t); code == 0 || fixture.runs != 0 || !strings.Contains(fixture.stderr.String(), "exactly one regular file") {
		t.Fatalf("exit=%d runs=%d stderr=%q", code, fixture.runs, fixture.stderr.String())
	}
}

func TestJobSettingsDefaults(t *testing.T) {
	env := map[string]string{"GA_JOB_OUTPUT_PREFIX": "/runs/scan-1/"}
	settings, err := jobSettingsFromEnv(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if settings.configPath != securitytoolrun.ConfigPath || settings.workdir != securitytoolrun.WorkDir ||
		settings.outputPrefix != "runs/scan-1" {
		t.Fatalf("settings = %+v", settings)
	}
}

func TestRunJobRejectsArguments(t *testing.T) {
	if code := runJob([]string{"--config", "x"}); code == 0 {
		t.Fatal("exit=0, want non-zero for unexpected job arguments")
	}
}

func slicesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
