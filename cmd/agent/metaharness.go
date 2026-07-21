package main

// Meta-Harness trace capture (observer).
//
// When ENABLE_METAHARNESS=true the runner records full execution traces —
// LLM calls, tool invocations, hook events, and structural spans (session,
// subagent, retry, compaction) — into a local filesystem trace store below
// the workspace. Because the pod and its workspace are deleted after the run,
// the finalized trace directory is archived, encrypted with the run's
// workspace snapshot key, and uploaded to the workspace object store; the
// resulting object is published on the AgentRun status as
// status.artifacts.metaHarnessTraceRef so traces stay discoverable after
// compute teardown.
//
// Capture policy: full capture, explicitly including subagent runs. The main
// run's per-turn hooks and runner.DefaultHooks (used by subagent runs) both
// include the trace writer, and the tracker's tracing processor is composed
// with the writer before installation so no structural span bypasses it.
//
// Retention: trace archives live under metaHarnessTraceStorePrefix in the
// same bucket as workspace checkpoints but are NOT deleted by workspace
// checkpoint cleanup — retention is governed by the bucket lifecycle policy.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	metaharness "github.com/gratefulagents/sdk/pkg/agentsdk/tracestore"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

// metaHarnessTraceStorePrefix is the tenant-scoped object-store prefix for
// encrypted trace archives. It is deliberately distinct from the workspace
// checkpoint prefix so checkpoint cleanup never removes trace artifacts.
const metaHarnessTraceStorePrefix = "metaharness-traces/v1"

// metaHarnessArtifactKind identifies object-store-backed trace archives in
// AgentRun status.artifacts.metaHarnessTraceRef.
const metaHarnessArtifactKind = "S3Object"

// metaHarnessFinalizeTimeout bounds the shutdown-path archive/encrypt/upload
// plus the status patch. Runner pods have a 60s termination grace period and
// the final workspace checkpoint (which runs after trace finalization) may
// itself take up to workspaceCheckpointTimeout (30s), so this budget must
// leave room for both inside the grace period.
const metaHarnessFinalizeTimeout = 15 * time.Second

func metaHarnessEnabled() bool {
	return strings.EqualFold(os.Getenv("ENABLE_METAHARNESS"), "true")
}

// metaHarnessTraceObjectKey returns the object key for one uploaded trace
// archive. The key is scoped by namespace and run UID (tenant isolation) and
// timestamped so a replacement pod never overwrites an earlier pod's trace.
func metaHarnessTraceObjectKey(namespace, taskUID string, now time.Time) string {
	return fmt.Sprintf("%s/%s/%s/%d.tar.gz.enc", metaHarnessTraceStorePrefix, namespace, taskUID, now.UnixNano())
}

// newMetaHarnessWriter initialises the filesystem trace store and trace
// writer. The store root is <workspace>/metaharness; the store itself nests
// traces/<run-id>, so the configured root must not already end in "traces"
// (passing <workspace>/metaharness/traces would produce a duplicated
// .../traces/traces/<run-id> layout). Returns the writer and the run's trace
// directory, or (nil, "") when capture could not be initialised.
func newMetaHarnessWriter(cfg runConfig, initialModeName string) (*metaharness.TraceWriter, string) {
	traceRoot := filepath.Join(cfg.WorkspaceDir, "metaharness")
	store, err := metaharness.NewFilesystemTraceStore(traceRoot)
	if err != nil {
		log.Printf("WARN: metaharness store init failed: %v — continuing without trace capture", err)
		return nil, ""
	}
	writer := metaharness.NewTraceWriter(store)
	if err := writer.InitRun(metaharness.RunMetadata{
		RunID:          cfg.TaskName,
		CandidateID:    strings.TrimSpace(os.Getenv("METAHARNESS_CANDIDATE")),
		Model:          cfg.Model,
		Mode:           initialModeName,
		PermissionMode: string(cfg.PermissionMode),
		Cwd:            cfg.RepoDir,
		StartedAt:      time.Now(),
	}); err != nil {
		log.Printf("WARN: metaharness trace init failed: %v — continuing without trace capture", err)
		return nil, ""
	}
	traceDir, err := store.RunDir(cfg.TaskName)
	if err != nil {
		log.Printf("WARN: metaharness run dir unavailable: %v — continuing without trace capture", err)
		return nil, ""
	}
	log.Printf("Meta-Harness trace capture enabled → %s", traceDir)
	return writer, traceDir
}

