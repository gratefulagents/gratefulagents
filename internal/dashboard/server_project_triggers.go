package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/emptypb"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	slackTriggerClaimAnnotation = "triggers.gratefulagents.dev/slack-trigger-claim"
	slackPendingClaimPrefix     = "pending|"
)

type slackTriggerClaimHandle struct {
	connectionName string
	claim          string
	pendingValue   string
}

func slackTriggerClaim(project, trigger string) string {
	return project + "/" + trigger
}

func parseSlackTriggerClaim(value string) (claim string, pendingSince time.Time) {
	if !strings.HasPrefix(value, slackPendingClaimPrefix) {
		return value, time.Time{}
	}
	parts := strings.SplitN(value, "|", 4)
	if len(parts) != 4 {
		return "", time.Time{}
	}
	unixNano, err := time.ParseDuration(parts[1] + "ns")
	if err != nil {
		return "", time.Time{}
	}
	return parts[3], time.Unix(0, int64(unixNano))
}

func (s *Server) slackTriggerClaimActive(ctx context.Context, namespace, connectionName, value string) (bool, error) {
	claim, pendingSince := parseSlackTriggerClaim(value)
	if !pendingSince.IsZero() {
		return true, nil
	}
	return s.slackTriggerClaimBackedByProject(ctx, namespace, connectionName, claim)
}

func (s *Server) slackTriggerClaimBackedByProject(ctx context.Context, namespace, connectionName, claim string) (bool, error) {
	projectName, triggerName, ok := strings.Cut(claim, "/")
	if !ok || projectName == "" || triggerName == "" {
		return false, nil
	}
	project := &triggersv1alpha1.Project{}
	if err := s.apiReader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: projectName}, project); err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	for _, trigger := range project.Spec.Triggers {
		if trigger.Name == triggerName && dashboardProjectTriggerEnabled(trigger) && trigger.Type == triggersv1alpha1.ProjectTriggerTypeSlack && trigger.Slack != nil {
			return trigger.Slack.ConnectionRef.Name == connectionName, nil
		}
	}
	return false, nil
}

func (s *Server) claimSlackTriggerConnection(ctx context.Context, namespace, projectName string, trigger triggersv1alpha1.ProjectTrigger) (slackTriggerClaimHandle, error) {
	if trigger.Type != triggersv1alpha1.ProjectTriggerTypeSlack || trigger.Slack == nil || !dashboardProjectTriggerEnabled(trigger) {
		return slackTriggerClaimHandle{}, nil
	}
	connectionName := trigger.Slack.ConnectionRef.Name
	claim := slackTriggerClaim(projectName, trigger.Name)
	handle := slackTriggerClaimHandle{connectionName: connectionName, claim: claim}
	key := client.ObjectKey{Namespace: namespace, Name: connectionName}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		connection := &triggersv1alpha1.Connection{}
		if err := s.apiReader.Get(ctx, key, connection); err != nil {
			return err
		}
		existing := connection.Annotations[slackTriggerClaimAnnotation]
		if existing != "" {
			existingClaim, pendingSince := parseSlackTriggerClaim(existing)
			if existingClaim == claim && !pendingSince.IsZero() {
				return connect.NewError(connect.CodeAborted, fmt.Errorf("trigger %s already has an operation in progress", claim))
			}
			if existingClaim != claim {
				active, err := s.slackTriggerClaimActive(ctx, namespace, connectionName, existing)
				if err != nil {
					return err
				}
				if active {
					return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("slack connection %q is already claimed by enabled trigger %s", connectionName, existingClaim))
				}
			}
		}
		if connection.Annotations == nil {
			connection.Annotations = map[string]string{}
		}
		handle.pendingValue = fmt.Sprintf("%s%d|%s|%s", slackPendingClaimPrefix, time.Now().UnixNano(), uuid.NewString(), claim)
		connection.Annotations[slackTriggerClaimAnnotation] = handle.pendingValue
		return s.k8sClient.Update(ctx, connection)
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return slackTriggerClaimHandle{}, err
		}
		return slackTriggerClaimHandle{}, mapK8sError("claim Slack connection", err)
	}
	return handle, nil
}

