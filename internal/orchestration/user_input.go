package orchestration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

type PendingUserAction struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Mode  string `json:"mode,omitempty"`
	Style string `json:"style,omitempty"`
}

type PendingUserInput struct {
	ID      string              `json:"id"`
	Type    string              `json:"type"`
	Message string              `json:"message"`
	Actions []PendingUserAction `json:"actions,omitempty"`
}

func PendingUserInputForSession(session *store.Session) *PendingUserInput {
	if session == nil {
		return nil
	}
	inputType := strings.TrimSpace(session.PendingInputType)
	requestID := strings.TrimSpace(session.PendingRequestID)
	if inputType == "" || requestID == "" {
		return nil
	}
	normalized := normalizePendingActions(session.PendingActions)
	request := &PendingUserInput{ID: requestID, Type: inputType, Message: strings.TrimSpace(session.PendingQuestion)}
	_ = json.Unmarshal(normalized, &request.Actions)
	return request
}

func BindPendingUserInputContext(request *PendingUserInput, contextID string) *PendingUserInput {
	if request == nil {
		return nil
	}
	out := *request
	out.Actions = append([]PendingUserAction(nil), request.Actions...)
	contextID = strings.TrimSpace(contextID)
	if contextID == "" {
		return &out
	}
	sum := sha256.Sum256([]byte(request.ID + "\x00" + contextID))
	out.ID = hex.EncodeToString(sum[:])
	return &out
}

func FindPendingUserAction(request *PendingUserInput, actionID string) *PendingUserAction {
	if request == nil {
		return nil
	}
	actionID = strings.TrimSpace(actionID)
	for i := range request.Actions {
		if request.Actions[i].ID == actionID {
			return &request.Actions[i]
		}
	}
	return nil
}

func normalizePendingActions(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var actions []PendingUserAction
	if err := json.Unmarshal(raw, &actions); err != nil || len(actions) == 0 {
		return nil
	}
	for i := range actions {
		actions[i].ID = strings.TrimSpace(actions[i].ID)
		actions[i].Label = strings.TrimSpace(actions[i].Label)
		actions[i].Mode = strings.TrimSpace(actions[i].Mode)
		actions[i].Style = strings.TrimSpace(actions[i].Style)
	}
	normalized, _ := json.Marshal(actions)
	return normalized
}
