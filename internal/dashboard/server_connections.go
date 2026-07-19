package dashboard

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/githubapp"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	// connectionSecretLabel marks Secrets the platform manages on behalf of a
	// Connection (created from raw credential values pasted in the dashboard).
	// Its value is the owning Connection's name; these Secrets are deleted
	// together with the Connection.
	connectionSecretLabel = "triggers.gratefulagents.dev/connection"

	// linearConnectionAPIKeyKey is the Secret key the LinearProject controller
	// reads the API key from.
	linearConnectionAPIKeyKey = "api-key"
)

func (s *Server) ListConnections(ctx context.Context, req *platform.ListConnectionsRequest) (*platform.ListConnectionsResponse, error) {
	namespace := strings.TrimSpace(req.GetNamespace())
	if namespace == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace is required"))
	}
	if _, err := s.authorizeConnectionNamespace(ctx, namespace); err != nil {
		return nil, err
	}
	list := &triggersv1alpha1.ConnectionList{}
	if err := s.k8sClient.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, mapK8sError("list Connections", err)
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	response := &platform.ListConnectionsResponse{Connections: make([]*platform.Connection, 0, len(list.Items))}
	for i := range list.Items {
		response.Connections = append(response.Connections, connectionToProto(&list.Items[i]))
	}
	return response, nil
}

func (s *Server) CreateConnection(ctx context.Context, req *platform.CreateConnectionRequest) (*platform.Connection, error) {
	namespace, err := s.authorizeConnectionNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	name, err := connectionName(req.GetName())
	if err != nil {
		return nil, err
	}
	if err := s.materializeConnectionSecrets(ctx, namespace, name, req.GetConnection()); err != nil {
		return nil, err
	}
	connection, err := connectionFromProto(req.GetConnection())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	connection.Name, connection.Namespace = name, namespace
	if err := s.k8sClient.Create(ctx, connection); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("connection %q already exists", name))
		}
		return nil, mapK8sError("create Connection", err)
	}
	return connectionToProto(connection), nil
}

func (s *Server) UpdateConnection(ctx context.Context, req *platform.UpdateConnectionRequest) (*platform.Connection, error) {
	namespace, err := s.authorizeConnectionNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	name, err := connectionName(req.GetName())
	if err != nil {
		return nil, err
	}
	if err := s.materializeConnectionSecrets(ctx, namespace, name, req.GetConnection()); err != nil {
		return nil, err
	}
	desired, err := connectionFromProto(req.GetConnection())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	current := &triggersv1alpha1.Connection{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, current); err != nil {
		return nil, mapK8sError("get Connection", err)
	}
	if current.Spec.Type != desired.Spec.Type {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("connection type is immutable"))
	}
	current.Spec = desired.Spec
	if err := s.k8sClient.Update(ctx, current); err != nil {
		return nil, mapK8sError("update Connection", err)
	}
	return connectionToProto(current), nil
}

func (s *Server) DeleteConnection(ctx context.Context, req *platform.DeleteConnectionRequest) (*emptypb.Empty, error) {
	namespace, err := s.authorizeConnectionNamespace(ctx, req.GetNamespace())
	if err != nil {
		return nil, err
	}
	name, err := connectionName(req.GetName())
	if err != nil {
		return nil, err
	}
	connection := &triggersv1alpha1.Connection{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, connection); err != nil {
		return nil, mapK8sError("get Connection", err)
	}
	projects := &triggersv1alpha1.ProjectList{}
	if err := s.k8sClient.List(ctx, projects, client.InNamespace(namespace)); err != nil {
		return nil, mapK8sError("list Projects", err)
	}
	for i := range projects.Items {
		if dashboardProjectReferencesConnection(&projects.Items[i], name) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("connection %q is used by project %q", name, projects.Items[i].Name))
		}
	}
	if err := s.k8sClient.Delete(ctx, connection); err != nil && !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("delete Connection", err)
	}
	// Best-effort cleanup of platform-managed credential Secrets created for
	// this connection from pasted raw values.
	secrets := &corev1.SecretList{}
	if err := s.k8sClient.List(ctx, secrets, client.InNamespace(namespace), client.MatchingLabels{connectionSecretLabel: name}); err == nil {
		for i := range secrets.Items {
			_ = s.k8sClient.Delete(ctx, &secrets.Items[i])
		}
	}
	return &emptypb.Empty{}, nil
}

// connectionSecretName is the platform-managed Secret holding raw credential
// values pasted for a connection in the dashboard.
func connectionSecretName(connection string, connectionType triggersv1alpha1.ConnectionType) string {
	return fmt.Sprintf("conn-%s-%s", connection, connectionType)
}

