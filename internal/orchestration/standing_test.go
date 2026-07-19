package orchestration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type standingTestStore struct {
	store.StateStore
	session  *store.Session
	creates  int
	appends  int
	messages []store.Message
}

func (s *standingTestStore) CreateSession(context.Context, string, string, string, string) (*store.Session, error) {
	s.creates++
	if s.session == nil {
		s.session = &store.Session{ID: uuid.New()}
	}
	return s.session, nil
}

func (s *standingTestStore) GetMessages(context.Context, uuid.UUID) ([]store.Message, error) {
	return append([]store.Message(nil), s.messages...), nil
}

func (s *standingTestStore) AppendMessage(_ context.Context, sessionID uuid.UUID, role, content string, _ json.RawMessage) (*store.Message, error) {
	s.appends++
	message := store.Message{ID: int64(s.appends), SessionID: sessionID, Role: role, Content: content}
	s.messages = append(s.messages, message)
	return &message, nil
}

func TestStandingRunNameDeterministicAndDNSLength(t *testing.T) {
	t.Parallel()
	owner := strings.Repeat("Owner_With.Invalid Characters", 5)
	first := StandingRunName(owner, "Code Review")
	second := StandingRunName(owner, "Code Review")
	if first != second {
		t.Fatalf("StandingRunName is not deterministic: %q != %q", first, second)
	}
	if len(first) > 63 {
		t.Fatalf("StandingRunName length = %d, want <= 63", len(first))
	}
	for _, r := range first {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			t.Fatalf("StandingRunName contains non-DNS character %q: %q", r, first)
		}
	}
}

func TestEnsureStandingRunCreatesAndSeedsOnce(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	owner := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "supervisor", Namespace: "default", UID: types.UID("owner-uid")}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(owner).Build()
	stateStore := &standingTestStore{}
	desired := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{StandingRunRoleLabel: "overseer"}}}

	run, created, err := EnsureStandingRun(context.Background(), k8sClient, scheme, stateStore, owner, desired, "Start supervising.")
	if err != nil {
		t.Fatalf("EnsureStandingRun() error = %v", err)
	}
	if !created || stateStore.creates != 1 || stateStore.appends != 1 {
		t.Fatalf("first ensure = created %v, creates %d, appends %d", created, stateStore.creates, stateStore.appends)
	}
	if run.Annotations[StandingRunSeededAnnotation] != "true" || run.Labels[SupervisedRunLabel] != owner.Name {
		t.Fatalf("standing run metadata = labels %#v annotations %#v", run.Labels, run.Annotations)
	}

	_, created, err = EnsureStandingRun(context.Background(), k8sClient, scheme, stateStore, owner, desired, "Start supervising.")
	if err != nil {
		t.Fatalf("second EnsureStandingRun() error = %v", err)
	}
	if created || stateStore.creates != 1 || stateStore.appends != 1 {
		t.Fatalf("second ensure = created %v, creates %d, appends %d", created, stateStore.creates, stateStore.appends)
	}
}

func TestMarkCheckpointHandled(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "standing", Namespace: "default"}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	key := client.ObjectKeyFromObject(run)
	if err := MarkCheckpointHandled(context.Background(), k8sClient, key, 7); err != nil {
		t.Fatalf("MarkCheckpointHandled() error = %v", err)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), key, updated); err != nil {
		t.Fatal(err)
	}
	if got := updated.Annotations[CheckpointHandledAnnotation]; got != "7" {
		t.Fatalf("handled annotation = %q, want 7", got)
	}
}
