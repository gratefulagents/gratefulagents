package tools

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/sdk/pkg/agentsdk/tools/signal"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FinishSummaryHolder captures the summary the agent passes to the finish tool
// so the run loop can surface it as the assistant's closing chat message.
type FinishSummaryHolder struct {
	mu      sync.Mutex
	summary string
}

func (h *FinishSummaryHolder) set(summary string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.summary = summary
}

// Summary returns the most recent finish summary, or "" if finish hasn't been
// called.
func (h *FinishSummaryHolder) Summary() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.summary
}

// RegisterFinishTool registers the SDK finish tool with an AgentRun status sink.
// It returns a holder that captures the finish summary for display.
func RegisterFinishTool(registry *Registry, k8sClient client.Client, taskName, namespace string) *FinishSummaryHolder {
	holder := &FinishSummaryHolder{}
	if registry == nil || k8sClient == nil {
		return holder
	}
	registry.Register(&signal.FinishTool{Sink: agentRunFinishSink{
		k8sClient: k8sClient,
		taskName:  taskName,
		namespace: namespace,
		holder:    holder,
	}})
	return holder
}

type agentRunFinishSink struct {
	k8sClient client.Client
	taskName  string
	namespace string
	holder    *FinishSummaryHolder
}

func (s agentRunFinishSink) Finish(ctx context.Context, summary string) error {
	if s.holder != nil {
		s.holder.set(strings.TrimSpace(summary))
	}

	key := types.NamespacedName{Name: s.taskName, Namespace: s.namespace}

	var run platformv1alpha1.AgentRun
	if err := s.k8sClient.Get(ctx, key, &run); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("AgentRun not found")
		}
		return fmt.Errorf("get AgentRun: %w", err)
	}

	patch := client.MergeFrom(run.DeepCopy())
	run.Status.CompletionRequested = true
	if err := s.k8sClient.Status().Patch(ctx, &run, patch); err != nil {
		return err
	}

	log.Printf("finish: run marked as completed")
	return nil
}
