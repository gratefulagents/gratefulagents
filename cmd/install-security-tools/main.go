// Command install-security-tools installs checksum-locked release binaries.
// It is a build-time helper used by the injector and security-tools images.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

const maxArchiveSize = 512 << 20

const downloadAttempts = 5

var supportedPlatforms = map[string]struct{}{"linux/amd64": {}, "linux/arm64": {}}

type artifact struct {
	Asset        string `json:"asset"`
	SHA256       string `json:"sha256"`
	BinarySHA256 string `json:"binary_sha256,omitempty"`
}
type lockedTool struct {
	Name                 string              `json:"name"`
	Status               string              `json:"status"`
	Binary               string              `json:"binary"`
	Reason               string              `json:"reason"`
	Platforms            map[string]artifact `json:"platforms"`
	UnsupportedPlatforms map[string]string   `json:"unsupported_platforms"`
}
type lockFile struct {
	SchemaVersion string       `json:"schema_version"`
	Tools         []lockedTool `json:"tools"`
}

func main() {
	if len(os.Args) != 4 {
		fatal("usage: install-security-tools LOCK OUTPUT PLATFORM")
	}
	if err := install(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fatal("%v", err)
	}
}

func install(lockPath, outputDir, platform string) error {
	raw, err := os.ReadFile(filepath.Clean(lockPath))
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	var lock lockFile
	if err := json.Unmarshal(raw, &lock); err != nil {
		return fmt.Errorf("parse lock: %w", err)
	}
	if lock.SchemaVersion != "security-tools-lock/v1" {
		return fmt.Errorf("unsupported schema %q", lock.SchemaVersion)
	}
	if _, ok := supportedPlatforms[platform]; !ok {
		return fmt.Errorf("unsupported target platform %q", platform)
	}
	if err := validateLock(lock); err != nil {
		return err
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	for _, tool := range lock.Tools {
		if tool.Status != "enabled" {
			continue
		}
		if filepath.Base(tool.Binary) != tool.Binary || tool.Binary == "." {
			return fmt.Errorf("%s: unsafe binary name", tool.Name)
		}
		asset, ok := tool.Platforms[platform]
		if !ok {
			if tool.UnsupportedPlatforms[platform] != "" {
				continue
			}
			return fmt.Errorf("%s: no artifact for %s", tool.Name, platform)
		}
		archivePath, err := download(client, asset, tool.Name)
		if err != nil {
			return err
		}
		destination := filepath.Join(outputDir, tool.Binary)
		err = extractBinary(archivePath, asset.Asset, tool.Binary, destination)
		_ = os.Remove(archivePath)
		if err != nil {
			return fmt.Errorf("%s: %w", tool.Name, err)
		}
		if asset.BinarySHA256 != "" {
			data, readErr := os.ReadFile(destination)
			if readErr != nil {
				return fmt.Errorf("%s: verify binary: %w", tool.Name, readErr)
			}
			sum := sha256.Sum256(data)
			if actual := hex.EncodeToString(sum[:]); actual != asset.BinarySHA256 {
				return fmt.Errorf("%s: extracted binary SHA-256 mismatch: got %s, want %s", tool.Name, actual, asset.BinarySHA256)
			}
		}
	}
	return nil
}

func validateLock(lock lockFile) error {
	for _, tool := range lock.Tools {
		if tool.Status != "enabled" {
			continue
		}
		if len(tool.Platforms) == 0 {
			return fmt.Errorf("%s: enabled tool has no supported platforms", tool.Name)
		}
		for platform := range supportedPlatforms {
			_, supported := tool.Platforms[platform]
			reason, unsupported := tool.UnsupportedPlatforms[platform]
			if supported && unsupported {
				return fmt.Errorf("%s: %s cannot be both supported and unsupported", tool.Name, platform)
			}
			if !supported && (!unsupported || strings.TrimSpace(reason) == "") {
				return fmt.Errorf("%s: no artifact or unsupported reason for %s", tool.Name, platform)
			}
		}
		for platform := range tool.Platforms {
			if _, ok := supportedPlatforms[platform]; !ok {
				return fmt.Errorf("%s: unknown platform %s", tool.Name, platform)
			}
		}
		for platform, reason := range tool.UnsupportedPlatforms {
			if _, ok := supportedPlatforms[platform]; !ok {
				return fmt.Errorf("%s: unknown unsupported platform %s", tool.Name, platform)
			}
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("%s: unsupported reason for %s is blank", tool.Name, platform)
			}
		}
	}
	return nil
}

