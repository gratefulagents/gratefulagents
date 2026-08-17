package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/usercreds"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func oauthSlotSecret(namespace, provider string, slot int, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      userCredentialSlotSecretName(provider, slot),
			Labels: map[string]string{
				usercreds.LabelUserCredential:     "true",
				usercreds.LabelCredentialProvider: provider,
				usercreds.LabelCredentialSlot:     strconv.Itoa(slot),
			},
		},
		Data: data,
	}
}

func TestUserCredentialSlotSecretName(t *testing.T) {
	if got := userCredentialSlotSecretName("anthropic", 0); got != "usercred-anthropic" {
		t.Fatalf("slot 0 = %q", got)
	}
	if got := userCredentialSlotSecretName("anthropic", 1); got != "usercred-anthropic" {
		t.Fatalf("slot 1 = %q", got)
	}
	if got := userCredentialSlotSecretName("openai", 2); got != "usercred-openai-2" {
		t.Fatalf("slot 2 = %q", got)
	}
	if got := userCredentialSlotSecretName("copilot", 9); got != "usercred-copilot-9" {
		t.Fatalf("slot 9 = %q", got)
	}
}

func TestWriteCredentialSlotDataNamingAndLabels(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	ctx := context.Background()

	if err := srv.applyCredentialOAuthSlot(ctx, "ns", triggersv1alpha1.ProviderAnthropic, 2,
		`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r"}}`, "", "two@example.com", false); err != nil {
		t.Fatalf("applyCredentialOAuthSlot(slot 2) error = %v", err)
	}
	sec := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "usercred-anthropic-2"}, sec); err != nil {
		t.Fatalf("get slot-2 secret: %v", err)
	}
	if sec.Labels[usercreds.LabelUserCredential] != "true" ||
		sec.Labels[usercreds.LabelCredentialProvider] != triggersv1alpha1.ProviderAnthropic ||
		sec.Labels[usercreds.LabelCredentialSlot] != "2" {
		t.Fatalf("slot-2 labels = %#v", sec.Labels)
	}
	if len(sec.Data[userCredOAuthJSONKey]) == 0 || string(sec.Data[userCredEmailKey]) != "two@example.com" {
		t.Fatalf("slot-2 data = %#v", sec.Data)
	}

	// Slot-1 writes keep the legacy name and opportunistically stamp slot "1".
	if err := srv.applyCredentialOAuthSlot(ctx, "ns", triggersv1alpha1.ProviderAnthropic, 1,
		`{"claudeAiOauth":{"accessToken":"a","refreshToken":"r"}}`, "", "", false); err != nil {
		t.Fatalf("applyCredentialOAuthSlot(slot 1) error = %v", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "usercred-anthropic"}, sec); err != nil {
		t.Fatalf("get slot-1 secret: %v", err)
	}
	if sec.Labels[usercreds.LabelCredentialSlot] != "1" {
		t.Fatalf("slot-1 labels = %#v, want slot label \"1\"", sec.Labels)
	}
}