func (s *Server) verifySlackTriggerClaim(ctx context.Context, namespace string, handle slackTriggerClaimHandle) error {
	if handle.connectionName == "" {
		return nil
	}
	connection := &triggersv1alpha1.Connection{}
	if err := s.apiReader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: handle.connectionName}, connection); err != nil {
		return mapK8sError("verify Slack connection claim", err)
	}
	if connection.Annotations[slackTriggerClaimAnnotation] != handle.pendingValue {
		return connect.NewError(connect.CodeAborted, fmt.Errorf("slack connection claim changed during trigger update"))
	}
	return nil
}

func (s *Server) finalizeSlackTriggerClaim(ctx context.Context, namespace string, handle slackTriggerClaimHandle) error {
	if handle.connectionName == "" || handle.pendingValue == "" {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	key := client.ObjectKey{Namespace: namespace, Name: handle.connectionName}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		connection := &triggersv1alpha1.Connection{}
		if err := s.apiReader.Get(cleanupCtx, key, connection); err != nil {
			return err
		}
		if connection.Annotations[slackTriggerClaimAnnotation] != handle.pendingValue {
			return fmt.Errorf("slack connection claim changed before it could be finalized")
		}
		connection.Annotations[slackTriggerClaimAnnotation] = handle.claim
		return s.k8sClient.Update(cleanupCtx, connection)
	})
}

func (s *Server) releasePendingSlackTriggerClaim(ctx context.Context, namespace string, handle slackTriggerClaimHandle) {
	if handle.connectionName == "" || handle.pendingValue == "" {
		return
	}
	s.releaseSlackTriggerClaimValue(ctx, namespace, handle.connectionName, handle.pendingValue)
}

func (s *Server) releaseStableSlackTriggerClaim(ctx context.Context, namespace, connectionName, claim string) {
	if connectionName == "" || claim == "" {
		return
	}
	s.releaseSlackTriggerClaimValue(ctx, namespace, connectionName, claim)
}

func (s *Server) releaseSlackTriggerClaimValue(ctx context.Context, namespace, connectionName, expected string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	key := client.ObjectKey{Namespace: namespace, Name: connectionName}
	_ = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		connection := &triggersv1alpha1.Connection{}
		if err := s.apiReader.Get(cleanupCtx, key, connection); err != nil {
			return client.IgnoreNotFound(err)
		}
		if connection.Annotations[slackTriggerClaimAnnotation] != expected {
			return nil
		}
		delete(connection.Annotations, slackTriggerClaimAnnotation)
		return s.k8sClient.Update(cleanupCtx, connection)
	})
}

func (s *Server) CreateProjectTrigger(ctx context.Context, req *platform.CreateProjectTriggerRequest) (*platform.Project, error) {
	namespace, project, err := projectTriggerTarget(req.GetNamespace(), req.GetProject())
	if err != nil {
		return nil, err
	}
	if err := s.requireResourceAccess(ctx, projectResourceType, project, namespace, AccessCollaborator, "create a project trigger"); err != nil {
		return nil, err
	}
	name, err := projectTriggerName(req.GetName())
	if err != nil {
		return nil, err
	}
	trigger, err := projectTriggerFromProto(req.GetTrigger())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if trigger.Name != name {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("trigger.name must match name"))
	}
	if err := s.validateProjectTriggerConnection(ctx, namespace, trigger); err != nil {
		return nil, err
	}
	claimHandle, err := s.claimSlackTriggerConnection(ctx, namespace, project, trigger)
	if err != nil {
		return nil, err
	}
	if err := s.validateSlackConnectionExclusive(ctx, namespace, project, name, trigger); err != nil {
		s.releasePendingSlackTriggerClaim(ctx, namespace, claimHandle)
		return nil, err
	}
	updated, err := s.patchProjectWithRetry(ctx, namespace, project, func(fresh *triggersv1alpha1.Project) error {
		for _, existing := range fresh.Spec.Triggers {
			if existing.Name == trigger.Name {
				return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("project trigger %q already exists", trigger.Name))
			}
		}
		if err := s.verifySlackTriggerClaim(ctx, namespace, claimHandle); err != nil {
			return err
		}
		fresh.Spec.Triggers = append(fresh.Spec.Triggers, trigger)
		return nil
	})
	if err != nil {
		if projectPatchDefinitelyNotCommitted(err) {
			s.releasePendingSlackTriggerClaim(ctx, namespace, claimHandle)
		}
		return nil, mapProjectTriggerError("create project trigger", err)
	}
	_ = s.finalizeSlackTriggerClaim(ctx, namespace, claimHandle)
	return s.enrichProjectProto(ctx, k8sProjectToProto(updated)), nil
}

