package dashboard

import (
	"context"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/githubapp"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestConnectionCRUDAndReferencedDeleteProtection(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	project := &triggersv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "team"}, Spec: triggersv1alpha1.ProjectSpec{Triggers: []triggersv1alpha1.ProjectTrigger{{Name: "issues", Type: triggersv1alpha1.ProjectTriggerTypeGitHub, GitHub: &triggersv1alpha1.GitHubProjectTriggerConfig{ConnectionRef: triggersv1alpha1.ConnectionRef{Name: "github"}, Owner: "acme", Repo: "payments"}}}}}
	server := NewServer(fake.NewClientBuilder().WithScheme(scheme).WithObjects(project).Build(), scheme, nil, nil, false)
	ctx := context.Background() // trusted internal invocation

	created, err := server.CreateConnection(ctx, &platform.CreateConnectionRequest{Namespace: "team", Name: "github", Connection: &platform.Connection{Type: "github", Github: &platform.GitHubConnection{TokenSecret: "github-token"}}})
	if err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}
	if created.GetGithub().GetTokenSecret() != "github-token" {
		t.Fatalf("created = %#v", created)
	}

	listed, err := server.ListConnections(ctx, &platform.ListConnectionsRequest{Namespace: "team"})
	if err != nil || len(listed.Connections) != 1 {
		t.Fatalf("ListConnections = %#v, %v", listed, err)
	}

	if _, err := server.DeleteConnection(ctx, &platform.DeleteConnectionRequest{Namespace: "team", Name: "github"}); err == nil {
		t.Fatal("DeleteConnection succeeded for referenced connection")
	}
}

