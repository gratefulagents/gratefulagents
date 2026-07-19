package dashboard

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// maxAgentRunDisplayNameLen bounds the user-supplied run title written to
// status.displayName.
const maxAgentRunDisplayNameLen = 120

// patchAgentRunStatusWithRetry re-fetches the run and applies mutate under
// optimistic-concurrency retry so user decisions (approvals, change requests)
// are never silently lost to a stale read or a write conflict.
func (s *Server) patchAgentRunStatusWithRetry(ctx context.Context, namespace, name string, mutate func(*platformv1alpha1.AgentRun)) error {
	key := client.ObjectKey{Namespace: namespace, Name: name}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := s.k8sClient.Get(ctx, key, fresh); err != nil {
			return err
		}
		patch := client.MergeFrom(fresh.DeepCopy())
		mutate(fresh)
		return s.k8sClient.Status().Patch(ctx, fresh, patch)
	})
}

func (s *Server) patchAgentRunWithRetry(ctx context.Context, namespace, name string, mutate func(*platformv1alpha1.AgentRun) error) (*platformv1alpha1.AgentRun, error) {
	key := client.ObjectKey{Namespace: namespace, Name: name}
	var updated *platformv1alpha1.AgentRun
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := s.k8sClient.Get(ctx, key, fresh); err != nil {
			return err
		}
		patch := client.MergeFrom(fresh.DeepCopy())
		if err := mutate(fresh); err != nil {
			return err
		}
		if err := s.k8sClient.Patch(ctx, fresh, patch); err != nil {
			return err
		}
		updated = fresh.DeepCopy()
		return nil
	})
	return updated, err
}

// DeleteAgentRun deletes an AgentRun when the caller is the owner or an admin.
func (s *Server) DeleteAgentRun(ctx context.Context, req *platform.DeleteAgentRunRequest) (*emptypb.Empty, error) {
	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, run); err != nil {
		return nil, mapK8sError("get AgentRun", err)
	}

	pb, err := s.enrichAgentRunProto(ctx, k8sAgentRunToProto(run))
	if err != nil {
		return nil, err
	}
	if pb.MyPermission != "owner" && pb.MyPermission != "admin" {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only the owner or admin can delete this run"))
	}
	if err := s.k8sClient.Delete(ctx, run); err != nil {
		return nil, mapK8sError("delete AgentRun", err)
	}
	return &emptypb.Empty{}, nil
}

// CancelAgentRun requests graceful cancellation of an active AgentRun.
func (s *Server) CancelAgentRun(ctx context.Context, req *platform.CancelAgentRunRequest) (*platform.AgentRun, error) {
	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, run); err != nil {
		return nil, mapK8sError("get AgentRun", err)
	}

	pb, err := s.enrichAgentRunProto(ctx, k8sAgentRunToProto(run))
	if err != nil {
		return nil, err
	}
	if pb.MyPermission != "owner" && pb.MyPermission != "admin" {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only the owner or admin can stop this run"))
	}
	if isTerminalAgentRunPhase(run.Status.Phase) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot stop terminal run in phase %s", run.Status.Phase))
	}

	updated, err := s.patchAgentRunWithRetry(ctx, req.Namespace, req.Name, func(fresh *platformv1alpha1.AgentRun) error {
		if isTerminalAgentRunPhase(fresh.Status.Phase) {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot stop terminal run in phase %s", fresh.Status.Phase))
		}
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		fresh.Annotations[cancelRequestedAnnotation] = time.Now().UTC().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, mapK8sError("cancel AgentRun", err)
	}
	return s.enrichAgentRunProto(ctx, k8sAgentRunToProto(updated))
}

