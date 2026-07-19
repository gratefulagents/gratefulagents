package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

// Workspace checkpoints protect work in the pod's ephemeral /workspace.
// Repository payloads are immutable encrypted Git bundles in object storage;
// one encrypted latest manifest is published only after every repository
// payload succeeds. Git is used locally as a compact delta encoder, but no
// checkpoint refs or objects are ever written to repository remotes.
const (
	workspaceCheckpointVersion  = 1
	workspaceCheckpointInterval = 10 * time.Second
	workspaceCheckpointTimeout  = 30 * time.Second
	workspaceBundleRef          = "refs/gratefulagents-checkpoint/snapshot"
	workspaceAnchorMagic        = "GAANCH\x01"
	workspaceLatestObject       = "latest.json.enc"
	maxWorkspaceBundleBytes     = maxEncryptedWorkspaceArchiveBytes - (1 << 20)
)

var activeWorkspaceSnapshotter atomic.Pointer[workspaceSnapshotter]

type workspaceCheckpointHooks struct {
	agent.NoOpRunHooks
	snapshotter *workspaceSnapshotter
}

func (h *workspaceCheckpointHooks) OnToolEnd(_ *agent.RunContext, _ *agent.Agent, _ agent.Tool, _ agent.ToolCallData, _ agent.ToolResult) {
}

func (h *workspaceCheckpointHooks) OnToolEndError(_ *agent.RunContext, _ *agent.Agent, tool agent.Tool, _ agent.ToolCallData, _ agent.ToolResult) error {
	if h == nil || h.snapshotter == nil || tool == nil || !toolMayMutateWorkspace(tool.Name()) {
		return nil
	}
	// Do not acknowledge a mutating tool boundary until its encrypted S3
	// checkpoint manifest is durably published.
	ctx, cancel := context.WithTimeout(context.Background(), workspaceCheckpointTimeout)
	defer cancel()
	if err := h.snapshotter.snapshotSync(ctx, "tool:"+tool.Name()); err != nil {
		return fmt.Errorf("publishing encrypted workspace checkpoint after %s: %w", tool.Name(), err)
	}
	return nil
}

func toolMayMutateWorkspace(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "bash", "bashstart", "bash_start", "bashpoll", "bash_poll",
		"terminal", "write", "edit", "apply_patch",
		"git_commit", "git_merge", "git_merge_abort", "git_pull",
		"attach_repository", "create_pull_request", "subagent",
		"multi_tool_use.parallel":
		return true
	default:
		return false
	}
}

type workspaceCheckpointManifest struct {
	Version      int                             `json:"version"`
	Generation   string                          `json:"generation"`
	CreatedAt    time.Time                       `json:"createdAt"`
	Repositories []workspaceCheckpointRepository `json:"repositories"`
}

type workspaceCheckpointRepository struct {
	ID              string `json:"id"`
	Alias           string `json:"alias,omitempty"`
	URL             string `json:"url"`
	Branch          string `json:"branch,omitempty"`
	Upstream        string `json:"upstream,omitempty"`
	Location        string `json:"location,omitempty"`
	Primary         bool   `json:"primary,omitempty"`
	SemanticKey     string `json:"semanticKey"`
	ObjectKey       string `json:"objectKey"`
	Snapshot        string `json:"snapshot"`
	Parent          string `json:"parent"`
	Anchor          string `json:"anchor,omitempty"`
	AnchorObjectKey string `json:"anchorObjectKey,omitempty"`
}

func workspaceCheckpointRunPrefix(root, namespace, taskUID string) string {
	return path.Join(strings.Trim(root, "/"), namespace, taskUID)
}

func workspaceCheckpointLatestKey(prefix string) string {
	return path.Join(prefix, workspaceLatestObject)
}

func loadWorkspaceCheckpoint(ctx context.Context, store workspaceObjectStore, prefix string, key []byte) (*workspaceCheckpointManifest, error) {
	if store == nil {
		return nil, fmt.Errorf("workspace checkpoint object store is unavailable")
	}
	data, found, err := store.Get(ctx, workspaceCheckpointLatestKey(prefix))
	if err != nil {
		return nil, fmt.Errorf("loading workspace checkpoint manifest: %w", err)
	}
	if !found {
		return nil, nil
	}
	plaintext, err := decryptWorkspaceArchive(key, data)
	if err != nil {
		return nil, fmt.Errorf("decrypting workspace checkpoint manifest: %w", err)
	}
	var manifest workspaceCheckpointManifest
	if err := json.Unmarshal(plaintext, &manifest); err != nil {
		return nil, fmt.Errorf("decoding workspace checkpoint manifest: %w", err)
	}
	if manifest.Version != workspaceCheckpointVersion {
		return nil, fmt.Errorf("unsupported workspace checkpoint version %d", manifest.Version)
	}
	seen := make(map[string]struct{}, len(manifest.Repositories))
	primary := 0
	for _, repo := range manifest.Repositories {
		if repo.ID == "" || repo.ObjectKey == "" || repo.Snapshot == "" || repo.Parent == "" {
			return nil, fmt.Errorf("workspace checkpoint contains an incomplete repository entry")
		}
		if _, ok := seen[repo.ID]; ok {
			return nil, fmt.Errorf("workspace checkpoint contains duplicate repository %q", repo.ID)
		}
		seen[repo.ID] = struct{}{}
		if repo.Primary {
			primary++
		}
		if !strings.HasPrefix(repo.ObjectKey, prefix+"/objects/") {
			return nil, fmt.Errorf("workspace checkpoint object %q is outside the run prefix", repo.ObjectKey)
		}
		if repo.Anchor != "" && !strings.HasPrefix(repo.AnchorObjectKey, prefix+"/anchors/") {
			return nil, fmt.Errorf("workspace checkpoint anchor object %q is outside the run prefix", repo.AnchorObjectKey)
		}
	}
	if primary > 1 {
		return nil, fmt.Errorf("workspace checkpoint contains multiple primary repositories")
	}
	return &manifest, nil
}

