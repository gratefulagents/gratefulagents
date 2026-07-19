package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
)

const (
	workspaceSnapshotKeyMetadataKey = "workspace_snapshot_encryption_key"
	workspaceSnapshotKeyVersion     = 1
	workspaceSnapshotKeyBytes       = 32

	encryptedWorkspaceArchiveSubject = "gratefulagents: encrypted untracked workspace snapshot v1"
	encryptedWorkspaceArchivePath    = "payload"
	encryptedWorkspaceArchiveMagic   = "GAWS\x01"

	// Bound in-memory authenticated payloads and S3 object downloads. A
	// checkpoint that exceeds this limit fails before publishing its manifest,
	// leaving the previous complete generation intact.
	maxEncryptedWorkspaceArchiveBytes = 512 << 20
	maxWorkspaceArchiveInputBytes     = 256 << 20
)

type workspaceSnapshotKeyRecord struct {
	Version int    `json:"version"`
	Key     string `json:"key"`
}

// loadOrCreateWorkspaceSnapshotKey returns the stable per-run key used for
// encrypted untracked-file payloads. The key is private platform state in
// Postgres, separate from the repository that carries ciphertext. It is
// persisted before use so every remotely reachable payload is decryptable by
// a replacement pod.
func loadOrCreateWorkspaceSnapshotKey(ctx context.Context, sc *sessionclient.Client) ([]byte, error) {
	if sc == nil {
		return nil, fmt.Errorf("session client is required for workspace snapshot encryption")
	}
	session, err := sc.Session(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading workspace snapshot encryption key: %w", err)
	}
	if session != nil && len(session.Metadata) > 0 {
		var metadata map[string]json.RawMessage
		if err := json.Unmarshal(session.Metadata, &metadata); err != nil {
			return nil, fmt.Errorf("decoding session metadata for workspace snapshot encryption: %w", err)
		}
		if raw := metadata[workspaceSnapshotKeyMetadataKey]; len(raw) > 0 && string(raw) != "null" {
			var record workspaceSnapshotKeyRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				return nil, fmt.Errorf("decoding workspace snapshot encryption key: %w", err)
			}
			return decodeWorkspaceSnapshotKey(record)
		}
	}

	key := make([]byte, workspaceSnapshotKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating workspace snapshot encryption key: %w", err)
	}
	record := workspaceSnapshotKeyRecord{
		Version: workspaceSnapshotKeyVersion,
		Key:     base64.RawStdEncoding.EncodeToString(key),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encoding workspace snapshot encryption key: %w", err)
	}
	if err := sc.StateStore().MergeSessionMetadata(ctx, sc.SessionID(), workspaceSnapshotKeyMetadataKey, encoded); err != nil {
		return nil, fmt.Errorf("persisting workspace snapshot encryption key: %w", err)
	}
	return key, nil
}

func decodeWorkspaceSnapshotKey(record workspaceSnapshotKeyRecord) ([]byte, error) {
	if record.Version != workspaceSnapshotKeyVersion {
		return nil, fmt.Errorf("unsupported workspace snapshot encryption key version %d", record.Version)
	}
	key, err := base64.RawStdEncoding.DecodeString(record.Key)
	if err != nil {
		return nil, fmt.Errorf("decoding workspace snapshot encryption key bytes: %w", err)
	}
	if len(key) != workspaceSnapshotKeyBytes {
		return nil, fmt.Errorf("workspace snapshot encryption key is %d bytes, want %d", len(key), workspaceSnapshotKeyBytes)
	}
	return key, nil
}

func encryptWorkspaceArchive(key, plaintext []byte) ([]byte, error) {
	if len(key) != workspaceSnapshotKeyBytes {
		return nil, fmt.Errorf("workspace snapshot encryption key is %d bytes, want %d", len(key), workspaceSnapshotKeyBytes)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generating workspace archive nonce: %w", err)
	}
	aad := []byte(encryptedWorkspaceArchiveMagic)
	out := make([]byte, 0, len(aad)+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, aad...)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, aad)
	if len(out) > maxEncryptedWorkspaceArchiveBytes {
		return nil, fmt.Errorf("encrypted workspace checkpoint payload is %d bytes; maximum object size is %d", len(out), maxEncryptedWorkspaceArchiveBytes)
	}
	return out, nil
}

