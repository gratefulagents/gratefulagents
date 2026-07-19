package dashboard

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

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
	updated, err := s.patchProjectWithRetry(ctx, namespace, project, func(fresh *triggersv1alpha1.Project) error {
		for _, existing := range fresh.Spec.Triggers {
			if existing.Name == trigger.Name {
				return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("project trigger %q already exists", trigger.Name))
			}
		}
		fresh.Spec.Triggers = append(fresh.Spec.Triggers, trigger)
		return nil
	})
	if err != nil {
		return nil, mapProjectTriggerError("create project trigger", err)
	}
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
	updated, err := s.patchProjectWithRetry(ctx, namespace, project, func(fresh *triggersv1alpha1.Project) error {
		for i, existing := range fresh.Spec.Triggers {
			if existing.Name != name {
				continue
			}
			if trigger.Type != existing.Type {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("trigger type is immutable"))
			}
			fresh.Spec.Triggers[i] = trigger
			return nil
		}
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("project trigger %q not found", name))
	})
	if err != nil {
		return nil, mapProjectTriggerError("update project trigger", err)
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
	_, err = s.patchProjectWithRetry(ctx, namespace, project, func(fresh *triggersv1alpha1.Project) error {
		for i, trigger := range fresh.Spec.Triggers {
			if trigger.Name != name {
				continue
			}
			fresh.Spec.Triggers = append(fresh.Spec.Triggers[:i:i], fresh.Spec.Triggers[i+1:]...)
			return nil
		}
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("project trigger %q not found", name))
	})
	if err != nil {
		return nil, mapProjectTriggerError("delete project trigger", err)
	}
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
	updated, err := s.patchProjectWithRetry(ctx, namespace, project, func(fresh *triggersv1alpha1.Project) error {
		for i := range fresh.Spec.Triggers {
			if fresh.Spec.Triggers[i].Name != name {
				continue
			}
			fresh.Spec.Triggers[i].Enabled = &enabled
			return nil
		}
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("project trigger %q not found", name))
	})
	if err != nil {
		return nil, mapProjectTriggerError("set project trigger enabled state", err)
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
		if connectionRef == "" || channel == "" {
			return triggersv1alpha1.ProjectTrigger{}, fmt.Errorf("slack trigger requires connection_ref and channel")
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
			Commanders:         nonEmptyTrimmedStrings(config.GetCommanders()),
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
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, connection); err != nil {
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

func mapProjectTriggerError(operation string, err error) error {
	if connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	return mapK8sError(operation, err)
}