// metaHarnessMetrics converts the process-local progress snapshot into the
// aggregate metrics.json payload. Counters cover this pod's process only; a
// replacement pod uploads its own archive with its own counters.
func metaHarnessMetrics(snap agent.ProgressSnapshot, startedAt time.Time, status string) map[string]any {
	return map[string]any{
		"status":                      status,
		"duration_sec":                time.Since(startedAt).Seconds(),
		"cost_usd":                    snap.CostUsd,
		"input_tokens":                snap.InputTokens,
		"output_tokens":               snap.OutputTokens,
		"cache_read_input_tokens":     snap.CacheReadInputTokens,
		"cache_creation_input_tokens": snap.CacheCreationInputTokens,
		"tool_calls":                  snap.ToolCallCount,
		"turns_used":                  snap.SessionNumber,
		"agent_count":                 snap.AgentCount,
		"api_retries":                 snap.ApiRetries,
	}
}

// finalizeMetaHarnessTrace writes aggregate metrics, finalizes run metadata,
// then archives, encrypts, and uploads the trace directory and publishes the
// artifact reference on the AgentRun status. Every step is best-effort: trace
// durability must never fail the run itself.
func finalizeMetaHarnessTrace(cfg runConfig, crdClient client.Client, writer *metaharness.TraceWriter, traceDir string, snap agent.ProgressSnapshot, startedAt time.Time, status string) {
	if writer == nil || traceDir == "" {
		return
	}
	writer.WriteMetrics(metaHarnessMetrics(snap, startedAt, status))
	writer.FinalizeRun(status)

	if cfg.WorkspaceCheckpointStore == nil {
		log.Printf("WARN: metaharness trace upload skipped: no workspace object store configured")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), metaHarnessFinalizeTimeout)
	defer cancel()

	objectKey, err := uploadMetaHarnessTrace(ctx, cfg.WorkspaceCheckpointStore, cfg.WorkspaceSnapshotKey, traceDir, cfg.Namespace, cfg.TaskUID)
	if err != nil {
		log.Printf("WARN: metaharness trace upload failed: %v", err)
		return
	}
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	if err := patchAgentRunStatus(ctx, crdClient, cfg.TaskName, cfg.Namespace, func(run *platformv1alpha1.AgentRun) {
		run.Status.Artifacts = ensureRunArtifacts(run.Status.Artifacts)
		run.Status.Artifacts.MetaHarnessTraceRef = &platformv1alpha1.ArtifactRef{
			Kind: metaHarnessArtifactKind,
			Name: bucket,
			Key:  objectKey,
		}
	}); err != nil {
		log.Printf("WARN: failed to publish metaharness trace artifact ref: %v", err)
		return
	}
	log.Printf("Meta-Harness trace archived → s3://%s/%s", bucket, objectKey)
}

// uploadMetaHarnessTrace archives the trace directory, encrypts it with the
// run's workspace snapshot key, and puts it in the object store. Returns the
// object key of the uploaded archive.
func uploadMetaHarnessTrace(ctx context.Context, store workspaceObjectStore, key []byte, traceDir, namespace, taskUID string) (string, error) {
	archive, err := buildMetaHarnessTraceArchive(traceDir)
	if err != nil {
		return "", fmt.Errorf("archiving metaharness trace dir: %w", err)
	}
	encrypted, err := encryptWorkspaceArchive(key, archive)
	if err != nil {
		return "", fmt.Errorf("encrypting metaharness trace archive: %w", err)
	}
	objectKey := metaHarnessTraceObjectKey(namespace, taskUID, time.Now())
	if err := store.Put(ctx, objectKey, encrypted); err != nil {
		return "", err
	}
	return objectKey, nil
}

// buildMetaHarnessTraceArchive tars and gzips the regular files below the
// run's trace directory. Paths inside the archive are relative to traceDir.
func buildMetaHarnessTraceArchive(traceDir string) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	err := filepath.Walk(traceDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(traceDir, path)
		if relErr != nil {
			return relErr
		}
		header := &tar.Header{
			Name:     filepath.ToSlash(rel),
			Mode:     0o600,
			Size:     info.Size(),
			ModTime:  info.ModTime(),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		f, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		defer func() { _ = f.Close() }()
		// Copy at most the header size so a concurrent append cannot corrupt
		// the archive framing.
		if _, err := io.CopyN(tw, f, info.Size()); err != nil && err != io.EOF {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
