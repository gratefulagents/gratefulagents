package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/gratefulagents/sdk/pkg/agentsdk"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// maxDisplayNameLen bounds the title written to status.displayName so a runaway
// model output cannot bloat the CRD or the UI.
const maxDisplayNameLen = 120

// RegisterSetDisplayNameTool registers the set_display_name tool, which lets the
// agent give its run a short human-readable title. The title is stored on
// status.displayName and shown in the dashboard instead of the generated
// resource name.
func RegisterSetDisplayNameTool(registry *Registry, k8sClient client.Client, taskName, namespace string) {
	if registry == nil || k8sClient == nil {
		return
	}
	registry.Register(&setDisplayNameTool{
		k8sClient: k8sClient,
		taskName:  taskName,
		namespace: namespace,
	})
}

type setDisplayNameTool struct {
	k8sClient client.Client
	taskName  string
	namespace string
}

type setDisplayNameInput struct {
	DisplayName string `json:"display_name"`
}

func (t *setDisplayNameTool) Name() string { return "set_display_name" }

func (t *setDisplayNameTool) Description() string {
	return "Set a short, human-readable title for this run so the user can recognize " +
		"it later instead of seeing a random id. Call this once, early — as soon as " +
		"you understand what the user wants — with a concise title (a few words, " +
		"like a chat title). This tool can only set the title if one does not " +
		"already exist."
}

func (t *setDisplayNameTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"display_name": {
				"type": "string",
				"description": "A concise human-readable title for this run (a few words, no trailing punctuation)"
			}
		},
		"required": ["display_name"]
	}`)
}

func (t *setDisplayNameTool) IsReadOnly() bool                      { return false }
func (t *setDisplayNameTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *setDisplayNameTool) NeedsApproval() bool                   { return false }
func (t *setDisplayNameTool) TimeoutSeconds() int                   { return 0 }

func (t *setDisplayNameTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in setDisplayNameInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	name := strings.TrimSpace(in.DisplayName)
	if name == "" {
		return Result{Content: "display_name is required", IsError: true}, nil
	}
	if len(name) > maxDisplayNameLen {
		name = strings.TrimSpace(name[:maxDisplayNameLen])
	}

	key := types.NamespacedName{Name: t.taskName, Namespace: t.namespace}
	var run platformv1alpha1.AgentRun
	if err := t.k8sClient.Get(ctx, key, &run); err != nil {
		if apierrors.IsNotFound(err) {
			return Result{Content: "AgentRun not found", IsError: true}, nil
		}
		return Result{Content: fmt.Sprintf("failed to get AgentRun: %v", err), IsError: true}, nil
	}

	if existing := strings.TrimSpace(run.Status.DisplayName); existing != "" {
		// Idempotent no-op: resumed sessions cannot know a title was already
		// set, and erroring here just burns a retry turn. Keep the first title.
		return Result{Content: fmt.Sprintf("Display name already set to %q; keeping it (no action needed).", existing)}, nil
	}

	patch := client.MergeFrom(run.DeepCopy())
	run.Status.DisplayName = name
	if err := t.k8sClient.Status().Patch(ctx, &run, patch); err != nil {
		return Result{Content: fmt.Sprintf("failed to set display name: %v", err), IsError: true}, nil
	}

	log.Printf("set_display_name: run titled %q", name)
	return Result{Content: fmt.Sprintf("Display name set to %q.", name)}, nil
}