func TestListOAuthSubscriptionsOrderingAndFiltering(t *testing.T) {
	scheme := testProjectScheme(t)
	authJSON := map[string][]byte{userCredOAuthJSONKey: []byte("{}")}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		oauthSlotSecret("ns", "anthropic", 3, map[string][]byte{
			userCredOAuthJSONKey: []byte("{}"),
			userCredAccountIDKey: []byte("acct-3"),
		}),
		oauthSlotSecret("ns", "anthropic", 2, map[string][]byte{
			userCredOAuthJSONKey: []byte("{}"),
			userCredEmailKey:     []byte("two@example.com"),
			userCredAccountIDKey: []byte("acct-2"),
		}),
		// Legacy primary without any labels still counts as slot 1.
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "usercred-anthropic"},
			Data:       authJSON,
		},
		// Labeled but no auth.json: not a subscription.
		oauthSlotSecret("ns", "anthropic", 4, map[string][]byte{userCredAPIKeyKey: []byte("k")}),
		// Different provider label: excluded.
		oauthSlotSecret("ns", "openai", 2, authJSON),
		// Labeled anthropic but non-deterministic name: excluded.
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "ns",
				Name:      "my-custom-anthropic",
				Labels: map[string]string{
					usercreds.LabelUserCredential:     "true",
					usercreds.LabelCredentialProvider: "anthropic",
				},
			},
			Data: authJSON,
		},
		// Slot out of range: excluded.
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "ns",
				Name:      "usercred-anthropic-10",
				Labels: map[string]string{
					usercreds.LabelUserCredential:     "true",
					usercreds.LabelCredentialProvider: "anthropic",
				},
			},
			Data: authJSON,
		},
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	subs := srv.listOAuthSubscriptions(context.Background(), "ns", "anthropic")
	if len(subs) != 3 {
		t.Fatalf("subs = %#v, want 3", subs)
	}
	if subs[0].slot != 1 || subs[0].secretName != "usercred-anthropic" {
		t.Fatalf("subs[0] = %#v", subs[0])
	}
	if subs[1].slot != 2 || subs[1].secretName != "usercred-anthropic-2" || subs[1].accountLabel != "two@example.com" {
		t.Fatalf("subs[1] = %#v, want email-preferred account label", subs[1])
	}
	if subs[2].slot != 3 || subs[2].secretName != "usercred-anthropic-3" || subs[2].accountLabel != "acct-3" {
		t.Fatalf("subs[2] = %#v, want account-id fallback label", subs[2])
	}
}

func TestResolveSavedProviderCredentialsReturnsOAuthFallbacksInSlotOrder(t *testing.T) {
	scheme := testProjectScheme(t)
	authJSON := map[string][]byte{userCredOAuthJSONKey: []byte("{}")}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		oauthSlotSecret("ns", "anthropic", 1, authJSON),
		oauthSlotSecret("ns", "anthropic", 3, authJSON),
		oauthSlotSecret("ns", "anthropic", 2, authJSON),
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	creds, err := srv.resolveSavedProviderCredentials(context.Background(), "ns", "anthropic", "")
	if err != nil {
		t.Fatalf("resolveSavedProviderCredentials() error = %v", err)
	}
	if creds.authMode != platformv1alpha1.AgentRunAuthModeOAuth {
		t.Fatalf("authMode = %q, want oauth", creds.authMode)
	}
	if creds.oauthSecretName != "usercred-anthropic" {
		t.Fatalf("primary = %q, want usercred-anthropic", creds.oauthSecretName)
	}
	want := []string{"usercred-anthropic-2", "usercred-anthropic-3"}
	if len(creds.oauthFallbackSecretNames) != 2 || creds.oauthFallbackSecretNames[0] != want[0] || creds.oauthFallbackSecretNames[1] != want[1] {
		t.Fatalf("fallbacks = %v, want %v", creds.oauthFallbackSecretNames, want)
	}
}

func TestUpdateMyCredentialsClearsSubscriptionSlot(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	ctx := credActorCtx("slot-user", "Slot User")
	namespace := deriveUserNamespaceName("Slot User", "slot-user")

	if err := c.Create(ctx, oauthSlotSecret(namespace, "anthropic", 2, map[string][]byte{
		userCredOAuthJSONKey: []byte("{}"),
		userCredEmailKey:     []byte("two@example.com"),
		userCredAccountIDKey: []byte("acct-2"),
	})); err != nil {
		t.Fatalf("create slot secret: %v", err)
	}

	got, err := srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{Clear: []string{"anthropic-oauth-2"}})
	if err != nil {
		t.Fatalf("UpdateMyCredentials(clear slot 2) error = %v", err)
	}
	err = c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "usercred-anthropic-2"}, &corev1.Secret{})
	if !k8serrors.IsNotFound(err) {
		t.Fatalf("slot-2 secret after clear: err = %v, want NotFound", err)
	}
	for _, sub := range got.OauthSubscriptions {
		if sub.Provider == "anthropic" && sub.Slot == 2 {
			t.Fatalf("cleared slot still listed: %#v", sub)
		}
	}
}

func TestStartProviderOAuthRejectsInvalidSlot(t *testing.T) {
	srv := newProviderOAuthTestServer(t, providerOAuthRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("no HTTP call expected")
		return nil, nil
	}))
	ctx := credActorCtx("slot-user", "Slot User")

	for _, slot := range []int32{-1, 10, 42} {
		_, err := srv.StartProviderOAuth(ctx, &platform.StartProviderOAuthRequest{Provider: "anthropic", Slot: slot})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("StartProviderOAuth(slot=%d) error = %v, want InvalidArgument", slot, err)
		}
	}
}