// PromoteAgentRun marks a non-terminal AgentRun as Succeeded on the user's
// explicit request. The controller tears the runner down exactly like a
// cancellation, but records the terminal phase as Succeeded.
func (s *Server) PromoteAgentRun(ctx context.Context, req *platform.PromoteAgentRunRequest) (*platform.AgentRun, error) {
	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, run); err != nil {
		return nil, mapK8sError("get AgentRun", err)
	}

	pb, err := s.enrichAgentRunProto(ctx, k8sAgentRunToProto(run))
	if err != nil {
		return nil, err
	}
	if pb.MyPermission != "owner" && pb.MyPermission != "admin" {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only the owner or admin can promote this run"))
	}
	if isTerminalAgentRunPhase(run.Status.Phase) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot promote terminal run in phase %s", run.Status.Phase))
	}

	updated, err := s.patchAgentRunWithRetry(ctx, req.Namespace, req.Name, func(fresh *platformv1alpha1.AgentRun) error {
		if isTerminalAgentRunPhase(fresh.Status.Phase) {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot promote terminal run in phase %s", fresh.Status.Phase))
		}
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		fresh.Annotations[promoteSucceededAnnotation] = time.Now().UTC().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, mapK8sError("promote AgentRun", err)
	}
	return s.enrichAgentRunProto(ctx, k8sAgentRunToProto(updated))
}

// InterruptAgentRun asks the runner to stop the run's in-flight turn — the
// current model call, running tools, and any active sub-agents — without
// terminating the run. The request is recorded on the Postgres session; the
// runner's per-turn watcher picks it up, cancels the turn, and the session
// stays alive awaiting the user's next message.
func (s *Server) InterruptAgentRun(ctx context.Context, req *platform.InterruptAgentRunRequest) (*platform.InterruptAgentRunResponse, error) {
	if err := s.requireAgentRunCollaborator(ctx, req.Namespace, req.Name, "interrupt"); err != nil {
		return nil, err
	}

	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, run); err != nil {
		return nil, mapK8sError("get AgentRun", err)
	}
	if isTerminalAgentRunPhase(run.Status.Phase) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot interrupt terminal run in phase %s", run.Status.Phase))
	}
	if s.stateStore == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("session store is not configured"))
	}

	sess, err := s.stateStore.GetSessionByRun(ctx, req.Name, req.Namespace)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run has no active session to interrupt"))
	}

	actor := resolveActorLabel(ctx)
	if err := sessionclient.RequestInterrupt(ctx, s.stateStore, sess.ID, actor); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("recording interrupt request: %w", err))
	}
	if _, err := s.stateStore.WriteActivityEvent(ctx, sess.ID, "interrupt_requested", fmt.Sprintf("Stop requested by %s — interrupting the current turn", actor), nil); err != nil {
		log.Printf("WARN: failed to write interrupt_requested activity for %s/%s: %v", req.Namespace, req.Name, err)
	}
	return &platform.InterruptAgentRunResponse{}, nil
}

// RetryAgentRun resumes a failed or user-stopped AgentRun from its persisted session.
func (s *Server) RetryAgentRun(ctx context.Context, req *platform.RetryAgentRunRequest) (*platform.AgentRun, error) {
	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, run); err != nil {
		return nil, mapK8sError("get AgentRun", err)
	}

	pb, err := s.enrichAgentRunProto(ctx, k8sAgentRunToProto(run))
	if err != nil {
		return nil, err
	}
	if pb.MyPermission != "owner" && pb.MyPermission != "admin" {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only the owner or admin can retry this run"))
	}
	if run.Status.Phase != platformv1alpha1.AgentRunPhaseFailed && run.Status.Phase != platformv1alpha1.AgentRunPhaseCancelled {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("can only retry failed or stopped runs, got phase %s", run.Status.Phase))
	}

	message := strings.TrimSpace(req.GetMessage())
	if message == "" {
		if run.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
			message = "Resume requested — continue from where the run stopped."
		} else {
			message = "Retry requested — continue from where the run failed."
		}
	}

	if s.stateStore != nil {
		idempotencyKey := strings.TrimSpace(req.GetIdempotencyKey())
		if idempotencyKey == "" {
			// Backward-compatible clients receive deterministic replay protection
			// for the same run generation and message.
			idempotencyKey = fmt.Sprintf("retry:%s:%d:%s", run.UID, run.Spec.WakeRequests+1, message)
		}
		if err := orchestration.WakeAgentRunIdempotent(ctx, s.k8sClient, s.stateStore, req.Namespace, req.Name, message, idempotencyKey,
			platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhaseCancelled); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		updated := &platformv1alpha1.AgentRun{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, updated); err != nil {
			return nil, mapK8sError("get AgentRun", err)
		}
		return s.enrichAgentRunProto(ctx, k8sAgentRunToProto(updated))
	}

	updated, err := s.patchAgentRunWithRetry(ctx, req.Namespace, req.Name, func(fresh *platformv1alpha1.AgentRun) error {
		if fresh.Status.Phase != platformv1alpha1.AgentRunPhaseFailed && fresh.Status.Phase != platformv1alpha1.AgentRunPhaseCancelled {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("can only retry failed or stopped runs, got phase %s", fresh.Status.Phase))
		}
		fresh.Spec.WakeRequests++
		return nil
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, mapK8sError("retry AgentRun", err)
	}
	return s.enrichAgentRunProto(ctx, k8sAgentRunToProto(updated))
}