func decryptWorkspaceArchive(key, envelope []byte) ([]byte, error) {
	if len(key) != workspaceSnapshotKeyBytes {
		return nil, fmt.Errorf("workspace snapshot encryption key is unavailable or invalid")
	}
	magic := []byte(encryptedWorkspaceArchiveMagic)
	if !bytes.HasPrefix(envelope, magic) {
		return nil, fmt.Errorf("unsupported encrypted workspace archive format")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(envelope) < len(magic)+gcm.NonceSize()+gcm.Overhead() {
		return nil, fmt.Errorf("encrypted workspace archive is truncated")
	}
	nonceStart := len(magic)
	nonceEnd := nonceStart + gcm.NonceSize()
	nonce := envelope[nonceStart:nonceEnd]
	plaintext, err := gcm.Open(nil, nonce, envelope[nonceEnd:], magic)
	if err != nil {
		return nil, fmt.Errorf("authenticating encrypted workspace archive: %w", err)
	}
	return plaintext, nil
}

// buildUntrackedWorkspaceArchive returns a deterministic gzip+tar payload for
// every non-ignored path that is absent from HEAD. This includes ordinary
// untracked files, staged new files, and rename destinations. Ignored files
// remain local by policy. Returning an error instead of selectively skipping a
// path prevents a checkpoint from appearing successful while losing data.
func buildUntrackedWorkspaceArchive(ctx context.Context, dir string) (archive []byte, contentHash string, fileCount int, err error) {
	paths, err := snapshotNewPaths(ctx, dir)
	if err != nil {
		return nil, "", 0, err
	}
	if len(paths) == 0 {
		return nil, "", 0, nil
	}

	var compressed bytes.Buffer
	zw, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return nil, "", 0, err
	}
	zw.Header.ModTime = time.Unix(0, 0)
	zw.Header.OS = 255
	tw := tar.NewWriter(zw)
	closeWriters := func() error {
		if err := tw.Close(); err != nil {
			_ = zw.Close()
			return err
		}
		return zw.Close()
	}

	var inputBytes int64
	for _, rel := range paths {
		if err := validateWorkspaceArchivePath(rel); err != nil {
			_ = closeWriters()
			return nil, "", 0, err
		}
		full := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		if err != nil {
			_ = closeWriters()
			return nil, "", 0, fmt.Errorf("reading untracked workspace path %q: %w", rel, err)
		}

		header := &tar.Header{
			Name:       rel,
			Mode:       int64(info.Mode().Perm()),
			ModTime:    time.Unix(0, 0),
			AccessTime: time.Time{},
			ChangeTime: time.Time{},
			Uid:        0,
			Gid:        0,
		}
		switch {
		case info.Mode().IsRegular():
			header.Typeflag = tar.TypeReg
			header.Size = info.Size()
			inputBytes += info.Size()
			if inputBytes > maxWorkspaceArchiveInputBytes {
				_ = closeWriters()
				return nil, "", 0, fmt.Errorf("untracked workspace files total more than %d bytes; checkpoint not advanced", maxWorkspaceArchiveInputBytes)
			}
			if err := tw.WriteHeader(header); err != nil {
				_ = closeWriters()
				return nil, "", 0, fmt.Errorf("archiving untracked workspace path %q: %w", rel, err)
			}
			f, err := os.Open(full)
			if err != nil {
				_ = closeWriters()
				return nil, "", 0, fmt.Errorf("opening untracked workspace path %q: %w", rel, err)
			}
			written, copyErr := io.Copy(tw, f)
			closeErr := f.Close()
			if copyErr != nil {
				_ = closeWriters()
				return nil, "", 0, fmt.Errorf("archiving untracked workspace path %q: %w", rel, copyErr)
			}
			if closeErr != nil {
				_ = closeWriters()
				return nil, "", 0, fmt.Errorf("closing untracked workspace path %q: %w", rel, closeErr)
			}
			if written != info.Size() {
				_ = closeWriters()
				return nil, "", 0, fmt.Errorf("untracked workspace path %q changed size during checkpoint", rel)
			}
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(full)
			if err != nil {
				_ = closeWriters()
				return nil, "", 0, fmt.Errorf("reading untracked symlink %q: %w", rel, err)
			}
			header.Typeflag = tar.TypeSymlink
			header.Linkname = target
			if err := tw.WriteHeader(header); err != nil {
				_ = closeWriters()
				return nil, "", 0, fmt.Errorf("archiving untracked symlink %q: %w", rel, err)
			}
		default:
			_ = closeWriters()
			return nil, "", 0, fmt.Errorf("untracked workspace path %q has unsupported file type %s; checkpoint not advanced", rel, info.Mode().Type())
		}
		fileCount++
	}
	if err := closeWriters(); err != nil {
		return nil, "", 0, fmt.Errorf("finalizing untracked workspace archive: %w", err)
	}
	if compressed.Len() > maxEncryptedWorkspaceArchiveBytes-aes.BlockSize-64 {
		return nil, "", 0, fmt.Errorf("compressed untracked workspace archive is %d bytes; maximum is %d", compressed.Len(), maxEncryptedWorkspaceArchiveBytes-aes.BlockSize-64)
	}
	sum := sha256.Sum256(compressed.Bytes())
	return compressed.Bytes(), hex.EncodeToString(sum[:]), fileCount, nil
}

