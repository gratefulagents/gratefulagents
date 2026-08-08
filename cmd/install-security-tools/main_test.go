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
		{Name: "scanner", Status: "enabled", Binary: "scanner", Platforms: platforms},
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
