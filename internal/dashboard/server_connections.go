package dashboard

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
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
	createdSecret, err := s.materializeConnectionSecrets(ctx, namespace, name, req.GetConnection())
	if err != nil {
		return nil, err
	}
	cleanup := func() { s.deleteManagedConnectionSecret(ctx, namespace, name, createdSecret) }
	connection, err := connectionFromProto(req.GetConnection())
	if err != nil {
		cleanup()
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	connection.Name, connection.Namespace = name, namespace
	if err := s.k8sClient.Create(ctx, connection); err != nil {
		if k8serrors.IsAlreadyExists(err) || k8serrors.IsInvalid(err) {
			cleanup()
		}
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
	current := &triggersv1alpha1.Connection{}
	key := client.ObjectKey{Namespace: namespace, Name: name}
	if err := s.k8sClient.Get(ctx, key, current); err != nil {
		return nil, mapK8sError("get Connection", err)
	}
	connectionType := triggersv1alpha1.ConnectionType(strings.TrimSpace(req.GetConnection().GetType()))
	if current.Spec.Type != connectionType {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("connection type is immutable"))
	}
	createdSecret, err := s.materializeConnectionSecrets(ctx, namespace, name, req.GetConnection())
	if err != nil {
		return nil, err
	}
	cleanupCreated := func() { s.deleteManagedConnectionSecret(ctx, namespace, name, createdSecret) }
	desired, err := connectionFromProto(req.GetConnection())
	if err != nil {
		cleanupCreated()
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	current.Spec = desired.Spec
	if err := s.k8sClient.Update(ctx, current); err != nil {
		if k8serrors.IsConflict(err) || k8serrors.IsInvalid(err) || k8serrors.IsNotFound(err) {
			cleanupCreated()
		}
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
	if claim := connection.Annotations[slackTriggerClaimAnnotation]; claim != "" {
		active, err := s.slackTriggerClaimActive(ctx, namespace, name, claim)
		if err != nil {
			return nil, mapK8sError("validate Slack trigger claim", err)
		}
		if active {
			return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("slack connection %q has a trigger operation in progress", name))
		}
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
	preconditions := client.Preconditions{UID: &connection.UID, ResourceVersion: &connection.ResourceVersion}
	if err := s.k8sClient.Delete(ctx, connection, preconditions); err != nil && !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("delete Connection", err)
	}
	// Versioned credential Secrets are intentionally retained for deferred
	// garbage collection. Eager deletion is unsafe because an overlapping
	// request may have committed a reference after this request's last read.
	return &emptypb.Empty{}, nil
}

// connectionSecretName is the platform-managed Secret holding raw credential
// values pasted for a connection in the dashboard.
func connectionSecretName(connection string, connectionType triggersv1alpha1.ConnectionType) string {
	return fmt.Sprintf("conn-%s-%s", connection, connectionType)
}

func validateSlackConnectionFields(sl *platform.SlackConnection) error {
	if sl == nil {
		return fmt.Errorf("slack configuration is required")
	}
	if teamID := strings.TrimSpace(sl.GetTeamId()); teamID != "" && !validSlackID(teamID, "T") {
		return fmt.Errorf("invalid Slack team ID %q; expected an ID starting with T", teamID)
	}
	if userID := strings.TrimSpace(sl.GetSlackUserId()); userID != "" && !validSlackID(userID, "UW") {
		return fmt.Errorf("invalid owner Slack user ID %q; expected an ID starting with U or W", userID)
	}
	if sl.GetClearUserToken() && strings.TrimSpace(sl.GetUserToken()) != "" {
		return fmt.Errorf("user_token and clear_user_token cannot be used together")
	}
	return nil
}

// materializeConnectionSecrets moves any write-only raw credential values off
// the wire message into a platform-managed Secret and rewrites the message to
// reference that Secret. Raw values never reach the Connection CR and are
// never echoed back to clients. Empty raw fields leave existing Secret keys
// untouched, so updates may omit previously stored credentials.
func (s *Server) materializeConnectionSecrets(ctx context.Context, namespace, name string, pb *platform.Connection) (string, error) {
	if pb == nil {
		return "", nil
	}
	switch triggersv1alpha1.ConnectionType(strings.TrimSpace(pb.GetType())) {
	case triggersv1alpha1.ConnectionTypeGitHub:
		github := pb.GetGithub()
		if github == nil {
			return "", nil
		}
		data := map[string][]byte{}
		if value := strings.TrimSpace(github.GetToken()); value != "" {
			data[userCredGithubTokenKey] = []byte(value)
		}
		if value := strings.TrimSpace(github.GetPrivateKey()); value != "" {
			data[githubapp.PrivateKeySecretKey] = []byte(value)
		}
		github.Token, github.PrivateKey = "", ""
		if len(data) == 0 {
			return "", nil
		}
		secretName, err := s.createManagedConnectionSecret(ctx, namespace, name, triggersv1alpha1.ConnectionTypeGitHub, data)
		if err != nil {
			return "", err
		}
		if _, ok := data[userCredGithubTokenKey]; ok {
			github.TokenSecret = secretName
		}
		if _, ok := data[githubapp.PrivateKeySecretKey]; ok {
			github.PrivateKeySecret = secretName
		}
		return secretName, nil
	case triggersv1alpha1.ConnectionTypeSlack:
		return s.materializeSlackConnectionSecret(ctx, namespace, name, pb.GetSlack())
	case triggersv1alpha1.ConnectionTypeLinear:
		linear := pb.GetLinear()
		if linear == nil {
			return "", nil
		}
		value := strings.TrimSpace(linear.GetApiKey())
		linear.ApiKey = ""
		if value == "" {
			return "", nil
		}
		secretName, err := s.createManagedConnectionSecret(ctx, namespace, name, triggersv1alpha1.ConnectionTypeLinear, map[string][]byte{linearConnectionAPIKeyKey: []byte(value)})
		if err != nil {
			return "", err
		}
		linear.ApiKeySecret = secretName
		return secretName, nil
	default:
		return "", nil
	}
}

func (s *Server) materializeSlackConnectionSecret(ctx context.Context, namespace, name string, slack *platform.SlackConnection) (string, error) {
	if slack == nil {
		return "", nil
	}
	if err := validateSlackConnectionFields(slack); err != nil {
		return "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	set := map[string][]byte{}
	if value := strings.TrimSpace(slack.GetBotToken()); value != "" {
		set[triggersv1alpha1.SlackBotTokenKey] = []byte(value)
	}
	if value := strings.TrimSpace(slack.GetAppToken()); value != "" {
		set[triggersv1alpha1.SlackAppTokenKey] = []byte(value)
	}
	if value := strings.TrimSpace(slack.GetUserToken()); value != "" {
		set[triggersv1alpha1.SlackUserTokenKey] = []byte(value)
	}
	clearUser := slack.GetClearUserToken()
	slack.BotToken, slack.AppToken, slack.UserToken, slack.ClearUserToken = "", "", "", false
	if len(set) == 0 && !clearUser {
		return "", nil
	}

	referencedName := strings.TrimSpace(slack.GetTokensSecret())
	merged := map[string][]byte{}
	if referencedName != "" {
		referenced := &corev1.Secret{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: referencedName}, referenced); err != nil {
			if k8serrors.IsNotFound(err) {
				return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("slack tokens secret %q not found", referencedName))
			}
			return "", mapK8sError("read Slack tokens secret", err)
		}
		for _, key := range []string{triggersv1alpha1.SlackBotTokenKey, triggersv1alpha1.SlackAppTokenKey, triggersv1alpha1.SlackUserTokenKey} {
			if value, ok := referenced.Data[key]; ok {
				merged[key] = append([]byte(nil), value...)
			}
		}
	}
	maps.Copy(merged, set)
	if clearUser {
		delete(merged, triggersv1alpha1.SlackUserTokenKey)
	}
	if len(merged[triggersv1alpha1.SlackBotTokenKey]) == 0 || len(merged[triggersv1alpha1.SlackAppTokenKey]) == 0 {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("slack connection requires both a bot token (xoxb-…) and an app-level token (xapp-…)"))
	}
	secretName, err := s.createManagedConnectionSecret(ctx, namespace, name, triggersv1alpha1.ConnectionTypeSlack, merged)
	if err != nil {
		return "", err
	}
	slack.TokensSecret = secretName
	return secretName, nil
}