func snapshotNewPaths(ctx context.Context, dir string) ([]string, error) {
	commands := [][]string{
		{"ls-files", "-z", "--others", "--exclude-standard"},
		// Compare the complete worktree to HEAD with rename detection disabled:
		// rename destinations and staged/intent-to-add files are additions that
		// the tracked-only temporary index cannot otherwise represent.
		{"diff", "--no-renames", "--name-only", "--diff-filter=A", "-z", "HEAD", "--"},
	}
	seen := make(map[string]struct{})
	for _, args := range commands {
		out, err := gitOutputRaw(ctx, dir, nil, nil, args...)
		if err != nil {
			return nil, fmt.Errorf("listing untracked workspace paths: %w", err)
		}
		for _, raw := range bytes.Split(out, []byte{0}) {
			if len(raw) == 0 {
				continue
			}
			rel := filepath.ToSlash(string(raw))
			if err := validateWorkspaceArchivePath(rel); err != nil {
				return nil, err
			}
			seen[rel] = struct{}{}
		}
	}
	paths := make([]string, 0, len(seen))
	for rel := range seen {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	return paths, nil
}

func validateWorkspaceArchivePath(rel string) error {
	if rel == "" || strings.ContainsRune(rel, '\x00') || strings.ContainsRune(rel, '\\') {
		return fmt.Errorf("invalid untracked workspace path %q", rel)
	}
	clean := pathpkg.Clean(rel)
	if clean != rel || clean == "." || pathpkg.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("unsafe untracked workspace path %q", rel)
	}
	for _, component := range strings.Split(clean, "/") {
		if strings.EqualFold(component, ".git") {
			return fmt.Errorf("refusing to archive Git metadata path %q", rel)
		}
	}
	return nil
}

