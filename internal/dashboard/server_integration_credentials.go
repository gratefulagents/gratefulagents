package dashboard

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// integrationNameRe constrains integration credential names: DNS-safe lowercase
// so the Secret name (usercred-<name>) is always valid.
var integrationNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// reservedCredentialNames are the built-in provider credential slots managed by
// the dedicated form fields; free-form integrations must not shadow them.
var reservedCredentialNames = map[string]bool{
	triggersv1alpha1.ProviderAnthropic:  true,
	triggersv1alpha1.ProviderOpenAI:     true,
	triggersv1alpha1.ProviderOpenRouter: true,
	triggersv1alpha1.ProviderXAI:        true,
	triggersv1alpha1.ProviderCopilot:    true,
	credentialGitHub:                    true,
}

// validateIntegrationName normalizes and validates a free-form integration
// credential name.
func validateIntegrationName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("integration name is required"))
	}
	if !integrationNameRe.MatchString(name) {
		return "", connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("integration name %q must be lowercase letters, digits, and hyphens (max 40 chars)", raw))
	}
	if reservedCredentialNames[name] {
		return "", connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("integration name %q is reserved for a built-in provider", name))
	}
	return name, nil
}

// applyIntegrationCredential writes, partially clears, or deletes one free-form
// integration credential Secret (usercred-<name>) in the user's namespace.
func (s *Server) applyIntegrationCredential(ctx context.Context, namespace string, upd *platform.IntegrationCredentialUpdate) error {
	name, err := validateIntegrationName(upd.GetName())
	if err != nil {
		return err
	}
	if upd.GetDelete() {
		secret := &corev1.Secret{}
		key := client.ObjectKey{Namespace: namespace, Name: userCredentialSecretName(name)}
		if err := s.k8sClient.Get(ctx, key, secret); err != nil {
			if k8serrors.IsNotFound(err) {
				return nil
			}
			return mapK8sError("read integration credential", err)
		}
		if err := s.k8sClient.Delete(ctx, secret); err != nil && !k8serrors.IsNotFound(err) {
			return mapK8sError("delete integration credential", err)
		}
		return nil
	}
	for _, key := range upd.GetClearKeys() {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if err := s.deleteCredentialKey(ctx, namespace, name, key); err != nil {
			return err
		}
	}
	data := map[string][]byte{}
	for key, value := range upd.GetEntries() {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		data[key] = []byte(value)
	}
	if len(data) == 0 {
		return nil
	}
	return s.writeCredentialData(ctx, namespace, name, data)
}

// integrationCredentialStates lists the user's free-form integration
// credentials (name + key presence only), excluding the built-in providers.
func (s *Server) integrationCredentialStates(ctx context.Context, namespace string) []*platform.IntegrationCredentialState {
	var secrets corev1.SecretList
	if err := s.k8sClient.List(ctx, &secrets,
		client.InNamespace(namespace),
		client.MatchingLabels{userCredentialLabel: "true"},
	); err != nil {
		return nil
	}
	out := make([]*platform.IntegrationCredentialState, 0, len(secrets.Items))
	for i := range secrets.Items {
		sec := &secrets.Items[i]
		provider := strings.TrimSpace(sec.Labels[userCredentialProviderLabel])
		if provider == "" || reservedCredentialNames[provider] {
			continue
		}
		keys := make([]string, 0, len(sec.Data))
		for k := range sec.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out = append(out, &platform.IntegrationCredentialState{Name: provider, Keys: keys})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
