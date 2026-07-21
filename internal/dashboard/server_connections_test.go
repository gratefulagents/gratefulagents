//nolint:goconst // Repeated identifiers are intentional test fixtures.
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/githubapp"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type conflictingConnectionUpdateClient struct{ client.Client }

type commitThenErrorClient struct {
	client.Client
	failCreate bool
	failUpdate bool
}

func (c commitThenErrorClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if err := c.Client.Create(ctx, object, options...); err != nil {
		return err
	}
	if _, ok := object.(*triggersv1alpha1.Connection); c.failCreate && ok {
		return errors.New("simulated response loss after create commit")
	}
	return nil
}

func (c commitThenErrorClient) Update(ctx context.Context, object client.Object, options ...client.UpdateOption) error {
	if err := c.Client.Update(ctx, object, options...); err != nil {
		return err
	}
	if _, ok := object.(*triggersv1alpha1.Connection); c.failUpdate && ok {
		return errors.New("simulated response loss after update commit")
	}
	return nil
}

func (c conflictingConnectionUpdateClient) Update(ctx context.Context, object client.Object, options ...client.UpdateOption) error {
	if _, ok := object.(*triggersv1alpha1.Connection); ok {
		return k8serrors.NewConflict(schema.GroupResource{Group: triggersv1alpha1.GroupVersion.Group, Resource: "connections"}, object.GetName(), errors.New("simulated concurrent update"))
	}
	return c.Client.Update(ctx, object, options...)
}

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
	githubSecret := created.GetGithub().GetTokenSecret()
	if !strings.HasPrefix(githubSecret, "conn-gh-github-") {
		t.Fatalf("tokenSecret = %q", githubSecret)
	}
	secret := &corev1.Secret{}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: githubSecret}, secret); err != nil {
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
	slackSecret := slack.GetSlack().GetTokensSecret()
	if !strings.HasPrefix(slackSecret, "conn-sl-slack-") || slack.GetSlack().GetBotToken() != "" || slack.GetSlack().GetUserToken() != "" {
		t.Fatalf("slack = %#v", slack)
	}
	if slack.GetSlack().GetTeamId() != "T123" || slack.GetSlack().GetSlackUserId() != "UOWNER1" {
		t.Fatalf("slack identity = %#v", slack.GetSlack())
	}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: slackSecret}, secret); err != nil {
		t.Fatalf("slack secret: %v", err)
	}
	if string(secret.Data[triggersv1alpha1.SlackBotTokenKey]) != "xoxb-fixture-1" || string(secret.Data[triggersv1alpha1.SlackAppTokenKey]) != "xapp-fixture-1" || string(secret.Data[triggersv1alpha1.SlackUserTokenKey]) != "xoxp-fixture-1" {
		t.Fatalf("slack secret data = %#v", secret.Data)
	}

	// Slack update: pasting only one token creates a complete immutable Secret.
	updatedSlack, err := server.UpdateConnection(ctx, &platform.UpdateConnectionRequest{Namespace: "team", Name: "sl", Connection: &platform.Connection{Type: "slack", Slack: &platform.SlackConnection{TokensSecret: slackSecret, BotToken: "xoxb-fixture-2"}}})
	if err != nil {
		t.Fatalf("UpdateConnection slack: %v", err)
	}
	updatedSlackSecret := updatedSlack.GetSlack().GetTokensSecret()
	if updatedSlackSecret == slackSecret {
		t.Fatal("credential update reused the previous Secret")
	}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: updatedSlackSecret}, secret); err != nil {
		t.Fatalf("slack secret after update: %v", err)
	}
	if string(secret.Data[triggersv1alpha1.SlackBotTokenKey]) != "xoxb-fixture-2" || string(secret.Data[triggersv1alpha1.SlackAppTokenKey]) != "xapp-fixture-1" {
		t.Fatalf("slack secret after update = %#v", secret.Data)
	}

	// Slack user tokens have an explicit deletion path.
	clearedSlack, err := server.UpdateConnection(ctx, &platform.UpdateConnectionRequest{Namespace: "team", Name: "sl", Connection: &platform.Connection{Type: "slack", Slack: &platform.SlackConnection{TokensSecret: updatedSlackSecret, SlackUserId: "UOWNER1", ClearUserToken: true}}})
	if err != nil {
		t.Fatalf("clear Slack user token: %v", err)
	}
	clearedSlackSecret := clearedSlack.GetSlack().GetTokensSecret()
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: clearedSlackSecret}, secret); err != nil {
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
	linearSecret := linear.GetLinear().GetApiKeySecret()
	if !strings.HasPrefix(linearSecret, "conn-lin-linear-") || linear.GetLinear().GetApiKey() != "" {
		t.Fatalf("linear = %#v", linear)
	}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: linearSecret}, secret); err != nil {
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
	appSecret := app.GetGithub().GetPrivateKeySecret()
	if !strings.HasPrefix(appSecret, "conn-gh-app-github-") || app.GetGithub().GetPrivateKey() != "" {
		t.Fatalf("github app = %#v", app)
	}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: appSecret}, secret); err != nil {
		t.Fatalf("github app secret: %v", err)
	}
	if string(secret.Data[githubapp.PrivateKeySecretKey]) != "pem-material-fixture" {
		t.Fatalf("github app secret data = %#v", secret.Data)
	}

	// Deleting the connection retains versioned credentials for deferred GC.
	if _, err := server.DeleteConnection(ctx, &platform.DeleteConnectionRequest{Namespace: "team", Name: "gh"}); err != nil {
		t.Fatalf("DeleteConnection: %v", err)
	}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: githubSecret}, secret); err != nil {
		t.Fatalf("versioned credentials were removed eagerly: %v", err)
	}
}

