package dashboard

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// Filenames inside the export archive. Kept as constants so tests and any
// future consumers stay in sync with the writer.
const (
	exportReadmeName   = "README.txt"
	exportRunName      = "run.json"
	exportActivityName = "activity.jsonl"
	exportTraceName    = "trace.json"
)

// ExportAgentRunArchive bundles the run metadata, activity log and OTel trace
// into a zip archive for download. It works for live runs too: the activity
// log falls back through Postgres and pod exec exactly like GetActivityLog,
// and the trace is fetched from Jaeger on demand. Missing pieces (no trace ID
// yet, Jaeger unconfigured) degrade to a note in README.txt instead of
// failing the whole export.
func (s *Server) ExportAgentRunArchive(ctx context.Context, req *platform.ExportAgentRunArchiveRequest) (*platform.ExportAgentRunArchiveResponse, error) {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return nil, err
	}
	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: req.Namespace,
		Name:      req.Name,
	}, run); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get AgentRun %s/%s", req.Namespace, req.Name), err)
	}

	pretty := protojson.MarshalOptions{Multiline: true, Indent: "  "}
	var notes []string

	// Run metadata (same enriched proto the dashboard renders).
	runPB, err := s.enrichAgentRunProto(ctx, k8sAgentRunToProto(run))
	if err != nil {
		return nil, err
	}
	runJSON, err := pretty.Marshal(runPB)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal run metadata: %w", err))
	}

	// Activity log: one protojson ActivityEntry per line. For ongoing runs
	// this is a snapshot of everything captured so far.
	activity := s.getAgentRunActivityLog(ctx, run)
	var activityBuf bytes.Buffer
	for _, entry := range activity.Entries {
		line, err := protojson.Marshal(entry)
		if err != nil {
			log.Printf("WARN: ExportAgentRunArchive %s/%s: skipping unmarshalable activity entry: %v", req.Namespace, req.Name, err)
			continue
		}
		activityBuf.Write(line)
		activityBuf.WriteByte('\n')
	}
	if len(activity.Entries) == 0 {
		notes = append(notes, "no activity entries were available at export time")
	}
	if !activity.IsComplete {
		notes = append(notes, "the run was still in progress: activity.jsonl and trace.json are a snapshot, re-export after completion for the full record")
	}

	// Trace: best effort. A live run may not have published a trace ID yet
	// and Jaeger may not be configured at all.
	traceID := ""
	if run.Status.Artifacts != nil {
		traceID = run.Status.Artifacts.TraceID
	}
	var traceJSON []byte
	switch {
	case s.jaeger == nil:
		notes = append(notes, "trace.json omitted: Jaeger is not configured on the server")
	case traceID == "":
		notes = append(notes, "trace.json omitted: the run has not published a trace ID")
	default:
		traceResp, err := s.jaeger.FetchTrace(traceID)
		if err != nil {
			log.Printf("WARN: ExportAgentRunArchive %s/%s: jaeger fetch for trace %s failed: %v", req.Namespace, req.Name, traceID, err)
			notes = append(notes, fmt.Sprintf("trace.json omitted: fetching trace %s from Jaeger failed", traceID))
		} else {
			traceResp.IsComplete = isTerminalAgentRunPhase(run.Status.Phase)
			if traceJSON, err = pretty.Marshal(traceResp); err != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal trace: %w", err))
			}
		}
	}

	now := time.Now().UTC()
	files := []struct {
		name string
		data []byte
	}{
		{exportReadmeName, buildExportReadme(run, activity.IsComplete, traceJSON != nil, now, notes)},
		{exportRunName, runJSON},
		{exportActivityName, activityBuf.Bytes()},
	}
	if traceJSON != nil {
		files = append(files, struct {
			name string
			data []byte
		}{exportTraceName, traceJSON})
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{
			Name:     f.name,
			Method:   zip.Deflate,
			Modified: now,
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create %s in archive: %w", f.name, err))
		}
		if _, err := w.Write(f.data); err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("write %s to archive: %w", f.name, err))
		}
	}
	if err := zw.Close(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("finalize archive: %w", err))
	}

	return &platform.ExportAgentRunArchiveResponse{
		Archive:  buf.Bytes(),
		Filename: fmt.Sprintf("%s-export-%s.zip", sanitizeExportName(run.Name), now.Format("20060102T150405Z")),
	}, nil
}

// buildExportReadme renders the archive manifest, including any notes about
// pieces that were unavailable at export time.
func buildExportReadme(run *platformv1alpha1.AgentRun, isComplete, hasTrace bool, exportedAt time.Time, notes []string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "AgentRun export\n===============\n\n")
	fmt.Fprintf(&b, "Run:       %s/%s\n", run.Namespace, run.Name)
	fmt.Fprintf(&b, "Phase:     %s\n", run.Status.Phase)
	fmt.Fprintf(&b, "Exported:  %s\n", exportedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "Complete:  %t\n", isComplete)
	b.WriteString("\nContents\n--------\n")
	fmt.Fprintf(&b, "%-16s AgentRun metadata and status (protojson platform.v1.AgentRun)\n", exportRunName)
	fmt.Fprintf(&b, "%-16s activity log, one protojson platform.v1.ActivityEntry per line\n", exportActivityName)
	if hasTrace {
		fmt.Fprintf(&b, "%-16s OTel trace spans from Jaeger (protojson platform.v1.GetAgentTraceResponse)\n", exportTraceName)
	}
	if len(notes) > 0 {
		b.WriteString("\nNotes\n-----\n")
		for _, n := range notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	return []byte(b.String())
}

// sanitizeExportName keeps download filenames shell- and filesystem-friendly.
// AgentRun names are already RFC 1123 labels, so this is defensive only.
func sanitizeExportName(name string) string {
	sanitized := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, name)
	if sanitized == "" {
		return "agentrun"
	}
	return sanitized
}
