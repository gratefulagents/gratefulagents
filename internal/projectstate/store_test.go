package projectstate

import (
	"strings"
	"testing"

	sdkprojectstate "github.com/gratefulagents/sdk/pkg/agentsdk/projectstate"
)

func TestNewStoreValidation(t *testing.T) {
	if _, err := NewStore(Options{}); err == nil {
		t.Fatal("NewStore() with no pool should error")
	}
}

func TestSanitizeProjectID(t *testing.T) {
	tests := []struct {
		in     string
		expect string
	}{
		{"team-a-https://github.com/acme/widgets.git", "team-a-https-github-com-acme-widgets-git"},
		{"  Mixed CASE  ", "mixed-case"},
		{"___", ""},
	}
	for _, tt := range tests {
		if got := SanitizeProjectID(tt.in); got != tt.expect {
			t.Errorf("SanitizeProjectID(%q) = %q, want %q", tt.in, got, tt.expect)
		}
	}
}

func TestProjectIDCanonicalEquivalenceAndCollisionResistance(t *testing.T) {
	canonical := ProjectID("Team-A", "https://github.com/Acme/Widgets.git")
	for _, repository := range []string{
		"https://github.com/acme/widgets.git/",
		"git@github.com:acme/widgets.git",
		"ssh://git@github.com/acme/widgets/",
	} {
		if got := ProjectID("team-a", repository); got != canonical {
			t.Errorf("ProjectID(%q) = %q, want %q", repository, got, canonical)
		}
	}
	if got := ProjectID("team-a", ""); got != "team-a-chat" {
		t.Errorf("repoless ProjectID = %q, want team-a-chat", got)
	}
	if first, second := ProjectID("team-a", "example.com/a/b"), ProjectID("team-a", "example.com/a-b"); first == second {
		t.Fatalf("sanitization collision produced identical ID %q", first)
	}
}

func TestFullTextQueryBuildsSanitizedORQuery(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"How does compaction threshold work?", "compaction | threshold | work"},
		{"memory_save dedupe", "memory | save | dedupe"},
		{"a the of", ""},
		{"it's (x & y) | !z:*", ""},
		{"Postgres pgvector!", "postgres | pgvector"},
	}
	for _, tt := range tests {
		got := fullTextQuery(tt.in)
		if got != tt.want {
			t.Errorf("fullTextQuery(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if strings.ContainsAny(fullTextQuery(tt.in), "&!:*()'") {
			t.Errorf("fullTextQuery(%q) leaked tsquery syntax: %q", tt.in, fullTextQuery(tt.in))
		}
	}
}

func TestNormalizedKindsMapsLegacyAndDedupes(t *testing.T) {
	got := normalizedKinds([]string{"Pinned", "decision", " ", "semantic", "fact", "procedural"})
	want := []string{sdkprojectstate.MemoryKindDecision, sdkprojectstate.MemoryKindFact, sdkprojectstate.MemoryKindProcedure}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("normalizedKinds() = %v, want %v", got, want)
	}
	if got := normalizedKinds(nil); got == nil || len(got) != 0 {
		t.Fatalf("normalizedKinds(nil) = %#v, want empty non-nil slice for text[] binding", got)
	}
}

func TestCitationsRoundTrip(t *testing.T) {
	raw, err := marshalCitations(nil)
	if err != nil || string(raw) != "[]" {
		t.Fatalf("marshalCitations(nil) = %q, %v; want []", raw, err)
	}
	in := []sdkprojectstate.Citation{{Path: "cmd/agent/loop.go"}, {URL: "https://example.test/pr/1"}}
	raw, err = marshalCitations(in)
	if err != nil {
		t.Fatal(err)
	}
	var mem sdkprojectstate.Memory
	if err := unmarshalCitations(raw, &mem); err != nil {
		t.Fatal(err)
	}
	if len(mem.Citations) != 2 || mem.Citations[0].Path != "cmd/agent/loop.go" || mem.Citations[1].URL == "" {
		t.Fatalf("round trip = %#v", mem.Citations)
	}
	mem = sdkprojectstate.Memory{}
	if err := unmarshalCitations([]byte("[]"), &mem); err != nil || mem.Citations != nil {
		t.Fatalf("empty citations = %#v, %v; want nil", mem.Citations, err)
	}
}

func TestNewIDShape(t *testing.T) {
	id := newID("task")
	if !strings.HasPrefix(id, "task_") || len(id) != len("task_")+12 {
		t.Fatalf("newID() = %q, want task_ prefix with 12 hex chars", id)
	}
}