func TestConcurrentCreateConnectionKeepsWinningCredentials(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	k8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	server := NewServer(k8s, scheme, nil, nil, false)
	ctx := context.Background()

	type result struct {
		index int
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for i := range 2 {
		go func(index int) {
			<-start
			_, err := server.CreateConnection(ctx, &platform.CreateConnectionRequest{
				Namespace: "team",
				Name:      "shared",
				Connection: &platform.Connection{Type: "slack", Slack: &platform.SlackConnection{
					BotToken: fmt.Sprintf("xoxb-create-%d", index), AppToken: fmt.Sprintf("xapp-create-%d", index), SlackUserId: "UOWNER1",
				}},
			})
			results <- result{index: index, err: err}
		}(i)
	}
	close(start)

	winner := -1
	for range 2 {
		result := <-results
		if result.err == nil {
			if winner != -1 {
				t.Fatal("both concurrent creates succeeded")
			}
			winner = result.index
		} else if !k8serrors.IsAlreadyExists(result.err) && connect.CodeOf(result.err) != connect.CodeAlreadyExists {
			t.Fatalf("losing create returned %v", result.err)
		}
	}
	if winner == -1 {
		t.Fatal("neither concurrent create succeeded")
	}
	storedConnection := &triggersv1alpha1.Connection{}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: "shared"}, storedConnection); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: "team", Name: storedConnection.Spec.Slack.TokensSecret}, secret); err != nil {
		t.Fatal(err)
	}
	if got, want := string(secret.Data[triggersv1alpha1.SlackBotTokenKey]), fmt.Sprintf("xoxb-create-%d", winner); got != want {
		t.Fatalf("stored bot token = %q, want winning request token %q", got, want)
	}
	managed := &corev1.SecretList{}
	if err := k8s.List(ctx, managed, client.InNamespace("team"), client.MatchingLabels{connectionSecretLabel: "shared"}); err != nil {
		t.Fatal(err)
	}
	if len(managed.Items) != 1 {
		t.Fatalf("managed Secrets = %d, want only the winner's Secret", len(managed.Items))
	}
}

func TestConnectionWriteUnknownOutcomeRetainsCommittedCredentials(t *testing.T) {
	newScheme := func(t *testing.T) *runtime.Scheme {
		t.Helper()
		scheme := runtime.NewScheme()
		if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		if err := corev1.AddToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		return scheme
	}
	t.Run("create", func(t *testing.T) {
		scheme := newScheme(t)
		baseClient := fake.NewClientBuilder().WithScheme(scheme).Build()
		server := NewServer(commitThenErrorClient{Client: baseClient, failCreate: true}, scheme, nil, nil, false)
		_, err := server.CreateConnection(context.Background(), &platform.CreateConnectionRequest{
			Namespace: "team", Name: "slack",
			Connection: &platform.Connection{Type: "slack", Slack: &platform.SlackConnection{BotToken: "xoxb-new", AppToken: "xapp-new"}},
		})
		if err == nil {
			t.Fatal("CreateConnection succeeded despite simulated response loss")
		}
		stored := &triggersv1alpha1.Connection{}
		if err := baseClient.Get(context.Background(), client.ObjectKey{Namespace: "team", Name: "slack"}, stored); err != nil {
			t.Fatal(err)
		}
		secret := &corev1.Secret{}
		if err := baseClient.Get(context.Background(), client.ObjectKey{Namespace: "team", Name: stored.Spec.Slack.TokensSecret}, secret); err != nil {
			t.Fatalf("committed credentials were removed: %v", err)
		}
	})
	t.Run("update", func(t *testing.T) {
		scheme := newScheme(t)
		connection := &triggersv1alpha1.Connection{
			ObjectMeta: metav1.ObjectMeta{Name: "slack", Namespace: "team"},
			Spec:       triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeSlack, Slack: &triggersv1alpha1.SlackConnectionConfig{TokensSecret: "old-secret"}},
		}
		oldSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "old-secret", Namespace: "team"}, Data: map[string][]byte{
			triggersv1alpha1.SlackBotTokenKey: []byte("xoxb-old"), triggersv1alpha1.SlackAppTokenKey: []byte("xapp-old"),
		}}
		baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(connection, oldSecret).Build()
		server := NewServer(commitThenErrorClient{Client: baseClient, failUpdate: true}, scheme, nil, nil, false)
		_, err := server.UpdateConnection(context.Background(), &platform.UpdateConnectionRequest{
			Namespace: "team", Name: "slack",
			Connection: &platform.Connection{Type: "slack", Slack: &platform.SlackConnection{TokensSecret: "old-secret", BotToken: "xoxb-new"}},
		})
		if err == nil {
			t.Fatal("UpdateConnection succeeded despite simulated response loss")
		}
		stored := &triggersv1alpha1.Connection{}
		if err := baseClient.Get(context.Background(), client.ObjectKeyFromObject(connection), stored); err != nil {
			t.Fatal(err)
		}
		secret := &corev1.Secret{}
		if err := baseClient.Get(context.Background(), client.ObjectKey{Namespace: "team", Name: stored.Spec.Slack.TokensSecret}, secret); err != nil {
			t.Fatalf("committed credentials were removed: %v", err)
		}
		if got := string(secret.Data[triggersv1alpha1.SlackBotTokenKey]); got != "xoxb-new" {
			t.Fatalf("committed bot token = %q", got)
		}
	})
}

