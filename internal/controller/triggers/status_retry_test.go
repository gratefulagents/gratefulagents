package triggers

import (
	"context"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type conflictOnceStatusClient struct {
	client.Client
	statusConflictCount int
}

func (c *conflictOnceStatusClient) Status() client.SubResourceWriter {
	return &conflictOnceStatusWriter{
		SubResourceWriter: c.Client.Status(),
		parent:            c,
	}
}

type conflictOnceStatusWriter struct {
	client.SubResourceWriter
	parent *conflictOnceStatusClient
}

func (w *conflictOnceStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if w.parent.statusConflictCount == 0 {
		w.parent.statusConflictCount++
		return apierrors.NewConflict(
			schema.GroupResource{Group: triggersv1alpha1.GroupVersion.Group, Resource: "status"},
			obj.GetName(),
			context.DeadlineExceeded,
		)
	}
	return w.SubResourceWriter.Update(ctx, obj, opts...)
}

func TestRetryLinearProjectStatusUpdateRetriesConflict(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add triggers scheme: %v", err)
	}

	lp := &triggersv1alpha1.LinearProject{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "default"},
	}
	baseClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.LinearProject{}).
		WithObjects(lp).
		Build()

	err := retryLinearProjectStatusUpdate(context.Background(), &conflictOnceStatusClient{Client: baseClient}, client.ObjectKeyFromObject(lp), func(fresh *triggersv1alpha1.LinearProject) {
		fresh.Status.LastError = "boom"
		fresh.Status.IssuesProcessed = 3
	})
	if err != nil {
		t.Fatalf("retryLinearProjectStatusUpdate() error = %v", err)
	}

	updated := &triggersv1alpha1.LinearProject{}
	if err := baseClient.Get(context.Background(), client.ObjectKeyFromObject(lp), updated); err != nil {
		t.Fatalf("get updated linear project: %v", err)
	}
	if updated.Status.LastError != "boom" {
		t.Fatalf("LastError = %q, want boom", updated.Status.LastError)
	}
	if updated.Status.IssuesProcessed != 3 {
		t.Fatalf("IssuesProcessed = %d, want 3", updated.Status.IssuesProcessed)
	}
}