// RenameAgentRun sets a human-readable display name on a run so users can
// recognize it in the UI instead of the generated resource name. The same
// status.displayName field is also set autonomously by the agent's
// set_display_name tool.
func (s *Server) RenameAgentRun(ctx context.Context, req *platform.RenameAgentRunRequest) (*platform.AgentRun, error) {
	if err := s.requireAgentRunCollaborator(ctx, req.Namespace, req.Name, "rename"); err != nil {
		return nil, err
	}

	displayName := strings.TrimSpace(req.GetDisplayName())
	if displayName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("display_name is required"))
	}
	if len(displayName) > maxAgentRunDisplayNameLen {
		displayName = strings.TrimSpace(displayName[:maxAgentRunDisplayNameLen])
	}

	if err := s.patchAgentRunStatusWithRetry(ctx, req.Namespace, req.Name, func(fresh *platformv1alpha1.AgentRun) {
		fresh.Status.DisplayName = displayName
	}); err != nil {
		return nil, mapK8sError("rename AgentRun", err)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, updated); err != nil {
		return nil, mapK8sError("get AgentRun", err)
	}
	return s.enrichAgentRunProto(ctx, k8sAgentRunToProto(updated))
}

// UpdateAgentRunRuntimeConfig changes the provider/model/reasoning used by
// subsequent turns of a non-terminal run. It updates only explicit run runtime
// fields; modes remain behavior-only.
func (s *Server) UpdateAgentRunRuntimeConfig(ctx context.Context, req *platform.UpdateAgentRunRuntimeConfigRequest) (*platform.AgentRun, error) {
	if strings.TrimSpace(req.GetProvider()) == "" && strings.TrimSpace(req.GetModel()) == "" && !req.GetUpdateReasoningLevel() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("provider, model, or update_reasoning_level is required"))
	}
	if err := s.requireAgentRunCollaborator(ctx, req.Namespace, req.Name, "update runtime config for"); err != nil {
		return nil, err
	}

	var reasoningLevel platformv1alpha1.ModeReasoningLevel
	if req.GetUpdateReasoningLevel() {
		var err error
		reasoningLevel, err = resolveReasoningLevel(req.GetReasoningLevel())
		if err != nil {
			return nil, err
		}
	}

	restarted := false
	switchedProvider := ""
	switchedModel := ""
	updated, err := s.patchAgentRunWithRetry(ctx, req.Namespace, req.Name, func(fresh *platformv1alpha1.AgentRun) error {
		restarted = false
		if isTerminalAgentRunPhase(fresh.Status.Phase) {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot update runtime config for terminal run in phase %s", fresh.Status.Phase))
		}

		model, provider, providerChanged, err := resolveRuntimeConfigUpdateTarget(fresh.Spec.Model, req.GetProvider(), req.GetModel())
		if err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		authMode := triggersv1alpha1.NormalizeAuthMode(string(fresh.Spec.AuthMode))

		switch {
		case !providerChanged:
			if err := validateActiveRunRuntimeCredentials(fresh, provider, authMode); err != nil {
				return err
			}
		case activeRunHasMountedProviderOAuth(fresh, provider, authMode):
			// Live OAuth switch: the target provider's OAuth material is
			// already mounted in the pod (spec.secrets.providerOAuthSecrets),
			// so the next turn authenticates with it without a restart. Point
			// the primary OAuth secret at the target so future pod restarts
			// and same-provider updates stay consistent.
			if name := mountedProviderOAuthSecretName(fresh, provider); name != "" {
				fresh.Spec.Secrets.OpenAIOAuthSecret = name
			}
			fresh.Spec.OpenAIBaseURL = triggersv1alpha1.ResolveOpenAIBaseURLWithAuth(provider, "", authMode)
		case activeRunHasMountedProviderKey(fresh, provider, authMode):
			// Live switch: the target provider's API key is already mounted
			// in the pod, so the next turn picks it up without a restart.
			// Reaching here on an OAuth run means the target is not
			// OAuth-capable: rewrite the stored auth mode to api-key and drop
			// the now-foreign OAuth secret so the spec stays valid for later
			// runtime updates and pod restarts.
			if authMode == platformv1alpha1.AgentRunAuthModeOAuth {
				authMode = platformv1alpha1.AgentRunAuthModeAPIKey
				fresh.Spec.AuthMode = platformv1alpha1.AgentRunAuthModeAPIKey
				if fresh.Spec.Secrets != nil {
					fresh.Spec.Secrets.OpenAIOAuthSecret = ""
				}
			}
			fresh.Spec.OpenAIBaseURL = triggersv1alpha1.ResolveOpenAIBaseURLWithAuth(provider, "", authMode)
		default:
			// The live pod lacks credentials for the target provider (e.g. an
			// OAuth switch): rewrite the run's credential spec from the
			// caller's saved credentials and request a compute restart — the
			// run resumes from its persisted session on a fresh pod.
			creds, err := s.resolveSavedProviderCredentials(ctx, req.Namespace, provider, "")
			if err != nil {
				return err
			}
			fresh.Spec.AuthMode = creds.authMode
			if fresh.Spec.Secrets == nil {
				fresh.Spec.Secrets = &platformv1alpha1.AgentRunSecrets{}
			}
			fresh.Spec.Secrets.OpenAIOAuthSecret = creds.oauthSecretName
			fresh.Spec.Secrets.ProviderKeys = mergeProviderKeys(fresh.Spec.Secrets.ProviderKeys, creds.providerKeys)
			fresh.Spec.OpenAIBaseURL = triggersv1alpha1.ResolveOpenAIBaseURLWithAuth(provider, "", creds.authMode)
			// The replacement pod mounts every saved credential so subsequent
			// switches apply live instead of needing another restart.
			s.appendAllSavedProviderCredentials(ctx, req.Namespace, fresh.Spec.Secrets)
			fresh.Spec.RestartRequests++
			restarted = true
		}

		if model != "" {
			fresh.Spec.Model = runtimeConfigStoredModel(model, provider)
		}
		if req.GetUpdateReasoningLevel() {
			fresh.Spec.ReasoningLevel = reasoningLevel
		}
		switchedProvider = provider
		switchedModel = model
		return nil
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, mapK8sError("update AgentRun runtime config", err)
	}
	if restarted {
		s.recordRuntimeConfigRestart(ctx, req.Namespace, req.Name, switchedProvider, switchedModel)
	}
	return s.enrichAgentRunProto(ctx, k8sAgentRunToProto(updated))
}

