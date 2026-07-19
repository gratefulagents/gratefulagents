package oauthrefresh

import (
	"context"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/usercreds"
	oauth "github.com/gratefulagents/sdk/pkg/agentsdk/providers/oauth"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func refresherTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}
	return scheme
}

func userCredSecret(namespace, name, provider string, data map[string][]byte) *corev1.Secret {
	labels := map[string]string{usercreds.LabelUserCredential: "true"}
	if provider != "" {
		labels[usercreds.LabelCredentialProvider] = provider
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Data:       data,
	}
}

func TestCollectUserCredentialSecretRefs(t *testing.T) {
	scheme := refresherTestScheme(t)
	objs := []client.Object{
		// Discoverable OAuth credential.
		userCredSecret("john-smith-abc", "usercred-anthropic", triggersv1alpha1.ProviderAnthropic, map[string][]byte{oauth.AuthJSONKey: []byte(`{"access_token":"a"}`)}),
		// Copilot OAuth credential.
		userCredSecret("jane-doe-xyz", "usercred-copilot", triggersv1alpha1.ProviderCopilot, map[string][]byte{oauth.AuthJSONKey: []byte(`{"oauth_token":"o","token":"t"}`)}),
		// API-key-only credential: no auth.json → skipped.
		userCredSecret("john-smith-abc", "usercred-openai", triggersv1alpha1.ProviderOpenAI, map[string][]byte{"api-key": []byte("sk-x")}),
		// GitHub token: no provider label → skipped.
		userCredSecret("john-smith-abc", "usercred-github", "", map[string][]byte{"github-token": []byte("gh")}),
		// Unrelated secret without the discovery label → skipped.
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "john-smith-abc"}, Data: map[string][]byte{oauth.AuthJSONKey: []byte("{}")}},
	}
	r := &Refresher{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()}

	seen := map[oauthSecretRef]struct{}{}
	if err := r.collectUserCredentialSecretRefs(context.Background(), seen); err != nil {
		t.Fatalf("collectUserCredentialSecretRefs() error = %v", err)
	}

	want := map[oauthSecretRef]struct{}{
		{NamespacedName: types.NamespacedName{Namespace: "john-smith-abc", Name: "usercred-anthropic"}, Provider: triggersv1alpha1.ProviderAnthropic}: {},
		{NamespacedName: types.NamespacedName{Namespace: "jane-doe-xyz", Name: "usercred-copilot"}, Provider: triggersv1alpha1.ProviderCopilot}:       {},
	}
	if len(seen) != len(want) {
		t.Fatalf("collected %d refs, want %d: %#v", len(seen), len(want), seen)
	}
	for ref := range want {
		if _, ok := seen[ref]; !ok {
			t.Fatalf("missing expected ref %#v in %#v", ref, seen)
		}
	}
}