func TestUpdateConnectionProtectsManagedSecretFromGarbageCollection(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	connection := &triggersv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "slack", Namespace: "team"},
		Spec:       triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeSlack, Slack: &triggersv1alpha1.SlackConnectionConfig{TokensSecret: "conn-slack-slack-old"}},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "conn-slack-slack-old", Namespace: "team",
			Labels:      map[string]string{connectionSecretLabel: "slack"},
			Annotations: map[string]string{connectionSecretOrphanedAt: "123"},
		},
		Data: map[string][]byte{
			triggersv1alpha1.SlackBotTokenKey: []byte("xoxb-original"),
			triggersv1alpha1.SlackAppTokenKey: []byte("xapp-original"),
		},
	}
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(connection, secret).Build()
	server := NewServer(k8s, scheme, nil, nil, false)
	if _, err := server.UpdateConnection(context.Background(), &platform.UpdateConnectionRequest{
		Namespace: "team", Name: "slack",
		Connection: &platform.Connection{Type: "slack", Slack: &platform.SlackConnection{TokensSecret: secret.Name}},
	}); err != nil {
		t.Fatalf("UpdateConnection: %v", err)
	}
	stored := &corev1.Secret{}
	if err := k8s.Get(context.Background(), client.ObjectKeyFromObject(secret), stored); err != nil {
		t.Fatal(err)
	}
	if stored.Annotations[connectionSecretOrphanedAt] != "" {
		t.Fatalf("orphan marker was not cleared: %#v", stored.Annotations)
	}
}

func TestConflictingUpdateConnectionDeletesOnlyRequestScopedSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	connection := &triggersv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "slack", Namespace: "team"},
		Spec:       triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeSlack, Slack: &triggersv1alpha1.SlackConnectionConfig{TokensSecret: "existing-slack-secret", SlackUserID: "UOWNER1"}},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "existing-slack-secret", Namespace: "team", Labels: map[string]string{connectionSecretLabel: "slack"}}, Data: map[string][]byte{
		triggersv1alpha1.SlackBotTokenKey: []byte("xoxb-original"), triggersv1alpha1.SlackAppTokenKey: []byte("xapp-original"),
	}}
	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(connection, secret).Build()
	server := NewServer(conflictingConnectionUpdateClient{Client: baseClient}, scheme, nil, nil, false)
	_, err := server.UpdateConnection(context.Background(), &platform.UpdateConnectionRequest{
		Namespace: "team", Name: "slack",
		Connection: &platform.Connection{Type: "slack", Slack: &platform.SlackConnection{TokensSecret: "existing-slack-secret", BotToken: "xoxb-new", SlackUserId: "UOWNER1"}},
	})
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("UpdateConnection error = %v, want aborted", err)
	}
	stored := &corev1.Secret{}
	if err := baseClient.Get(context.Background(), client.ObjectKeyFromObject(secret), stored); err != nil {
		t.Fatal(err)
	}
	if got := string(stored.Data[triggersv1alpha1.SlackBotTokenKey]); got != "xoxb-original" {
		t.Fatalf("stored bot token changed to %q", got)
	}
	managed := &corev1.SecretList{}
	if err := baseClient.List(context.Background(), managed, client.InNamespace("team"), client.MatchingLabels{connectionSecretLabel: "slack"}); err != nil {
		t.Fatal(err)
	}
	if len(managed.Items) != 1 || managed.Items[0].Name != "existing-slack-secret" {
		t.Fatalf("managed Secrets after conflict = %#v", managed.Items)
	}
}
