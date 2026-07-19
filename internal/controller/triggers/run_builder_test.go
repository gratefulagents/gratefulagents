package triggers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBuildTriggerRunUsesGeneratedProjectProvenance(t *testing.T) {
	owner := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Labels: generatedProjectRuntimeMetadata("payments", "project-uid", "issues", "GitHub")}}
	run := BuildTriggerRun(TriggerRunSpec{
		RunName:     "run-1",
		Namespace:   "default",
		TriggerKind: "GitHubRepository",
		TriggerName: "generated-github-child",
		Defaults:    triggerRunTestDefaults(),
		OwnerRef:    owner,
		Context:     &platformv1alpha1.AgentRunContext{ProjectRef: &platformv1alpha1.ProjectRef{Kind: "GitHubRepository", Name: "generated-github-child"}},
	})

	if run.Spec.Trigger.Kind != "GitHubRepository" || run.Spec.Trigger.Name != "issues" || run.Spec.Trigger.Type != "github" {
		t.Fatalf("Trigger = %#v, want legacy child kind with Project trigger name/type", run.Spec.Trigger)
	}
	if got := RuntimeTriggerName(run); got != "generated-github-child" {
		t.Fatalf("RuntimeTriggerName = %q, want generated-github-child", got)
	}
	if run.Spec.Context == nil || run.Spec.Context.ProjectRef == nil || run.Spec.Context.ProjectRef.Kind != "Project" || run.Spec.Context.ProjectRef.Name != "payments" {
		t.Fatalf("Context = %#v, want Project/payments", run.Spec.Context)
	}
}

func TestCreateTriggerRunRetainsGeneratedProjectRunButKeepsStandaloneOwnership(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	generatedOwner := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Labels: generatedProjectRuntimeMetadata("payments", "project-uid", "issues", "github")}}
	created, generatedRun, err := CreateTriggerRun(context.Background(), c, nil, TriggerRunSpec{
		RunName:     "generated-run",
		Namespace:   "default",
		TriggerKind: "GitHubRepository",
		TriggerName: "generated-github-child",
		Defaults:    triggerRunTestDefaults(),
		OwnerRef:    generatedOwner,
		Scheme:      scheme,
	})
	if err != nil {
		t.Fatalf("CreateTriggerRun(generated) error = %v", err)
	}
	if !created || len(generatedRun.OwnerReferences) != 0 {
		t.Fatalf("generated run created/owners = %t/%#v, want true/no owner references", created, generatedRun.OwnerReferences)
	}

	standaloneOwner := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "standalone", Namespace: "default"}}
	created, standaloneRun, err := CreateTriggerRun(context.Background(), c, nil, TriggerRunSpec{
		RunName:     "standalone-run",
		Namespace:   "default",
		TriggerKind: "GitHubRepository",
		TriggerName: "standalone",
		Defaults:    triggerRunTestDefaults(),
		OwnerRef:    standaloneOwner,
		Scheme:      scheme,
	})
	if err != nil {
		t.Fatalf("CreateTriggerRun(standalone) error = %v", err)
	}
	if !created || len(standaloneRun.OwnerReferences) != 1 {
		t.Fatalf("standalone run created/owners = %t/%#v, want true/one owner reference", created, standaloneRun.OwnerReferences)
	}
}

func TestResolveGeneratedProjectOwner(t *testing.T) {
	store := newTriggerRunOwnerStore("alice")
	owner := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Labels: generatedProjectRuntimeMetadata("payments", "project-uid", "issues", "github")}}
	spec := TriggerRunSpec{Namespace: "default", OwnerRef: owner}
	if err := resolveGeneratedProjectOwner(context.Background(), store, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.OwnerID != "alice" {
		t.Fatalf("OwnerID = %q, want alice", spec.OwnerID)
	}
}

type triggerRunOwnerStore struct {
	store.StateStore
	ownerID string
}

func newTriggerRunOwnerStore(ownerID string) *triggerRunOwnerStore {
	return &triggerRunOwnerStore{ownerID: ownerID}
}
func (s *triggerRunOwnerStore) GetResourceOwner(_ context.Context, resourceType, resourceID, namespace string) (*store.ResourceOwnership, error) {
	if resourceType != "project" || resourceID != "payments" || namespace != "default" {
		return nil, nil
	}
	return &store.ResourceOwnership{OwnerID: s.ownerID}, nil
}

func TestRetainRunInstructionConfigMapTransfersOwnership(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	owner := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "runtime", Namespace: "default", UID: "runtime-uid"}}
	instructions := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "run-instructions", Namespace: "default", OwnerReferences: []metav1.OwnerReference{{Name: owner.Name, UID: owner.UID, Controller: boolPtrForTest(true)}}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instructions).Build()
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "default", UID: "run-uid", Annotations: map[string]string{"platform.gratefulagents.dev/instructions-configmap-ref": instructions.Name}}}
	if err := retainRunInstructionConfigMap(context.Background(), c, scheme, run); err != nil {
		t.Fatal(err)
	}
	updated := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(instructions), updated); err != nil {
		t.Fatal(err)
	}
	controller := metav1.GetControllerOf(updated)
	if controller == nil || controller.Kind != "AgentRun" || controller.Name != run.Name {
		t.Fatalf("controller = %#v", controller)
	}
}

