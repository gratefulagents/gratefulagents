package dashboard

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// runningDeterministicScan builds a SecurityScan whose last deterministic
// execution is still live, i.e. a cancel request has something to stop.
func runningDeterministicScan(ns, name string) *triggersv1alpha1.SecurityScan {
	return &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/example/app.git"},
		Status: triggersv1alpha1.SecurityScanStatus{
			LastExecution: &triggersv1alpha1.SecurityScanExecutionStatus{
				ID:    "20260101-abc",
				Mode:  triggersv1alpha1.SecurityScanExecutionModeDeterministic,
				Phase: triggersv1alpha1.SecurityScanExecutionPhaseRunning,
			},
		},
	}
}

func TestCancelSecurityScanRunStampsAnnotationToken(t *testing.T) {
	ns := testUserNS()
	srv, c := newCronTestServer(t, runningDeterministicScan(ns, "nightly"))
	ms := newMockStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "nightly", ns, testProjectSubject); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	resp, err := srv.CancelSecurityScanRun(projectActorCtx(),
		&platform.CancelSecurityScanRunRequest{Namespace: ns, Name: "nightly"})
	if err != nil {
		t.Fatalf("CancelSecurityScanRun() error = %v", err)
	}
	if resp.Namespace != ns || resp.Name != "nightly" {
		t.Fatalf("resp = %s/%s, want %s/nightly", resp.Namespace, resp.Name, ns)
	}

	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	if cr.Annotations[triggersv1alpha1.SecurityScanCancelAnnotation] == "" {
		t.Fatalf("cancel annotation not set: %#v", cr.Annotations)
	}
}

func TestCancelSecurityScanRunStopsCoordinatorRun(t *testing.T) {
	ns := testUserNS()
	scan := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns},
		Spec:       triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/example/app.git"},
		Status: triggersv1alpha1.SecurityScanStatus{
			Phase:       "Running",
			LastRunName: "nightly-run-1",
		},
	}
	srv, c := newCronTestServer(t, scan)
	ms := newMockStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "nightly", ns, testProjectSubject); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	if _, err := srv.CancelSecurityScanRun(projectActorCtx(),
		&platform.CancelSecurityScanRunRequest{Namespace: ns, Name: "nightly"}); err != nil {
		t.Fatalf("CancelSecurityScanRun() error = %v", err)
	}
	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	if cr.Annotations[triggersv1alpha1.SecurityScanCancelAnnotation] == "" {
		t.Fatalf("cancel annotation not set: %#v", cr.Annotations)
	}
}

func TestCancelSecurityScanRunRequiresRunInProgress(t *testing.T) {
	ns := testUserNS()
	idleScan := func(mutate func(*triggersv1alpha1.SecurityScan)) *triggersv1alpha1.SecurityScan {
		scan := &triggersv1alpha1.SecurityScan{
			ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns},
			Spec:       triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/example/app.git"},
		}
		if mutate != nil {
			mutate(scan)
		}
		return scan
	}
	cases := []struct {
		name string
		scan *triggersv1alpha1.SecurityScan
	}{
		{name: "no execution and no run", scan: idleScan(nil)},
		{
			name: "succeeded execution",
			scan: failedDeterministicScan(ns, "nightly", triggersv1alpha1.SecurityScanExecutionPhaseSucceeded),
		},
		{
			name: "failed execution",
			scan: failedDeterministicScan(ns, "nightly", triggersv1alpha1.SecurityScanExecutionPhaseFailed),
		},
		{
			name: "already cancelled execution",
			scan: failedDeterministicScan(ns, "nightly", triggersv1alpha1.SecurityScanExecutionPhaseCancelled),
		},
		{
			name: "running phase without a run name",
			scan: idleScan(func(scan *triggersv1alpha1.SecurityScan) {
				scan.Status.Phase = "Running"
			}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, c := newCronTestServer(t, tc.scan)
			ms := newMockStateStore()
			srv.stateStore = ms
			if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "nightly", ns, testProjectSubject); err != nil {
				t.Fatalf("SetResourceOwner: %v", err)
			}
			_, err := srv.CancelSecurityScanRun(projectActorCtx(),
				&platform.CancelSecurityScanRunRequest{Namespace: ns, Name: "nightly"})
			if connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("CancelSecurityScanRun() error = %v, want FailedPrecondition", err)
			}
			cr := &triggersv1alpha1.SecurityScan{}
			if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly"}, cr); err != nil {
				t.Fatalf("Get(SecurityScan) error = %v", err)
			}
			if cr.Annotations[triggersv1alpha1.SecurityScanCancelAnnotation] != "" {
				t.Fatalf("annotation stamped despite precondition failure: %#v", cr.Annotations)
			}
		})
	}
}