// materializeConnectionSecrets moves any write-only raw credential values off
// the wire message into a platform-managed Secret and rewrites the message to
// reference that Secret. Raw values never reach the Connection CR and are
// never echoed back to clients. Empty raw fields leave existing Secret keys
// untouched, so updates may omit previously stored credentials.
func (s *Server) materializeConnectionSecrets(ctx context.Context, namespace, name string, pb *platform.Connection) error {
	if pb == nil {
		return nil
	}
	switch triggersv1alpha1.ConnectionType(strings.TrimSpace(pb.GetType())) {
	case triggersv1alpha1.ConnectionTypeGitHub:
		g := pb.GetGithub()
		if g == nil {
			return nil
		}
		set := map[string][]byte{}
		if v := strings.TrimSpace(g.GetToken()); v != "" {
			set[userCredGithubTokenKey] = []byte(v)
		}
		if v := strings.TrimSpace(g.GetPrivateKey()); v != "" {
			set[githubapp.PrivateKeySecretKey] = []byte(v)
		}
		g.Token, g.PrivateKey = "", ""
		if len(set) == 0 {
			return nil
		}
		secretName := connectionSecretName(name, triggersv1alpha1.ConnectionTypeGitHub)
		if err := s.upsertConnectionSecret(ctx, namespace, secretName, name, set); err != nil {
			return err
		}
		if _, ok := set[userCredGithubTokenKey]; ok {
			g.TokenSecret = secretName
		}
		if _, ok := set[githubapp.PrivateKeySecretKey]; ok {
			g.PrivateKeySecret = secretName
		}
	case triggersv1alpha1.ConnectionTypeSlack:
		sl := pb.GetSlack()
		if sl == nil {
			return nil
		}
		set := map[string][]byte{}
		if v := strings.TrimSpace(sl.GetBotToken()); v != "" {
			set[triggersv1alpha1.SlackBotTokenKey] = []byte(v)
		}
		if v := strings.TrimSpace(sl.GetAppToken()); v != "" {
			set[triggersv1alpha1.SlackAppTokenKey] = []byte(v)
		}
		sl.BotToken, sl.AppToken = "", ""
		if len(set) == 0 {
			return nil
		}
		// A Slack trigger needs both tokens to connect (Socket Mode). Only a
		// brand-new tokens Secret must be complete; merges may send one.
		if strings.TrimSpace(sl.GetTokensSecret()) == "" && (len(set) < 2) {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("slack connection requires both a bot token (xoxb-…) and an app-level token (xapp-…)"))
		}
		secretName := connectionSecretName(name, triggersv1alpha1.ConnectionTypeSlack)
		if err := s.upsertConnectionSecret(ctx, namespace, secretName, name, set); err != nil {
			return err
		}
		sl.TokensSecret = secretName
	case triggersv1alpha1.ConnectionTypeLinear:
		l := pb.GetLinear()
		if l == nil {
			return nil
		}
		v := strings.TrimSpace(l.GetApiKey())
		l.ApiKey = ""
		if v == "" {
			return nil
		}
		secretName := connectionSecretName(name, triggersv1alpha1.ConnectionTypeLinear)
		if err := s.upsertConnectionSecret(ctx, namespace, secretName, name, map[string][]byte{linearConnectionAPIKeyKey: []byte(v)}); err != nil {
			return err
		}
		l.ApiKeySecret = secretName
	}
	return nil
}

// upsertConnectionSecret merges the given keys into the named platform-managed
// Secret, creating and labeling it on first use.
func (s *Server) upsertConnectionSecret(ctx context.Context, namespace, secretName, connection string, set map[string][]byte) error {
	secret := &corev1.Secret{}
	err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return mapK8sError("read connection secret", err)
		}
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
				Labels:    map[string]string{connectionSecretLabel: connection},
			},
			Data: set,
		}
		if err := s.k8sClient.Create(ctx, secret); err != nil {
			return mapK8sError("create connection secret", err)
		}
		return nil
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	maps.Copy(secret.Data, set)
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	secret.Labels[connectionSecretLabel] = connection
	if err := s.k8sClient.Update(ctx, secret); err != nil {
		return mapK8sError("update connection secret", err)
	}
	return nil
}

func (s *Server) authorizeConnectionNamespace(ctx context.Context, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace is required"))
	}
	actor, recorded := requestActorFromContextOK(ctx)
	if !recorded {
		return requested, nil
	}
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return "", err
	}
	if requested != namespace && actor.Role != "admin" && actor.Role != "owner" {
		return "", connect.NewError(connect.CodePermissionDenied, fmt.Errorf("you do not have permission to manage connections in namespace %q", requested))
	}
	return requested, nil
}

