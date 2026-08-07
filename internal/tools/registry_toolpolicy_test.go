package tools

import (
	"reflect"
	"testing"
)

func TestRegistry_ToolNameFilterDeniesTools(t *testing.T) {
	r := NewRegistry("/tmp/test", WithToolNameFilter(nil, []string{"Bash", "WebFetch"}))
	for _, name := range []string{"Bash", "WebFetch"} {
		if r.Get(name) != nil {
			t.Errorf("denied tool %q remained registered", name)
		}
	}
	if r.Get("read_file") == nil {
		t.Error("non-denied tool read_file was dropped")
	}
}

func TestRegistry_ToolNameFilterAllowListNarrows(t *testing.T) {
	r := NewRegistry("/tmp/test", WithToolNameFilter([]string{"read_file", "grep"}, nil))
	if got, want := r.Names(), []string{"grep", "read_file"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestRegistry_ToolNameFilterDenyWinsOverAllow(t *testing.T) {
	r := NewRegistry("/tmp/test", WithToolNameFilter([]string{"Bash", "read_file"}, []string{"Bash"}))
	if r.Get("Bash") != nil {
		t.Error("tool on both lists must be denied (deny wins)")
	}
	if r.Get("read_file") == nil {
		t.Error("allowed, non-denied tool read_file was dropped")
	}
}

// The filter is narrow-only: an allow list entry must never restore a tool
// that mode/profile permission policy already removed.
func TestRegistry_ToolNameFilterNeverWidensPermissionPolicy(t *testing.T) {
	r := NewRegistry("/tmp/test", WithReadOnlyTools(), WithToolNameFilter([]string{"NonReadOnlyTestTool", "read_file"}, nil))
	r.Register(&nonReadOnlyTestTool{})
	if r.Get("NonReadOnlyTestTool") != nil {
		t.Fatal("tool policy allow list re-enabled a tool the read-only clamp removed")
	}
	if r.Get("read_file") == nil {
		t.Fatal("read-only tool on the allow list was dropped")
	}
}

func TestRegistry_ToolNameFilterExemptsControlFlowTools(t *testing.T) {
	exempt := []string{"finish", "save_plan", "get_plan", "RequestMCPBreakGlass"}
	r := NewRegistry("/tmp/test", WithToolNameFilter([]string{"read_file"}, exempt))
	for _, name := range exempt {
		r.Register(&controlFlowTestTool{name: name})
		if r.Get(name) == nil {
			t.Errorf("control-flow tool %q must be exempt from the tool policy filter", name)
		}
	}
}

func TestSplitToolNameList(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{" , ,", nil},
		{"Bash", []string{"Bash"}},
		{"b, a , a,", []string{"a", "b"}},
	}
	for _, tc := range cases {
		if got := SplitToolNameList(tc.raw); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("SplitToolNameList(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
