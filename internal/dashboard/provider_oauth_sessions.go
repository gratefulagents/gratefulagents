package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

const (
	providerOAuthSessionSecretName = "gratefulagents-provider-oauth-session"
	providerOAuthSessionDataKey    = "session.json"
)

type providerOAuthSessionData struct {
	ID           string        `json:"id"`
	Provider     string        `json:"provider"`
	Started      time.Time     `json:"started"`
	Verifier     string        `json:"verifier,omitempty"`
	State        string        `json:"state,omitempty"`
	DeviceAuthID string        `json:"device_auth_id,omitempty"`
	UserCode     string        `json:"user_code,omitempty"`
	PollInterval time.Duration `json:"poll_interval,omitempty"`
	NextPollAt   time.Time     `json:"next_poll_at,omitempty"`
	InFlight     bool          `json:"in_flight"`
}

func providerOAuthSessionToData(session providerOAuthSession) providerOAuthSessionData {
	return providerOAuthSessionData{
		ID: session.id, Provider: session.provider, Started: session.started,
		Verifier: session.verifier, State: session.state, DeviceAuthID: session.deviceAuthID,
		UserCode: session.userCode, PollInterval: session.pollInterval,
		NextPollAt: session.nextPollAt, InFlight: session.inFlight,
	}
}

func (data providerOAuthSessionData) session() providerOAuthSession {
	return providerOAuthSession{
		id: data.ID, provider: data.Provider, started: data.Started,
		verifier: data.Verifier, state: data.State, deviceAuthID: data.DeviceAuthID,
		userCode: data.UserCode, pollInterval: data.PollInterval,
		nextPollAt: data.NextPollAt, inFlight: data.InFlight,
	}
}

func encodeProviderOAuthSession(session providerOAuthSession) ([]byte, error) {
	return json.Marshal(providerOAuthSessionToData(session))
}

func decodeProviderOAuthSession(secret *corev1.Secret) (providerOAuthSession, error) {
	raw := secret.Data[providerOAuthSessionDataKey]
	if len(raw) == 0 {
		return providerOAuthSession{}, fmt.Errorf("OAuth session secret has no session data")
	}
	var data providerOAuthSessionData
	if err := json.Unmarshal(raw, &data); err != nil {
		return providerOAuthSession{}, fmt.Errorf("decode OAuth session: %w", err)
	}
	if strings.TrimSpace(data.ID) == "" || strings.TrimSpace(data.Provider) == "" || data.Started.IsZero() {
		return providerOAuthSession{}, fmt.Errorf("OAuth session secret is incomplete")
	}
	return data.session(), nil
}

func providerOAuthStoreError(action string, err error) error {
	if err == nil {
		return nil
	}
	if connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("%s provider sign-in session: %w", action, err))
}

func providerOAuthRetryable(err error) bool {
	return k8serrors.IsConflict(err) || k8serrors.IsAlreadyExists(err)
}

func (s *Server) reserveProviderOAuthStart(ctx context.Context, key, provider, sessionID string) error {
	if s.providerOAuthKube == nil {
		return s.reserveProviderOAuthStartMemory(key, provider, sessionID)
	}
	secrets := s.providerOAuthKube.CoreV1().Secrets(key)
	now := time.Now()
	reservation := providerOAuthSession{id: sessionID, provider: provider, started: now, inFlight: true}
	raw, err := encodeProviderOAuthSession(reservation)
	if err != nil {
		return providerOAuthStoreError("encode", err)
	}
	err = retry.OnError(retry.DefaultBackoff, providerOAuthRetryable, func() error {
		secret, getErr := secrets.Get(ctx, providerOAuthSessionSecretName, metav1.GetOptions{})
		if k8serrors.IsNotFound(getErr) {
			_, createErr := secrets.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: providerOAuthSessionSecretName, Namespace: key},
				Type:       corev1.SecretTypeOpaque,
				Data:       map[string][]byte{providerOAuthSessionDataKey: raw},
			}, metav1.CreateOptions{})
			return createErr
		}
		if getErr != nil {
			return getErr
		}
		existing, decodeErr := decodeProviderOAuthSession(secret)
		if decodeErr != nil {
			return decodeErr
		}
		if now.Sub(existing.started) <= providerOAuthTTL {
			if existing.inFlight {
				return connect.NewError(connect.CodeAborted, fmt.Errorf("a provider sign-in request is already in progress"))
			}
			if now.Sub(existing.started) < time.Second {
				return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("wait a moment before restarting provider sign-in"))
			}
		}
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{providerOAuthSessionDataKey: raw}
		_, updateErr := secrets.Update(ctx, secret, metav1.UpdateOptions{})
		return updateErr
	})
	return providerOAuthStoreError("reserve", err)
}

