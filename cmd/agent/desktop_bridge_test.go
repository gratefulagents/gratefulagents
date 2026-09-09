package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gratefulagents/gratefulagents/internal/computeruse"
)

func TestDesktopBridgeRoundTrip(t *testing.T) {
	uid := uuid.NewString()
	t.Setenv("PLANTASK_UID", uid)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, closeBroker, err := startDesktopBroker(ctx, runConfig{Namespace: "ns", TaskName: "run", TaskUID: uid})
	if err != nil {
		t.Fatal(err)
	}
	defer closeBroker()
	path, err := computeruse.SocketPath(uid)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filepath.Dir(path))
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions: %v %v", info, err)
	}
	e := computeruse.Exchange{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "desktop", Operation: "attach"}
	raw, _ := json.Marshal(e)
	var out bytes.Buffer
	if err := runDesktopBridge(bytes.NewReader(raw), &out); err != nil {
		t.Fatal(err)
	}
	var response computeruse.Response
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || !response.Active {
		t.Fatalf("response: %+v %v", response, err)
	}
	if !computeruse.FromContext(ctx).Active() {
		t.Fatal("broker not bound to context")
	}
	closeBroker()
	out.Reset()
	if err := runDesktopBridge(bytes.NewReader(raw), &out); err == nil || out.Len() != 0 {
		t.Fatal("closed bridge returned data")
	}
}

func TestDesktopBridgeBoundsAndSafePath(t *testing.T) {
	t.Setenv("PLANTASK_UID", "../../workspace/repo/secret")
	path, err := computeruse.SocketPath(os.Getenv("PLANTASK_UID"))
	if err != nil || !strings.HasPrefix(path, "/tmp/gratefulagents-desktop-") || strings.Contains(path, "secret") || strings.Contains(path, "..") {
		t.Fatalf("unsafe path: %q %v", path, err)
	}
	var out bytes.Buffer
	if runDesktopBridge(strings.NewReader(strings.Repeat("x", computeruse.MaxWire+1)), &out) == nil || out.Len() != 0 {
		t.Fatal("accepted oversized input")
	}
	if runDesktopBridge(strings.NewReader(`{"owner":"secret","text":"secret"}`), &out) == nil || out.Len() != 0 {
		t.Fatal("accepted unknown fields")
	}
	t.Setenv("PLANTASK_UID", "")
	if _, err := computeruse.SocketPath(""); err == nil {
		t.Fatal("accepted missing run UID")
	}
}
