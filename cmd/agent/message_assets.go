package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
)

const messageAssetWorkspaceDir = ".gratefulagents/assets"

func materializeMessageAssets(ctx context.Context, workDir string, run *platformv1alpha1.AgentRun, stateStore store.StateStore, images []sessionclient.MessageImage) ([]string, error) {
	projectName := runProjectName(run)
	if projectName == "" || len(images) == 0 {
		return nil, nil
	}
	contentStore, ok := stateStore.(store.ProjectContentStore)
	if !ok {
		return nil, fmt.Errorf("state store does not support project assets")
	}
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return nil, fmt.Errorf("opening workspace for project assets: %w", err)
	}
	defer func() { _ = root.Close() }()

	var paths []string
	for _, image := range images {
		if image.AssetID == "" {
			continue
		}
		id, err := uuid.Parse(image.AssetID)
		if err != nil {
			return nil, fmt.Errorf("invalid project asset ID %q: %w", image.AssetID, err)
		}
		item, err := contentStore.GetContent(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("loading project asset %s: %w", id, err)
		}
		if item.ProjectNamespace != run.Namespace || item.ProjectName != projectName || image.ProjectName != projectName {
			return nil, fmt.Errorf("project asset %s does not belong to run project %s/%s", id, run.Namespace, projectName)
		}
		if item.DeletedAt != nil || item.ScanStatus != store.ScanStatusClean {
			return nil, fmt.Errorf("project asset %s is not available for workspace materialization", id)
		}
		if image.AssetVersion <= 0 || image.AssetSHA256 == "" {
			return nil, fmt.Errorf("project asset %s reference is not pinned to an immutable version", id)
		}
		version, err := contentStore.GetContentVersion(ctx, id, image.AssetVersion)
		if err != nil {
			return nil, fmt.Errorf("loading project asset %s version %d bytes: %w", id, image.AssetVersion, err)
		}
		if version.Version != image.AssetVersion || version.SHA256 != image.AssetSHA256 {
			return nil, fmt.Errorf("project asset %s version %d does not match the pinned attachment hash", id, image.AssetVersion)
		}
		rel, err := safeMessageAssetPath(image.AssetPath)
		if err != nil {
			return nil, err
		}
		if err := root.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
			return nil, fmt.Errorf("creating project asset workspace directory: %w", err)
		}
		if existing, openErr := root.Open(rel); openErr == nil {
			existingBytes, readErr := io.ReadAll(io.LimitReader(existing, store.MaxProjectContentVersionBytes+1))
			closeErr := existing.Close()
			if readErr != nil || closeErr != nil {
				return nil, fmt.Errorf("reading existing workspace project asset %q", filepath.ToSlash(rel))
			}
			if !bytes.Equal(existingBytes, version.Content) {
				return nil, fmt.Errorf("workspace project asset %q was modified; copy it to another path before editing", filepath.ToSlash(rel))
			}
			paths = append(paths, filepath.ToSlash(rel))
			continue
		} else if !os.IsNotExist(openErr) {
			return nil, fmt.Errorf("checking existing workspace project asset %q: %w", filepath.ToSlash(rel), openErr)
		}
		file, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
		if err != nil {
			return nil, fmt.Errorf("creating workspace project asset %q: %w", filepath.ToSlash(rel), err)
		}
		removePartial := true
		defer func() {
			if removePartial {
				_ = root.Remove(rel)
			}
		}()
		n, writeErr := file.Write(version.Content)
		closeErr := file.Close()
		if writeErr != nil {
			return nil, fmt.Errorf("writing workspace project asset %q: %w", filepath.ToSlash(rel), writeErr)
		}
		if n != len(version.Content) {
			return nil, fmt.Errorf("writing workspace project asset %q: short write", filepath.ToSlash(rel))
		}
		if closeErr != nil {
			return nil, fmt.Errorf("closing workspace project asset %q: %w", filepath.ToSlash(rel), closeErr)
		}
		removePartial = false
		paths = append(paths, filepath.ToSlash(rel))
	}
	if len(paths) > 0 {
		ensureMessageAssetExclude(workDir)
	}
	return paths, nil
}

func materializeRecentConversationAssets(ctx context.Context, workDir string, run *platformv1alpha1.AgentRun, stateStore store.StateStore, messages []store.Message, limit int) ([]store.Message, error) {
	prepared := append([]store.Message(nil), messages...)
	start := 0
	if limit > 0 && len(prepared) > limit {
		start = len(prepared) - limit
	}
	for i := start; i < len(prepared); i++ {
		if prepared[i].Role != "user" {
			continue
		}
		images := sessionclient.ImagesFromMetadata(prepared[i].Metadata)
		paths, err := materializeMessageAssets(ctx, workDir, run, stateStore, images)
		if err != nil {
			return prepared, fmt.Errorf("materializing assets for message %d: %w", prepared[i].ID, err)
		}
		prepared[i].Content = appendMessageAssetNotice(prepared[i].Content, paths)
	}
	return prepared, nil
}

func runProjectName(run *platformv1alpha1.AgentRun) string {
	if run == nil || run.Spec.Context == nil || run.Spec.Context.ProjectRef == nil || run.Spec.Context.ProjectRef.Kind != "Project" {
		return ""
	}
	return strings.TrimSpace(run.Spec.Context.ProjectRef.Name)
}

func safeMessageAssetPath(assetPath string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(assetPath)))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe project asset path %q", assetPath)
	}
	return filepath.Join(messageAssetWorkspaceDir, clean), nil
}

func appendMessageAssetNotice(text string, paths []string) string {
	if len(paths) == 0 {
		return text
	}
	var notice strings.Builder
	notice.WriteString("Attached project assets are available in the workspace at:\n")
	for _, path := range paths {
		fmt.Fprintf(&notice, "- `%s`\n", path)
	}
	notice.WriteString("These originals are read-only. Copy one to another path before modifying or transforming it.")
	if strings.TrimSpace(text) == "" {
		return notice.String()
	}
	return text + "\n\n" + notice.String()
}

func ensureMessageAssetExclude(workDir string) {
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return
	}
	defer func() { _ = root.Close() }()
	if info, err := root.Stat(".git"); err != nil || !info.IsDir() {
		return
	}
	file := filepath.Join(".git", "info", "exclude")
	if err := root.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return
	}
	var existing []byte
	if existingFile, openErr := root.Open(file); openErr == nil {
		existing, err = io.ReadAll(existingFile)
		_ = existingFile.Close()
		if err != nil {
			return
		}
	} else if !os.IsNotExist(openErr) {
		return
	}
	pattern := messageAssetWorkspaceDir + "/"
	if strings.Contains("\n"+string(existing)+"\n", "\n"+pattern+"\n") {
		return
	}
	f, err := root.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		_, _ = f.WriteString("\n")
	}
	_, _ = f.WriteString(pattern + "\n")
}
