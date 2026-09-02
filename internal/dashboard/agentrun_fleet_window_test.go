package dashboard

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func fleetRun(name string, created time.Time, phase platformv1alpha1.AgentRunPhase) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: metav1.NewTime(created)},
		Spec:       platformv1alpha1.AgentRunSpec{Trigger: platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "chat"}},
		Status:     platformv1alpha1.AgentRunStatus{Phase: phase},
	}
}

func fleetNames(runs []*platformv1alpha1.AgentRun) []string {
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.Name)
	}
	return out
}

func TestSelectAgentRunFleetWindowPagesTerminalRunsAndKeepsActiveOnes(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Oldest to newest: t0..t5; a2 (old) and a5 (new) are active.
	candidates := []*platformv1alpha1.AgentRun{
		fleetRun("t0", base, platformv1alpha1.AgentRunPhaseSucceeded),
		fleetRun("t1", base.Add(1*time.Minute), platformv1alpha1.AgentRunPhaseFailed),
		fleetRun("a2", base.Add(2*time.Minute), platformv1alpha1.AgentRunPhaseRunning),
		fleetRun("t3", base.Add(3*time.Minute), platformv1alpha1.AgentRunPhaseCancelled),
		fleetRun("t4", base.Add(4*time.Minute), platformv1alpha1.AgentRunPhaseSucceeded),
		fleetRun("a5", base.Add(5*time.Minute), platformv1alpha1.AgentRunPhasePending),
	}

	legacy, err := selectAgentRunFleetWindow(candidates, 0, "")
	if err != nil {
		t.Fatalf("legacy window: %v", err)
	}
	if got := fleetNames(legacy.runs); len(got) != 6 || got[0] != "a5" || got[5] != "t0" || legacy.nextPageToken != "" || legacy.total != 6 {
		t.Fatalf("legacy window = %v token=%q total=%d, want all six newest first", got, legacy.nextPageToken, legacy.total)
	}

	first, err := selectAgentRunFleetWindow(candidates, 2, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if got := fleetNames(first.runs); len(got) != 4 || got[0] != "a5" || got[1] != "a2" || got[2] != "t4" || got[3] != "t3" {
		t.Fatalf("first page = %v, want actives then two newest terminal", got)
	}
	if first.nextPageToken == "" || first.total != 6 {
		t.Fatalf("first page token=%q total=%d, want cursor and 6", first.nextPageToken, first.total)
	}

	second, err := selectAgentRunFleetWindow(candidates, 2, first.nextPageToken)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if got := fleetNames(second.runs); len(got) != 2 || got[0] != "t1" || got[1] != "t0" {
		t.Fatalf("second page = %v, want t1, t0 (no actives repeated)", got)
	}
	if second.nextPageToken != "" {
		t.Fatalf("second page token = %q, want exhausted", second.nextPageToken)
	}

	if _, err := selectAgentRunFleetWindow(candidates, 2, "not-a-token"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad token error = %v, want InvalidArgument", err)
	}
	if _, err := selectAgentRunFleetWindow(candidates, 0, first.nextPageToken); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("token without limit error = %v, want InvalidArgument", err)
	}
}

func TestSelectAgentRunFleetWindowSameSecondOrderingIsStable(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	candidates := make([]*platformv1alpha1.AgentRun, 0, 4)
	for _, n := range []string{"b", "d", "a", "c"} {
		candidates = append(candidates, fleetRun(n, ts, platformv1alpha1.AgentRunPhaseSucceeded))
	}
	var got []string
	token := ""
	for range 4 {
		w, err := selectAgentRunFleetWindow(candidates, 1, token)
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		got = append(got, fleetNames(w.runs)...)
		token = w.nextPageToken
		if token == "" {
			break
		}
	}
	if len(got) != 4 || got[0] != "d" || got[1] != "c" || got[2] != "b" || got[3] != "a" {
		t.Fatalf("paged names = %v, want each run exactly once in stable order", got)
	}
}

func TestAgentRunSourceFilterMatchesTriggerOrProject(t *testing.T) {
	run := fleetRun("r", time.Now(), platformv1alpha1.AgentRunPhaseRunning)
	run.Spec.Context = &platformv1alpha1.AgentRunContext{ProjectRef: &platformv1alpha1.ProjectRef{Kind: "Project", Name: "payments"}}
	cases := []struct {
		kind, name string
		want       bool
	}{
		{"", "", true},
		{"ProjectChat", "chat", true},
		{"Project", "payments", true},
		{"Project", "", true},
		{"", "payments", true},
		{"Cron", "chat", false},
		{"Project", "billing", false},
	}
	for _, c := range cases {
		if got := newAgentRunSourceFilter(c.kind, c.name).matches(run); got != c.want {
			t.Errorf("filter(%q,%q) = %v, want %v", c.kind, c.name, got, c.want)
		}
	}
}

