package main

import (
	"context"
	"testing"
)

func TestReserveSDKDurablePassResumesAndAdvances(t *testing.T) {
	sc, _ := newSubAgentCheckpointTestClient(t)
	ctx := context.Background()

	first, err := reserveSDKDurablePass(ctx, sc, 101)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := reserveSDKDurablePass(ctx, sc, 101)
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 || resumed != first {
		t.Fatalf("first=%d resumed=%d", first, resumed)
	}
	if err := completeSDKDurablePass(ctx, sc, 101, first); err != nil {
		t.Fatal(err)
	}
	second, err := reserveSDKDurablePass(ctx, sc, 101)
	if err != nil {
		t.Fatal(err)
	}
	if second != 2 {
		t.Fatalf("second=%d, want 2", second)
	}
}

func TestReserveSDKDurablePassNewMessageAbandonsPriorPass(t *testing.T) {
	sc, _ := newSubAgentCheckpointTestClient(t)
	ctx := context.Background()
	if _, err := reserveSDKDurablePass(ctx, sc, 101); err != nil {
		t.Fatal(err)
	}
	pass, err := reserveSDKDurablePass(ctx, sc, 102)
	if err != nil {
		t.Fatal(err)
	}
	if pass != 2 {
		t.Fatalf("pass=%d, want 2", pass)
	}
}

func TestCompleteSDKDurablePassRejectsStaleIdentity(t *testing.T) {
	sc, _ := newSubAgentCheckpointTestClient(t)
	ctx := context.Background()
	pass, err := reserveSDKDurablePass(ctx, sc, 101)
	if err != nil {
		t.Fatal(err)
	}
	if err := completeSDKDurablePass(ctx, sc, 101, pass+1); err == nil {
		t.Fatal("expected stale pass rejection")
	}
}