func saveWorkspaceCheckpoint(ctx context.Context, store workspaceObjectStore, prefix string, key []byte, manifest workspaceCheckpointManifest) error {
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encoding workspace checkpoint manifest: %w", err)
	}
	encrypted, err := encryptWorkspaceArchive(key, data)
	if err != nil {
		return fmt.Errorf("encrypting workspace checkpoint manifest: %w", err)
	}
	if err := store.Put(ctx, workspaceCheckpointLatestKey(prefix), encrypted); err != nil {
		return fmt.Errorf("publishing workspace checkpoint manifest: %w", err)
	}
	return nil
}

// Extra-repo locations recorded in a checkpoint. The empty store location is
// kept as the natural zero value of the new format.
const (
	extraRepoStoreDirName      = "repos"
	extraRepoLocationStore     = ""
	extraRepoLocationWorkspace = "workspace"
)

type workspaceSnapshotter struct {
	repoDir     string
	repoless    bool
	namespace   string
	runName     string
	prefix      string
	store       workspaceObjectStore
	sc          *sessionclient.Client
	snapshotKey []byte

	mu             sync.Mutex
	asyncMu        sync.Mutex
	asyncCancel    context.CancelFunc
	pending        atomic.Bool
	lastKeys       map[string]string
	lastEntries    map[string]workspaceCheckpointRepository
	lastGeneration string
}

func newWorkspaceSnapshotter(cfg runConfig, sc *sessionclient.Client) *workspaceSnapshotter {
	s := &workspaceSnapshotter{
		repoDir: cfg.RepoDir, repoless: cfg.Repoless, namespace: cfg.Namespace,
		runName: cfg.TaskName, prefix: cfg.WorkspaceCheckpointPrefix,
		store: cfg.WorkspaceCheckpointStore, sc: sc,
		snapshotKey: append([]byte(nil), cfg.WorkspaceSnapshotKey...),
		lastKeys:    make(map[string]string), lastEntries: make(map[string]workspaceCheckpointRepository),
	}
	if cfg.WorkspaceCheckpoint != nil {
		s.lastGeneration = cfg.WorkspaceCheckpoint.Generation
		for _, entry := range cfg.WorkspaceCheckpoint.Repositories {
			s.lastKeys[entry.ID] = entry.SemanticKey
			s.lastEntries[entry.ID] = entry
		}
	}
	return s
}

func (s *workspaceSnapshotter) SnapshotAsync(reason string) {
	if s == nil {
		return
	}
	s.pending.Store(true)
	if !s.mu.TryLock() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), workspaceCheckpointTimeout)
	s.asyncMu.Lock()
	s.asyncCancel = cancel
	s.asyncMu.Unlock()
	go func() {
		defer s.mu.Unlock()
		defer func() {
			s.asyncMu.Lock()
			s.asyncCancel = nil
			s.asyncMu.Unlock()
			cancel()
		}()
		for s.pending.Swap(false) {
			if err := s.snapshotLocked(ctx, reason); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("WARN: workspace checkpoint (%s) failed: %v", reason, err)
				return
			}
		}
	}()
}

func (s *workspaceSnapshotter) StartPeriodic(parent context.Context) context.CancelFunc {
	ctx, cancel := context.WithCancel(parent)
	if s == nil {
		return cancel
	}
	go func() {
		ticker := time.NewTicker(workspaceCheckpointInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.SnapshotAsync("periodic")
			}
		}
	}()
	return cancel
}

func (s *workspaceSnapshotter) Snapshot(ctx context.Context, reason string) {
	if err := s.snapshotSync(ctx, reason); err != nil {
		log.Printf("WARN: workspace checkpoint (%s) failed: %v", reason, err)
	}
}

func (s *workspaceSnapshotter) snapshotSync(ctx context.Context, reason string) error {
	if s == nil {
		return nil
	}
	s.cancelAsyncSnapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending.Store(false)
	return s.snapshotLocked(ctx, reason)
}

func (s *workspaceSnapshotter) cancelAsyncSnapshot() {
	s.asyncMu.Lock()
	defer s.asyncMu.Unlock()
	if s.asyncCancel != nil {
		s.asyncCancel()
	}
}

