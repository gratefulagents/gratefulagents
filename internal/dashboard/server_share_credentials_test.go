package dashboard

import (
	"context"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// shareCredsServer builds a Server with a fake k8s client, an auth store that
// knows the given users, and a collaboration state store for notifications.
func shareCredsServer(t *testing.T, users ...*auth.User) (*Server, client.Client, *collaborationStateStore) {
	t.Helper()
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	ms := newCollaborationStateStore()
	srv := &Server{
		k8sClient:  c,
		scheme:     scheme,
		authStore:  &collaborationAuthStore{users: users},
		stateStore: ms,
	}
	return srv, c, ms
}

// seedCredentialSecret writes a usercred-<name> Secret in the caller's
// namespace via the production write path so labels match reality.
func seedCredentialSecret(t *testing.T, srv *Server, ctx context.Context, name string, data map[string][]byte) string {
	t.Helper()
	namespace, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		t.Fatalf("ensureUserNamespace() error = %v", err)
	}
	if err := srv.writeCredentialData(ctx, namespace, name, data); err != nil {
		t.Fatalf("writeCredentialData(%s) error = %v", name, err)
	}
	return namespace
}

func TestShareMyCredentialsCopiesSecrets(t *testing.T) {
	target := &auth.User{ID: "user-bob", Email: "bob@example.com", Name: "Bob Builder"}
	srv, c, ms := shareCredsServer(t, target)
	ctx := credActorCtx("user-alice", "Alice Adams")

	sourceNS := seedCredentialSecret(t, srv, ctx, "openai", map[string][]byte{
		userCredAPIKeyKey: []byte("value-openai"),
	})
	seedCredentialSecret(t, srv, ctx, "copilot", map[string][]byte{
		userCredOAuthJSONKey: []byte(`{"access_token":"value-copilot"}`),
	})
	seedCredentialSecret(t, srv, ctx, "grafana", map[string][]byte{
		"url":   []byte("https://grafana.example.com"),
		"token": []byte("value-grafana"),
	})

	resp, err := srv.ShareMyCredentials(ctx, &platform.ShareMyCredentialsRequest{
		TargetEmail: "bob@example.com",
		Credentials: []string{"OpenAI", "copilot", "grafana", "copilot"}, // mixed case + dupe
	})
	if err != nil {
		t.Fatalf("ShareMyCredentials() error = %v", err)
	}
	if got, want := len(resp.GetShared()), 3; got != want {
		t.Fatalf("shared = %v, want %d entries", resp.GetShared(), want)
	}

	targetNS, err := srv.ensureNamespaceForUser(ctx, target.ID, target.Name)
	if err != nil {
		t.Fatalf("ensureNamespaceForUser() error = %v", err)
	}
	if targetNS == sourceNS {
		t.Fatalf("target namespace %q must differ from source %q", targetNS, sourceNS)
	}

	for name, wantKey := range map[string]string{
		"openai":  userCredAPIKeyKey,
		"copilot": userCredOAuthJSONKey,
		"grafana": "token",
	} {
		secret := &corev1.Secret{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: targetNS, Name: userCredentialSecretName(name)}, secret); err != nil {
			t.Fatalf("target secret %s not copied: %v", name, err)
		}
		if len(secret.Data[wantKey]) == 0 {
			t.Errorf("copied secret %s missing key %s", name, wantKey)
		}
		if secret.Labels[userCredentialLabel] != "true" || secret.Labels[userCredentialProviderLabel] != name {
			t.Errorf("copied secret %s labels = %v, want credential labels", name, secret.Labels)
		}
	}

	// Source secrets must be untouched.
	sourceSecret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: sourceNS, Name: userCredentialSecretName("openai")}, sourceSecret); err != nil {
		t.Fatalf("source secret missing after share: %v", err)
	}

	// Recipient got a notification.
	if len(ms.notifications) != 1 {
		t.Fatalf("notifications = %d, want 1", len(ms.notifications))
	}
	if n := ms.notifications[0]; n.UserID != target.ID || n.Type != "credentials_shared" {
		t.Errorf("notification = %+v, want credentials_shared for %s", n, target.ID)
	}
}

