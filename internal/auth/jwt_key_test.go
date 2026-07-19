package auth

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEnsureSigningKeyPEMGeneratesAndPersists(t *testing.T) {
	t.Parallel()
	clientset := fake.NewSimpleClientset()

	pem1, err := EnsureSigningKeyPEM(context.Background(), clientset)
	if err != nil {
		t.Fatalf("first EnsureSigningKeyPEM: %v", err)
	}
	if len(pem1) == 0 {
		t.Fatal("expected non-empty PEM")
	}

	// The key must round-trip into a working issuer.
	if _, err := NewJWTIssuerFromPEM(pem1); err != nil {
		t.Fatalf("NewJWTIssuerFromPEM: %v", err)
	}

	// Secret must exist with the expected key.
	secret, err := clientset.CoreV1().Secrets("default").Get(context.Background(), jwtKeySecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading persisted secret: %v", err)
	}
	if len(secret.Data[jwtKeySecretKey]) == 0 {
		t.Fatalf("secret missing %q key", jwtKeySecretKey)
	}

	// Second call must return the SAME key (restart stability).
	pem2, err := EnsureSigningKeyPEM(context.Background(), clientset)
	if err != nil {
		t.Fatalf("second EnsureSigningKeyPEM: %v", err)
	}
	if string(pem1) != string(pem2) {
		t.Fatal("expected identical key across calls")
	}
}

func TestEnsureSigningKeyPEMIssuersAgreeAcrossRestarts(t *testing.T) {
	t.Parallel()
	clientset := fake.NewSimpleClientset()

	pem1, err := EnsureSigningKeyPEM(context.Background(), clientset)
	if err != nil {
		t.Fatalf("EnsureSigningKeyPEM: %v", err)
	}
	issuer1, err := NewJWTIssuerFromPEM(pem1)
	if err != nil {
		t.Fatalf("issuer1: %v", err)
	}
	token, _, err := issuer1.IssueAccessToken(AccessTokenClaims{Sub: "u1", Username: "u1", Role: RoleMember}, time.Minute)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	// Simulate a restart: new issuer from the same persisted secret.
	pem2, err := EnsureSigningKeyPEM(context.Background(), clientset)
	if err != nil {
		t.Fatalf("EnsureSigningKeyPEM after restart: %v", err)
	}
	issuer2, err := NewJWTIssuerFromPEM(pem2)
	if err != nil {
		t.Fatalf("issuer2: %v", err)
	}
	cache, err := NewJWKSCacheFromIssuer(issuer2)
	if err != nil {
		t.Fatalf("jwks cache: %v", err)
	}
	if _, err := cache.VerifyToken(token); err != nil {
		t.Fatalf("token from before restart should verify after restart: %v", err)
	}
}