func (s *workspaceSnapshotter) Cleanup(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.cancelAsyncSnapshot()
	s.mu.Lock()
	defer s.mu.Unlock()

	var repos []string
	if !s.repoless {
		repos = append(repos, s.repoDir)
	}
	extras, err := discoverExtraReposStrict(s.repoDir)
	if err != nil {
		log.Printf("WARN: retaining workspace checkpoint: repository discovery failed: %v", err)
		if snapshotErr := s.snapshotLocked(ctx, "successful shutdown"); snapshotErr != nil {
			return fmt.Errorf("repository discovery failed (%v) and final checkpoint failed: %w", err, snapshotErr)
		}
		return fmt.Errorf("repository discovery failed during final checkpoint: %w", err)
	}
	for _, extra := range extras {
		repos = append(repos, extra.dir)
	}
	for _, dir := range repos {
		opCtx, cancel := context.WithTimeout(ctx, workspaceCheckpointTimeout)
		safe, err := repoSafeForSnapshotCleanup(opCtx, dir)
		cancel()
		if err != nil || !safe {
			if err != nil {
				log.Printf("WARN: retaining workspace checkpoint: cannot verify %s: %v", dir, err)
			} else {
				log.Printf("Workspace checkpoint retained: %s has tracked, untracked, or unpushed work", dir)
			}
			if err := s.snapshotLocked(ctx, "successful shutdown"); err != nil {
				return fmt.Errorf("persisting final dirty workspace checkpoint: %w", err)
			}
			return nil
		}
	}
	if err := s.store.Delete(ctx, workspaceCheckpointLatestKey(s.prefix)); err != nil {
		log.Printf("WARN: failed to retire workspace checkpoint: %v", err)
		return nil
	}
	deleted := make(map[string]struct{}, len(s.lastEntries)*2)
	for _, entry := range s.lastEntries {
		for _, objectKey := range []string{entry.ObjectKey, entry.AnchorObjectKey} {
			if objectKey == "" {
				continue
			}
			if _, ok := deleted[objectKey]; ok {
				continue
			}
			deleted[objectKey] = struct{}{}
			if err := s.store.Delete(ctx, objectKey); err != nil {
				log.Printf("WARN: failed to delete retired workspace checkpoint payload %s: %v", objectKey, err)
			}
		}
	}
	log.Printf("Workspace checkpoint retired: %s", s.prefix)
	return nil
}