func TestShareMyCredentialsReplacesExistingWholesale(t *testing.T) {
	target := &auth.User{ID: "user-bob", Email: "bob@example.com", Name: "Bob Builder"}
	srv, c, _ := shareCredsServer(t, target)
	ctx := credActorCtx("user-alice", "Alice Adams")

	seedCredentialSecret(t, srv, ctx, "anthropic", map[string][]byte{
		userCredAPIKeyKey: []byte("value-alice"),
	})

	// Target already has an anthropic credential with OAuth material that must
	// not survive the copy (no mixing of two accounts).
	targetNS, err := srv.ensureNamespaceForUser(ctx, target.ID, target.Name)
	if err != nil {
		t.Fatalf("ensureNamespaceForUser() error = %v", err)
	}
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      userCredentialSecretName("anthropic"),
			Namespace: targetNS,
		},
		Data: map[string][]byte{
			userCredAPIKeyKey:    []byte("value-bob"),
			userCredOAuthJSONKey: []byte(`{"stale":"material"}`),
		},
	}
	if err := c.Create(ctx, existing); err != nil {
		t.Fatalf("seed target secret: %v", err)
	}

	if _, err := srv.ShareMyCredentials(ctx, &platform.ShareMyCredentialsRequest{
		TargetEmail: "bob@example.com",
		Credentials: []string{"anthropic"},
	}); err != nil {
		t.Fatalf("ShareMyCredentials() error = %v", err)
	}

	got := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: targetNS, Name: userCredentialSecretName("anthropic")}, got); err != nil {
		t.Fatalf("read target secret: %v", err)
	}
	if string(got.Data[userCredAPIKeyKey]) != "value-alice" {
		t.Errorf("api key = %q, want sender's value", got.Data[userCredAPIKeyKey])
	}
	if _, stale := got.Data[userCredOAuthJSONKey]; stale {
		t.Errorf("stale oauth key survived the copy; data keys = %v", mapKeys(got.Data))
	}
}

func TestShareMyCredentialsErrors(t *testing.T) {
	target := &auth.User{ID: "user-bob", Email: "bob@example.com", Name: "Bob Builder"}
	self := &auth.User{ID: "user-alice", Email: "alice@example.com", Name: "Alice Adams"}

	tests := []struct {
		name     string
		ctx      context.Context
		req      *platform.ShareMyCredentialsRequest
		seed     bool
		wantCode connect.Code
	}{
		{
			name:     "unauthenticated",
			ctx:      context.WithValue(context.Background(), requestActorContextKey{}, requestActor{}),
			req:      &platform.ShareMyCredentialsRequest{TargetEmail: "bob@example.com", Credentials: []string{"openai"}},
			wantCode: connect.CodeUnauthenticated,
		},
		{
			name:     "no credentials selected",
			ctx:      credActorCtx("user-alice", "Alice Adams"),
			req:      &platform.ShareMyCredentialsRequest{TargetEmail: "bob@example.com"},
			wantCode: connect.CodeInvalidArgument,
		},
		{
			name:     "invalid credential name",
			ctx:      credActorCtx("user-alice", "Alice Adams"),
			req:      &platform.ShareMyCredentialsRequest{TargetEmail: "bob@example.com", Credentials: []string{"Not A Name!"}},
			wantCode: connect.CodeInvalidArgument,
		},
		{
			name:     "missing target email",
			ctx:      credActorCtx("user-alice", "Alice Adams"),
			req:      &platform.ShareMyCredentialsRequest{Credentials: []string{"openai"}},
			wantCode: connect.CodeInvalidArgument,
		},
		{
			name:     "unknown user",
			ctx:      credActorCtx("user-alice", "Alice Adams"),
			req:      &platform.ShareMyCredentialsRequest{TargetEmail: "nobody@example.com", Credentials: []string{"openai"}},
			seed:     true,
			wantCode: connect.CodeNotFound,
		},
		{
			name:     "share with self",
			ctx:      credActorCtx("user-alice", "Alice Adams"),
			req:      &platform.ShareMyCredentialsRequest{TargetEmail: "alice@example.com", Credentials: []string{"openai"}},
			seed:     true,
			wantCode: connect.CodeInvalidArgument,
		},
		{
			name:     "no saved credential for provider",
			ctx:      credActorCtx("user-alice", "Alice Adams"),
			req:      &platform.ShareMyCredentialsRequest{TargetEmail: "bob@example.com", Credentials: []string{"github"}},
			wantCode: connect.CodeFailedPrecondition,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, ms := shareCredsServer(t, target, self)
			if tt.seed {
				seedCredentialSecret(t, srv, tt.ctx, "openai", map[string][]byte{
					userCredAPIKeyKey: []byte("value-openai"),
				})
			}
			_, err := srv.ShareMyCredentials(tt.ctx, tt.req)
			requireConnectCode(t, err, tt.wantCode)
			if len(ms.notifications) != 0 {
				t.Errorf("notifications = %d, want none on failure", len(ms.notifications))
			}
		})
	}
}

