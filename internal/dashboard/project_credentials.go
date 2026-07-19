package dashboard

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	projectCredentialsSecretSuffix = "-credentials"
	githubTokenSecretKey           = "github-token"
	anthropicAPIKeySecretKey       = "anthropic-api-key"
	openAIAPIKeySecretKey          = "openai-api-key"
)

func projectCredentialsSecretName(projectName string) string {
	return projectName + projectCredentialsSecretSuffix
}

func (s *Server) enrichProjectProto(ctx context.Context, pb *platform.Project) *platform.Project {
	if pb == nil {
		return nil
	}
	pb.CredentialStatus = &platform.ProjectCredentialStatus{
		GithubTokenPresent:     s.secretKeyPresent(ctx, pb.Namespace, pb.GithubTokenSecret, githubTokenSecretKey),
		AnthropicApiKeyPresent: s.secretKeyPresent(ctx, pb.Namespace, pb.ClaudeApiKeySecret, anthropicAPIKeySecretKey),
		OpenaiApiKeyPresent:    s.projectOpenAIKeyPresent(ctx, pb),
	}
	if pb.RuntimeProfileRef != "" {
		pb.PermissionMode, pb.EgressMode = s.runtimeProfileModes(ctx, pb.Namespace, pb.RuntimeProfileRef)
	}
	if pb.McpPolicyRef != "" {
		pb.McpPolicyDefaultAction, pb.McpPolicyAllowedServers = s.mcpPolicyConfig(ctx, pb.Namespace, pb.McpPolicyRef)
	}

	// Enrich owner from collaboration store.
	if s.stateStore != nil {
		ownership, err := s.stateStore.GetResourceOwner(ctx, projectResourceType, pb.Name, pb.Namespace)
		if err == nil && ownership != nil {
			pb.Owner = s.enrichOwner(ctx, ownership.OwnerID)
			actor := requestActorFromContext(ctx)
			if actor.Role == "admin" || actor.Role == "owner" {
				pb.MyPermission = "admin"
			} else if ownership.OwnerID == actor.Subject {
				pb.MyPermission = "owner"
			} else if actor.Subject != "" {
				share, _ := s.stateStore.GetSharePermission(ctx, projectResourceType, pb.Name, pb.Namespace, actor.Subject)
				if share != nil {
					pb.MyPermission = share.Permission
				}
			}
		}
	}

	return pb
}

func (s *Server) projectOpenAIKeyPresent(ctx context.Context, pb *platform.Project) bool {
	for _, keyRef := range pb.ProviderKeys {
		if strings.TrimSpace(strings.ToLower(keyRef.Provider)) != triggersv1alpha1.ProviderOpenAI {
			continue
		}
		key := strings.TrimSpace(keyRef.SecretKey)
		if key == "" {
			key = openAIAPIKeySecretKey
		}
		return s.secretKeyPresent(ctx, pb.Namespace, keyRef.SecretName, key)
	}
	return false
}

func (s *Server) secretKeyPresent(ctx context.Context, namespace, secretName, key string) bool {
	secretName = strings.TrimSpace(secretName)
	key = strings.TrimSpace(key)
	if secretName == "" || key == "" {
		return false
	}
	secret := &corev1.Secret{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret); err != nil {
		return false
	}
	value, ok := secret.Data[key]
	if ok {
		return strings.TrimSpace(string(value)) != ""
	}
	stringValue, ok := secret.StringData[key]
	return ok && strings.TrimSpace(stringValue) != ""
}
