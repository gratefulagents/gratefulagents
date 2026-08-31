package auth

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	authpb "github.com/gratefulagents/gratefulagents/rpc/auth"
)

// setupFakeStore is a minimal in-memory Store for exercising SeedAdmin and
// RedeemSetupToken.
type setupFakeStore struct {
	Store // panics on unimplemented methods

	usersByName map[string]*User
	sessions    []*Session
}

func newSetupFakeStore(users ...*User) *setupFakeStore {
	s := &setupFakeStore{usersByName: map[string]*User{}}
	for _, u := range users {
		s.usersByName[u.Username] = u
	}
	return s
}

func (s *setupFakeStore) GetUserByUsername(_ context.Context, username string) (*User, error) {
	u, ok := s.usersByName[username]
	if !ok {
		return nil, context.Canceled
	}
	return u, nil
}

func (s *setupFakeStore) UpsertUser(_ context.Context, u *User) (*User, error) {
	if u.ID == "" {
		u.ID = "user-" + u.Username
	}
	s.usersByName[u.Username] = u
	return u, nil
}

func (s *setupFakeStore) TouchUserLastLogin(context.Context, string) error {
	return nil
}

func (s *setupFakeStore) CreateSession(_ context.Context, session *Session) error {
	s.sessions = append(s.sessions, session)
	return nil
}

func TestSeedAdminWritesSetupToken(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	store := newSetupFakeStore()

	if err := SeedAdmin(context.Background(), clientset, store); err != nil {
		t.Fatalf("SeedAdmin: %v", err)
	}

	secret, err := clientset.CoreV1().Secrets("default").Get(context.Background(), adminSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get admin secret: %v", err)
	}
	token := string(secret.Data[setupTokenKey])
	if token == "" {
		t.Fatal("setup token was not written to the admin secret")
	}
	expiry, err := time.Parse(time.RFC3339, string(secret.Data[setupTokenExpiryKey]))
	if err != nil {
		t.Fatalf("setup token expiry is not RFC3339: %v", err)
	}
	if until := time.Until(expiry); until < 6*24*time.Hour || until > 8*24*time.Hour {
		t.Errorf("expiry %v is not ~7 days out", expiry)
	}
	if store.usersByName[adminUsername] == nil {
		t.Error("admin user was not seeded")
	}

	// Re-seeding with an existing secret must not regenerate the token.
	if err := SeedAdmin(context.Background(), clientset, store); err != nil {
		t.Fatalf("re-run SeedAdmin: %v", err)
	}
	secret, err = clientset.CoreV1().Secrets("default").Get(context.Background(), adminSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("re-read admin secret: %v", err)
	}
	if got := string(secret.Data[setupTokenKey]); got != token {
		t.Errorf("setup token changed on re-seed: %q -> %q", token, got)
	}
}

func newSetupTestServer(t *testing.T, store Store, clientset *fake.Clientset) *Server {
	t.Helper()
	issuer, _ := newTestIssuer(t)
	return NewServer(store, nil, issuer, nil, clientset)
}

func seedForRedeem(t *testing.T) (*fake.Clientset, *setupFakeStore, string) {
	t.Helper()
	clientset := fake.NewSimpleClientset()
	store := newSetupFakeStore()
	if err := SeedAdmin(context.Background(), clientset, store); err != nil {
		t.Fatalf("SeedAdmin: %v", err)
	}
	secret, err := clientset.CoreV1().Secrets("default").Get(context.Background(), adminSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get admin secret: %v", err)
	}
	return clientset, store, string(secret.Data[setupTokenKey])
}

func redeem(srv *Server, token string) (*connect.Response[authpb.LoginResponse], error) {
	return srv.RedeemSetupToken(context.Background(), connect.NewRequest(&authpb.RedeemSetupTokenRequest{Token: token}))
}

func TestRedeemSetupTokenSucceedsOnceThenFails(t *testing.T) {
	clientset, store, token := seedForRedeem(t)
	srv := newSetupTestServer(t, store, clientset)

	resp, err := redeem(srv, token)
	if err != nil {
		t.Fatalf("RedeemSetupToken: %v", err)
	}
	if resp.Msg.AccessToken == "" || resp.Msg.RefreshToken == "" {
		t.Error("expected access and refresh tokens")
	}
	if resp.Msg.User == nil || resp.Msg.User.Username != adminUsername || resp.Msg.User.Role != RoleAdmin {
		t.Errorf("unexpected user: %+v", resp.Msg.User)
	}
	if len(store.sessions) != 1 {
		t.Errorf("got %d sessions, want 1", len(store.sessions))
	}

	// The token keys must be removed from the secret.
	secret, err := clientset.CoreV1().Secrets("default").Get(context.Background(), adminSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("re-read admin secret: %v", err)
	}
	if _, ok := secret.Data[setupTokenKey]; ok {
		t.Error("setup-token key was not deleted after redemption")
	}
	if _, ok := secret.Data[setupTokenExpiryKey]; ok {
		t.Error("setup-token-expiry key was not deleted after redemption")
	}
	if string(secret.Data["password"]) == "" {
		t.Error("password key must survive redemption")
	}

	// Second redemption of the same token must fail.
	if _, err := redeem(srv, token); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("second redeem: got %v, want CodeUnauthenticated", err)
	}
}

func TestRedeemSetupTokenRejectsWrongToken(t *testing.T) {
	clientset, store, token := seedForRedeem(t)
	srv := newSetupTestServer(t, store, clientset)

	if _, err := redeem(srv, token+"x"); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("wrong token: got %v, want CodeUnauthenticated", err)
	}
	if _, err := redeem(srv, ""); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("empty token: got %v, want CodeUnauthenticated", err)
	}
	if len(store.sessions) != 0 {
		t.Errorf("no session must be created on rejection, got %d", len(store.sessions))
	}

	// The token must remain redeemable after failed attempts.
	if _, err := redeem(srv, token); err != nil {
		t.Fatalf("valid token after failed attempts: %v", err)
	}
}

func TestRedeemSetupTokenRejectsExpiredToken(t *testing.T) {
	clientset, store, token := seedForRedeem(t)
	secret, err := clientset.CoreV1().Secrets("default").Get(context.Background(), adminSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get admin secret: %v", err)
	}
	secret.Data[setupTokenExpiryKey] = []byte(time.Now().Add(-time.Hour).Format(time.RFC3339))
	if _, err := clientset.CoreV1().Secrets("default").Update(context.Background(), secret, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("expire token: %v", err)
	}
	srv := newSetupTestServer(t, store, clientset)

	if _, err := redeem(srv, token); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("expired token: got %v, want CodeUnauthenticated", err)
	}
}

func TestRedeemSetupTokenRejectsMissingSecret(t *testing.T) {
	srv := newSetupTestServer(t, newSetupFakeStore(), fake.NewSimpleClientset())

	if _, err := redeem(srv, "any-token"); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("missing secret: got %v, want CodeUnauthenticated", err)
	}
}
