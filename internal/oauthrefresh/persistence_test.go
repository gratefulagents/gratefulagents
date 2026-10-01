package oauthrefresh

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	oauth "github.com/gratefulagents/sdk/pkg/agentsdk/providers/oauth"
)

func persistenceRefresher(t *testing.T, provider string) (*Refresher, oauthSecretRef, *atomic.Int32) {
	t.Helper()
	now := time.Now().UTC()
	auth := `{"access_token":"old-access","refresh_token":"old-refresh","expired":"` + now.Add(-time.Hour).Format(time.RFC3339Nano) + `","type":"claude"}`
	response := `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`
	switch provider {
	case triggersv1alpha1.ProviderOpenAI:
		auth = `{"tokens":{"access_token":"e30.eyJleHAiOjF9.sig","refresh_token":"old-refresh","account_id":"acct"}}`
	case triggersv1alpha1.ProviderCopilot:
		auth = `{"oauth_token":"github-oauth","type":"copilot"}`
		response = `{"token":"new-access","expires_at":` + strconv.FormatInt(now.Add(time.Hour).Unix(), 10) + `}`
	}
	secret := secretWithAuthJSON("rotating-secret", auth)
	secret.UID = "original-secret"
	calls := &atomic.Int32{}
	r := newTestRefresher(t, now, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return jsonResponse(http.StatusOK, response), nil
	}), secret)
	ref := oauthSecretRef{NamespacedName: types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, Provider: provider}
	return r, ref, calls
}

func TestRefreshPersistenceRetriesLaterWithoutExchange(t *testing.T) {
	for _, provider := range []string{triggersv1alpha1.ProviderAnthropic, triggersv1alpha1.ProviderOpenAI, triggersv1alpha1.ProviderCopilot} {
		t.Run(provider, func(t *testing.T) {
			r, ref, calls := persistenceRefresher(t, provider)
			base := r.client.(client.WithWatch)
			updates := 0
			r.client = interceptor.NewClient(base, interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					updates++
					if updates == 1 {
						return apierrors.NewServiceUnavailable("temporary outage")
					}
					return c.Update(ctx, obj, opts...)
				},
			})
			if err := r.refreshSecret(context.Background(), ref); err == nil {
				t.Fatal("expected initial Update failure")
			}
			pending, ok := r.pending[ref.NamespacedName]
			if !ok || !bytes.Contains(pending.updated, []byte("new-access")) {
				t.Fatal("exchanged credentials not retained")
			}
			if len(r.staleSecrets) != 0 {
				t.Fatal("persistence failure marked credentials stale")
			}
			secret := &corev1.Secret{}
			if err := base.Get(context.Background(), ref.NamespacedName, secret); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(secret.Data[oauth.AuthJSONKey], pending.original) {
				t.Fatal("failed write changed persisted credentials")
			}
			secret.Data["unrelated"] = []byte("latest")
			secret.Labels = map[string]string{"new-label": "latest"}
			if err := base.Update(context.Background(), secret); err != nil {
				t.Fatal(err)
			}
			if err := r.refreshSecret(context.Background(), ref); err != nil {
				t.Fatal(err)
			}
			if err := base.Get(context.Background(), ref.NamespacedName, secret); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(secret.Data[oauth.AuthJSONKey], pending.updated) || string(secret.Data["unrelated"]) != "latest" || secret.Labels["new-label"] != "latest" {
				t.Fatal("retry did not preserve exchanged credentials and latest unrelated fields")
			}
			if err := r.refreshSecret(context.Background(), ref); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || updates != 2 || len(r.pending) != 0 {
				t.Fatalf("exchanges=%d updates=%d pending=%d", calls.Load(), updates, len(r.pending))
			}
		})
	}
}

