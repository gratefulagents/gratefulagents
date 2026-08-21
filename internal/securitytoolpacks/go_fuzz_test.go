package securitytoolpacks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func goFuzzTestConfig(value string) RunConfig {
	return RunConfig{
		Tool: "go-fuzz-tests",
		Target: Target{
			Type:      "go_fuzz_project",
			Locator:   "/workspace/project",
			Revision:  "fixture-v1",
			Digest:    sha256Digest([]byte("go-fuzz")),
			MediaType: "application/vnd.gratefulagents.go-fuzz-project.v1+directory",
		},
		Arguments: map[string]string{"package": "./parser", "fuzz": "^FuzzDecode$", "fuzztime": value},
	}
}

func TestGoFuzzCampaignValidatesFuzztimeBounds(t *testing.T) {
	registry, err := NewRegistry(DefaultManifest(sha256Digest([]byte("wrapper")), nil))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		value string
		valid bool
	}{
		{"29.999s", false},
		{"30s", true},
		{"15m", true},
		{"15m0.001s", false},
		{"soon", false},
		{"-1m", false},
	}
	for _, test := range cases {
		t.Run(test.value, func(t *testing.T) {
			invocation, _, err := registry.BuildInvocation(goFuzzTestConfig(test.value))
			if test.valid && err != nil {
				t.Fatalf("BuildInvocation() error = %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("BuildInvocation() accepted an out-of-range campaign")
			}
			if test.valid && !strings.Contains(strings.Join(invocation.Argv, " "), "-fuzztime="+test.value) {
				t.Fatalf("argv does not carry fuzztime: %q", invocation.Argv)
			}
		})
	}
}

func TestGoFuzzCampaignBudgetIncludesHeadroom(t *testing.T) {
	registry, err := NewRegistry(DefaultManifest(sha256Digest([]byte("wrapper")), nil))
	if err != nil {
		t.Fatal(err)
	}
	invocation, _, err := registry.BuildInvocation(goFuzzTestConfig("15m"))
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Budgets.Timeout < 15*time.Minute+fuzzCampaignOverhead {
		t.Fatalf("timeout = %s, want at least campaign plus %s headroom", invocation.Budgets.Timeout, fuzzCampaignOverhead)
	}
}

func TestGoFuzzBoundedScopeRecordsCampaignProvenance(t *testing.T) {
	cases := []struct {
		name      string
		inputs    int
		restored  int
		outputs   int
		newInputs int
		want      []string
	}{
		{
			name: "cold campaign with checked-in seeds", inputs: 2, restored: 0, outputs: 3, newInputs: 1,
			want: []string{"inputs in=2", "committed=2", "restored=0", "inputs out=3", "new inputs=1", "provenance=cold"},
		},
		{
			name: "warm campaign", inputs: 3, restored: 2, outputs: 4, newInputs: 1,
			want: []string{"inputs in=3", "committed=1", "restored=2", "inputs out=4", "new inputs=1", "provenance=restored"},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			bounded := goFuzzBoundedScope("./parser", "FuzzDecode", 2*time.Minute, 73*time.Second, test.inputs, test.restored, test.outputs, test.newInputs)
			for _, want := range test.want {
				if !strings.Contains(bounded.Corpus, want) {
					t.Fatalf("corpus = %q, missing %q", bounded.Corpus, want)
				}
			}
			if bounded.Bounds != "fuzztime=2m0s, wall_time=1m13s" {
				t.Fatalf("bounds = %q", bounded.Bounds)
			}
		})
	}
}

func TestGoFuzzCampaignMetadataRoundTrip(t *testing.T) {
	root := t.TempDir()
	encoded, err := EncodeGoFuzzCampaignMetadata(3)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(GoFuzzCampaignMetadataPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readGoFuzzCampaignMetadata(root)
	if err != nil || got != 3 {
		t.Fatalf("read metadata = %d, %v", got, err)
	}
}