func repoSafeForSnapshotCleanup(ctx context.Context, dir string) (bool, error) {
	status, err := gitOutput(ctx, dir, nil, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	if status != "" {
		return false, nil
	}
	branch, err := gitOutput(ctx, dir, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return false, fmt.Errorf("resolving current branch: %w", err)
	}
	remoteRef := "refs/heads/" + branch
	exists, err := remoteRefExists(ctx, dir, remoteRef)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	if _, err := gitOutput(ctx, dir, nil, "fetch", "--quiet", "origin", remoteRef); err != nil {
		return false, fmt.Errorf("fetching origin branch: %w", err)
	}
	remoteTip, err := gitOutput(ctx, dir, nil, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return false, err
	}
	head, err := gitOutput(ctx, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	return isGitAncestor(ctx, dir, head, remoteTip), nil
}

func (s *workspaceSnapshotter) snapshotLocked(ctx context.Context, reason string) error {
	if s.store == nil {
		return fmt.Errorf("workspace checkpoint object store is unavailable")
	}
	entries := make([]workspaceCheckpointRepository, 0, 1)
	keys := make(map[string]string)
	entryMap := make(map[string]workspaceCheckpointRepository)

	if !s.repoless {
		entry, err := s.snapshotRepo(ctx, extraRepo{dir: s.repoDir}, true, reason)
		if err != nil {
			return fmt.Errorf("checkpointing primary repository: %w", err)
		}
		entries = append(entries, entry)
		keys[entry.ID], entryMap[entry.ID] = entry.SemanticKey, entry
	}
	extras, err := discoverExtraReposStrict(s.repoDir)
	if err != nil {
		return fmt.Errorf("discovering attached repositories: %w", err)
	}
	for _, extra := range extras {
		entry, err := s.snapshotRepo(ctx, extra, false, reason)
		if err != nil {
			return fmt.Errorf("checkpointing attached repository %q: %w", extra.alias, err)
		}
		entries = append(entries, entry)
		keys[entry.ID], entryMap[entry.ID] = entry.SemanticKey, entry
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })

	generationHash := sha256.New()
	for _, entry := range entries {
		encoded, _ := json.Marshal(entry)
		_, _ = generationHash.Write(encoded)
		_, _ = generationHash.Write([]byte{0})
	}
	generation := hex.EncodeToString(generationHash.Sum(nil))
	if generation == s.lastGeneration {
		return nil
	}
	manifest := workspaceCheckpointManifest{
		Version: workspaceCheckpointVersion, Generation: generation,
		CreatedAt: time.Now().UTC(), Repositories: entries,
	}
	if err := saveWorkspaceCheckpoint(ctx, s.store, s.prefix, s.snapshotKey, manifest); err != nil {
		return err
	}
	s.lastKeys, s.lastEntries, s.lastGeneration = keys, entryMap, generation
	if s.sc != nil {
		detail, _ := json.Marshal(map[string]any{"generation": generation, "reason": reason, "repositories": len(entries)})
		writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.sc.WriteActivity(writeCtx, "workspace_snapshot", "Workspace checkpoint saved", detail)
		cancel()
	}
	log.Printf("Workspace checkpoint published (%s): %s (%d repositories)", reason, generation[:12], len(entries))
	return nil
}

func (s *workspaceSnapshotter) snapshotRepo(ctx context.Context, repo extraRepo, primary bool, reason string) (workspaceCheckpointRepository, error) {
	dir := repo.dir
	head, err := gitOutput(ctx, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		return workspaceCheckpointRepository{}, fmt.Errorf("resolving HEAD: %w", err)
	}
	tree, err := writeWorkingTree(ctx, dir, head)
	if err != nil {
		return workspaceCheckpointRepository{}, err
	}
	archive, archiveHash, archiveFiles, err := buildUntrackedWorkspaceArchive(ctx, dir)
	if err != nil {
		return workspaceCheckpointRepository{}, fmt.Errorf("building untracked-file checkpoint: %w", err)
	}
	if len(archive) > 0 && len(s.snapshotKey) != workspaceSnapshotKeyBytes {
		return workspaceCheckpointRepository{}, fmt.Errorf("workspace checkpoint encryption key is unavailable; refusing to omit %d untracked files", archiveFiles)
	}

	id := workspaceRepoID(repo, primary)
	urlValue, err := gitOutput(ctx, dir, nil, "remote", "get-url", "origin")
	if err != nil {
		return workspaceCheckpointRepository{}, fmt.Errorf("resolving origin URL: %w", err)
	}
	branch, _ := gitOutput(ctx, dir, nil, "rev-parse", "--abbrev-ref", "HEAD")
	upstream, _ := gitOutput(ctx, dir, nil, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	semanticKey := tree + "|" + head + "|" + archiveHash
	if semanticKey == s.lastKeys[id] {
		if entry, ok := s.lastEntries[id]; ok {
			entry.URL, entry.Branch, entry.Upstream = urlValue, branch, upstream
			entry.Alias, entry.Location, entry.Primary = repo.alias, repo.location, primary
			return entry, nil
		}
	}

	ident := []string{
		"GIT_AUTHOR_NAME=gratefulagents", "GIT_AUTHOR_EMAIL=agent@gratefulagents.local",
		"GIT_COMMITTER_NAME=gratefulagents", "GIT_COMMITTER_EMAIL=agent@gratefulagents.local",
	}
	commitArgs := []string{"commit-tree", tree, "-p", head}
	if len(archive) > 0 {
		archiveCommit, err := createEncryptedWorkspaceArchiveCommit(ctx, dir, s.snapshotKey, archive, ident)
		if err != nil {
			return workspaceCheckpointRepository{}, fmt.Errorf("creating untracked-file checkpoint: %w", err)
		}
		commitArgs = append(commitArgs, "-p", archiveCommit)
	}
	commitArgs = append(commitArgs, "-m", fmt.Sprintf("gratefulagents: object-store workspace checkpoint (%s)", reason))
	snap, err := gitOutput(ctx, dir, ident, commitArgs...)
	if err != nil {
		return workspaceCheckpointRepository{}, fmt.Errorf("creating checkpoint commit: %w", err)
	}

	bundle, anchor, err := createWorkspaceBundle(ctx, dir, head, snap)
	if err != nil {
		return workspaceCheckpointRepository{}, err
	}
	anchorObjectKey := s.cachedAnchorObject(anchor)
	if anchor != "" && anchorObjectKey == "" {
		anchorPayload, err := createWorkspaceAnchorPayload(ctx, dir, anchor)
		if err != nil {
			return workspaceCheckpointRepository{}, err
		}
		encryptedAnchor, err := encryptWorkspaceArchive(s.snapshotKey, anchorPayload)
		if err != nil {
			return workspaceCheckpointRepository{}, fmt.Errorf("encrypting repository checkpoint anchor: %w", err)
		}
		anchorDigest := sha256.Sum256(anchorPayload)
		anchorObjectKey = path.Join(s.prefix, "anchors", hex.EncodeToString(anchorDigest[:])+".pack.enc")
		if err := s.store.Put(ctx, anchorObjectKey, encryptedAnchor); err != nil {
			return workspaceCheckpointRepository{}, fmt.Errorf("uploading repository checkpoint anchor: %w", err)
		}
	}
	encrypted, err := encryptWorkspaceArchive(s.snapshotKey, bundle)
	if err != nil {
		return workspaceCheckpointRepository{}, fmt.Errorf("encrypting repository checkpoint: %w", err)
	}
	digest := sha256.Sum256(bundle)
	objectKey := path.Join(s.prefix, "objects", id, hex.EncodeToString(digest[:])+".bundle.enc")
	if err := s.store.Put(ctx, objectKey, encrypted); err != nil {
		return workspaceCheckpointRepository{}, fmt.Errorf("uploading repository checkpoint: %w", err)
	}

	entry := workspaceCheckpointRepository{
		ID: id, Alias: repo.alias, URL: urlValue, Branch: branch, Upstream: upstream,
		Location: repo.location, Primary: primary, SemanticKey: semanticKey,
		ObjectKey: objectKey, Snapshot: snap, Parent: head, Anchor: anchor, AnchorObjectKey: anchorObjectKey,
	}
	log.Printf("Workspace repository checkpoint uploaded (%s): %s (%d encrypted untracked files)", id, shortSHA(snap), archiveFiles)
	return entry, nil
}

func workspaceRepoID(repo extraRepo, primary bool) string {
	if primary {
		return "primary"
	}
	if repo.location == extraRepoLocationWorkspace {
		return "workspace-" + repo.alias
	}
	return "store-" + repo.alias
}

func (s *workspaceSnapshotter) cachedAnchorObject(anchor string) string {
	if anchor == "" {
		return ""
	}
	for _, entry := range s.lastEntries {
		if entry.Anchor == anchor && entry.AnchorObjectKey != "" {
			return entry.AnchorObjectKey
		}
	}
	return ""
}

// createWorkspaceAnchorPayload preserves the excluded bundle prerequisite and
// every tree/blob reachable from it without copying the prerequisite's full
// history. Restore can recreate a shallow boundary even if origin later stops
// advertising or retaining the anchor commit.
func createWorkspaceAnchorPayload(ctx context.Context, dir, anchor string) ([]byte, error) {
	commit, err := gitOutputRaw(ctx, dir, nil, nil, "cat-file", "commit", anchor)
	if err != nil {
		return nil, fmt.Errorf("reading checkpoint anchor commit: %w", err)
	}
	pack, err := gitOutputRaw(ctx, dir, nil, []byte(anchor+"^{tree}\n"), "pack-objects", "--stdout", "--revs")
	if err != nil {
		return nil, fmt.Errorf("packing checkpoint anchor tree: %w", err)
	}
	if len(commit) > int(^uint32(0)) {
		return nil, fmt.Errorf("checkpoint anchor commit is unexpectedly large")
	}
	payload := make([]byte, 0, len(workspaceAnchorMagic)+4+len(commit)+len(pack))
	payload = append(payload, workspaceAnchorMagic...)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(commit)))
	payload = append(payload, size[:]...)
	payload = append(payload, commit...)
	payload = append(payload, pack...)
	if len(payload) > maxWorkspaceBundleBytes {
		return nil, fmt.Errorf("repository checkpoint anchor payload is %d bytes; maximum is %d", len(payload), maxWorkspaceBundleBytes)
	}
	return payload, nil
}