func (s *Server) createManagedConnectionSecret(ctx context.Context, namespace, connection string, connectionType triggersv1alpha1.ConnectionType, data map[string][]byte) (string, error) {
	secretName := fmt.Sprintf("%s-%s", connectionSecretName(connection, connectionType), uuid.NewString())
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
			Labels:    map[string]string{connectionSecretLabel: connection},
		},
		Data: data,
	}
	if err := s.k8sClient.Create(ctx, secret); err != nil {
		return "", mapK8sError("create connection secret", err)
	}
	return secretName, nil
}

func (s *Server) deleteManagedConnectionSecret(ctx context.Context, namespace, connection, secretName string) {
	if secretName == "" {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	secret := &corev1.Secret{}
	if err := s.k8sClient.Get(cleanupCtx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret); err != nil {
		return
	}
	if secret.Labels[connectionSecretLabel] != connection {
		return
	}
	_ = s.k8sClient.Delete(cleanupCtx, secret, client.Preconditions{UID: &secret.UID, ResourceVersion: &secret.ResourceVersion})
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
		if err := validateSlackConnectionFields(s); err != nil {
			return nil, err
		}
		connection.Spec = triggersv1alpha1.ConnectionSpec{Type: triggersv1alpha1.ConnectionTypeSlack, Slack: &triggersv1alpha1.SlackConnectionConfig{
			TokensSecret: strings.TrimSpace(s.GetTokensSecret()),
			TeamID:       strings.TrimSpace(s.GetTeamId()),
			SlackUserID:  strings.TrimSpace(s.GetSlackUserId()),
		}}
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
		pb.Slack = &platform.SlackConnection{TokensSecret: s.TokensSecret, TeamId: s.TeamID, SlackUserId: s.SlackUserID}
	}
	if connection.Spec.Linear != nil {
		l := connection.Spec.Linear
		pb.Linear = &platform.LinearConnection{ApiKeySecret: l.APIKeySecret, WorkspaceId: l.WorkspaceID}
	}
	return pb
}