func TestListAgentRunsPaginatesAndFiltersBySource(t *testing.T) {
	scheme := newDashboardTestScheme(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cronRun := fleetRun("cron-1", base.Add(4*time.Minute), platformv1alpha1.AgentRunPhaseSucceeded)
	cronRun.Spec.Trigger = platformv1alpha1.TriggerRef{Kind: "Cron", Name: "nightly"}
	builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		fleetRun("t0", base, platformv1alpha1.AgentRunPhaseSucceeded),
		fleetRun("t1", base.Add(time.Minute), platformv1alpha1.AgentRunPhaseSucceeded),
		fleetRun("t2", base.Add(2*time.Minute), platformv1alpha1.AgentRunPhaseSucceeded),
		fleetRun("a3", base.Add(3*time.Minute), platformv1alpha1.AgentRunPhaseRunning),
		cronRun,
	)
	srv := &Server{k8sClient: builder.Build(), scheme: scheme}

	first, err := srv.ListAgentRuns(context.Background(), &platform.ListAgentRunsRequest{Namespace: "default", Limit: 1, SourceKind: "ProjectChat"})
	if err != nil {
		t.Fatalf("ListAgentRuns page 1: %v", err)
	}
	if len(first.Runs) != 2 || first.Runs[0].Name != "a3" || first.Runs[1].Name != "t2" || first.TotalCount != 4 || first.NextPageToken == "" {
		t.Fatalf("page 1 = %v total=%d token=%q, want [a3 t2], total 4, cursor", runNames(first.Runs), first.TotalCount, first.NextPageToken)
	}
	second, err := srv.ListAgentRuns(context.Background(), &platform.ListAgentRunsRequest{Namespace: "default", Limit: 1, SourceKind: "ProjectChat", PageToken: first.NextPageToken})
	if err != nil {
		t.Fatalf("ListAgentRuns page 2: %v", err)
	}
	if len(second.Runs) != 1 || second.Runs[0].Name != "t1" || second.NextPageToken == "" {
		t.Fatalf("page 2 = %v token=%q, want [t1] with cursor", runNames(second.Runs), second.NextPageToken)
	}
	third, err := srv.ListAgentRuns(context.Background(), &platform.ListAgentRunsRequest{Namespace: "default", Limit: 1, SourceKind: "ProjectChat", PageToken: second.NextPageToken})
	if err != nil {
		t.Fatalf("ListAgentRuns page 3: %v", err)
	}
	if len(third.Runs) != 1 || third.Runs[0].Name != "t0" || third.NextPageToken != "" {
		t.Fatalf("page 3 = %v token=%q, want [t0] exhausted", runNames(third.Runs), third.NextPageToken)
	}

	all, err := srv.ListAgentRuns(context.Background(), &platform.ListAgentRunsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListAgentRuns legacy: %v", err)
	}
	if len(all.Runs) != 5 || all.TotalCount != 5 || all.NextPageToken != "" {
		t.Fatalf("legacy list = %v total=%d token=%q, want all five", runNames(all.Runs), all.TotalCount, all.NextPageToken)
	}
}

func runNames(runs []*platform.AgentRun) []string {
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.GetName())
	}
	return out
}

func TestWatchAgentRunsLimitKeepsActiveRunsAndReportsDeletions(t *testing.T) {
	scheme := newDashboardTestScheme(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	oldActive := fleetRun("a0", base, platformv1alpha1.AgentRunPhaseRunning)
	oldTerminal := fleetRun("t1", base.Add(time.Minute), platformv1alpha1.AgentRunPhaseSucceeded)
	newTerminal := fleetRun("t2", base.Add(2*time.Minute), platformv1alpha1.AgentRunPhaseSucceeded)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(oldActive, oldTerminal, newTerminal).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := &recordingAgentRunEventConn{ch: make(chan *platform.AgentRunEvent, 16)}
	done := make(chan error, 1)
	go func() {
		done <- srv.WatchAgentRuns(ctx, &platform.WatchAgentRunsRequest{Namespace: "default", Limit: 1}, newAgentRunEventServerStream(conn))
	}()
	waitEvent := func() *platform.AgentRunEvent {
		select {
		case ev := <-conn.ch:
			return ev
		case <-time.After(6 * time.Second):
			t.Fatal("timed out waiting for fleet event")
			return nil
		}
	}
	got := map[string]string{}
	for range 2 {
		ev := waitEvent()
		got[ev.GetRun().GetName()] = ev.GetType()
	}
	if got["a0"] != "MODIFIED" || got["t2"] != "MODIFIED" {
		t.Fatalf("initial window = %v, want the old active run and the newest terminal run", got)
	}
	select {
	case ev := <-conn.ch:
		t.Fatalf("unexpected extra event %v: t1 is outside the window", ev)
	case <-time.After(100 * time.Millisecond):
	}

	// Deleting the in-window terminal run promotes t1 into the window and
	// reports the deletion.
	if err := c.Delete(ctx, newTerminal); err != nil {
		t.Fatalf("delete t2: %v", err)
	}
	after := map[string]string{}
	for range 2 {
		ev := waitEvent()
		after[ev.GetRun().GetName()] = ev.GetType()
	}
	if after["t2"] != "DELETED" || after["t1"] != "MODIFIED" {
		t.Fatalf("events after delete = %v, want t2 DELETED and t1 MODIFIED", after)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchAgentRuns did not return after cancel")
	}
}