func (s *Server) UpdateProjectTrigger(ctx context.Context, req *platform.UpdateProjectTriggerRequest) (*platform.Project, error) {
	namespace, project, err := projectTriggerTarget(req.GetNamespace(), req.GetProject())
	if err != nil {
		return nil, err
	}
	name, err := projectTriggerName(req.GetName())
	if err != nil {
		return nil, err
	}
	if err := s.requireResourceAccess(ctx, projectResourceType, project, namespace, AccessCollaborator, "update a project trigger"); err != nil {
		return nil, err
	}
	trigger, err := projectTriggerFromProto(req.GetTrigger())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if trigger.Name != name {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("trigger.name must match name"))
	}
	if err := s.validateProjectTriggerConnection(ctx, namespace, trigger); err != nil {
		return nil, err
	}
	claimHandle, err := s.claimSlackTriggerConnection(ctx, namespace, project, trigger)
	if err != nil {
		return nil, err
	}
	if err := s.validateSlackConnectionExclusive(ctx, namespace, project, name, trigger); err != nil {
		s.releasePendingSlackTriggerClaim(ctx, namespace, claimHandle)
		return nil, err
	}
	replacedConnection := ""
	updated, err := s.patchProjectWithRetry(ctx, namespace, project, func(fresh *triggersv1alpha1.Project) error {
		for i, existing := range fresh.Spec.Triggers {
			if existing.Name != name {
				continue
			}
			if trigger.Type != existing.Type {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("trigger type is immutable"))
			}
			if dashboardProjectTriggerEnabled(existing) && existing.Type == triggersv1alpha1.ProjectTriggerTypeSlack && existing.Slack != nil {
				replacedConnection = existing.Slack.ConnectionRef.Name
			}
			if err := s.verifySlackTriggerClaim(ctx, namespace, claimHandle); err != nil {
				return err
			}
			fresh.Spec.Triggers[i] = trigger
			return nil
		}
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("project trigger %q not found", name))
	})
	if err != nil {
		if projectPatchDefinitelyNotCommitted(err) {
			s.releasePendingSlackTriggerClaim(ctx, namespace, claimHandle)
		}
		return nil, mapProjectTriggerError("update project trigger", err)
	}
	_ = s.finalizeSlackTriggerClaim(ctx, namespace, claimHandle)
	if replacedConnection != claimHandle.connectionName {
		s.releaseStableSlackTriggerClaim(ctx, namespace, replacedConnection, slackTriggerClaim(project, name))
	}
	return s.enrichProjectProto(ctx, k8sProjectToProto(updated)), nil
}

func (s *Server) DeleteProjectTrigger(ctx context.Context, req *platform.DeleteProjectTriggerRequest) (*emptypb.Empty, error) {
	namespace, project, err := projectTriggerTarget(req.GetNamespace(), req.GetProject())
	if err != nil {
		return nil, err
	}
	name, err := projectTriggerName(req.GetName())
	if err != nil {
		return nil, err
	}
	if err := s.requireResourceAccess(ctx, projectResourceType, project, namespace, AccessCollaborator, "delete a project trigger"); err != nil {
		return nil, err
	}
	releasedConnection := ""
	_, err = s.patchProjectWithRetry(ctx, namespace, project, func(fresh *triggersv1alpha1.Project) error {
		for i, trigger := range fresh.Spec.Triggers {
			if trigger.Name != name {
				continue
			}
			if dashboardProjectTriggerEnabled(trigger) && trigger.Type == triggersv1alpha1.ProjectTriggerTypeSlack && trigger.Slack != nil {
				releasedConnection = trigger.Slack.ConnectionRef.Name
			}
			fresh.Spec.Triggers = append(fresh.Spec.Triggers[:i:i], fresh.Spec.Triggers[i+1:]...)
			return nil
		}
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("project trigger %q not found", name))
	})
	if err != nil {
		return nil, mapProjectTriggerError("delete project trigger", err)
	}
	s.releaseStableSlackTriggerClaim(ctx, namespace, releasedConnection, slackTriggerClaim(project, name))
	return &emptypb.Empty{}, nil
}