func TestAnthropicOAuthSlotTwoStoresSubscriptionSecret(t *testing.T) {
	transport := providerOAuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return providerOAuthResponse(http.StatusOK, `{"access_token":"claude-access","refresh_token":"claude-refresh","expires_in":3600,"account":{"uuid":"acct-2","email_address":"second@example.com"}}`), nil
	})
	srv := newProviderOAuthTestServer(t, transport)
	ctx := credActorCtx("slot-user", "Slot User")
	namespace := deriveUserNamespaceName("Slot User", "slot-user")

	start, err := srv.StartProviderOAuth(ctx, &platform.StartProviderOAuthRequest{Provider: "anthropic", Slot: 2})
	if err != nil {
		t.Fatalf("StartProviderOAuth() error = %v", err)
	}
	session := srv.providerOAuthSessions[namespace]
	if session.slot != 2 {
		t.Fatalf("session slot = %d, want 2", session.slot)
	}
	// The slot survives a session round-trip through its persisted form.
	raw, err := encodeProviderOAuthSession(session)
	if err != nil {
		t.Fatalf("encode session: %v", err)
	}
	var data providerOAuthSessionData
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if data.Slot != 2 || data.session().slot != 2 {
		t.Fatalf("persisted slot = %d, want 2", data.Slot)
	}

	result, err := srv.CompleteProviderOAuth(ctx, &platform.CompleteProviderOAuthRequest{
		Provider:  "anthropic",
		Code:      "authorization-code#" + session.state,
		SessionId: start.SessionId,
	})
	if err != nil {
		t.Fatalf("CompleteProviderOAuth() error = %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("result = %#v", result)
	}

	sec := &corev1.Secret{}
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "usercred-anthropic-2"}, sec); err != nil {
		t.Fatalf("get slot-2 secret: %v", err)
	}
	if sec.Labels[usercreds.LabelCredentialSlot] != "2" {
		t.Fatalf("labels = %#v, want slot label 2", sec.Labels)
	}
	if len(sec.Data[userCredOAuthJSONKey]) == 0 || string(sec.Data[userCredEmailKey]) != "second@example.com" {
		t.Fatalf("data = %#v, want auth.json + email", sec.Data)
	}
	// The primary secret must not exist: slot 2 was written directly.
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "usercred-anthropic"}, &corev1.Secret{}); !k8serrors.IsNotFound(err) {
		t.Fatalf("primary secret err = %v, want NotFound", err)
	}
}

func TestAppendAllSavedProviderCredentialsPopulatesOAuthFallbacks(t *testing.T) {
	scheme := testProjectScheme(t)
	authJSON := map[string][]byte{userCredOAuthJSONKey: []byte("{}")}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		oauthSlotSecret("ns", "anthropic", 1, authJSON),
		oauthSlotSecret("ns", "anthropic", 2, authJSON),
		oauthSlotSecret("ns", "anthropic", 3, authJSON),
		oauthSlotSecret("ns", "openai", 1, authJSON),
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	secrets := &platformv1alpha1.AgentRunSecrets{OpenAIOAuthSecret: "usercred-anthropic"}
	srv.appendAllSavedProviderCredentials(context.Background(), "ns", secrets)

	if len(secrets.ProviderOAuthFallbackSecrets) != 2 {
		t.Fatalf("fallbacks = %#v, want anthropic slots 2 and 3", secrets.ProviderOAuthFallbackSecrets)
	}
	for i, want := range []string{"usercred-anthropic-2", "usercred-anthropic-3"} {
		ref := secrets.ProviderOAuthFallbackSecrets[i]
		if ref.Provider != "anthropic" || ref.SecretName != want {
			t.Fatalf("fallbacks[%d] = %#v, want %s", i, ref, want)
		}
	}

	// Idempotent: a second pass adds nothing.
	before := len(secrets.ProviderOAuthFallbackSecrets)
	srv.appendAllSavedProviderCredentials(context.Background(), "ns", secrets)
	if len(secrets.ProviderOAuthFallbackSecrets) != before {
		t.Fatalf("not idempotent: %d -> %d", before, len(secrets.ProviderOAuthFallbackSecrets))
	}

	// A run whose primary already points at a subscription slot does not get
	// that slot duplicated as a fallback.
	secrets = &platformv1alpha1.AgentRunSecrets{OpenAIOAuthSecret: "usercred-anthropic-2"}
	srv.appendAllSavedProviderCredentials(context.Background(), "ns", secrets)
	for _, ref := range secrets.ProviderOAuthFallbackSecrets {
		if ref.SecretName == "usercred-anthropic-2" {
			t.Fatalf("primary slot duplicated as fallback: %#v", secrets.ProviderOAuthFallbackSecrets)
		}
	}
}