func TestCancelSecurityScanRunDeniedForStranger(t *testing.T) {
	srv, c := newCronTestServer(t, runningDeterministicScan("default", "owned"))
	ms := newMockStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "owned", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	_, err := srv.CancelSecurityScanRun(actorContext("mallory", "member", "", ""),
		&platform.CancelSecurityScanRunRequest{Namespace: "default", Name: "owned"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("CancelSecurityScanRun by stranger: want PermissionDenied, got %v", err)
	}
	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "owned"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	if cr.Annotations[triggersv1alpha1.SecurityScanCancelAnnotation] != "" {
		t.Fatalf("annotation stamped despite denial: %#v", cr.Annotations)
	}
}

func TestCancelSecurityScanRunAllowedForSharedCollaborator(t *testing.T) {
	ns := testUserNS()
	srv, c := newCronTestServer(t, runningDeterministicScan(ns, "shared"))
	ms := newCollaborationStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "shared", ns, testProjectSubject); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}
	for subject, permission := range map[string]string{"victor": "viewer", "carl": "collaborator"} {
		if _, err := ms.ShareResource(context.Background(), &store.ResourceShare{
			ResourceType: securityScanResourceType, ResourceID: "shared", ResourceNamespace: ns,
			SharedWithUserID: subject, SharedByUserID: testProjectSubject, Permission: permission,
		}); err != nil {
			t.Fatalf("ShareResource(%s): %v", permission, err)
		}
	}

	req := &platform.CancelSecurityScanRunRequest{Namespace: ns, Name: "shared"}
	// Stopping a run mutates the scan, so a viewer share (read access) is
	// not enough.
	if _, err := srv.CancelSecurityScanRun(actorContext("victor", "member", "", ""), req); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("CancelSecurityScanRun(viewer) error = %v, want PermissionDenied", err)
	}
	if _, err := srv.CancelSecurityScanRun(actorContext("carl", "member", "", ""), req); err != nil {
		t.Fatalf("CancelSecurityScanRun(collaborator) error = %v", err)
	}
	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "shared"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	if cr.Annotations[triggersv1alpha1.SecurityScanCancelAnnotation] == "" {
		t.Fatalf("cancel annotation not set for the shared collaborator: %#v", cr.Annotations)
	}
}

func TestCancelSecurityScanRunAllowedForAdmin(t *testing.T) {
	srv, c := newCronTestServer(t, runningDeterministicScan("default", "owned"))
	ms := newMockStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "owned", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	if _, err := srv.CancelSecurityScanRun(actorContext("root", "admin", "", ""),
		&platform.CancelSecurityScanRunRequest{Namespace: "default", Name: "owned"}); err != nil {
		t.Fatalf("CancelSecurityScanRun by admin error = %v", err)
	}
	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "owned"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	if cr.Annotations[triggersv1alpha1.SecurityScanCancelAnnotation] == "" {
		t.Fatalf("cancel annotation not set for an admin: %#v", cr.Annotations)
	}
}

func TestCancelSecurityScanRunValidatesRequest(t *testing.T) {
	srv, _ := newCronTestServer(t)
	for _, req := range []*platform.CancelSecurityScanRunRequest{
		{Namespace: "", Name: "nightly"},
		{Namespace: "default", Name: ""},
	} {
		if _, err := srv.CancelSecurityScanRun(projectActorCtx(), req); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("CancelSecurityScanRun(%v) error = %v, want InvalidArgument", req, err)
		}
	}
}