// TestShareMyCredentialsAllOrNothing verifies no partial copy happens when one
// of the requested credentials is missing.
func TestShareMyCredentialsAllOrNothing(t *testing.T) {
	target := &auth.User{ID: "user-bob", Email: "bob@example.com", Name: "Bob Builder"}
	srv, c, _ := shareCredsServer(t, target)
	ctx := credActorCtx("user-alice", "Alice Adams")

	seedCredentialSecret(t, srv, ctx, "openai", map[string][]byte{
		userCredAPIKeyKey: []byte("value-openai"),
	})

	_, err := srv.ShareMyCredentials(ctx, &platform.ShareMyCredentialsRequest{
		TargetEmail: "bob@example.com",
		Credentials: []string{"openai", "copilot"}, // copilot not saved
	})
	requireConnectCode(t, err, connect.CodeFailedPrecondition)

	targetNS, err := srv.ensureNamespaceForUser(ctx, target.ID, target.Name)
	if err != nil {
		t.Fatalf("ensureNamespaceForUser() error = %v", err)
	}
	secret := &corev1.Secret{}
	getErr := c.Get(ctx, client.ObjectKey{Namespace: targetNS, Name: userCredentialSecretName("openai")}, secret)
	if getErr == nil {
		t.Fatalf("openai secret was copied despite failed request")
	}
}

// TestShareMyCredentialsRollsBackOnPartialFailure verifies that when a later
// target write fails mid-share, the earlier writes are reverted: a Secret the
// share created is deleted and a Secret it replaced is restored, so the
// recipient never keeps a partial share the UI reported as failed.
func TestShareMyCredentialsRollsBackOnPartialFailure(t *testing.T) {
	target := &auth.User{ID: "user-bob", Email: "bob@example.com", Name: "Bob Builder"}
	scheme := testProjectScheme(t)

	// fail lets the test arm an injected Create failure for one Secret after
	// the namespaces are known.
	fail := &struct{ namespace, name string }{}
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if obj.GetNamespace() == fail.namespace && obj.GetName() == fail.name {
				return fmt.Errorf("injected create failure")
			}
			return cl.Create(ctx, obj, opts...)
		},
	}).Build()
	ms := newCollaborationStateStore()
	srv := &Server{
		k8sClient:  c,
		scheme:     scheme,
		authStore:  &collaborationAuthStore{users: []*auth.User{target}},
		stateStore: ms,
	}
	ctx := credActorCtx("user-alice", "Alice Adams")

	seedCredentialSecret(t, srv, ctx, "anthropic", map[string][]byte{
		userCredAPIKeyKey: []byte("value-alice"),
	})
	seedCredentialSecret(t, srv, ctx, "openai", map[string][]byte{
		userCredAPIKeyKey: []byte("value-alice-openai"),
	})
	seedCredentialSecret(t, srv, ctx, "copilot", map[string][]byte{
		userCredOAuthJSONKey: []byte(`{"access_token":"alice"}`),
	})

	targetNS, err := srv.ensureNamespaceForUser(ctx, target.ID, target.Name)
	if err != nil {
		t.Fatalf("ensureNamespaceForUser() error = %v", err)
	}

	// Bob already has an anthropic credential: the share updates it (undo =
	// restore). He has no openai credential: the share creates it (undo =
	// delete). The copilot create then fails.
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      userCredentialSecretName("anthropic"),
			Namespace: targetNS,
			Labels:    map[string]string{"custom": "label"},
		},
		Data: map[string][]byte{userCredAPIKeyKey: []byte("value-bob")},
	}
	if err := c.Create(ctx, existing); err != nil {
		t.Fatalf("seed target secret: %v", err)
	}
	fail.namespace = targetNS
	fail.name = userCredentialSecretName("copilot")

	_, err = srv.ShareMyCredentials(ctx, &platform.ShareMyCredentialsRequest{
		TargetEmail: "bob@example.com",
		Credentials: []string{"anthropic", "openai", "copilot"},
	})
	if err == nil {
		t.Fatalf("ShareMyCredentials() error = nil, want injected failure")
	}

	// Bob's pre-existing anthropic credential is restored, labels included.
	restored := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: targetNS, Name: userCredentialSecretName("anthropic")}, restored); err != nil {
		t.Fatalf("read rolled-back secret: %v", err)
	}
	if got := string(restored.Data[userCredAPIKeyKey]); got != "value-bob" {
		t.Errorf("anthropic api key after rollback = %q, want %q", got, "value-bob")
	}
	if restored.Labels["custom"] != "label" || restored.Labels[userCredentialLabel] == "true" {
		t.Errorf("anthropic labels after rollback = %v, want original labels", restored.Labels)
	}

	// The openai copy the share created is deleted again.
	leftover := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: targetNS, Name: userCredentialSecretName("openai")}, leftover); err == nil {
		t.Fatalf("openai secret survived rollback")
	}

	// No notification for a failed share.
	if len(ms.notifications) != 0 {
		t.Errorf("notifications = %d, want none on failure", len(ms.notifications))
	}
}

func mapKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