func TestSetProviderOAuthFallbackSecretsReplacesOnlyProviderEntries(t *testing.T) {
	secrets := &platformv1alpha1.AgentRunSecrets{
		ProviderOAuthFallbackSecrets: []platformv1alpha1.ProviderOAuthSecretRef{
			{Provider: "openai", SecretName: "usercred-openai-2"},
			{Provider: "anthropic", SecretName: "usercred-anthropic-9"},
		},
	}
	setProviderOAuthFallbackSecrets(secrets, "anthropic", []string{"usercred-anthropic-2", "usercred-anthropic-3"})
	got := secrets.ProviderOAuthFallbackSecrets
	if len(got) != 3 || got[0].SecretName != "usercred-openai-2" ||
		got[1].SecretName != "usercred-anthropic-2" || got[2].SecretName != "usercred-anthropic-3" {
		t.Fatalf("fallbacks = %#v", got)
	}
}

func TestMyCredentialsProtoListsOAuthSubscriptions(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		oauthSlotSecret("ns", "openai", 1, map[string][]byte{
			userCredOAuthJSONKey: []byte("{}"),
			userCredEmailKey:     []byte("one@example.com"),
		}),
		oauthSlotSecret("ns", "openai", 2, map[string][]byte{
			userCredOAuthJSONKey: []byte("{}"),
			userCredAccountIDKey: []byte("acct-two"),
		}),
		oauthSlotSecret("ns", "anthropic", 1, map[string][]byte{userCredOAuthJSONKey: []byte("{}")}),
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	got := srv.myCredentialsProto(context.Background(), "ns")
	if len(got.OauthSubscriptions) != 3 {
		t.Fatalf("OauthSubscriptions = %#v, want 3", got.OauthSubscriptions)
	}
	want := []*platform.ProviderOAuthSubscription{
		{Provider: "openai", Slot: 1, SecretName: "usercred-openai", AccountLabel: "one@example.com"},
		{Provider: "openai", Slot: 2, SecretName: "usercred-openai-2", AccountLabel: "acct-two"},
		{Provider: "anthropic", Slot: 1, SecretName: "usercred-anthropic"},
	}
	for i, w := range want {
		g := got.OauthSubscriptions[i]
		if g.Provider != w.Provider || g.Slot != w.Slot || g.SecretName != w.SecretName || g.AccountLabel != w.AccountLabel {
			t.Fatalf("OauthSubscriptions[%d] = %#v, want %#v", i, g, w)
		}
	}
}

