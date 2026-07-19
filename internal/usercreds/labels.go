// Package usercreds defines shared identifiers for a user's saved provider
// credentials. The dashboard writes these Secrets (in each user's personal
// namespace) and the OAuth refresher reads them, so both must agree on the
// labels used to discover credential Secrets and identify their provider.
package usercreds

const (
	// LabelUserCredential marks a Secret as one of a user's saved provider
	// credentials. The OAuth refresher uses it to discover credential Secrets
	// independently of any project that references them.
	LabelUserCredential = "platform.gratefulagents.dev/user-credential"
	// LabelCredentialProvider records which provider a credential Secret holds
	// (e.g. "anthropic", "openai", "copilot") so the refresher knows how to rotate
	// its OAuth material.
	LabelCredentialProvider = "platform.gratefulagents.dev/credential-provider"
)