func restoreWorkspaceAnchorPayload(ctx context.Context, dir, wantAnchor string, payload []byte) error {
	magic := []byte(workspaceAnchorMagic)
	if len(payload) < len(magic)+4 || !bytes.Equal(payload[:len(magic)], magic) {
		return fmt.Errorf("unsupported repository checkpoint anchor format")
	}
	commitSize := int(binary.BigEndian.Uint32(payload[len(magic) : len(magic)+4]))
	commitStart := len(magic) + 4
	commitEnd := commitStart + commitSize
	if commitSize == 0 || commitEnd > len(payload) {
		return fmt.Errorf("repository checkpoint anchor payload is truncated")
	}
	commit, pack := payload[commitStart:commitEnd], payload[commitEnd:]
	if len(pack) == 0 {
		return fmt.Errorf("repository checkpoint anchor pack is empty")
	}
	if _, err := gitOutputRaw(ctx, dir, nil, pack, "index-pack", "--stdin"); err != nil {
		return fmt.Errorf("importing checkpoint anchor tree: %w", err)
	}
	written, err := gitOutputRaw(ctx, dir, nil, commit, "hash-object", "-t", "commit", "-w", "--stdin")
	if err != nil {
		return fmt.Errorf("importing checkpoint anchor commit: %w", err)
	}
	if strings.TrimSpace(string(written)) != wantAnchor {
		return fmt.Errorf("checkpoint anchor commit resolved to %s, want %s", strings.TrimSpace(string(written)), wantAnchor)
	}
	return markGitShallowBoundary(ctx, dir, wantAnchor)
}

// markGitShallowBoundary tells Git that the restored anchor intentionally has
// no locally available parents. Without this, bundle prerequisite validation
// rejects a non-root anchor after its original history was pruned by origin.
func markGitShallowBoundary(ctx context.Context, dir, anchor string) error {
	decoded, err := hex.DecodeString(anchor)
	if err != nil || (len(decoded) != 20 && len(decoded) != 32) {
		return fmt.Errorf("invalid checkpoint anchor object ID %q", anchor)
	}
	shallowPath, err := gitOutput(ctx, dir, nil, "rev-parse", "--git-path", "shallow")
	if err != nil {
		return fmt.Errorf("resolving Git shallow-boundary file: %w", err)
	}
	if !filepath.IsAbs(shallowPath) {
		shallowPath = filepath.Join(dir, shallowPath)
	}
	existing, err := os.ReadFile(shallowPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading Git shallow-boundary file: %w", err)
	}
	if strings.Contains("\n"+string(existing)+"\n", "\n"+anchor+"\n") {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(shallowPath), 0o755); err != nil {
		return fmt.Errorf("creating Git shallow-boundary directory: %w", err)
	}
	f, err := os.OpenFile(shallowPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening Git shallow-boundary file: %w", err)
	}
	defer func() { _ = f.Close() }()
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	if _, err := f.WriteString(anchor + "\n"); err != nil {
		return fmt.Errorf("writing Git shallow boundary: %w", err)
	}
	return nil
}

func createWorkspaceBundle(ctx context.Context, dir, head, snap string) ([]byte, string, error) {
	anchor := bestRemoteAncestor(ctx, dir, head)
	bundle, err := os.CreateTemp("", "gratefulagents-checkpoint-*.bundle")
	if err != nil {
		return nil, "", fmt.Errorf("creating checkpoint bundle file: %w", err)
	}
	bundlePath := bundle.Name()
	_ = bundle.Close()
	_ = os.Remove(bundlePath)
	defer func() { _ = os.Remove(bundlePath) }()

	if _, err := gitOutput(ctx, dir, nil, "update-ref", workspaceBundleRef, snap); err != nil {
		return nil, "", fmt.Errorf("creating local checkpoint ref: %w", err)
	}
	defer func() { _, _ = gitOutput(context.Background(), dir, nil, "update-ref", "-d", workspaceBundleRef) }()
	args := []string{"bundle", "create", bundlePath, workspaceBundleRef}
	if anchor != "" {
		args = append(args, "^"+anchor)
	}
	if _, err := gitOutput(ctx, dir, nil, args...); err != nil {
		return nil, "", fmt.Errorf("creating repository checkpoint bundle: %w", err)
	}
	info, err := os.Stat(bundlePath)
	if err != nil {
		return nil, "", err
	}
	if info.Size() > maxWorkspaceBundleBytes {
		return nil, "", fmt.Errorf("repository checkpoint bundle is %d bytes; maximum is %d", info.Size(), maxWorkspaceBundleBytes)
	}
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		return nil, "", fmt.Errorf("reading repository checkpoint bundle: %w", err)
	}
	return data, anchor, nil
}

