package dashboard

import triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"

func providerOrDefault(provider string) string {
	return triggersv1alpha1.NormalizeProvider(provider)
}
