package dashboard

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	"connectrpc.com/connect"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"google.golang.org/protobuf/types/known/emptypb"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// slackWorkspaceTokensLabel marks a shared workspace app's tokens Secret.
	slackWorkspaceTokensLabel = "platform.gratefulagents.dev/slack-workspace-tokens"

	slackWorkspaceResourceType = "slackworkspace"
)

func slackWorkspaceTokensSecretName(name string) string {
	return defaultManagedResourceName(name, "slack-ws-tokens")
}

// ListSlackWorkspaces returns every shared workspace app in the cluster: any
// user may join any workspace (the workspace admin's Slack app serves the whole
// Slack workspace), so the list is not scoped to the caller's namespace. Token
// values are never returned.
func (s *Server) ListSlackWorkspaces(ctx context.Context, _ *platform.ListSlackWorkspacesRequest) (*platform.ListSlackWorkspacesResponse, error) {
	actor := requestActorFromContext(ctx)
	callerNamespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}

	list := &triggersv1alpha1.SlackWorkspaceList{}
	if err := s.k8sClient.List(ctx, list); err != nil {
		return nil, mapK8sError("list SlackWorkspaces", err)
	}
	sort.Slice(list.Items, func(i, j int) bool {
		if list.Items[i].Namespace != list.Items[j].Namespace {
			return list.Items[i].Namespace < list.Items[j].Namespace
		}
		return list.Items[i].Name < list.Items[j].Name
	})

	out := &platform.ListSlackWorkspacesResponse{}
	for i := range list.Items {
		out.Workspaces = append(out.Workspaces, s.slackWorkspaceToProto(ctx, &list.Items[i], callerNamespace))
	}
	return out, nil
}

func (s *Server) slackWorkspaceToProto(ctx context.Context, ws *triggersv1alpha1.SlackWorkspace, callerNamespace string) *platform.SlackWorkspace {
	secretName := strings.TrimSpace(ws.Spec.TokensSecret)
	if secretName == "" {
		secretName = slackWorkspaceTokensSecretName(ws.Name)
	}
	return &platform.SlackWorkspace{
		Namespace:       ws.Namespace,
		Name:            ws.Name,
		BotTokenPresent: s.secretKeyPresent(ctx, ws.Namespace, secretName, triggersv1alpha1.SlackBotTokenKey),
		AppTokenPresent: s.secretKeyPresent(ctx, ws.Namespace, secretName, triggersv1alpha1.SlackAppTokenKey),
		TeamId:          ws.Spec.TeamID,
		ResolvedTeamId:  ws.Status.TeamID,
		BotUserId:       ws.Status.BotUserID,
		Suspended:       ws.Spec.Suspend,
		Ready:           meta.IsStatusConditionTrue(ws.Status.Conditions, triggersv1alpha1.ConditionSlackWorkspaceReady),
		TokenValid:      meta.IsStatusConditionTrue(ws.Status.Conditions, triggersv1alpha1.ConditionSlackWorkspaceTokenValid),
		MemberCount:     ws.Status.MemberCount,
		LastError:       ws.Status.LastError,
		Mine:            ws.Namespace == callerNamespace,
	}
}

// UpdateSlackWorkspace creates or updates a shared workspace app in the
// caller's namespace.
func (s *Server) UpdateSlackWorkspace(ctx context.Context, req *platform.UpdateSlackWorkspaceRequest) (*platform.SlackWorkspace, error) {
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	name, err := normalizeSlackAgentName(req.GetName())
	if err != nil {
		return nil, err
	}

	if err := s.writeSlackWorkspaceTokens(ctx, namespace, name, req); err != nil {
		return nil, err
	}

	ws := &triggersv1alpha1.SlackWorkspace{}
	getErr := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, ws)
	if getErr != nil && !k8serrors.IsNotFound(getErr) {
		return nil, mapK8sError("read SlackWorkspace", getErr)
	}
	creating := k8serrors.IsNotFound(getErr)
	if creating {
		ws = &triggersv1alpha1.SlackWorkspace{
			TypeMeta: metav1.TypeMeta{
				APIVersion: triggersv1alpha1.GroupVersion.String(),
				Kind:       "SlackWorkspace",
			},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		}
	}
	ws.Spec.TokensSecret = slackWorkspaceTokensSecretName(name)
	ws.Spec.TeamID = strings.TrimSpace(req.GetTeamId())
	ws.Spec.Suspend = req.GetSuspend()

	if creating {
		if err := s.k8sClient.Create(ctx, ws); err != nil {
			return nil, mapK8sError("create SlackWorkspace", err)
		}
	} else if err := s.k8sClient.Update(ctx, ws); err != nil {
		return nil, mapK8sError("update SlackWorkspace", err)
	}

	if s.stateStore != nil && actor.Subject != "" {
		if err := s.stateStore.SetResourceOwner(ctx, slackWorkspaceResourceType, name, namespace, actor.Subject); err != nil {
			_ = err // ownership is best-effort; the workspace is already created
		}
	}

	fresh := &triggersv1alpha1.SlackWorkspace{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, fresh); err != nil {
		return nil, mapK8sError("read SlackWorkspace", err)
	}
	return s.slackWorkspaceToProto(ctx, fresh, namespace), nil
}