func bestRemoteAncestor(ctx context.Context, dir, head string) string {
	out, err := gitOutput(ctx, dir, nil, "for-each-ref", "--format=%(objectname)", "refs/remotes/origin")
	if err != nil || out == "" {
		return ""
	}
	best, bestDistance := "", int64(^uint64(0)>>1)
	seen := map[string]struct{}{}
	for _, candidate := range strings.Fields(out) {
		if _, ok := seen[candidate]; ok || !isGitAncestor(ctx, dir, candidate, head) {
			continue
		}
		seen[candidate] = struct{}{}
		distanceText, err := gitOutput(ctx, dir, nil, "rev-list", "--count", candidate+".."+head)
		if err != nil {
			continue
		}
		var distance int64
		if _, err := fmt.Sscan(distanceText, &distance); err == nil && distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	return best
}

func restorePrimaryWorkspaceCheckpoint(cfg runConfig, branchWasPushed bool) error {
	if cfg.WorkspaceCheckpoint == nil {
		log.Printf("No workspace checkpoint to restore")
		return nil
	}
	for _, entry := range cfg.WorkspaceCheckpoint.Repositories {
		if entry.Primary {
			return restoreWorkspaceCheckpointRepo(context.Background(), cfg, cfg.RepoDir, entry, branchWasPushed)
		}
	}
	return nil
}

func restoreWorkspaceCheckpointRepo(ctx context.Context, cfg runConfig, dest string, entry workspaceCheckpointRepository, branchWasPushed bool) error {
	ctx, cancel := context.WithTimeout(ctx, workspaceCheckpointTimeout)
	defer cancel()
	payload, found, err := cfg.WorkspaceCheckpointStore.Get(ctx, entry.ObjectKey)
	if err != nil {
		return fmt.Errorf("downloading repository checkpoint: %w", err)
	}
	if !found {
		return fmt.Errorf("published repository checkpoint object %q is missing", entry.ObjectKey)
	}
	bundleData, err := decryptWorkspaceArchive(cfg.WorkspaceSnapshotKey, payload)
	if err != nil {
		return fmt.Errorf("decrypting repository checkpoint: %w", err)
	}
	bundle, err := os.CreateTemp("", "gratefulagents-restore-*.bundle")
	if err != nil {
		return err
	}
	bundlePath := bundle.Name()
	defer func() { _ = os.Remove(bundlePath) }()
	if _, err := bundle.Write(bundleData); err != nil {
		_ = bundle.Close()
		return err
	}
	if err := bundle.Close(); err != nil {
		return err
	}

	if entry.Anchor != "" && !gitObjectExists(ctx, dest, entry.Anchor) {
		anchorEnvelope, found, err := cfg.WorkspaceCheckpointStore.Get(ctx, entry.AnchorObjectKey)
		if err != nil {
			return fmt.Errorf("downloading repository checkpoint anchor: %w", err)
		}
		if !found {
			return fmt.Errorf("published repository checkpoint anchor object %q is missing", entry.AnchorObjectKey)
		}
		anchorPayload, err := decryptWorkspaceArchive(cfg.WorkspaceSnapshotKey, anchorEnvelope)
		if err != nil {
			return fmt.Errorf("decrypting repository checkpoint anchor: %w", err)
		}
		if err := restoreWorkspaceAnchorPayload(ctx, dest, entry.Anchor, anchorPayload); err != nil {
			return err
		}
	}
	if _, err := gitOutput(ctx, dest, nil, "fetch", "--quiet", bundlePath, workspaceBundleRef); err != nil {
		return fmt.Errorf("importing repository checkpoint bundle: %w", err)
	}
	snap, err := gitOutput(ctx, dest, nil, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return err
	}
	if snap != entry.Snapshot {
		return fmt.Errorf("repository checkpoint resolved to %s, want %s", snap, entry.Snapshot)
	}
	parent, err := gitOutput(ctx, dest, nil, "rev-parse", snap+"^1")
	if err != nil {
		return fmt.Errorf("resolving repository checkpoint parent: %w", err)
	}
	if parent != entry.Parent {
		return fmt.Errorf("repository checkpoint parent is %s, want %s", parent, entry.Parent)
	}

	tip, err := gitOutput(ctx, dest, nil, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	switch {
	case parent == tip:
	case isGitAncestor(ctx, dest, tip, parent):
	case !branchWasPushed && isGitAncestor(ctx, dest, parent, tip):
	default:
		log.Printf("WARN: workspace checkpoint %s diverged from branch tip %s; keeping fresh checkout", shortSHA(snap), shortSHA(tip))
		return nil
	}

	if _, err := gitOutput(ctx, dest, nil, "reset", "--hard", snap); err != nil {
		return fmt.Errorf("applying repository checkpoint tree: %w", err)
	}
	if _, err := gitOutput(ctx, dest, nil, "reset", "--quiet", parent); err != nil {
		return fmt.Errorf("unwrapping repository checkpoint: %w", err)
	}
	encryptedArchive, found, err := encryptedWorkspaceArchiveFromSnapshot(ctx, dest, snap)
	if err != nil {
		return err
	}
	untrackedFiles := 0
	if found {
		untrackedFiles, err = restoreEncryptedWorkspaceArchive(dest, cfg.WorkspaceSnapshotKey, encryptedArchive)
		if err != nil {
			return fmt.Errorf("restoring encrypted untracked files: %w", err)
		}
	}
	log.Printf("Workspace repository checkpoint restored: %s (HEAD at %s, encrypted untracked files: %d)", shortSHA(snap), shortSHA(parent), untrackedFiles)
	return nil
}

func gitObjectExists(ctx context.Context, dir, object string) bool {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "-e", object+"^{commit}")
	cmd.Dir = dir
	return cmd.Run() == nil
}

// restoreExtraRepos re-clones every attached repository represented in the
// atomically published workspace checkpoint.
func restoreExtraRepos(ctx context.Context, cfg runConfig, _ *sessionclient.Client) error {
	if cfg.WorkspaceCheckpoint == nil {
		return nil
	}
	for _, entry := range cfg.WorkspaceCheckpoint.Repositories {
		if entry.Primary {
			continue
		}
		if err := restoreExtraRepo(ctx, cfg, entry); err != nil {
			return fmt.Errorf("restoring attached repo %q: %w", entry.Alias, err)
		}
	}
	if !cfg.Repoless {
		ensureExtraRepoExclude(cfg.RepoDir)
	}
	return nil
}

func extraRepoRestoreDest(cfg runConfig, entry workspaceCheckpointRepository) (string, error) {
	alias := strings.TrimSpace(entry.Alias)
	if alias == "" || alias == "." || alias == ".." || alias != filepath.Base(alias) {
		return "", fmt.Errorf("invalid checkpoint repository alias %q", entry.Alias)
	}
	switch entry.Location {
	case extraRepoLocationStore:
		return filepath.Join(cfg.RepoDir, extraRepoStoreDirName, alias), nil
	case extraRepoLocationWorkspace:
		if alias == filepath.Base(cfg.RepoDir) {
			return "", fmt.Errorf("workspace clone alias %q collides with the primary repository", alias)
		}
		return filepath.Join(filepath.Dir(cfg.RepoDir), alias), nil
	default:
		return "", fmt.Errorf("unknown repository location %q", entry.Location)
	}
}

func restoreExtraRepo(ctx context.Context, cfg runConfig, entry workspaceCheckpointRepository) error {
	if strings.TrimSpace(entry.URL) == "" {
		return fmt.Errorf("checkpoint repository %q is missing its URL", entry.Alias)
	}
	dest, err := extraRepoRestoreDest(cfg, entry)
	if err != nil {
		return err
	}
	exists, err := validateOrRemoveExtraRepoDest(ctx, dest, entry.URL)
	if err != nil {
		return err
	}
	if !exists {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if _, err := gitOutput(ctx, "", nil, "clone", "--quiet", "--depth", "1", "--single-branch", "--no-tags", entry.URL, dest); err != nil {
			return fmt.Errorf("cloning: %w", err)
		}
	}
	branch := strings.TrimSpace(entry.Branch)
	branchWasPushed := false
	if branch != "" && branch != "HEAD" {
		remoteRef := "refs/heads/" + branch
		branchWasPushed, err = remoteRefExists(ctx, dest, remoteRef)
		if err != nil {
			return fmt.Errorf("probing attached repository work branch: %w", err)
		}
		if branchWasPushed {
			if _, err := gitOutput(ctx, dest, nil, "fetch", "--quiet", "origin", remoteRef+":refs/remotes/origin/"+branch); err != nil {
				return fmt.Errorf("fetching attached repository work branch: %w", err)
			}
			if _, err := gitOutput(ctx, dest, nil, "checkout", "--quiet", "--no-track", "-B", branch, "origin/"+branch); err != nil {
				return fmt.Errorf("checking out attached repository work branch: %w", err)
			}
		} else if _, err := gitOutput(ctx, dest, nil, "checkout", "--quiet", "-B", branch); err != nil {
			return fmt.Errorf("creating attached repository work branch: %w", err)
		}
	}
	if err := restoreWorkspaceCheckpointRepo(ctx, cfg, dest, entry, branchWasPushed); err != nil {
		return err
	}
	if branch != "" && branch != "HEAD" {
		restoreExtraRepoUpstream(ctx, dest, entry.Alias, branch, entry.Upstream)
	}
	return nil
}

func restoreExtraRepoUpstream(ctx context.Context, dest, alias, branch, upstream string) {
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return
	}
	if _, err := gitOutput(ctx, dest, nil, "branch", "--quiet", "--set-upstream-to", upstream, branch); err != nil {
		log.Printf("WARN: attached repo %q: restoring upstream %s: %v", alias, upstream, err)
	}
}