func createEncryptedWorkspaceArchiveCommit(ctx context.Context, dir string, key, plaintext []byte, ident []string) (string, error) {
	envelope, err := encryptWorkspaceArchive(key, plaintext)
	if err != nil {
		return "", err
	}
	blobOut, err := gitOutputRaw(ctx, dir, nil, envelope, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", fmt.Errorf("writing encrypted workspace archive blob: %w", err)
	}
	blob := strings.TrimSpace(string(blobOut))
	if blob == "" {
		return "", fmt.Errorf("writing encrypted workspace archive blob returned no object ID")
	}

	idx, err := os.CreateTemp("", "gratefulagents-archive-index-*")
	if err != nil {
		return "", fmt.Errorf("creating encrypted archive index: %w", err)
	}
	idxPath := idx.Name()
	_ = idx.Close()
	_ = os.Remove(idxPath)
	defer func() { _ = os.Remove(idxPath) }()
	env := []string{"GIT_INDEX_FILE=" + idxPath}
	if _, err := gitOutput(ctx, dir, env, "read-tree", "--empty"); err != nil {
		return "", fmt.Errorf("initializing encrypted archive tree: %w", err)
	}
	if _, err := gitOutput(ctx, dir, env, "update-index", "--add", "--cacheinfo", "100644", blob, encryptedWorkspaceArchivePath); err != nil {
		return "", fmt.Errorf("indexing encrypted workspace archive: %w", err)
	}
	tree, err := gitOutput(ctx, dir, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("writing encrypted workspace archive tree: %w", err)
	}
	commit, err := gitOutput(ctx, dir, ident, "commit-tree", tree, "-m", encryptedWorkspaceArchiveSubject)
	if err != nil {
		return "", fmt.Errorf("creating encrypted workspace archive commit: %w", err)
	}
	return commit, nil
}

func encryptedWorkspaceArchiveFromSnapshot(ctx context.Context, dir, snapshot string) ([]byte, bool, error) {
	parents, err := gitOutput(ctx, dir, nil, "rev-list", "--parents", "-n", "1", snapshot)
	if err != nil {
		return nil, false, fmt.Errorf("resolving workspace snapshot parents: %w", err)
	}
	fields := strings.Fields(parents)
	if len(fields) < 3 {
		return nil, false, nil
	}
	if len(fields) != 3 {
		return nil, false, fmt.Errorf("workspace snapshot has %d parents; expected at most two", len(fields)-1)
	}
	archiveCommit := fields[2]
	subject, err := gitOutput(ctx, dir, nil, "log", "-1", "--format=%s", archiveCommit)
	if err != nil {
		return nil, false, fmt.Errorf("reading encrypted workspace archive commit: %w", err)
	}
	if subject != encryptedWorkspaceArchiveSubject {
		return nil, false, fmt.Errorf("workspace snapshot second parent has unrecognized format %q", subject)
	}
	payload, err := gitOutputRaw(ctx, dir, nil, nil, "cat-file", "blob", archiveCommit+":"+encryptedWorkspaceArchivePath)
	if err != nil {
		return nil, false, fmt.Errorf("reading encrypted workspace archive payload: %w", err)
	}
	return payload, true, nil
}

func restoreEncryptedWorkspaceArchive(repoDir string, key, envelope []byte) (int, error) {
	plaintext, err := decryptWorkspaceArchive(key, envelope)
	if err != nil {
		return 0, err
	}
	if _, err := walkWorkspaceArchive(repoDir, plaintext, false); err != nil {
		return 0, err
	}
	return walkWorkspaceArchive(repoDir, plaintext, true)
}