func resolveRuntimeConfigUpdateTarget(currentModel, providerOverride, modelOverride string) (model, provider string, providerChanged bool, err error) {
	currentProvider, currentBareModel, err := runtimeProviderAndBareModel(currentModel)
	if err != nil {
		return "", "", false, err
	}
	provider = currentProvider
	model = currentBareModel

	if override := strings.TrimSpace(providerOverride); override != "" {
		provider, err = resolveProvider(override, "")
		if err != nil {
			return "", "", false, err
		}
	}
	if override := strings.TrimSpace(modelOverride); override != "" {
		if strings.TrimSpace(providerOverride) == "" {
			prefixProvider, bareModel := resolveProviderFromModel(override)
			if prefixProvider != "" {
				resolvedPrefix, prefixErr := resolveProvider(prefixProvider, "")
				if prefixErr != nil {
					return "", "", false, prefixErr
				}
				provider = resolvedPrefix
				override = bareModel
			}
		} else if prefix, bareModel := resolveProviderFromModel(override); strings.EqualFold(prefix, provider) {
			override = bareModel
		}
		model = override
	}
	model, err = effectiveModelForProvider(model, provider)
	if err != nil {
		return "", "", false, err
	}
	return model, provider, provider != currentProvider, nil
}

func runtimeProviderAndBareModel(model string) (provider, bareModel string, err error) {
	prefixProvider, bareModel := resolveProviderFromModel(strings.TrimSpace(model))
	if prefixProvider == "" {
		provider = triggersv1alpha1.ProviderOpenAI
	} else if provider, err = resolveProvider(prefixProvider, ""); err != nil {
		return "", "", err
	}
	return provider, bareModel, nil
}