func (s *Server) SetProjectTriggerEnabled(ctx context.Context, req *platform.SetProjectTriggerEnabledRequest) (*platform.Project, error) {
	namespace, project, err := projectTriggerTarget(req.GetNamespace(), req.GetProject())
	if err != nil {
		return nil, err
	}
	name, err := projectTriggerName(req.GetName())
	if err != nil {
		return nil, err
	}
	if err := s.requireResourceAccess(ctx, projectResourceType, project, namespace, AccessCollaborator, "set a project trigger enabled state"); err != nil {
		return nil, err
	}
	enabled := req.GetEnabled()
	claim := slackTriggerClaim(project, name)
	claimedConnections := map[string]slackTriggerClaimHandle{}
	previousConnection := ""
	finalConnection := ""
	updated, err := s.patchProjectWithRetry(ctx, namespace, project, func(fresh *triggersv1alpha1.Project) error {
		previousConnection = ""
		finalConnection = ""
		for i := range fresh.Spec.Triggers {
			trigger := fresh.Spec.Triggers[i]
			if trigger.Name != name {
				continue
			}
			if dashboardProjectTriggerEnabled(trigger) && trigger.Type == triggersv1alpha1.ProjectTriggerTypeSlack && trigger.Slack != nil {
				previousConnection = trigger.Slack.ConnectionRef.Name
			}
			trigger.Enabled = &enabled
			if enabled {
				if err := s.validateProjectTriggerConnection(ctx, namespace, trigger); err != nil {
					return err
				}
				connectionName := ""
				if trigger.Type == triggersv1alpha1.ProjectTriggerTypeSlack && trigger.Slack != nil {
					connectionName = trigger.Slack.ConnectionRef.Name
				}
				claimHandle, ok := claimedConnections[connectionName]
				if !ok {
					var err error
					claimHandle, err = s.claimSlackTriggerConnection(ctx, namespace, project, trigger)
					if err != nil {
						return err
					}
					if claimHandle.connectionName != "" {
						claimedConnections[claimHandle.connectionName] = claimHandle
					}
				}
				if err := s.validateSlackConnectionExclusive(ctx, namespace, project, name, trigger); err != nil {
					return err
				}
				if err := s.verifySlackTriggerClaim(ctx, namespace, claimHandle); err != nil {
					return err
				}
				finalConnection = claimHandle.connectionName
			}
			fresh.Spec.Triggers[i] = trigger
			return nil
		}
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("project trigger %q not found", name))
	})
	if err != nil {
		if projectPatchDefinitelyNotCommitted(err) {
			for _, claimHandle := range claimedConnections {
				s.releasePendingSlackTriggerClaim(ctx, namespace, claimHandle)
			}
		}
		return nil, mapProjectTriggerError("set project trigger enabled state", err)
	}
	for connectionName, claimHandle := range claimedConnections {
		if connectionName == finalConnection {
			_ = s.finalizeSlackTriggerClaim(ctx, namespace, claimHandle)
		} else {
			s.releasePendingSlackTriggerClaim(ctx, namespace, claimHandle)
		}
	}
	if previousConnection != finalConnection {
		s.releaseStableSlackTriggerClaim(ctx, namespace, previousConnection, claim)
	}
	return s.enrichProjectProto(ctx, k8sProjectToProto(updated)), nil
}

func (s *Server) DeleteProject(ctx context.Context, req *platform.DeleteProjectRequest) (*emptypb.Empty, error) {
	namespace := strings.TrimSpace(req.GetNamespace())
	name := strings.TrimSpace(req.GetName())
	if namespace == "" || name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace and name are required"))
	}
	if err := s.requireResourceAccess(ctx, projectResourceType, name, namespace, AccessOwner, "delete this project"); err != nil {
		return nil, err
	}
	project := &triggersv1alpha1.Project{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, project); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get Project %s/%s", namespace, name), err)
	}
	if err := s.k8sClient.Delete(ctx, project); err != nil && !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("delete Project", err)
	}
	return &emptypb.Empty{}, nil
}