func TestRefreshPersistenceConflictPreservesLatestSecret(t *testing.T) {
	for _, replacement := range []string{"unrelated", "credentials", "account", "recreated"} {
		t.Run(replacement, func(t *testing.T) {
			r, ref, calls := persistenceRefresher(t, triggersv1alpha1.ProviderAnthropic)
			base := r.client.(client.WithWatch)
			updates := 0
			var external *corev1.Secret
			r.client = interceptor.NewClient(base, interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					updates++
					if updates == 1 {
						external = &corev1.Secret{}
						if err := c.Get(ctx, ref.NamespacedName, external); err != nil {
							return err
						}
						external.Annotations = map[string]string{"external": "latest"}
						external.Data["unrelated"] = []byte("latest")
						switch replacement {
						case "credentials":
							external.Data[oauth.AuthJSONKey] = []byte(`{"refresh_token":"reconnected"}`)
						case "account":
							external.Data["account-id"] = []byte("reconnected-account")
						case "recreated":
							if err := c.Delete(ctx, external); err != nil {
								return err
							}
							external.ResourceVersion = ""
							external.UID = "replacement-secret"
							if err := c.Create(ctx, external); err != nil {
								return err
							}
							return apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, ref.Name, errors.New("recreated"))
						}
						if err := c.Update(ctx, external); err != nil {
							return err
						}
					}
					return c.Update(ctx, obj, opts...)
				},
			})
			if err := r.refreshSecret(context.Background(), ref); err != nil {
				t.Fatal(err)
			}
			secret := &corev1.Secret{}
			if err := base.Get(context.Background(), ref.NamespacedName, secret); err != nil {
				t.Fatal(err)
			}
			if secret.Annotations["external"] != "latest" || string(secret.Data["unrelated"]) != "latest" {
				t.Fatal("unrelated external fields lost")
			}
			if replacement == "unrelated" {
				if updates != 2 || !bytes.Contains(secret.Data[oauth.AuthJSONKey], []byte("new-refresh")) {
					t.Fatal("conflict retry did not persist rotated credentials")
				}
			} else if updates != 1 || !bytes.Equal(secret.Data[oauth.AuthJSONKey], external.Data[oauth.AuthJSONKey]) || !bytes.Equal(secret.Data["account-id"], external.Data["account-id"]) || secret.UID != external.UID || len(r.pending) != 0 {
				t.Fatal("external credential replacement not preserved")
			}
			if calls.Load() != 1 {
				t.Fatalf("exchanges=%d, want 1", calls.Load())
			}
		})
	}
}

func TestRefreshPersistenceRetainsAcrossConflictExhaustionAndCancellation(t *testing.T) {
	for _, cancelDuringWrite := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelDuringWrite), func(t *testing.T) {
			r, ref, calls := persistenceRefresher(t, triggersv1alpha1.ProviderAnthropic)
			base := r.client.(client.WithWatch)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			updates := 0
			r.client = interceptor.NewClient(base, interceptor.Funcs{
				Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
					updates++
					if cancelDuringWrite {
						cancel()
					}
					return apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, ref.Name, errors.New("conflict"))
				},
			})
			err := r.refreshSecret(ctx, ref)
			if err == nil || (cancelDuringWrite && !errors.Is(err, context.Canceled)) {
				t.Fatalf("unexpected error: %v", err)
			}
			wantUpdates := retry.DefaultBackoff.Steps
			if cancelDuringWrite {
				wantUpdates = 1
			}
			if updates != wantUpdates || len(r.pending) != 1 {
				t.Fatalf("updates=%d want=%d pending=%d", updates, wantUpdates, len(r.pending))
			}
			r.client = base
			if err := r.refreshSecret(context.Background(), ref); err != nil {
				t.Fatal(err)
			}
			if auth := reloadAnthropicAuth(t, r, ref.NamespacedName); auth.RefreshToken != "new-refresh" || calls.Load() != 1 {
				t.Fatalf("retry failed to persist exchanged credentials; exchanges=%d", calls.Load())
			}
		})
	}
}