func (s *Server) publishProviderOAuthStart(ctx context.Context, key, sessionID string, session providerOAuthSession) (bool, error) {
	if s.providerOAuthKube == nil {
		return s.publishProviderOAuthStartMemory(key, sessionID, session), nil
	}
	secrets := s.providerOAuthKube.CoreV1().Secrets(key)
	published := false
	err := retry.OnError(retry.DefaultBackoff, providerOAuthRetryable, func() error {
		published = false
		secret, err := secrets.Get(ctx, providerOAuthSessionSecretName, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		reservation, err := decodeProviderOAuthSession(secret)
		if err != nil {
			return err
		}
		if reservation.id != sessionID || !reservation.inFlight {
			return nil
		}
		session.inFlight = false
		raw, err := encodeProviderOAuthSession(session)
		if err != nil {
			return err
		}
		secret.Data = map[string][]byte{providerOAuthSessionDataKey: raw}
		if _, err = secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return err
		}
		published = true
		return nil
	})
	return published, providerOAuthStoreError("publish", err)
}

func (s *Server) acquireProviderOAuthSession(ctx context.Context, key, provider, sessionID string, poll bool) (providerOAuthSession, error) {
	if s.providerOAuthKube == nil {
		return s.acquireProviderOAuthSessionMemory(key, provider, sessionID, poll)
	}
	secrets := s.providerOAuthKube.CoreV1().Secrets(key)
	var acquired providerOAuthSession
	err := retry.OnError(retry.DefaultBackoff, providerOAuthRetryable, func() error {
		acquired = providerOAuthSession{}
		secret, err := secrets.Get(ctx, providerOAuthSessionSecretName, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no matching %s sign-in in progress; start again", provider))
		}
		if err != nil {
			return err
		}
		session, err := decodeProviderOAuthSession(secret)
		if err != nil {
			return err
		}
		if session.provider != provider || session.id == "" || session.id != strings.TrimSpace(sessionID) {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no matching %s sign-in in progress; start again", provider))
		}
		if time.Since(session.started) > providerOAuthTTL {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s sign-in expired; start again", provider))
		}
		if session.inFlight {
			if poll {
				return nil
			}
			return connect.NewError(connect.CodeAborted, fmt.Errorf("%s sign-in completion is already in progress", provider))
		}
		now := time.Now()
		if poll && now.Before(session.nextPollAt) {
			return nil
		}
		session.inFlight = true
		if poll {
			session.nextPollAt = now.Add(session.pollInterval)
		}
		raw, err := encodeProviderOAuthSession(session)
		if err != nil {
			return err
		}
		secret.Data = map[string][]byte{providerOAuthSessionDataKey: raw}
		if _, err = secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return err
		}
		acquired = session
		return nil
	})
	return acquired, providerOAuthStoreError("acquire", err)
}

func (s *Server) providerOAuthSessionIsCurrent(ctx context.Context, key, sessionID string) (bool, error) {
	if s.providerOAuthKube == nil {
		return s.providerOAuthSessionIsCurrentMemory(key, sessionID), nil
	}
	secret, err := s.providerOAuthKube.CoreV1().Secrets(key).Get(ctx, providerOAuthSessionSecretName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, providerOAuthStoreError("read", err)
	}
	session, err := decodeProviderOAuthSession(secret)
	if err != nil {
		return false, providerOAuthStoreError("decode", err)
	}
	return session.id == sessionID && session.inFlight, nil
}

func (s *Server) releaseProviderOAuthSession(ctx context.Context, key, sessionID string) {
	if s.providerOAuthKube == nil {
		s.releaseProviderOAuthSessionMemory(key, sessionID)
		return
	}
	secrets := s.providerOAuthKube.CoreV1().Secrets(key)
	_ = retry.OnError(retry.DefaultBackoff, providerOAuthRetryable, func() error {
		secret, err := secrets.Get(ctx, providerOAuthSessionSecretName, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		session, err := decodeProviderOAuthSession(secret)
		if err != nil || session.id != sessionID {
			return err
		}
		session.inFlight = false
		raw, err := encodeProviderOAuthSession(session)
		if err != nil {
			return err
		}
		secret.Data = map[string][]byte{providerOAuthSessionDataKey: raw}
		_, err = secrets.Update(ctx, secret, metav1.UpdateOptions{})
		return err
	})
}

func (s *Server) deleteProviderOAuthSession(ctx context.Context, key, sessionID string) {
	if s.providerOAuthKube == nil {
		s.deleteProviderOAuthSessionMemory(key, sessionID)
		return
	}
	secrets := s.providerOAuthKube.CoreV1().Secrets(key)
	_ = retry.OnError(retry.DefaultBackoff, providerOAuthRetryable, func() error {
		secret, err := secrets.Get(ctx, providerOAuthSessionSecretName, metav1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		session, err := decodeProviderOAuthSession(secret)
		if err != nil || session.id != sessionID {
			return err
		}
		uid := secret.UID
		resourceVersion := secret.ResourceVersion
		err = secrets.Delete(ctx, providerOAuthSessionSecretName, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &resourceVersion},
		})
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return err
	})
}