func runtimeConfigStoredModel(model, provider string) string {
	model = strings.TrimSpace(model)
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || provider == triggersv1alpha1.ProviderOpenAI {
		return model
	}
	if prefix, _ := resolveProviderFromModel(model); strings.EqualFold(prefix, provider) {
		return model
	}
	return provider + "/" + model
}

// validateActiveRunRuntimeCredentials checks that a same-provider runtime
// update (model/reasoning change) still has usable credentials mounted on the
// run; it rides the existing material, so no restart is needed.
func validateActiveRunRuntimeCredentials(run *platformv1alpha1.AgentRun, provider string, authMode platformv1alpha1.AgentRunAuthMode) error {
	if run == nil || run.Spec.Secrets == nil {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run has no configured provider credentials"))
	}
	secrets := run.Spec.Secrets
	if err := validateProviderAuthConfiguration(provider, authMode, secrets.ClaudeAPIKeySecret, secrets.OpenAIOAuthSecret, secrets.ProviderKeys); err != nil {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}

	if triggersv1alpha1.RequiresOpenAIOAuthSecret(provider, authMode) {
		return nil
	}

	for _, pk := range secrets.ProviderKeys {
		if strings.EqualFold(strings.TrimSpace(pk.Provider), provider) && strings.TrimSpace(pk.SecretName) != "" {
			return nil
		}
	}
	if strings.TrimSpace(secrets.ClaudeAPIKeySecret) != "" {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("active run does not have a mounted API key for provider %q", provider))
}

// activeRunHasMountedProviderKey reports whether the live pod already has an
// API key mounted for provider, making a provider switch effective on the
// next turn without a compute restart. OAuth-bound targets always need a
// restart: the pod's OAuth material belongs to the run's original provider.
func activeRunHasMountedProviderKey(run *platformv1alpha1.AgentRun, provider string, authMode platformv1alpha1.AgentRunAuthMode) bool {
	if run == nil || run.Spec.Secrets == nil {
		return false
	}
	if triggersv1alpha1.RequiresOpenAIOAuthSecret(provider, authMode) {
		return false
	}
	for _, pk := range run.Spec.Secrets.ProviderKeys {
		if strings.EqualFold(strings.TrimSpace(pk.Provider), provider) && strings.TrimSpace(pk.SecretName) != "" {
			return true
		}
	}
	return false
}

// activeRunHasMountedProviderOAuth reports whether the live pod already has
// the target provider's OAuth material mounted (via
// spec.secrets.providerOAuthSecrets) and the run authenticates with OAuth, so
// a switch to an OAuth-capable target applies on the next turn without a
// compute restart.
func activeRunHasMountedProviderOAuth(run *platformv1alpha1.AgentRun, provider string, authMode platformv1alpha1.AgentRunAuthMode) bool {
	return triggersv1alpha1.RequiresOpenAIOAuthSecret(provider, authMode) &&
		mountedProviderOAuthSecretName(run, provider) != ""
}

// mountedProviderOAuthSecretName returns the secret holding provider's OAuth
// material mounted on the run, or "" when none is mounted.
func mountedProviderOAuthSecretName(run *platformv1alpha1.AgentRun, provider string) string {
	if run == nil || run.Spec.Secrets == nil {
		return ""
	}
	for _, ref := range run.Spec.Secrets.ProviderOAuthSecrets {
		if strings.EqualFold(strings.TrimSpace(ref.Provider), provider) && strings.TrimSpace(ref.SecretName) != "" {
			return strings.TrimSpace(ref.SecretName)
		}
	}
	return ""
}