// writeWorkingTree creates a synthetic tree for tracked modifications and
// deletions without changing the user's index.
func writeWorkingTree(ctx context.Context, dir, head string) (string, error) {
	idx, err := os.CreateTemp("", "gratefulagents-checkpoint-index-*")
	if err != nil {
		return "", fmt.Errorf("creating temporary index: %w", err)
	}
	idxPath := idx.Name()
	_ = idx.Close()
	_ = os.Remove(idxPath)
	defer func() { _ = os.Remove(idxPath) }()
	env := []string{"GIT_INDEX_FILE=" + idxPath}
	if _, err := gitOutput(ctx, dir, env, "read-tree", head); err != nil {
		return "", fmt.Errorf("seeding temporary index: %w", err)
	}
	if _, err := gitOutput(ctx, dir, env, "add", "-u", "--", "."); err != nil {
		return "", fmt.Errorf("staging tracked workspace: %w", err)
	}
	tree, err := gitOutput(ctx, dir, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("writing checkpoint tree: %w", err)
	}
	return tree, nil
}

type extraRepo struct {
	alias    string
	dir      string
	location string
}

func discoverExtraRepos(repoDir string) []extraRepo {
	repos, _ := discoverExtraReposStrict(repoDir)
	return repos
}

func discoverExtraReposStrict(repoDir string) ([]extraRepo, error) {
	storeRepos, err := discoverStoreReposStrict(repoDir)
	if err != nil {
		return nil, err
	}
	workspaceRepos, err := discoverWorkspaceRootReposStrict(repoDir)
	if err != nil {
		return nil, err
	}
	return append(storeRepos, workspaceRepos...), nil
}