// walkWorkspaceArchive validates, then optionally extracts, an archive. The
// validation pass rejects traversal, duplicate paths, special files, and paths
// nested below archive symlinks before the filesystem is modified.
func walkWorkspaceArchive(repoDir string, archive []byte, extract bool) (int, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return 0, fmt.Errorf("opening untracked workspace archive: %w", err)
	}
	tr := tar.NewReader(zr)
	seen := make(map[string]byte)
	count := 0
	var total int64
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = zr.Close()
			return 0, fmt.Errorf("reading untracked workspace archive: %w", err)
		}
		if err := validateWorkspaceArchivePath(header.Name); err != nil {
			_ = zr.Close()
			return 0, err
		}
		if _, duplicate := seen[header.Name]; duplicate {
			_ = zr.Close()
			return 0, fmt.Errorf("untracked workspace archive repeats path %q", header.Name)
		}
		for parent := pathpkg.Dir(header.Name); parent != "."; parent = pathpkg.Dir(parent) {
			if seen[parent] == tar.TypeSymlink {
				_ = zr.Close()
				return 0, fmt.Errorf("untracked workspace archive path %q is nested below symlink %q", header.Name, parent)
			}
		}
		seen[header.Name] = header.Typeflag
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 {
				_ = zr.Close()
				return 0, fmt.Errorf("untracked workspace archive path %q has negative size", header.Name)
			}
			total += header.Size
			if total > maxWorkspaceArchiveInputBytes {
				_ = zr.Close()
				return 0, fmt.Errorf("untracked workspace archive expands beyond %d bytes", maxWorkspaceArchiveInputBytes)
			}
		case tar.TypeSymlink:
			if strings.ContainsRune(header.Linkname, '\x00') {
				_ = zr.Close()
				return 0, fmt.Errorf("untracked workspace symlink %q has invalid target", header.Name)
			}
		default:
			_ = zr.Close()
			return 0, fmt.Errorf("untracked workspace archive path %q has unsupported type %d", header.Name, header.Typeflag)
		}
		if extract {
			if err := extractWorkspaceArchiveEntry(repoDir, header, tr); err != nil {
				_ = zr.Close()
				return 0, err
			}
		}
		count++
	}
	// Check parent symlinks again after the complete path set is known so a
	// malicious archive cannot evade validation by ordering a child first.
	for name := range seen {
		for parent := pathpkg.Dir(name); parent != "."; parent = pathpkg.Dir(parent) {
			if seen[parent] == tar.TypeSymlink {
				_ = zr.Close()
				return 0, fmt.Errorf("untracked workspace archive path %q is nested below symlink %q", name, parent)
			}
		}
	}
	if err := zr.Close(); err != nil {
		return 0, fmt.Errorf("closing untracked workspace archive: %w", err)
	}
	return count, nil
}

func extractWorkspaceArchiveEntry(repoDir string, header *tar.Header, r io.Reader) error {
	dest, err := safeWorkspaceArchiveDestination(repoDir, header.Name)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("refusing to overwrite existing path %q while restoring workspace snapshot", header.Name)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking workspace restore path %q: %w", header.Name, err)
	}

	switch header.Typeflag {
	case tar.TypeReg, tar.TypeRegA:
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("creating restored workspace path %q: %w", header.Name, err)
		}
		written, copyErr := io.CopyN(f, r, header.Size)
		if copyErr == nil && written != header.Size {
			copyErr = io.ErrUnexpectedEOF
		}
		if copyErr == nil {
			copyErr = f.Chmod(os.FileMode(header.Mode) & os.ModePerm)
		}
		closeErr := f.Close()
		if copyErr != nil {
			_ = os.Remove(dest)
			return fmt.Errorf("restoring workspace path %q: %w", header.Name, copyErr)
		}
		if closeErr != nil {
			_ = os.Remove(dest)
			return fmt.Errorf("closing restored workspace path %q: %w", header.Name, closeErr)
		}
	case tar.TypeSymlink:
		if err := os.Symlink(header.Linkname, dest); err != nil {
			return fmt.Errorf("restoring workspace symlink %q: %w", header.Name, err)
		}
	}
	return nil
}

func safeWorkspaceArchiveDestination(root, rel string) (string, error) {
	if err := validateWorkspaceArchivePath(rel); err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	parts := strings.Split(rel, "/")
	current := rootAbs
	for _, component := range parts[:len(parts)-1] {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o755); err != nil && !os.IsExist(err) {
				return "", fmt.Errorf("creating workspace restore directory %q: %w", component, err)
			}
			continue
		}
		if err != nil {
			return "", fmt.Errorf("checking workspace restore directory %q: %w", component, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("workspace restore path %q traverses a non-directory or symlink", rel)
		}
	}
	dest := filepath.Join(rootAbs, filepath.FromSlash(rel))
	if dest == rootAbs || !strings.HasPrefix(dest, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("workspace restore path %q escapes repository", rel)
	}
	return dest, nil
}

func gitOutputRaw(ctx context.Context, dir string, extraEnv []string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