func projectTriggerTarget(namespace, project string) (string, string, error) {
	namespace = strings.TrimSpace(namespace)
	project = strings.TrimSpace(project)
	if namespace == "" || project == "" {
		return "", "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace and project are required"))
	}
	return namespace, project, nil
}

func projectTriggerName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("trigger name is required"))
	}
	if name == "manual" {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("trigger name %q is reserved", name))
	}
	if problems := validation.IsDNS1123Label(name); len(problems) != 0 {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("trigger name must be a valid DNS-1123 label: %s", strings.Join(problems, "; ")))
	}
	return name, nil
}

func validSlackID(value, prefixes string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 2 || !strings.ContainsRune(prefixes, rune(value[0])) {
		return false
	}
	for _, r := range value[1:] {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func normalizedSlackUserIDs(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		id := strings.TrimSpace(value)
		if id == "" {
			continue
		}
		if !validSlackID(id, "UW") {
			return nil, fmt.Errorf("invalid Slack user ID %q; expected an ID starting with U or W", id)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func projectTriggerFromProto(pb *platform.ProjectTrigger) (triggersv1alpha1.ProjectTrigger, error) {
	if pb == nil {
		return triggersv1alpha1.ProjectTrigger{}, fmt.Errorf("trigger is required")
	}
	name, err := projectTriggerName(pb.GetName())
	if err != nil {
		return triggersv1alpha1.ProjectTrigger{}, err
	}
	trigger := triggersv1alpha1.ProjectTrigger{Name: name, Type: triggersv1alpha1.ProjectTriggerType(strings.TrimSpace(pb.GetType()))}
	if pb.Enabled != nil {
		enabled := pb.GetEnabled()
		trigger.Enabled = &enabled
	}

	switch trigger.Type {
	case triggersv1alpha1.ProjectTriggerTypeGitHub:
		if pb.GetGithub() == nil || pb.GetSlack() != nil || pb.GetCron() != nil || pb.GetLinear() != nil {
			return triggersv1alpha1.ProjectTrigger{}, fmt.Errorf("github trigger requires only github configuration")
		}
		config := pb.GetGithub()
		connectionRef, owner, repo := strings.TrimSpace(config.GetConnectionRef()), strings.TrimSpace(config.GetOwner()), strings.TrimSpace(config.GetRepo())
		if connectionRef == "" || owner == "" || repo == "" {
			return triggersv1alpha1.ProjectTrigger{}, fmt.Errorf("github trigger requires connection_ref, owner, and repo")
		}
		pollInterval, err := projectTriggerDuration(config.GetPollInterval(), "poll_interval")
		if err != nil {
			return triggersv1alpha1.ProjectTrigger{}, err
		}
		authAllowedUsers := nonEmptyTrimmedStrings(config.GetAuthAllowedUsers())
		authDenyUsers := nonEmptyTrimmedStrings(config.GetAuthDenyUsers())
		var auth *triggersv1alpha1.TriggerAuth
		if len(authAllowedUsers) > 0 || len(authDenyUsers) > 0 {
			auth = &triggersv1alpha1.TriggerAuth{AllowedUsers: authAllowedUsers, DenyUsers: authDenyUsers}
		}
		trigger.GitHub = &triggersv1alpha1.GitHubProjectTriggerConfig{
			ConnectionRef:  triggersv1alpha1.ConnectionRef{Name: connectionRef},
			Owner:          owner,
			Repo:           repo,
			Issues:         config.GetIssues(),
			Comments:       config.GetComments(),
			TriggerKeyword: strings.TrimSpace(config.GetTriggerKeyword()),
			PollInterval:   pollInterval,
			Auth:           auth,
		}
	case triggersv1alpha1.ProjectTriggerTypeSlack:
		if pb.GetGithub() != nil || pb.GetSlack() == nil || pb.GetCron() != nil || pb.GetLinear() != nil {
			return triggersv1alpha1.ProjectTrigger{}, fmt.Errorf("slack trigger requires only slack configuration")
		}
		config := pb.GetSlack()
		connectionRef, channel := strings.TrimSpace(config.GetConnectionRef()), strings.TrimSpace(config.GetChannel())
		if connectionRef == "" {
			return triggersv1alpha1.ProjectTrigger{}, fmt.Errorf("slack trigger requires connection_ref")
		}
		// An empty channel means the agent responds in any conversation the bot
		// is invited to and @mentioned in.
		if channel != "" && !validSlackID(channel, "CGD") {
			return triggersv1alpha1.ProjectTrigger{}, fmt.Errorf("invalid Slack conversation ID %q; expected an ID starting with C, G, or D", channel)
		}
		commanders, err := normalizedSlackUserIDs(config.GetCommanders())
		if err != nil {
			return triggersv1alpha1.ProjectTrigger{}, err
		}
		replyMode := triggersv1alpha1.SlackChannelReplyMode(strings.TrimSpace(config.GetChannelReplyMode()))
		if replyMode != "" && replyMode != triggersv1alpha1.SlackChannelReplyRequireApproval && replyMode != triggersv1alpha1.SlackChannelReplyAuto {
			return triggersv1alpha1.ProjectTrigger{}, fmt.Errorf("invalid channel_reply_mode %q", config.GetChannelReplyMode())
		}
		if config.SessionIdleMinutes != nil && config.GetSessionIdleMinutes() <= 0 {
			return triggersv1alpha1.ProjectTrigger{}, fmt.Errorf("session_idle_minutes must be greater than zero")
		}
		trigger.Slack = &triggersv1alpha1.SlackProjectTriggerConfig{
			ConnectionRef:      triggersv1alpha1.ConnectionRef{Name: connectionRef},
			Channel:            channel,
			ChannelReplyMode:   replyMode,
			Commanders:         commanders,
			SessionIdleMinutes: config.SessionIdleMinutes,
		}
	case triggersv1alpha1.ProjectTriggerTypeCron:
		if pb.GetGithub() != nil || pb.GetSlack() != nil || pb.GetCron() == nil || pb.GetLinear() != nil {
			return triggersv1alpha1.ProjectTrigger{}, fmt.Errorf("cron trigger requires only cron configuration")
		}
		config := pb.GetCron()
		if err := validateCronSchedule(config.GetSchedule(), config.GetTimeZone()); err != nil {
			return triggersv1alpha1.ProjectTrigger{}, err
		}
		policy, err := cronConcurrencyPolicy(config.GetConcurrencyPolicy())
		if err != nil {
			return triggersv1alpha1.ProjectTrigger{}, err
		}
		prompt := strings.TrimSpace(config.GetPrompt())
		if prompt == "" {
			return triggersv1alpha1.ProjectTrigger{}, fmt.Errorf("cron trigger requires prompt")
		}
		trigger.Cron = &triggersv1alpha1.CronProjectTriggerConfig{
			Schedule:          strings.TrimSpace(config.GetSchedule()),
			TimeZone:          strings.TrimSpace(config.GetTimeZone()),
			ConcurrencyPolicy: policy,
			Prompt:            prompt,
		}
	case triggersv1alpha1.ProjectTriggerTypeLinear:
		if pb.GetGithub() != nil || pb.GetSlack() != nil || pb.GetCron() != nil || pb.GetLinear() == nil {
			return triggersv1alpha1.ProjectTrigger{}, fmt.Errorf("linear trigger requires only linear configuration")
		}
		config := pb.GetLinear()
		connectionRef, projectID, teamID := strings.TrimSpace(config.GetConnectionRef()), strings.TrimSpace(config.GetProjectId()), strings.TrimSpace(config.GetTeamId())
		if connectionRef == "" || projectID == "" || teamID == "" {
			return triggersv1alpha1.ProjectTrigger{}, fmt.Errorf("linear trigger requires connection_ref, project_id, and team_id")
		}
		pollInterval, err := projectTriggerDuration(config.GetPollInterval(), "poll_interval")
		if err != nil {
			return triggersv1alpha1.ProjectTrigger{}, err
		}
		trigger.Linear = &triggersv1alpha1.LinearProjectTriggerConfig{
			ConnectionRef: triggersv1alpha1.ConnectionRef{Name: connectionRef},
			ProjectID:     projectID,
			TeamID:        teamID,
			ApprovedLabel: strings.TrimSpace(config.GetApprovedLabel()),
			PollInterval:  pollInterval,
			AutoCreate:    config.GetAutoCreate(),
		}
	default:
		return triggersv1alpha1.ProjectTrigger{}, fmt.Errorf("invalid trigger type %q", pb.GetType())
	}
	return trigger, nil
}

func dashboardProjectTriggerEnabled(trigger triggersv1alpha1.ProjectTrigger) bool {
	return trigger.Enabled == nil || *trigger.Enabled
}

func (s *Server) validateSlackConnectionExclusive(ctx context.Context, namespace, projectName, triggerName string, trigger triggersv1alpha1.ProjectTrigger) error {
	if trigger.Type != triggersv1alpha1.ProjectTriggerTypeSlack || trigger.Slack == nil || !dashboardProjectTriggerEnabled(trigger) {
		return nil
	}
	projects := &triggersv1alpha1.ProjectList{}
	if err := s.apiReader.List(ctx, projects, client.InNamespace(namespace)); err != nil {
		return mapK8sError("list Projects for Slack connection exclusivity", err)
	}
	connectionName := trigger.Slack.ConnectionRef.Name
	for i := range projects.Items {
		project := &projects.Items[i]
		for _, existing := range project.Spec.Triggers {
			if project.Name == projectName && existing.Name == triggerName {
				continue
			}
			if dashboardProjectTriggerEnabled(existing) && existing.Type == triggersv1alpha1.ProjectTriggerTypeSlack && existing.Slack != nil && existing.Slack.ConnectionRef.Name == connectionName {
				return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("slack connection %q is already used by enabled trigger %s/%s; Socket Mode connections cannot be shared", connectionName, project.Name, existing.Name))
			}
		}
	}
	return nil
}

func (s *Server) validateProjectTriggerConnection(ctx context.Context, namespace string, trigger triggersv1alpha1.ProjectTrigger) error {
	var name string
	var expectedType triggersv1alpha1.ConnectionType
	switch trigger.Type {
	case triggersv1alpha1.ProjectTriggerTypeGitHub:
		name, expectedType = trigger.GitHub.ConnectionRef.Name, triggersv1alpha1.ConnectionTypeGitHub
	case triggersv1alpha1.ProjectTriggerTypeSlack:
		name, expectedType = trigger.Slack.ConnectionRef.Name, triggersv1alpha1.ConnectionTypeSlack
	case triggersv1alpha1.ProjectTriggerTypeLinear:
		name, expectedType = trigger.Linear.ConnectionRef.Name, triggersv1alpha1.ConnectionTypeLinear
	default:
		return nil
	}
	connection := &triggersv1alpha1.Connection{}
	if err := s.apiReader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, connection); err != nil {
		if k8serrors.IsNotFound(err) {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("connection %q not found", name))
		}
		return mapK8sError(fmt.Sprintf("get Connection %s/%s", namespace, name), err)
	}
	if connection.Spec.Type != expectedType {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("connection %q must have type %q", name, expectedType))
	}
	return nil
}

func projectTriggerDuration(value, field string) (metav1.Duration, error) {
	var duration metav1.Duration
	value = strings.TrimSpace(value)
	if value == "" {
		return duration, nil
	}
	if err := duration.UnmarshalJSON([]byte(fmt.Sprintf("%q", value))); err != nil || duration.Duration <= 0 {
		return metav1.Duration{}, fmt.Errorf("%s must be a positive Go duration", field)
	}
	return duration, nil
}

func projectPatchDefinitelyNotCommitted(err error) bool {
	if err == nil {
		return false
	}
	if connect.CodeOf(err) != connect.CodeUnknown {
		return true
	}
	return k8serrors.IsConflict(err) || k8serrors.IsInvalid(err) || k8serrors.IsNotFound(err) || k8serrors.IsAlreadyExists(err)
}

func mapProjectTriggerError(operation string, err error) error {
	if connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	return mapK8sError(operation, err)
}