func connectionName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if problems := validation.IsDNS1123Label(name); len(problems) != 0 {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("connection name must be a valid DNS-1123 label: %s", strings.Join(problems, "; ")))
	}
	return name, nil
}

func connectionFromProto(pb *platform.Connection) (*triggersv1alpha1.Connection, error) {
	if pb == nil {
		return nil, fmt.Errorf("connection is required")
	}
	connection := &triggersv1alpha1.Connection{TypeMeta: metav1.TypeMeta{APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: "Connection"}}
	switch triggersv1alpha1.ConnectionType(strings.TrimSpace(pb.GetType())) {
	case triggersv1alpha1.ConnectionTypeGitHub:
		if pb.GetGithub() == nil || pb.GetSlack() != nil || pb.GetLinear() != nil {
			return nil, fmt.Errorf("github connection requires only github configuration")
		}
		g := pb.GetGithub()
		if strings.TrimSpace(g.GetTokenSecret()) == "" && (g.GetAppId() <= 0 || g.GetInstallationId() <= 0 || strings.TrimSpace(g.GetPrivateKeySecret()) == "") {
			return nil, fmt.Errorf("github connection requires a token (token or token_secret) or complete GitHub App configuration (app_id, installation_id, private_key or private_key_secret)")
		}
		connection.Spec = triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeGitHub, GitHub: &triggersv1alpha1.GitHubConnectionConfig{TokenSecret: strings.TrimSpace(g.GetTokenSecret()), AppID: g.GetAppId(), InstallationID: g.GetInstallationId(), PrivateKeySecret: strings.TrimSpace(g.GetPrivateKeySecret())}}
	case triggersv1alpha1.ConnectionTypeSlack:
		if pb.GetSlack() == nil || pb.GetGithub() != nil || pb.GetLinear() != nil || strings.TrimSpace(pb.GetSlack().GetTokensSecret()) == "" {
			return nil, fmt.Errorf("slack connection requires only slack configuration with tokens (bot_token + app_token or tokens_secret)")
		}
		s := pb.GetSlack()
		connection.Spec = triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeSlack, Slack: &triggersv1alpha1.SlackConnectionConfig{TokensSecret: strings.TrimSpace(s.GetTokensSecret()), TeamID: strings.TrimSpace(s.GetTeamId())}}
	case triggersv1alpha1.ConnectionTypeLinear:
		if pb.GetLinear() == nil || pb.GetGithub() != nil || pb.GetSlack() != nil || strings.TrimSpace(pb.GetLinear().GetApiKeySecret()) == "" {
			return nil, fmt.Errorf("linear connection requires only linear configuration with an API key (api_key or api_key_secret)")
		}
		l := pb.GetLinear()
		connection.Spec = triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeLinear, Linear: &triggersv1alpha1.LinearConnectionConfig{APIKeySecret: strings.TrimSpace(l.GetApiKeySecret()), WorkspaceID: strings.TrimSpace(l.GetWorkspaceId())}}
	default:
		return nil, fmt.Errorf("invalid connection type %q", pb.GetType())
	}
	return connection, nil
}

func dashboardProjectReferencesConnection(project *triggersv1alpha1.Project, name string) bool {
	for _, trigger := range project.Spec.Triggers {
		switch trigger.Type {
		case triggersv1alpha1.ProjectTriggerTypeGitHub:
			if trigger.GitHub != nil && trigger.GitHub.ConnectionRef.Name == name {
				return true
			}
		case triggersv1alpha1.ProjectTriggerTypeSlack:
			if trigger.Slack != nil && trigger.Slack.ConnectionRef.Name == name {
				return true
			}
		case triggersv1alpha1.ProjectTriggerTypeLinear:
			if trigger.Linear != nil && trigger.Linear.ConnectionRef.Name == name {
				return true
			}
		}
	}
	return false
}

func connectionToProto(connection *triggersv1alpha1.Connection) *platform.Connection {
	pb := &platform.Connection{Namespace: connection.Namespace, Name: connection.Name, Type: string(connection.Spec.Type)}
	if connection.Spec.GitHub != nil {
		g := connection.Spec.GitHub
		pb.Github = &platform.GitHubConnection{TokenSecret: g.TokenSecret, AppId: g.AppID, InstallationId: g.InstallationID, PrivateKeySecret: g.PrivateKeySecret}
	}
	if connection.Spec.Slack != nil {
		s := connection.Spec.Slack
		pb.Slack = &platform.SlackConnection{TokensSecret: s.TokensSecret, TeamId: s.TeamID}
	}
	if connection.Spec.Linear != nil {
		l := connection.Spec.Linear
		pb.Linear = &platform.LinearConnection{ApiKeySecret: l.APIKeySecret, WorkspaceId: l.WorkspaceID}
	}
	return pb
}