func download(client *http.Client, asset artifact, name string) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= downloadAttempts; attempt++ {
		path, err := downloadOnce(client, asset, name)
		if err == nil {
			return path, nil
		}
		lastErr = err
		if attempt < downloadAttempts {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
	return "", fmt.Errorf("%s: download failed after %d attempts: %w", name, downloadAttempts, lastErr)
}

func downloadOnce(client *http.Client, asset artifact, name string) (string, error) {
	// #nosec G107 -- build-time URL is restricted and checksum-pinned by the verified lock.
	response, err := client.Get(asset.Asset)
	if err != nil {
		return "", fmt.Errorf("%s: download: %w", name, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: download returned %s", name, response.Status)
	}
	file, err := os.CreateTemp("", "ga-security-archive-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, maxArchiveSize+1))
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if written > maxArchiveSize {
		_ = os.Remove(path)
		return "", errors.New("release archive exceeds size limit")
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != asset.SHA256 {
		_ = os.Remove(path)
		return "", fmt.Errorf("%s: SHA-256 mismatch: got %s, want %s", name, actual, asset.SHA256)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func extractBinary(archivePath, assetURL, binary, destination string) error {
	var source io.ReadCloser
	isArchive := strings.HasSuffix(assetURL, ".zip") || strings.HasSuffix(assetURL, ".tar.gz") ||
		strings.HasSuffix(assetURL, ".tar.xz")
	if !isArchive {
		file, err := os.Open(archivePath)
		if err != nil {
			return err
		}
		source = file
	} else if strings.HasSuffix(assetURL, ".zip") {
		reader, err := zip.OpenReader(archivePath)
		if err != nil {
			return err
		}
		defer func() { _ = reader.Close() }()
		for _, file := range reader.File {
			if filepath.Base(file.Name) == binary && !file.FileInfo().IsDir() {
				opened, openErr := file.Open()
				if openErr != nil {
					return openErr
				}
				source = opened
				break
			}
		}
		if source == nil {
			return fmt.Errorf("archive does not contain %q", binary)
		}
	} else {
		file, err := os.Open(archivePath)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		var archive io.Reader
		if strings.HasSuffix(assetURL, ".tar.xz") {
			xzReader, err := xz.NewReader(file)
			if err != nil {
				return err
			}
			archive = xzReader
		} else {
			gz, err := gzip.NewReader(file)
			if err != nil {
				return err
			}
			defer func() { _ = gz.Close() }()
			archive = gz
		}
		reader := tar.NewReader(archive)
		for {
			header, nextErr := reader.Next()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				return nextErr
			}
			if filepath.Base(header.Name) == binary && header.Typeflag == tar.TypeReg {
				source = io.NopCloser(reader)
				break
			}
		}
		if source == nil {
			return fmt.Errorf("archive does not contain %q", binary)
		}
	}
	defer func() { _ = source.Close() }()
	temp, err := os.CreateTemp(filepath.Dir(destination), ".security-tool-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	written, err := io.Copy(temp, io.LimitReader(source, maxArchiveSize+1))
	if err != nil {
		_ = temp.Close()
		return err
	}
	if written > maxArchiveSize {
		_ = temp.Close()
		return errors.New("extracted binary exceeds size limit")
	}
	if err := temp.Chmod(0o755); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, destination)
}

func fatal(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(1) }
