package dashboard

import (
	"context"
	"strings"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/usercreds"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDeriveUserNamespaceName(t *testing.T) {
	a := deriveUserNamespaceName("John Smith", "subject-1")
	b := deriveUserNamespaceName("John Smith", "subject-1")
	if a != b {
		t.Fatalf("not deterministic: %q != %q", a, b)
	}
	if !strings.HasPrefix(a, "john-smith-") {
		t.Fatalf("namespace %q missing john-smith prefix", a)
	}
	// Same display name, different subject → different namespace (no collision).
	if c := deriveUserNamespaceName("John Smith", "subject-2"); c == a {
		t.Fatalf("distinct subjects collided: %q", c)
	}
	// DNS-1123: lowercase, <=63, alphanumeric start/end.
	if a != strings.ToLower(a) || len(a) > maxDNSLabelLen {
		t.Fatalf("namespace %q is not a valid DNS-1123 label", a)
	}
	// No usable name falls back to a "user-" prefix.
	if got := deriveUserNamespaceName("", "subject-1"); !strings.HasPrefix(got, "user-") {
		t.Fatalf("empty name namespace = %q, want user- prefix", got)
	}
	// Funky characters are sanitized.
	if got := deriveUserNamespaceName("Renée O'Brien!!", "s"); strings.ContainsAny(got, " '!É") {
		t.Fatalf("namespace %q not sanitized", got)
	}
}

func credActorCtx(subject, name string) context.Context {
	return context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: subject, Name: name})
}

func TestUpdateAndListMyCredentials(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	ctx := credActorCtx("user-cred", "Cred User")

	// Save an anthropic API key, an openai OAuth blob, and a github token.
	got, err := srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{
		AnthropicApiKey:  "sk-ant-x",
		OpenrouterApiKey: "openrouter-test-key",
		XaiApiKey:        "xai-test-key",
		OpenaiOauthJson:  `{"tokens":{"access_token":"a","refresh_token":"r"}}`,
		GithubToken:      "gh-x",
	})
	if err != nil {
		t.Fatalf("UpdateMyCredentials() error = %v", err)
	}
	if got.Namespace == "" {
		t.Fatal("expected a namespace in response")
	}
	if !got.AnthropicApiKeyPresent || !got.OpenrouterApiKeyPresent || !got.XaiApiKeyPresent || !got.OpenaiOauthPresent || !got.GithubTokenPresent {
		t.Fatalf("presence = %#v, want anthropic-key/openai-oauth/github present", got)
	}
	if got.CopilotOauthPresent || got.AnthropicOauthPresent || got.OpenaiApiKeyPresent {
		t.Fatalf("presence = %#v, want others absent", got)
	}

	// The anthropic secret carries discovery + provider labels.
	sec := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: got.Namespace, Name: userCredentialSecretName(triggersv1alpha1.ProviderAnthropic)}, sec); err != nil {
		t.Fatalf("get anthropic secret: %v", err)
	}
	if sec.Labels[usercreds.LabelUserCredential] != "true" || sec.Labels[usercreds.LabelCredentialProvider] != triggersv1alpha1.ProviderAnthropic {
		t.Fatalf("labels = %#v, want discovery + provider", sec.Labels)
	}

	// List reflects the same state.
	list, err := srv.ListMyCredentials(ctx, &platform.ListMyCredentialsRequest{})
	if err != nil {
		t.Fatalf("ListMyCredentials() error = %v", err)
	}
	if !list.AnthropicApiKeyPresent || !list.OpenrouterApiKeyPresent || !list.XaiApiKeyPresent || !list.OpenaiOauthPresent || !list.GithubTokenPresent {
		t.Fatalf("list presence = %#v", list)
	}

	// Clearing the anthropic key removes it and deletes the now-empty secret.
	got, err = srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{Clear: []string{"anthropic-api-key"}})
	if err != nil {
		t.Fatalf("clear error = %v", err)
	}
	if got.AnthropicApiKeyPresent {
		t.Fatal("anthropic key still present after clear")
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: got.Namespace, Name: userCredentialSecretName(triggersv1alpha1.ProviderAnthropic)}, &corev1.Secret{}); err == nil {
		t.Fatal("expected anthropic secret deleted after clearing its only key")
	}
}

func TestMyCredentialsListsOnlyCallerNamespaceSecretMetadata(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "user-one", Name: "zeta"}, Data: map[string][]byte{"token": []byte("private"), "url": []byte("private")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "user-one", Name: "alpha"}, Data: map[string][]byte{"api-key": []byte("private")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "user-two", Name: "other-user-secret"}, Data: map[string][]byte{"token": []byte("private")}},
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	got := srv.myCredentialsProto(context.Background(), "user-one")
	if len(got.Secrets) != 2 {
		t.Fatalf("Secrets = %#v, want only two caller-namespace secrets", got.Secrets)
	}
	if got.Secrets[0].Name != "alpha" || got.Secrets[1].Name != "zeta" {
		t.Fatalf("Secrets = %#v, want names sorted", got.Secrets)
	}
	if diff := strings.Join(got.Secrets[1].Keys, ","); diff != "token,url" {
		t.Fatalf("zeta keys = %q, want token,url", diff)
	}
}

func TestUpdateMyCredentialsRejectsBadOAuth(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	ctx := credActorCtx("user-cred", "Cred User")

	_, err := srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{
		AnthropicOauthJson: "not-json",
	})
	if err == nil {
		t.Fatal("expected error for malformed anthropic OAuth JSON")
	}
}

func TestMyCredentialsRequireAuth(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	if _, err := srv.ListMyCredentials(context.Background(), &platform.ListMyCredentialsRequest{}); err == nil {
		t.Fatal("expected unauthenticated error")
	}
}
