package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestInstallVerifiedZipAndSkipDisabled(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.Create("release/bin/scanner")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("verified-binary"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive.Bytes())
	binarySum := sha256.Sum256([]byte("verified-binary"))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(archive.Bytes())
	}))
	defer server.Close()
	platforms := map[string]artifact{"linux/amd64": {
		Asset: server.URL + "/scanner_1.0.0.zip", SHA256: hex.EncodeToString(sum[:]),
		BinarySHA256: hex.EncodeToString(binarySum[:]),
	}}
	lock := lockFile{SchemaVersion: "security-tools-lock/v1", Tools: []lockedTool{
		{Name: "scanner", Status: "enabled", Binary: "scanner", Platforms: platforms, UnsupportedPlatforms: map[string]string{"linux/arm64": "fixture unsupported"}},
		{Name: "disabled", Status: "disabled", Reason: "fixture"},
	}}
	output := t.TempDir()
	if err := install(writeLock(t, lock), output, "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "scanner"))
	if err != nil || string(data) != "verified-binary" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestInstallRejectsChecksumAndMissingPlatform(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("bad"))
	}))
	defer server.Close()
	platforms := map[string]artifact{"linux/amd64": {
		Asset: server.URL + "/scanner_1.0.0-update", SHA256: string(bytes.Repeat([]byte{'0'}, 64)),
	}}
	base := lockedTool{Name: "scanner", Status: "enabled", Binary: "scanner", Platforms: platforms}
	lock := lockFile{SchemaVersion: "security-tools-lock/v1", Tools: []lockedTool{base}}
	if err := install(writeLock(t, lock), t.TempDir(), "linux/amd64"); err == nil {
		t.Fatal("expected checksum rejection")
	}
	if err := install(writeLock(t, lock), t.TempDir(), "linux/arm64"); err == nil {
		t.Fatal("expected missing-platform rejection")
	}
}

func TestInstallSkipsExplicitlyUnsupportedPlatform(t *testing.T) {
	lock := lockFile{SchemaVersion: "security-tools-lock/v1", Tools: []lockedTool{{
		Name: "scanner", Status: "enabled", Binary: "scanner",
		Platforms:            map[string]artifact{"linux/amd64": {Asset: "https://example.test/scanner-1.0.0.tar.gz", SHA256: string(bytes.Repeat([]byte{'0'}, 64))}},
		UnsupportedPlatforms: map[string]string{"linux/arm64": "upstream does not publish this platform"},
	}}}
	output := t.TempDir()
	if err := install(writeLock(t, lock), output, "linux/arm64"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(output, "scanner")); !os.IsNotExist(err) {
		t.Fatalf("unsupported scanner should not be installed, stat error = %v", err)
	}
}

func TestValidateLockRejectsMalformedPlatformDeclarations(t *testing.T) {
	fixtureArtifact := artifact{Asset: "https://example.test/scanner-1.0.0.tar.gz", SHA256: string(bytes.Repeat([]byte{'0'}, 64))}
	for name, tool := range map[string]lockedTool{
		"overlap": {
			Name: "scanner", Status: "enabled", Platforms: map[string]artifact{"linux/amd64": fixtureArtifact, "linux/arm64": fixtureArtifact},
			UnsupportedPlatforms: map[string]string{"linux/arm64": "contradiction"},
		},
		"unknown": {
			Name: "scanner", Status: "enabled", Platforms: map[string]artifact{"linux/amd64": fixtureArtifact},
			UnsupportedPlatforms: map[string]string{"linux/arm64": "unsupported", "linux/riscv64": "unknown"},
		},
		"blank reason": {
			Name: "scanner", Status: "enabled", Platforms: map[string]artifact{"linux/amd64": fixtureArtifact},
			UnsupportedPlatforms: map[string]string{"linux/arm64": "  "},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateLock(lockFile{Tools: []lockedTool{tool}}); err == nil {
				t.Fatal("expected malformed lock rejection")
			}
		})
	}
}

func TestDownloadRetriesTransientFailure(t *testing.T) {
	payload := []byte("verified-binary")
	sum := sha256.Sum256(payload)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) < 3 {
			http.Error(response, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = response.Write(payload)
	}))
	defer server.Close()

	path, err := download(server.Client(), artifact{Asset: server.URL, SHA256: hex.EncodeToString(sum[:])}, "scanner")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(path) }()
	if requests.Load() != 3 {
		t.Fatalf("requests = %d, want 3", requests.Load())
	}
}

func writeLock(t *testing.T, lock lockFile) string {
	t.Helper()
	data, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "lock.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