func discoverStoreRepos(repoDir string) []extraRepo {
	repos, _ := discoverStoreReposStrict(repoDir)
	return repos
}

func discoverStoreReposStrict(repoDir string) ([]extraRepo, error) {
	storeDir := filepath.Join(repoDir, extraRepoStoreDirName)
	entries, err := os.ReadDir(storeDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading attached-repository store %s: %w", storeDir, err)
	}
	var repos []extraRepo
	for _, entry := range entries {
		dir := filepath.Join(storeDir, entry.Name())
		if !entry.IsDir() {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, ".git"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspecting attached repository %s: %w", dir, err)
		}
		if info.IsDir() {
			repos = append(repos, extraRepo{alias: entry.Name(), dir: dir})
		}
	}
	return repos, nil
}

func discoverWorkspaceRootRepos(repoDir string) []extraRepo {
	repos, _ := discoverWorkspaceRootReposStrict(repoDir)
	return repos
}

func discoverWorkspaceRootReposStrict(repoDir string) ([]extraRepo, error) {
	root, self := filepath.Dir(repoDir), filepath.Base(repoDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("reading workspace root %s: %w", root, err)
	}
	var repos []extraRepo
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || name == self || strings.HasPrefix(name, ".") {
			continue
		}
		dir := filepath.Join(root, name)
		info, err := os.Stat(filepath.Join(dir, ".git"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspecting workspace repository %s: %w", dir, err)
		}
		if info.IsDir() {
			repos = append(repos, extraRepo{alias: name, dir: dir, location: extraRepoLocationWorkspace})
		}
	}
	return repos, nil
}

func finalizeWorkspaceSnapshot(result runResult, s *workspaceSnapshotter) error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), workspaceCheckpointTimeout)
	defer cancel()
	if result.Status == "succeeded" {
		return s.Cleanup(ctx)
	}
	return s.snapshotSync(ctx, "shutdown")
}

func validateOrRemoveExtraRepoDest(ctx context.Context, dest, expectedURL string) (bool, error) {
	if _, err := os.Lstat(dest); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if _, err := gitOutput(ctx, dest, nil, "rev-parse", "--is-inside-work-tree"); err != nil {
		if err := os.RemoveAll(dest); err != nil {
			return false, fmt.Errorf("removing incomplete destination: %w", err)
		}
		return false, nil
	}
	actualURL, err := gitOutput(ctx, dest, nil, "remote", "get-url", "origin")
	if err != nil {
		return false, fmt.Errorf("validating existing origin: %w", err)
	}
	if canonicalGitURL(actualURL) != canonicalGitURL(expectedURL) {
		return false, fmt.Errorf("existing destination origin %q does not match expected %q", actualURL, expectedURL)
	}
	return true, nil
}

func canonicalGitURL(raw string) string {
	raw = strings.TrimSpace(strings.TrimSuffix(raw, "/"))
	if strings.HasPrefix(raw, "git@github.com:") {
		raw = "https://github.com/" + strings.TrimPrefix(raw, "git@github.com:")
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		parsed.User = nil
		parsed.Path = strings.TrimSuffix(parsed.Path, ".git")
		return parsed.String()
	}
	if absolute, err := filepath.Abs(raw); err == nil {
		return filepath.Clean(absolute)
	}
	return strings.TrimSuffix(raw, ".git")
}

func ensureExtraRepoExclude(repoDir string) {
	pattern := extraRepoStoreDirName + "/"
	file := filepath.Join(repoDir, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return
	}
	existing, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return
	}
	if strings.Contains("\n"+string(existing)+"\n", "\n"+pattern+"\n") {
		return
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		_, _ = f.WriteString("\n")
	}
	_, _ = f.WriteString(pattern + "\n")
}

func remoteRefExists(ctx context.Context, dir, ref string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--exit-code", "origin", ref)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
		return false, nil
	}
	return false, fmt.Errorf("git ls-remote origin %s: %w (%s)", ref, err, strings.TrimSpace(stderr.String()))
}

func gitOutput(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func isGitAncestor(ctx context.Context, repoDir, ancestor, descendant string) bool {
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = repoDir
	return cmd.Run() == nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
