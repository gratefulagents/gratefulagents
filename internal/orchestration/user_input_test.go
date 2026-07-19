package orchestration

import (
	"encoding/json"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"testing"
)

func TestPendingUserInputForSession(t *testing.T) {
	s := &store.Session{PendingRequestID: "request-1", PendingInputType: "choice", PendingQuestion: "Choose", PendingActions: json.RawMessage(`[{"id":"go","label":"Go","mode":"auto"}]`)}
	r := PendingUserInputForSession(s)
	if r == nil || r.ID != "request-1" || len(r.Actions) != 1 || r.Actions[0].ID != "go" {
		t.Fatalf("request = %#v", r)
	}
	s.PendingRequestID = "request-2"
	if PendingUserInputForSession(s).ID == r.ID {
		t.Fatal("replacement reused identity")
	}
}

func TestBindPendingUserInputContext(t *testing.T) {
	r := &PendingUserInput{ID: "request-1"}
	if BindPendingUserInputContext(r, "mcp-1").ID == BindPendingUserInputContext(r, "mcp-2").ID {
		t.Fatal("contexts share identity")
	}
}