func TestDeleteCredentialSlotKeysSingleMutation(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		oauthSlotSecret("ns", triggersv1alpha1.ProviderOpenAI, 2, map[string][]byte{
			userCredOAuthJSONKey: []byte(`{}`),
			userCredAccountIDKey: []byte("acct"),
			userCredEmailKey:     []byte("two@example.com"),
		}),
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	ctx := context.Background()

	if err := srv.deleteCredentialSlotKeys(ctx, "ns", triggersv1alpha1.ProviderOpenAI, 2,
		userCredOAuthJSONKey, userCredAccountIDKey, userCredEmailKey); err != nil {
		t.Fatalf("deleteCredentialSlotKeys: %v", err)
	}
	secret := &corev1.Secret{}
	err := c.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "usercred-openai-2"}, secret)
	if !k8serrors.IsNotFound(err) {
		t.Fatalf("expected slot secret deleted in one mutation, got err=%v data=%v", err, secret.Data)
	}
	// Idempotent on a missing secret.
	if err := srv.deleteCredentialSlotKeys(ctx, "ns", triggersv1alpha1.ProviderOpenAI, 2, userCredOAuthJSONKey); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestResolveSavedProviderCredentialsUsesSecondaryOnlyOAuth(t *testing.T) {
	scheme := testProjectScheme(t)
	// Slot 1 was disconnected; only slot 3 remains and no API key is saved.
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		oauthSlotSecret("ns", triggersv1alpha1.ProviderOpenAI, 3, map[string][]byte{
			userCredOAuthJSONKey: []byte(`{}`),
		}),
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	creds, err := srv.resolveSavedProviderCredentials(context.Background(), "ns", triggersv1alpha1.ProviderOpenAI, "")
	if err != nil {
		t.Fatalf("resolveSavedProviderCredentials: %v", err)
	}
	if creds.authMode != platformv1alpha1.AgentRunAuthModeOAuth {
		t.Fatalf("authMode = %q, want oauth", creds.authMode)
	}
	if creds.oauthSecretName != "usercred-openai-3" {
		t.Fatalf("oauthSecretName = %q, want usercred-openai-3", creds.oauthSecretName)
	}
	if len(creds.oauthFallbackSecretNames) != 0 {
		t.Fatalf("fallbacks = %v, want none", creds.oauthFallbackSecretNames)
	}
}

func TestAppendAllSavedProviderCredentialsMountsSecondaryOnlyOAuth(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		oauthSlotSecret("ns", triggersv1alpha1.ProviderAnthropic, 2, map[string][]byte{
			userCredOAuthJSONKey: []byte(`{}`),
		}),
		oauthSlotSecret("ns", triggersv1alpha1.ProviderAnthropic, 3, map[string][]byte{
			userCredOAuthJSONKey: []byte(`{}`),
		}),
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	secrets := &platformv1alpha1.AgentRunSecrets{}
	srv.appendAllSavedProviderCredentials(context.Background(), "ns", secrets)

	if len(secrets.ProviderOAuthSecrets) != 1 ||
		secrets.ProviderOAuthSecrets[0].Provider != triggersv1alpha1.ProviderAnthropic ||
		secrets.ProviderOAuthSecrets[0].SecretName != "usercred-anthropic-2" {
		t.Fatalf("ProviderOAuthSecrets = %#v, want lowest slot mounted as the provider OAuth entry", secrets.ProviderOAuthSecrets)
	}
	if len(secrets.ProviderOAuthFallbackSecrets) != 1 ||
		secrets.ProviderOAuthFallbackSecrets[0].SecretName != "usercred-anthropic-3" {
		t.Fatalf("ProviderOAuthFallbackSecrets = %#v, want the remaining slot", secrets.ProviderOAuthFallbackSecrets)
	}
}

func TestEnsureOAuthSubscriptionInProtoPatchesStaleList(t *testing.T) {
	credentials := &platform.MyCredentials{
		OauthSubscriptions: []*platform.ProviderOAuthSubscription{
			{Provider: triggersv1alpha1.ProviderOpenAI, Slot: 1, SecretName: "usercred-openai"},
		},
	}
	// Stale cache omitted the just-written slot 2: it must be patched in, in order.
	ensureOAuthSubscriptionInProto(credentials, triggersv1alpha1.ProviderOpenAI, 2, "acct-2", "two@example.com")
	if len(credentials.OauthSubscriptions) != 2 {
		t.Fatalf("subscriptions = %#v, want slot 2 appended", credentials.OauthSubscriptions)
	}
	sub := credentials.OauthSubscriptions[1]
	if sub.GetSlot() != 2 || sub.GetSecretName() != "usercred-openai-2" || sub.GetAccountLabel() != "two@example.com" {
		t.Fatalf("patched subscription = %#v", sub)
	}
	// Already-present slots are not duplicated.
	ensureOAuthSubscriptionInProto(credentials, triggersv1alpha1.ProviderOpenAI, 2, "acct-2", "two@example.com")
	if len(credentials.OauthSubscriptions) != 2 {
		t.Fatalf("subscriptions duplicated: %#v", credentials.OauthSubscriptions)
	}
}