// mergeProviderKeys overlays incoming provider key refs onto existing ones,
// replacing entries for the same provider and keeping the rest mounted.
func mergeProviderKeys(existing, incoming []platformv1alpha1.ProviderKeyRef) []platformv1alpha1.ProviderKeyRef {
	if len(incoming) == 0 {
		return existing
	}
	replaced := make(map[string]bool, len(incoming))
	for _, pk := range incoming {
		replaced[strings.ToLower(strings.TrimSpace(pk.Provider))] = true
	}
	merged := make([]platformv1alpha1.ProviderKeyRef, 0, len(existing)+len(incoming))
	for _, pk := range existing {
		if replaced[strings.ToLower(strings.TrimSpace(pk.Provider))] {
			continue
		}
		merged = append(merged, pk)
	}
	return append(merged, incoming...)
}

// recordRuntimeConfigRestart appends a session note for a provider switch
// that bounces compute, so the resumed pod has context (and an input) to
// continue with. Best-effort: the restart itself is already committed on the
// spec.
func (s *Server) recordRuntimeConfigRestart(ctx context.Context, namespace, name, provider, model string) {
	if s.stateStore == nil {
		return
	}
	sess, err := s.stateStore.GetSessionByRun(ctx, name, namespace)
	if err != nil {
		log.Printf("WARN: no session found for %s/%s to record provider switch: %v", namespace, name, err)
		return
	}
	if _, err := s.stateStore.AppendMessage(ctx, sess.ID, "user",
		fmt.Sprintf("Provider switched to %s (%s) — the run compute restarted with the new credentials; continue from where you left off.", provider, model), nil); err != nil {
		log.Printf("WARN: failed to append provider-switch message for %s/%s: %v", namespace, name, err)
	}
	if _, err := s.stateStore.WriteActivityEvent(ctx, sess.ID, "runtime_config",
		fmt.Sprintf("Provider switched to %s (%s) — restarting run compute to remount credentials", provider, model), nil); err != nil {
		log.Printf("WARN: failed to write provider-switch activity for %s/%s: %v", namespace, name, err)
	}
}

// ExtendAgentRunRuntime adds time to an active or paused AgentRun's maxRuntime.
func (s *Server) ExtendAgentRunRuntime(ctx context.Context, req *platform.ExtendAgentRunRuntimeRequest) (*platform.AgentRun, error) {
	additionalRuntime, err := parsePositiveDashboardDuration("additional_runtime", req.GetAdditionalRuntime())
	if err != nil {
		return nil, err
	}
	if err := s.requireAgentRunCollaborator(ctx, req.Namespace, req.Name, "extend runtime for"); err != nil {
		return nil, err
	}

	key := client.ObjectKey{Namespace: req.Namespace, Name: req.Name}
	var updated platformv1alpha1.AgentRun
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		run := &platformv1alpha1.AgentRun{}
		if err := s.k8sClient.Get(ctx, key, run); err != nil {
			return err
		}
		if isTerminalAgentRunPhase(run.Status.Phase) {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot extend runtime for terminal run in phase %s", run.Status.Phase))
		}

		currentRuntime := time.Duration(0)
		if run.Spec.Limits != nil {
			currentRuntime = run.Spec.Limits.MaxRuntime.Duration
		}
		baseRuntime := currentRuntime
		if run.Status.StartedAt != nil {
			elapsed := time.Since(run.Status.StartedAt.Time)
			if elapsed > baseRuntime {
				baseRuntime = elapsed
			}
		}
		if baseRuntime < 0 {
			baseRuntime = 0
		}
		if run.Spec.Limits == nil {
			run.Spec.Limits = &platformv1alpha1.AgentRunLimits{}
		}
		run.Spec.Limits.MaxRuntime = metav1.Duration{Duration: baseRuntime + additionalRuntime}
		if err := s.k8sClient.Update(ctx, run); err != nil {
			return err
		}
		updated = *run
		return nil
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, mapK8sError("extend AgentRun runtime", err)
	}

	return s.enrichAgentRunProto(ctx, k8sAgentRunToProto(&updated))
}