// writeSlackWorkspaceTokens merges the supplied tokens into the workspace's
// tokens Secret. Non-empty values are written; keys named in clear are removed;
// the Secret is deleted when it becomes empty.
func (s *Server) writeSlackWorkspaceTokens(ctx context.Context, namespace, name string, req *platform.UpdateSlackWorkspaceRequest) error {
	clears := map[string]bool{}
	for _, c := range req.GetClear() {
		clears[strings.ToLower(strings.TrimSpace(c))] = true
	}

	set := map[string][]byte{}
	del := map[string]bool{}
	for _, kv := range []struct{ key, value string }{
		{triggersv1alpha1.SlackBotTokenKey, req.GetBotToken()},
		{triggersv1alpha1.SlackAppTokenKey, req.GetAppToken()},
	} {
		if clears[kv.key] {
			del[kv.key] = true
			continue
		}
		if v := strings.TrimSpace(kv.value); v != "" {
			set[kv.key] = []byte(v)
		}
	}
	if len(set) == 0 && len(del) == 0 {
		return nil
	}

	secretName := slackWorkspaceTokensSecretName(name)
	secret := &corev1.Secret{}
	err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret)
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return mapK8sError("read Slack workspace tokens secret", err)
		}
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
				Labels:    map[string]string{slackWorkspaceTokensLabel: "true"},
			},
			Data: set,
		}
		if err := s.k8sClient.Create(ctx, secret); err != nil {
			return mapK8sError("create Slack workspace tokens secret", err)
		}
		return nil
	}

	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	maps.Copy(secret.Data, set)
	for k := range del {
		delete(secret.Data, k)
	}
	if len(secret.Data) == 0 {
		if err := s.k8sClient.Delete(ctx, secret); err != nil && !k8serrors.IsNotFound(err) {
			return mapK8sError("delete Slack workspace tokens secret", err)
		}
		return nil
	}
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	secret.Labels[slackWorkspaceTokensLabel] = "true"
	if err := s.k8sClient.Update(ctx, secret); err != nil {
		return mapK8sError("update Slack workspace tokens secret", err)
	}
	return nil
}

// DeleteSlackWorkspace removes a workspace app from the caller's namespace. It
// refuses while member SlackAgents still reference it, so members never lose
// their connector silently.
func (s *Server) DeleteSlackWorkspace(ctx context.Context, req *platform.DeleteSlackWorkspaceRequest) (*emptypb.Empty, error) {
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	name, err := normalizeSlackAgentName(req.GetName())
	if err != nil {
		return nil, err
	}

	agents := &triggersv1alpha1.SlackAgentList{}
	if err := s.k8sClient.List(ctx, agents); err != nil {
		return nil, mapK8sError("list SlackAgents", err)
	}
	var members []string
	for _, agent := range agents.Items {
		ns, wsName := agent.ResolvedWorkspaceRef()
		if ns == namespace && wsName == name {
			members = append(members, agent.Namespace+"/"+agent.Name)
		}
	}
	if len(members) > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("workspace still has %d member agent(s) (%s); they must leave first", len(members), strings.Join(members, ", ")))
	}

	ws := &triggersv1alpha1.SlackWorkspace{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if err := s.k8sClient.Delete(ctx, ws); err != nil && !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("delete SlackWorkspace", err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: slackWorkspaceTokensSecretName(name), Namespace: namespace}}
	if err := s.k8sClient.Delete(ctx, secret); err != nil && !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("delete Slack workspace tokens secret", err)
	}
	return &emptypb.Empty{}, nil
}
