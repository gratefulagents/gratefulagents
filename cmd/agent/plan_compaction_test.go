package main

import (
	"testing"

	sdkopenai "github.com/gratefulagents/sdk/pkg/agentsdk/providers/openai"
)

func TestCompactionDefaultsFromModelMetadataUsesCodexLimit(t *testing.T) {
	trigger, target, ok := sdkopenai.CompactionDefaultsFromModelMetadata(sdkopenai.ModelMetadata{
		ID:               "gpt-5.5",
		ContextWindow:    272000,
		MaxContextWindow: 272000,
	})
	if !ok {
		t.Fatal("expected metadata to produce compaction defaults")
	}
	if trigger != 244800 {
		t.Fatalf("trigger = %d, want 244800", trigger)
	}
	if target != 136000 {
		t.Fatalf("target = %d, want 136000", target)
	}
}

func TestCompactionDefaultsFromModelMetadataClampsConfiguredLimit(t *testing.T) {
	trigger, target, ok := sdkopenai.CompactionDefaultsFromModelMetadata(sdkopenai.ModelMetadata{
		ID:                    "gpt-test",
		ContextWindow:         1000,
		AutoCompactTokenLimit: 950,
	})
	if !ok {
		t.Fatal("expected metadata to produce compaction defaults")
	}
	if trigger != 900 {
		t.Fatalf("trigger = %d, want 900", trigger)
	}
	if target != 500 {
		t.Fatalf("target = %d, want 500", target)
	}
}