func boolPtrForTest(value bool) *bool { return &value }

func generatedProjectRuntimeMetadata(projectName, projectUID, triggerName, triggerType string) map[string]string {
	return map[string]string{
		GeneratedRuntimeMarkerMetadataKey: "true",
		ProjectNameMetadataKey:            projectName,
		ProjectUIDMetadataKey:             projectUID,
		ProjectTriggerNameMetadataKey:     triggerName,
		ProjectTriggerTypeMetadataKey:     triggerType,
	}
}

func triggerRunTestDefaults() triggersv1alpha1.AgentRunDefaults {
	return triggersv1alpha1.AgentRunDefaults{
		Model:    "gpt-4.1",
		Provider: triggersv1alpha1.ProviderOpenAI,
		AuthMode: platformv1alpha1.AgentRunAuthModeAPIKey,
		Secrets: triggersv1alpha1.AgentRunSecrets{ProviderKeys: []platformv1alpha1.ProviderKeyRef{{
			Provider:   triggersv1alpha1.ProviderOpenAI,
			SecretName: "openai-key",
		}}},
	}
}

// seedTestStore records sessions and messages so tests can assert that
// re-running CreateTriggerRun against an existing run never duplicates the
// seed message.
type seedTestStore struct {
	store.StateStore
	sessions map[string]uuid.UUID
	messages map[uuid.UUID][]store.Message
}

func newSeedTestStore() *seedTestStore {
	return &seedTestStore{sessions: map[string]uuid.UUID{}, messages: map[uuid.UUID][]store.Message{}}
}

func (s *seedTestStore) CreateSession(_ context.Context, runName, runNS, _, _ string) (*store.Session, error) {
	key := runNS + "/" + runName
	id, ok := s.sessions[key]
	if !ok {
		id = uuid.New()
		s.sessions[key] = id
	}
	return &store.Session{ID: id, AgentRunName: runName, AgentRunNS: runNS}, nil
}

func (s *seedTestStore) GetMessages(_ context.Context, sessionID uuid.UUID) ([]store.Message, error) {
	return append([]store.Message(nil), s.messages[sessionID]...), nil
}

func (s *seedTestStore) AppendMessage(_ context.Context, sessionID uuid.UUID, role, content string, metadata json.RawMessage) (*store.Message, error) {
	msg := store.Message{ID: int64(len(s.messages[sessionID]) + 1), SessionID: sessionID, Role: role, Content: content, Metadata: metadata}
	s.messages[sessionID] = append(s.messages[sessionID], msg)
	return &msg, nil
}

func (s *seedTestStore) GetResourceOwner(context.Context, string, string, string) (*store.ResourceOwnership, error) {
	return nil, nil
}

func (s *seedTestStore) SetResourceOwner(context.Context, string, string, string, string) error {
	return nil
}

func TestCreateTriggerRunSeedOnAlreadyExistsDoesNotDuplicateSeed(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	stateStore := newSeedTestStore()

	spec := TriggerRunSpec{
		RunName:             "gh-acme-widgets-7",
		Namespace:           "default",
		TriggerKind:         "GitHubRepository",
		TriggerName:         "acme-widgets",
		ExternalID:          "7",
		SeedMessage:         "# Issue title\n\nIssue body",
		Defaults:            triggerRunTestDefaults(),
		OwnerRef:            &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "acme-widgets", Namespace: "default"}},
		Scheme:              scheme,
		SeedOnAlreadyExists: true,
	}

	created, run, err := CreateTriggerRun(context.Background(), c, stateStore, spec)
	if err != nil {
		t.Fatalf("CreateTriggerRun(first) error = %v", err)
	}
	if !created {
		t.Fatal("first CreateTriggerRun should create the run")
	}

	// Simulate later reconciles (or another trigger watching the same repo)
	// hitting AlreadyExists: the seed must not be appended again.
	for i := 0; i < 3; i++ {
		created, _, err = CreateTriggerRun(context.Background(), c, stateStore, spec)
		if err != nil {
			t.Fatalf("CreateTriggerRun(repeat %d) error = %v", i, err)
		}
		if created {
			t.Fatalf("repeat %d should not create the run", i)
		}
	}

	sessID := stateStore.sessions["default/"+run.Name]
	if got := len(stateStore.messages[sessID]); got != 1 {
		t.Fatalf("seed messages = %d, want exactly 1", got)
	}
}

func TestSeedTriggerRunSessionRecoversMissingSeed(t *testing.T) {
	stateStore := newSeedTestStore()
	run := &platformv1alpha1.AgentRun{}
	run.Name = "gh-acme-widgets-8"
	run.Namespace = "default"
	spec := TriggerRunSpec{TriggerKind: "GitHubRepository", SeedMessage: "seed"}

	// Run exists but crashed before seeding: session empty, recovery seeds once.
	seedTriggerRunSession(context.Background(), stateStore, run, spec, false)
	sessID := stateStore.sessions["default/"+run.Name]
	if got := len(stateStore.messages[sessID]); got != 1 {
		t.Fatalf("messages after recovery = %d, want 1", got)
	}
	seedTriggerRunSession(context.Background(), stateStore, run, spec, false)
	if got := len(stateStore.messages[sessID]); got != 1 {
		t.Fatalf("messages after repeat = %d, want still 1", got)
	}
}