func TestConnectionRawCredentialMaterialization(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	server := NewServer(k8s, scheme, nil, nil, false)
	ctx := context.Background() // trusted internal invocation

	// GitHub: pasting a raw token creates a managed Secret and fills the ref.
	created, err := server.CreateConnection(ctx, &platform.CreateConnectionRequest{Namespace: "team", Name: "gh", Connection: &platform.Connection{Type: "github", Github: &platform.GitHubConnection{Token: "ghp-raw-fixture"}}})
	if err != nil {
		t.Fatalf("CreateConnection github: %v", err)
	}
	if created.GetGithub().GetToken() != "" {
		t.Fatal("raw token echoed back in response")
	}
	if got := created.GetGithub().GetTokenSecret(); got != "conn-gh-github" {
		t.Fatalf("tokenSecret = %q", got)
	}
	secret := &corev1.Secret{}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: "conn-gh-github"}, secret); err != nil {
		t.Fatalf("managed secret: %v", err)
	}
	if string(secret.Data["token"]) != "ghp-raw-fixture" || secret.Labels[connectionSecretLabel] != "gh" {
		t.Fatalf("secret = %#v", secret)
	}

	// Slack: a new connection needs both tokens.
	if _, err := server.CreateConnection(ctx, &platform.CreateConnectionRequest{Namespace: "team", Name: "sl", Connection: &platform.Connection{Type: "slack", Slack: &platform.SlackConnection{BotToken: "xoxb-fixture-1"}}}); err == nil {
		t.Fatal("slack create with only a bot token succeeded")
	}
	if _, err := server.CreateConnection(ctx, &platform.CreateConnectionRequest{Namespace: "team", Name: "sl", Connection: &platform.Connection{Type: "slack", Slack: &platform.SlackConnection{BotToken: "xoxb-fixture-1", UserToken: "xoxp-fixture-1"}}}); err == nil {
		t.Fatal("slack create without an app token succeeded")
	}
	slack, err := server.CreateConnection(ctx, &platform.CreateConnectionRequest{Namespace: "team", Name: "sl", Connection: &platform.Connection{Type: "slack", Slack: &platform.SlackConnection{BotToken: "xoxb-fixture-1", AppToken: "xapp-fixture-1", UserToken: "xoxp-fixture-1", TeamId: "T123", SlackUserId: "UOWNER1"}}})
	if err != nil {
		t.Fatalf("CreateConnection slack: %v", err)
	}
	if slack.GetSlack().GetTokensSecret() != "conn-sl-slack" || slack.GetSlack().GetBotToken() != "" || slack.GetSlack().GetUserToken() != "" {
		t.Fatalf("slack = %#v", slack)
	}
	if slack.GetSlack().GetTeamId() != "T123" || slack.GetSlack().GetSlackUserId() != "UOWNER1" {
		t.Fatalf("slack identity = %#v", slack.GetSlack())
	}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: "conn-sl-slack"}, secret); err != nil {
		t.Fatalf("slack secret: %v", err)
	}
	if string(secret.Data[triggersv1alpha1.SlackBotTokenKey]) != "xoxb-fixture-1" || string(secret.Data[triggersv1alpha1.SlackAppTokenKey]) != "xapp-fixture-1" || string(secret.Data[triggersv1alpha1.SlackUserTokenKey]) != "xoxp-fixture-1" {
		t.Fatalf("slack secret data = %#v", secret.Data)
	}

	// Slack update: pasting only one token merges into the existing Secret.
	if _, err := server.UpdateConnection(ctx, &platform.UpdateConnectionRequest{Namespace: "team", Name: "sl", Connection: &platform.Connection{Type: "slack", Slack: &platform.SlackConnection{TokensSecret: "conn-sl-slack", BotToken: "xoxb-fixture-2"}}}); err != nil {
		t.Fatalf("UpdateConnection slack: %v", err)
	}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: "conn-sl-slack"}, secret); err != nil {
		t.Fatalf("slack secret after update: %v", err)
	}
	if string(secret.Data[triggersv1alpha1.SlackBotTokenKey]) != "xoxb-fixture-2" || string(secret.Data[triggersv1alpha1.SlackAppTokenKey]) != "xapp-fixture-1" {
		t.Fatalf("slack secret after update = %#v", secret.Data)
	}

	// Slack user tokens have an explicit deletion path.
	if _, err := server.UpdateConnection(ctx, &platform.UpdateConnectionRequest{Namespace: "team", Name: "sl", Connection: &platform.Connection{Type: "slack", Slack: &platform.SlackConnection{TokensSecret: "conn-sl-slack", SlackUserId: "UOWNER1", ClearUserToken: true}}}); err != nil {
		t.Fatalf("clear Slack user token: %v", err)
	}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: "conn-sl-slack"}, secret); err != nil {
		t.Fatalf("slack secret after user-token clear: %v", err)
	}
	if _, exists := secret.Data[triggersv1alpha1.SlackUserTokenKey]; exists {
		t.Fatalf("user-token remains after clear: %#v", secret.Data)
	}

	// Linear: raw API key materializes under the controller's expected key.
	linear, err := server.CreateConnection(ctx, &platform.CreateConnectionRequest{Namespace: "team", Name: "lin", Connection: &platform.Connection{Type: "linear", Linear: &platform.LinearConnection{ApiKey: "lin-api-fixture"}}})
	if err != nil {
		t.Fatalf("CreateConnection linear: %v", err)
	}
	if linear.GetLinear().GetApiKeySecret() != "conn-lin-linear" || linear.GetLinear().GetApiKey() != "" {
		t.Fatalf("linear = %#v", linear)
	}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: "conn-lin-linear"}, secret); err != nil {
		t.Fatalf("linear secret: %v", err)
	}
	if string(secret.Data[linearConnectionAPIKeyKey]) != "lin-api-fixture" {
		t.Fatalf("linear secret data = %#v", secret.Data)
	}

	// GitHub App: pasting key material plus ids completes app configuration.
	app, err := server.CreateConnection(ctx, &platform.CreateConnectionRequest{Namespace: "team", Name: "gh-app", Connection: &platform.Connection{Type: "github", Github: &platform.GitHubConnection{AppId: 7, InstallationId: 11, PrivateKey: "pem-material-fixture"}}})
	if err != nil {
		t.Fatalf("CreateConnection github app: %v", err)
	}
	if app.GetGithub().GetPrivateKeySecret() != "conn-gh-app-github" || app.GetGithub().GetPrivateKey() != "" {
		t.Fatalf("github app = %#v", app)
	}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: "conn-gh-app-github"}, secret); err != nil {
		t.Fatalf("github app secret: %v", err)
	}
	if string(secret.Data[githubapp.PrivateKeySecretKey]) != "pem-material-fixture" {
		t.Fatalf("github app secret data = %#v", secret.Data)
	}

	// Deleting the connection removes its managed Secret.
	if _, err := server.DeleteConnection(ctx, &platform.DeleteConnectionRequest{Namespace: "team", Name: "gh"}); err != nil {
		t.Fatalf("DeleteConnection: %v", err)
	}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: "conn-gh-github"}, secret); !k8serrors.IsNotFound(err) {
		t.Fatalf("managed secret survived delete: %v", err)
	}
}
