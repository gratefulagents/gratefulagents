package dashboard

import (
	"context"
	"fmt"
	"testing"
	"time"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestConnectionSecretGarbageCollectorSweep(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	old := metav1.NewTime(now.Add(-2 * connectionSecretGCGracePeriod))
	recent := metav1.NewTime(now.Add(-connectionSecretGCGracePeriod / 2))
	managedSecret := func(namespace, name, connection string, created metav1.Time) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace, CreationTimestamp: created,
			Labels: map[string]string{connectionSecretLabel: connection},
		}}
	}

	objects := []client.Object{
		&triggersv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "github", Namespace: "team"},
			Spec: triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeGitHub, GitHub: &triggersv1alpha1.GitHubConnectionConfig{
				TokenSecret: "github-token", PrivateKeySecret: "github-key",
			}},
		},
		&triggersv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "slack", Namespace: "team"},
			Spec:       triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeSlack, Slack: &triggersv1alpha1.SlackConnectionConfig{TokensSecret: "slack-current"}},
		},
		&triggersv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "linear", Namespace: "other"},
			Spec:       triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeLinear, Linear: &triggersv1alpha1.LinearConnectionConfig{APIKeySecret: "linear-current"}},
		},
		managedSecret("team", "github-token", "github", old),
		managedSecret("team", "github-key", "github", old),
		managedSecret("team", "slack-current", "slack", old),
		managedSecret("other", "linear-current", "linear", old),
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: "slack-rotated", Namespace: "team", CreationTimestamp: old,
			Labels:      map[string]string{connectionSecretLabel: "slack"},
			Annotations: map[string]string{connectionSecretOrphanedAt: fmt.Sprintf("%d", now.Add(-2*connectionSecretGCGracePeriod).UnixNano())},
		}},
		managedSecret("team", "old-unmarked-orphan", "slack", old),
		managedSecret("team", "recent-orphan", "slack", recent),
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unmanaged", Namespace: "team", CreationTimestamp: old}},
	}
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	collector := NewConnectionSecretGarbageCollector(k8s, k8s)
	collector.now = func() time.Time { return now }

	deleted, err := collector.sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if err := k8s.Get(context.Background(), client.ObjectKey{Namespace: "team", Name: "slack-rotated"}, &corev1.Secret{}); !k8serrors.IsNotFound(err) {
		t.Fatalf("old orphan still exists or lookup failed unexpectedly: %v", err)
	}
	for _, key := range []client.ObjectKey{
		{Namespace: "team", Name: "github-token"},
		{Namespace: "team", Name: "github-key"},
		{Namespace: "team", Name: "slack-current"},
		{Namespace: "other", Name: "linear-current"},
		{Namespace: "team", Name: "old-unmarked-orphan"},
		{Namespace: "team", Name: "recent-orphan"},
		{Namespace: "team", Name: "unmanaged"},
	} {
		if err := k8s.Get(context.Background(), key, &corev1.Secret{}); err != nil {
			t.Errorf("Secret %s/%s should remain: %v", key.Namespace, key.Name, err)
		}
	}
	marked := &corev1.Secret{}
	if err := k8s.Get(context.Background(), client.ObjectKey{Namespace: "team", Name: "old-unmarked-orphan"}, marked); err != nil {
		t.Fatal(err)
	}
	if marked.Annotations[connectionSecretOrphanedAt] == "" {
		t.Fatal("old orphan was not marked before deletion")
	}
}