func TestRefreshPersistenceHandlesAmbiguousWriteAndStaleCache(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(strconv.FormatBool(ambiguous), func(t *testing.T) {
			r, ref, calls := persistenceRefresher(t, triggersv1alpha1.ProviderAnthropic)
			base := r.client.(client.WithWatch)
			original := &corev1.Secret{}
			if err := base.Get(context.Background(), ref.NamespacedName, original); err != nil {
				t.Fatal(err)
			}
			updates := 0
			r.client = interceptor.NewClient(base, interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					updates++
					if err := c.Update(ctx, obj, opts...); err != nil {
						return err
					}
					if ambiguous {
						return context.DeadlineExceeded
					}
					return nil
				},
			})
			if err := r.refreshSecret(context.Background(), ref); (err != nil) != ambiguous {
				t.Fatalf("unexpected initial write error: %v", err)
			}
			r.client = interceptor.NewClient(r.client.(client.WithWatch), interceptor.Funcs{
				Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					original.DeepCopyInto(obj.(*corev1.Secret))
					return nil
				},
			})
			if err := r.refreshSecret(context.Background(), ref); err == nil {
				t.Fatal("expected conflict exhaustion for stale cached Secret")
			}
			r.client = interceptor.NewClient(base, interceptor.Funcs{
				Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
					return apierrors.NewServiceUnavailable("read outage")
				},
			})
			if err := r.refreshSecret(context.Background(), ref); err == nil || len(r.pending) != 1 {
				t.Fatal("read failure lost pending credentials")
			}
			r.client = base
			if err := r.refreshSecret(context.Background(), ref); err != nil {
				t.Fatal(err)
			}
			if auth := reloadAnthropicAuth(t, r, ref.NamespacedName); auth.RefreshToken != "new-refresh" || calls.Load() != 1 || len(r.pending) != 0 || updates != 1+retry.DefaultBackoff.Steps {
				t.Fatalf("exchanges=%d updates=%d pending=%d", calls.Load(), updates, len(r.pending))
			}
		})
	}
}

func TestRefreshPersistenceCancellationAfterExchangeRetainsResult(t *testing.T) {
	r, ref, calls := persistenceRefresher(t, triggersv1alpha1.ProviderAnthropic)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := r.httpClient.Transport
	r.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(req)
		cancel()
		return response, err
	})
	if err := r.refreshSecret(ctx, ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation after successful exchange, got %v", err)
	}
	if len(r.pending) != 1 {
		t.Fatal("cancellation lost exchanged credentials")
	}
	if err := r.refreshSecret(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if auth := reloadAnthropicAuth(t, r, ref.NamespacedName); auth.RefreshToken != "new-refresh" || calls.Load() != 1 {
		t.Fatalf("retry failed to persist exchanged credentials; exchanges=%d", calls.Load())
	}
}

func TestRefreshPersistenceLaterTickPreservesReconnect(t *testing.T) {
	r, ref, calls := persistenceRefresher(t, triggersv1alpha1.ProviderAnthropic)
	base := r.client.(client.WithWatch)
	r.client = interceptor.NewClient(base, interceptor.Funcs{
		Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
			return apierrors.NewServiceUnavailable("outage")
		},
	})
	if err := r.refreshSecret(context.Background(), ref); err == nil {
		t.Fatal("expected Update failure")
	}
	secret := &corev1.Secret{}
	if err := base.Get(context.Background(), ref.NamespacedName, secret); err != nil {
		t.Fatal(err)
	}
	reconnected := []byte(`{"access_token":"reconnected","refresh_token":"user-refresh"}`)
	secret.Data[oauth.AuthJSONKey] = reconnected
	if err := base.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	r.client = base
	if err := r.refreshSecret(context.Background(), ref); err != nil {
		t.Fatal(err)
	}
	if err := base.Get(context.Background(), ref.NamespacedName, secret); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(secret.Data[oauth.AuthJSONKey], reconnected) || calls.Load() != 1 || len(r.pending) != 0 {
		t.Fatal("pending exchange was not discarded in favor of user reconnect")
	}
}

func TestRefreshPersistenceSerializesExchangeAndCancelsWaiter(t *testing.T) {
	r, ref, calls := persistenceRefresher(t, triggersv1alpha1.ProviderAnthropic)
	transport := r.httpClient.Transport
	entered := make(chan struct{})
	release := make(chan struct{})
	r.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return transport.RoundTrip(req)
	})
	results := make(chan error, 9)
	go func() { results <- r.refreshSecret(context.Background(), ref) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.refreshSecret(ctx, ref); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled waiter error = %v", err)
	}
	for range 8 {
		go func() { results <- r.refreshSecret(context.Background(), ref) }()
	}
	close(release)
	for range 9 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("exchanges=%d, want 1", calls.Load())
	}
}
