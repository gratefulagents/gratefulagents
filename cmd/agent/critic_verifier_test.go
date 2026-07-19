package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

type retryingCriticModel struct {
	mu         sync.Mutex
	calls      int
	blockFirst bool
	responses  []*agent.ModelResponse
}

func (m *retryingCriticModel) GetResponse(ctx context.Context, _ agent.ModelRequest) (*agent.ModelResponse, error) {
	m.mu.Lock()
	idx := m.calls
	m.calls++
	block := m.blockFirst && idx == 0
	var resp *agent.ModelResponse
	if idx < len(m.responses) {
		resp = m.responses[idx]
	}
	m.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if resp != nil {
		return resp, nil
	}
	return nil, errors.New("no response configured")
}

func (m *retryingCriticModel) StreamResponse(ctx context.Context, req agent.ModelRequest) (*agent.ModelStream, error) {
	resp, err := m.GetResponse(ctx, req)
	if err != nil {
		return nil, err
	}
	events := make(chan agent.ModelStreamEvent, len(resp.Items)+1)
	done := make(chan *agent.ModelResponse, 1)
	for i := range resp.Items {
		item := resp.Items[i]
		events <- agent.ModelStreamEvent{Type: agent.ModelStreamItemDone, Item: &item}
	}
	events <- agent.ModelStreamEvent{Type: agent.ModelStreamComplete, Response: resp}
	done <- resp
	close(events)
	return agent.NewModelStream(events, done), nil
}

func (*retryingCriticModel) GetRetryAdvice(error) *agent.ModelRetryAdvice { return nil }
func (*retryingCriticModel) CalculateCost(agent.Usage) float64            { return 0 }
func (*retryingCriticModel) Provider() string                             { return "test" }

func TestNewCriticVerifierRetriesTransientFailure(t *testing.T) {
	model := &retryingCriticModel{
		blockFirst: true,
		responses: []*agent.ModelResponse{
			nil,
			{Items: []agent.RunItem{{Type: agent.RunItemMessage, Message: &agent.MessageOutput{Text: "VERDICT: APPROVED"}}}},
		},
	}
	policy := &agent.RetryPolicy{
		MaxRetries: 1,
		Backoff: agent.RetryBackoffSettings{
			InitialDelayMS: 1,
			MaxDelayMS:     1,
			Multiplier:     1,
		},
	}
	verify := newCriticVerifier(
		agent.NewRunnerWithModel(model),
		&agent.Agent{Name: "critic"},
		"finish the task",
		policy,
		50*time.Millisecond,
	)

	feedback, err := verify(context.Background(), "done")
	if err != nil {
		t.Fatal(err)
	}
	if feedback != "" {
		t.Fatalf("feedback = %q, want approval", feedback)
	}
	model.mu.Lock()
	calls := model.calls
	model.mu.Unlock()
	if calls != 2 {
		t.Fatalf("model calls = %d, want failed call plus retry", calls)
	}
}

func TestNewCriticVerifierReturnsRejection(t *testing.T) {
	model := &retryingCriticModel{responses: []*agent.ModelResponse{{
		Items: []agent.RunItem{{Type: agent.RunItemMessage, Message: &agent.MessageOutput{Text: "VERDICT: REJECTED\n1. missing test"}}},
	}}}
	verify := newCriticVerifier(agent.NewRunnerWithModel(model), &agent.Agent{Name: "critic"}, "finish", nil, 0)
	feedback, err := verify(context.Background(), "done")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(feedback, "missing test") {
		t.Fatalf("feedback = %q, want rejection detail", feedback)
	}
}
