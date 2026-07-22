package triggers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strings"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	projectGeneratedRuntimeLabel  = "triggers.gratefulagents.dev/generated-runtime"
	projectNameLabel              = "triggers.gratefulagents.dev/project-name"
	projectUIDLabel               = "triggers.gratefulagents.dev/project-uid"
	projectTriggerNameLabel       = "triggers.gratefulagents.dev/project-trigger-name"
	projectTriggerTypeLabel       = "triggers.gratefulagents.dev/project-trigger-type"
	projectNameAnnotation         = "triggers.gratefulagents.dev/project-name"
	projectUIDAnnotation          = "triggers.gratefulagents.dev/project-uid"
	projectTriggerNameAnnotation  = "triggers.gratefulagents.dev/project-trigger-name"
	projectTriggerTypeAnnotation  = "triggers.gratefulagents.dev/project-trigger-type"
	projectSlackChannelAnnotation = "triggers.gratefulagents.dev/project-trigger-channel"
	projectSlackTeamIDAnnotation  = "triggers.gratefulagents.dev/project-slack-team-id"
)

// ProjectReconciler compiles Project trigger declarations into standalone
// runtime trigger CRs. The runtime controllers remain responsible for their
// own source-specific work and status.
type ProjectReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=projects,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=projects/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=connections,verbs=get;list;watch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=crons;githubrepositories;linearprojects;slackagents,verbs=get;list;watch;create;update;patch;delete

func (r *ProjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	project := &triggersv1alpha1.Project{}
	if err := r.Get(ctx, req.NamespacedName, project); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	desired := map[string]client.Object{}
	statuses := make([]triggersv1alpha1.ProjectTriggerStatus, 0, len(project.Spec.Triggers))
	ready := true

	for _, trigger := range project.Spec.Triggers {
		if !projectTriggerEnabled(trigger) {
			statuses = append(statuses, projectDisabledTriggerStatus(project, trigger))
			continue
		}

		child, err := r.compileTrigger(ctx, project, trigger)
		if err != nil {
			ready = false
			statuses = append(statuses, projectCompileErrorStatus(project, trigger, err))
			continue
		}
		desired[projectGeneratedChildKey(child)] = child
	}

	if err := r.cleanupGeneratedChildren(ctx, project, desired); err != nil {
		return ctrl.Result{}, err
	}
	for _, child := range desired {
		if err := r.upsertGeneratedChild(ctx, child); err != nil {
			return ctrl.Result{}, err
		}
	}

	for _, trigger := range project.Spec.Triggers {
		if !projectTriggerEnabled(trigger) {
			continue
		}
		child, ok := desired[projectGeneratedChildKeyForTrigger(project, trigger)]
		if !ok {
			continue
		}
		status, triggerReady, err := r.normalizedChildStatus(ctx, project, trigger, child)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !triggerReady {
			ready = false
		}
		statuses = append(statuses, status)
	}

	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	if err := r.updateProjectStatus(ctx, client.ObjectKeyFromObject(project), statuses, ready); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("updating Project status: %w", err)
	}
	return ctrl.Result{}, nil
}

func projectTriggerEnabled(trigger triggersv1alpha1.ProjectTrigger) bool {
	return trigger.Enabled == nil || *trigger.Enabled
}

func (r *ProjectReconciler) ensureSlackConnectionExclusive(ctx context.Context, owner *triggersv1alpha1.Project, trigger triggersv1alpha1.ProjectTrigger) error {
	projects := &triggersv1alpha1.ProjectList{}
	if err := r.List(ctx, projects, client.InNamespace(owner.Namespace)); err != nil {
		return fmt.Errorf("listing Projects for Slack connection exclusivity: %w", err)
	}
	for i := range projects.Items {
		project := &projects.Items[i]
		for _, other := range project.Spec.Triggers {
			if project.Name == owner.Name && other.Name == trigger.Name {
				continue
			}
			if projectTriggerEnabled(other) && other.Type == triggersv1alpha1.ProjectTriggerTypeSlack && other.Slack != nil && other.Slack.ConnectionRef.Name == trigger.Slack.ConnectionRef.Name {
				return fmt.Errorf("slack connection %q is also used by enabled trigger %s/%s; Socket Mode connections cannot be shared", trigger.Slack.ConnectionRef.Name, project.Name, other.Name)
			}
		}
	}
	return nil
}

func (r *ProjectReconciler) compileTrigger(ctx context.Context, project *triggersv1alpha1.Project, trigger triggersv1alpha1.ProjectTrigger) (client.Object, error) {
	metadata := r.generatedMetadata(project, trigger)
	defaults := projectDefaults(project)

	switch trigger.Type {
	case triggersv1alpha1.ProjectTriggerTypeGitHub:
		if trigger.GitHub == nil {
			return nil, fmt.Errorf("github configuration is required")
		}
		connection, err := r.connection(ctx, project.Namespace, trigger.GitHub.ConnectionRef.Name, triggersv1alpha1.ConnectionTypeGitHub)
		if err != nil {
			return nil, err
		}
		child := &triggersv1alpha1.GitHubRepository{
			ObjectMeta: metadata,
			Spec: triggersv1alpha1.GitHubRepositorySpec{
				Owner:          trigger.GitHub.Owner,
				Repo:           trigger.GitHub.Repo,
				PollInterval:   trigger.GitHub.PollInterval,
				TriggerKeyword: trigger.GitHub.TriggerKeyword,
				Auth:           projectGitHubAuth(project.Spec.Auth, trigger.GitHub.Auth),
				Maintainer:     trigger.GitHub.Maintainer.DeepCopy(),
				Defaults:       defaults,
			},
		}
		if project.Spec.ReviewLoop != nil {
			child.Spec.ReviewLoop = &triggersv1alpha1.ReviewLoopSpec{Disabled: project.Spec.ReviewLoop.Disabled}
		}
		child.Annotations["triggers.gratefulagents.dev/project-trigger-issues"] = fmt.Sprintf("%t", trigger.GitHub.Issues)
		child.Annotations["triggers.gratefulagents.dev/project-trigger-comments"] = fmt.Sprintf("%t", trigger.GitHub.Comments)
		if connection.Spec.GitHub.TokenSecret != "" {
			child.Spec.GitHubTokenSecret = connection.Spec.GitHub.TokenSecret
		} else if connection.Spec.GitHub.AppID > 0 && connection.Spec.GitHub.InstallationID > 0 && connection.Spec.GitHub.PrivateKeySecret != "" {
			child.Spec.GitHubApp = &triggersv1alpha1.GitHubAppAuth{
				AppID:            connection.Spec.GitHub.AppID,
				InstallationID:   connection.Spec.GitHub.InstallationID,
				PrivateKeySecret: connection.Spec.GitHub.PrivateKeySecret,
			}
		} else {
			return nil, fmt.Errorf("Connection %q must specify github.tokenSecret or complete github app configuration", connection.Name)
		}
		return child, r.setProjectControllerReference(project, child)

	case triggersv1alpha1.ProjectTriggerTypeSlack:
		if trigger.Slack == nil {
			return nil, fmt.Errorf("slack configuration is required")
		}
		if err := r.ensureSlackConnectionExclusive(ctx, project, trigger); err != nil {
			return nil, err
		}
		connection, err := r.connection(ctx, project.Namespace, trigger.Slack.ConnectionRef.Name, triggersv1alpha1.ConnectionTypeSlack)
		if err != nil {
			return nil, err
		}
		if connection.Spec.Slack.TokensSecret == "" {
			return nil, fmt.Errorf("Connection %q must specify slack.tokensSecret", connection.Name)
		}
		child := &triggersv1alpha1.SlackAgent{
			ObjectMeta: metadata,
			Spec: triggersv1alpha1.SlackAgentSpec{
				TokensSecret:       connection.Spec.Slack.TokensSecret,
				SlackUserID:        connection.Spec.Slack.SlackUserID,
				ChannelReplyMode:   trigger.Slack.ChannelReplyMode,
				Commanders:         append([]string(nil), trigger.Slack.Commanders...),
				SessionIdleMinutes: trigger.Slack.SessionIdleMinutes,
				Defaults:           defaults,
			},
		}
		child.Annotations[projectSlackChannelAnnotation] = trigger.Slack.Channel
		if teamID := strings.TrimSpace(connection.Spec.Slack.TeamID); teamID != "" {
			child.Annotations[projectSlackTeamIDAnnotation] = teamID
		}
		return child, r.setProjectControllerReference(project, child)

	case triggersv1alpha1.ProjectTriggerTypeCron:
		if trigger.Cron == nil {
			return nil, fmt.Errorf("cron configuration is required")
		}
		child := &triggersv1alpha1.Cron{
			ObjectMeta: metadata,
			Spec: triggersv1alpha1.CronSpec{
				Schedule:          trigger.Cron.Schedule,
				TimeZone:          trigger.Cron.TimeZone,
				ConcurrencyPolicy: trigger.Cron.ConcurrencyPolicy,
				Prompt:            trigger.Cron.Prompt,
				Defaults:          defaults,
			},
		}
		return child, r.setProjectControllerReference(project, child)

	case triggersv1alpha1.ProjectTriggerTypeLinear:
		if trigger.Linear == nil {
			return nil, fmt.Errorf("linear configuration is required")
		}
		connection, err := r.connection(ctx, project.Namespace, trigger.Linear.ConnectionRef.Name, triggersv1alpha1.ConnectionTypeLinear)
		if err != nil {
			return nil, err
		}
		if connection.Spec.Linear.APIKeySecret == "" {
			return nil, fmt.Errorf("Connection %q must specify linear.apiKeySecret", connection.Name)
		}
		child := &triggersv1alpha1.LinearProject{
			ObjectMeta: metadata,
			Spec: triggersv1alpha1.LinearProjectSpec{
				LinearAPIKeySecret: connection.Spec.Linear.APIKeySecret,
				ProjectID:          trigger.Linear.ProjectID,
				TeamID:             trigger.Linear.TeamID,
				PollInterval:       trigger.Linear.PollInterval,
				ApprovedLabel:      trigger.Linear.ApprovedLabel,
				AutoCreateTasks:    trigger.Linear.AutoCreate,
				Defaults:           defaults,
			},
		}
		return child, r.setProjectControllerReference(project, child)
	default:
		return nil, fmt.Errorf("unsupported trigger type %q", trigger.Type)
	}
}

func (r *ProjectReconciler) connection(ctx context.Context, namespace, name string, wantType triggersv1alpha1.ConnectionType) (*triggersv1alpha1.Connection, error) {
	connection := &triggersv1alpha1.Connection{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, connection); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("Connection %q not found", name)
		}
		return nil, err
	}
	if connection.Spec.Type != wantType {
		return nil, fmt.Errorf("Connection %q has type %q, want %q", name, connection.Spec.Type, wantType)
	}
	return connection, nil
}

func projectDefaults(project *triggersv1alpha1.Project) triggersv1alpha1.AgentRunDefaults {
	var defaults triggersv1alpha1.AgentRunDefaults
	project.Spec.Defaults.DeepCopyInto(&defaults)
	if project.Spec.KubernetesAdmin {
		defaults.KubernetesAdmin = true
	}
	return defaults
}

func projectGitHubAuth(projectAuth, triggerAuth *triggersv1alpha1.TriggerAuth) *triggersv1alpha1.TriggerAuth {
	if projectAuth == nil && triggerAuth == nil {
		return nil
	}
	if projectAuth == nil {
		return triggerAuth.DeepCopy()
	}
	if triggerAuth == nil {
		return projectAuth.DeepCopy()
	}

	auth := &triggersv1alpha1.TriggerAuth{DenyUsers: append([]string(nil), projectAuth.DenyUsers...)}
	for _, user := range triggerAuth.DenyUsers {
		if !containsProjectUser(auth.DenyUsers, user) {
			auth.DenyUsers = append(auth.DenyUsers, user)
		}
	}
	switch {
	case len(projectAuth.AllowedUsers) == 0:
		auth.AllowedUsers = append([]string(nil), triggerAuth.AllowedUsers...)
	case len(triggerAuth.AllowedUsers) == 0:
		auth.AllowedUsers = append([]string(nil), projectAuth.AllowedUsers...)
	default:
		for _, user := range projectAuth.AllowedUsers {
			if containsProjectUser(triggerAuth.AllowedUsers, user) {
				auth.AllowedUsers = append(auth.AllowedUsers, user)
			}
		}
	}
	return auth
}

func containsProjectUser(users []string, user string) bool {
	for _, candidate := range users {
		if candidate == user {
			return true
		}
	}
	return false
}

func (r *ProjectReconciler) generatedMetadata(project *triggersv1alpha1.Project, trigger triggersv1alpha1.ProjectTrigger) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      projectGeneratedChildName(project.Name, trigger.Name),
		Namespace: project.Namespace,
		Labels: map[string]string{
			projectGeneratedRuntimeLabel: "true",
			projectNameLabel:             projectLabelValue(project.Name),
			projectUIDLabel:              projectLabelValue(string(project.UID)),
			projectTriggerNameLabel:      projectLabelValue(trigger.Name),
			projectTriggerTypeLabel:      projectLabelValue(string(trigger.Type)),
		},
		Annotations: map[string]string{
			projectGeneratedRuntimeLabel: "true",
			projectNameAnnotation:        project.Name,
			projectUIDAnnotation:         string(project.UID),
			projectTriggerNameAnnotation: trigger.Name,
			projectTriggerTypeAnnotation: string(trigger.Type),
		},
	}
}

func projectGeneratedChildName(projectName, triggerName string) string {
	return triggersv1alpha1.ProjectGeneratedChildName(projectName, triggerName)
}

func projectLabelValue(value string) string {
	return projectDNSName(value)
}

func projectDNSName(value string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "project"
	}
	if len(name) <= 63 {
		return name
	}
	sum := sha256.Sum256([]byte(value))
	suffix := hex.EncodeToString(sum[:])[:8]
	return strings.TrimRight(name[:63-len(suffix)-1], "-") + "-" + suffix
}

func (r *ProjectReconciler) setProjectControllerReference(project *triggersv1alpha1.Project, child client.Object) error {
	if err := ctrl.SetControllerReference(project, child, r.Scheme); err != nil {
		return fmt.Errorf("setting Project controller reference: %w", err)
	}
	return nil
}

func projectGeneratedChildKeyForTrigger(project *triggersv1alpha1.Project, trigger triggersv1alpha1.ProjectTrigger) string {
	return string(trigger.Type) + "/" + projectGeneratedChildName(project.Name, trigger.Name)
}

func projectGeneratedChildKey(child client.Object) string {
	switch child.(type) {
	case *triggersv1alpha1.GitHubRepository:
		return string(triggersv1alpha1.ProjectTriggerTypeGitHub) + "/" + child.GetName()
	case *triggersv1alpha1.SlackAgent:
		return string(triggersv1alpha1.ProjectTriggerTypeSlack) + "/" + child.GetName()
	case *triggersv1alpha1.Cron:
		return string(triggersv1alpha1.ProjectTriggerTypeCron) + "/" + child.GetName()
	case *triggersv1alpha1.LinearProject:
		return string(triggersv1alpha1.ProjectTriggerTypeLinear) + "/" + child.GetName()
	default:
		panic(fmt.Sprintf("unsupported generated child type %T", child))
	}
}

func (r *ProjectReconciler) upsertGeneratedChild(ctx context.Context, desired client.Object) error {
	current := desired.DeepCopyObject().(client.Object)
	current.SetResourceVersion("")
	if err := r.Get(ctx, client.ObjectKeyFromObject(desired), current); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		if err := r.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating generated %T %s: %w", desired, desired.GetName(), err)
		}
		return nil
	}
	if current.GetLabels()[projectGeneratedRuntimeLabel] != "true" || current.GetLabels()[projectUIDLabel] != desired.GetLabels()[projectUIDLabel] {
		return fmt.Errorf("generated %T %s conflicts with an existing resource", desired, desired.GetName())
	}

	if generatedChildEqual(current, desired) {
		return nil
	}
	current.SetLabels(desired.GetLabels())
	current.SetAnnotations(mergeGeneratedAnnotations(current.GetAnnotations(), desired.GetAnnotations()))
	current.SetOwnerReferences(desired.GetOwnerReferences())
	switch current := current.(type) {
	case *triggersv1alpha1.GitHubRepository:
		current.Spec = desired.(*triggersv1alpha1.GitHubRepository).Spec
	case *triggersv1alpha1.SlackAgent:
		current.Spec = desired.(*triggersv1alpha1.SlackAgent).Spec
	case *triggersv1alpha1.Cron:
		current.Spec = desired.(*triggersv1alpha1.Cron).Spec
	case *triggersv1alpha1.LinearProject:
		current.Spec = desired.(*triggersv1alpha1.LinearProject).Spec
	default:
		panic(fmt.Sprintf("unsupported generated child type %T", current))
	}
	if err := r.Update(ctx, current); err != nil {
		return fmt.Errorf("updating generated %T %s: %w", desired, desired.GetName(), err)
	}
	return nil
}

func generatedChildEqual(current, desired client.Object) bool {
	if !reflect.DeepEqual(current.GetLabels(), desired.GetLabels()) || !generatedAnnotationsCurrent(current.GetAnnotations(), desired.GetAnnotations()) || !reflect.DeepEqual(current.GetOwnerReferences(), desired.GetOwnerReferences()) {
		return false
	}
	switch current := current.(type) {
	case *triggersv1alpha1.GitHubRepository:
		return reflect.DeepEqual(current.Spec, desired.(*triggersv1alpha1.GitHubRepository).Spec)
	case *triggersv1alpha1.SlackAgent:
		return reflect.DeepEqual(current.Spec, desired.(*triggersv1alpha1.SlackAgent).Spec)
	case *triggersv1alpha1.Cron:
		return reflect.DeepEqual(current.Spec, desired.(*triggersv1alpha1.Cron).Spec)
	case *triggersv1alpha1.LinearProject:
		return reflect.DeepEqual(current.Spec, desired.(*triggersv1alpha1.LinearProject).Spec)
	default:
		panic(fmt.Sprintf("unsupported generated child type %T", current))
	}
}

// generatedAnnotationsCurrent reports whether every project-managed annotation
// on the desired child is already present with the same value. Annotations the
// project controller does not manage (for example the maintainer dispatch
// reservation ledger written at runtime) are ignored so drift detection does
// not fight runtime state.
func generatedAnnotationsCurrent(current, desired map[string]string) bool {
	for key, value := range desired {
		if current[key] != value {
			return false
		}
	}
	return true
}

// mergeGeneratedAnnotations overlays the project-managed annotations onto the
// child's existing annotations. Runtime-owned annotations (such as the
// maintainer dispatch reservation ledger) must survive regeneration: wiping
// them cancels in-flight dispatch reservations and hard-blocks the maintainer.
func mergeGeneratedAnnotations(current, desired map[string]string) map[string]string {
	merged := make(map[string]string, len(current)+len(desired))
	for key, value := range current {
		merged[key] = value
	}
	for key, value := range desired {
		merged[key] = value
	}
	return merged
}

func (r *ProjectReconciler) cleanupGeneratedChildren(ctx context.Context, project *triggersv1alpha1.Project, desired map[string]client.Object) error {
	github := &triggersv1alpha1.GitHubRepositoryList{}
	if err := r.List(ctx, github, client.InNamespace(project.Namespace)); err != nil {
		return err
	}
	children := make([]client.Object, 0, len(github.Items))
	for i := range github.Items {
		children = append(children, &github.Items[i])
	}
	if err := r.deleteStaleGeneratedChildren(ctx, project, desired, children); err != nil {
		return err
	}

	slack := &triggersv1alpha1.SlackAgentList{}
	if err := r.List(ctx, slack, client.InNamespace(project.Namespace)); err != nil {
		return err
	}
	children = children[:0]
	for i := range slack.Items {
		children = append(children, &slack.Items[i])
	}
	if err := r.deleteStaleGeneratedChildren(ctx, project, desired, children); err != nil {
		return err
	}

	cron := &triggersv1alpha1.CronList{}
	if err := r.List(ctx, cron, client.InNamespace(project.Namespace)); err != nil {
		return err
	}
	children = children[:0]
	for i := range cron.Items {
		children = append(children, &cron.Items[i])
	}
	if err := r.deleteStaleGeneratedChildren(ctx, project, desired, children); err != nil {
		return err
	}

	linear := &triggersv1alpha1.LinearProjectList{}
	if err := r.List(ctx, linear, client.InNamespace(project.Namespace)); err != nil {
		return err
	}
	children = children[:0]
	for i := range linear.Items {
		children = append(children, &linear.Items[i])
	}
	return r.deleteStaleGeneratedChildren(ctx, project, desired, children)
}

func (r *ProjectReconciler) deleteStaleGeneratedChildren(ctx context.Context, project *triggersv1alpha1.Project, desired map[string]client.Object, children []client.Object) error {
	for _, child := range children {
		if !projectOwnsGeneratedChild(project, child) {
			continue
		}
		if _, ok := desired[projectGeneratedChildKey(child)]; ok {
			continue
		}
		if err := r.Delete(ctx, child); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting stale generated %T %s: %w", child, child.GetName(), err)
		}
	}
	return nil
}

func projectOwnsGeneratedChild(project *triggersv1alpha1.Project, child client.Object) bool {
	labels := child.GetLabels()
	if labels[projectGeneratedRuntimeLabel] != "true" || labels[projectUIDLabel] != projectLabelValue(string(project.UID)) {
		return false
	}
	owner := metav1.GetControllerOf(child)
	return owner != nil && owner.APIVersion == triggersv1alpha1.GroupVersion.String() && owner.Kind == "Project" && owner.Name == project.Name && owner.UID == project.UID
}

func projectDisabledTriggerStatus(project *triggersv1alpha1.Project, trigger triggersv1alpha1.ProjectTrigger) triggersv1alpha1.ProjectTriggerStatus {
	return projectTriggerStatus(project, trigger, nil, "Disabled", "trigger is disabled")
}

func projectCompileErrorStatus(project *triggersv1alpha1.Project, trigger triggersv1alpha1.ProjectTrigger, err error) triggersv1alpha1.ProjectTriggerStatus {
	return projectTriggerStatus(project, trigger, err, "CompilationFailed", err.Error())
}

func projectTriggerStatus(project *triggersv1alpha1.Project, trigger triggersv1alpha1.ProjectTrigger, err error, reason, message string) triggersv1alpha1.ProjectTriggerStatus {
	status := triggersv1alpha1.ProjectTriggerStatus{
		Name:               trigger.Name,
		Type:               trigger.Type,
		ObservedGeneration: project.Generation,
	}
	if err != nil {
		status.LastError = err.Error()
	}
	status.Conditions = []metav1.Condition{{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		ObservedGeneration: project.Generation,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}}
	return status
}

func (r *ProjectReconciler) normalizedChildStatus(ctx context.Context, project *triggersv1alpha1.Project, trigger triggersv1alpha1.ProjectTrigger, desired client.Object) (triggersv1alpha1.ProjectTriggerStatus, bool, error) {
	child := desired.DeepCopyObject().(client.Object)
	if err := r.Get(ctx, client.ObjectKeyFromObject(desired), child); err != nil {
		if apierrors.IsNotFound(err) {
			return projectTriggerStatus(project, trigger, nil, "RuntimeMissing", "generated runtime is not present"), false, nil
		}
		return triggersv1alpha1.ProjectTriggerStatus{}, false, err
	}

	status := triggersv1alpha1.ProjectTriggerStatus{Name: trigger.Name, Type: trigger.Type, ObservedGeneration: child.GetGeneration()}
	switch child := child.(type) {
	case *triggersv1alpha1.GitHubRepository:
		status.Conditions = append([]metav1.Condition(nil), child.Status.Conditions...)
		status.LastActivityTime = child.Status.LastPollTime
		status.LastError = child.Status.LastError
		status.Maintainer = child.Status.Maintainer.DeepCopy()
	case *triggersv1alpha1.SlackAgent:
		status.Conditions = append([]metav1.Condition(nil), child.Status.Conditions...)
		status.LastActivityTime = child.Status.LastEventTime
		status.LastError = child.Status.LastError
	case *triggersv1alpha1.Cron:
		status.Conditions = append([]metav1.Condition(nil), child.Status.Conditions...)
		status.LastActivityTime = child.Status.LastScheduleTime
		status.NextActivityTime = child.Status.NextScheduleTime
		status.LastError = child.Status.LastError
	case *triggersv1alpha1.LinearProject:
		status.Conditions = append([]metav1.Condition(nil), child.Status.Conditions...)
		status.LastActivityTime = child.Status.LastPollTime
		status.LastError = child.Status.LastError
	default:
		panic(fmt.Sprintf("unsupported generated child type %T", child))
	}

	if status.LastError != "" {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			ObservedGeneration: child.GetGeneration(),
			Reason:             "RuntimeError",
			Message:            status.LastError,
		})
		return status, false, nil
	}
	if condition := meta.FindStatusCondition(status.Conditions, "Ready"); condition != nil {
		return status, condition.Status == metav1.ConditionTrue, nil
	}
	if status.LastActivityTime != nil || status.NextActivityTime != nil {
		meta.SetStatusCondition(&status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			ObservedGeneration: child.GetGeneration(),
			Reason:             "RuntimeActive",
			Message:            "generated runtime is active",
		})
		return status, true, nil
	}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionUnknown,
		ObservedGeneration: child.GetGeneration(),
		Reason:             "RuntimePending",
		Message:            "generated runtime has not reported activity",
	})
	return status, false, nil
}

func (r *ProjectReconciler) updateProjectStatus(ctx context.Context, key client.ObjectKey, statuses []triggersv1alpha1.ProjectTriggerStatus, ready bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		project := &triggersv1alpha1.Project{}
		if err := r.Get(ctx, key, project); err != nil {
			return err
		}
		before := project.DeepCopy().Status
		preserveProjectTriggerTransitionTimes(before.Triggers, statuses)
		project.Status.ObservedGeneration = project.Generation
		project.Status.Triggers = statuses
		setProjectReadyCondition(project, ready)
		if reflect.DeepEqual(before, project.Status) {
			return nil
		}
		base := project.DeepCopy()
		base.Status = before
		return r.Status().Patch(ctx, project, client.MergeFrom(base))
	})
}

func preserveProjectTriggerTransitionTimes(current, desired []triggersv1alpha1.ProjectTriggerStatus) {
	for i := range desired {
		for _, oldStatus := range current {
			if oldStatus.Name != desired[i].Name || oldStatus.Type != desired[i].Type {
				continue
			}
			for j := range desired[i].Conditions {
				for _, oldCondition := range oldStatus.Conditions {
					if desired[i].Conditions[j].Type == oldCondition.Type && desired[i].Conditions[j].Status == oldCondition.Status && desired[i].Conditions[j].Reason == oldCondition.Reason && desired[i].Conditions[j].Message == oldCondition.Message {
						desired[i].Conditions[j].LastTransitionTime = oldCondition.LastTransitionTime
						break
					}
				}
			}
			break
		}
	}
}

func setProjectReadyCondition(project *triggersv1alpha1.Project, ready bool) {
	status := metav1.ConditionFalse
	reason := "TriggersNotReady"
	message := "one or more enabled triggers are not ready"
	if ready {
		status = metav1.ConditionTrue
		reason = "TriggersReady"
		message = "all enabled triggers are ready"
	}
	meta.SetStatusCondition(&project.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		ObservedGeneration: project.Generation,
		Reason:             reason,
		Message:            message,
	})
}

func (r *ProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&triggersv1alpha1.Project{}).
		Owns(&triggersv1alpha1.GitHubRepository{}).
		Owns(&triggersv1alpha1.SlackAgent{}).
		Owns(&triggersv1alpha1.Cron{}).
		Owns(&triggersv1alpha1.LinearProject{}).
		Watches(&triggersv1alpha1.Connection{}, handler.EnqueueRequestsFromMapFunc(r.mapConnectionToProjects)).
		Named("project").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}

func (r *ProjectReconciler) mapConnectionToProjects(ctx context.Context, obj client.Object) []reconcile.Request {
	connection, ok := obj.(*triggersv1alpha1.Connection)
	if !ok {
		return nil
	}
	projects := &triggersv1alpha1.ProjectList{}
	if err := r.List(ctx, projects, client.InNamespace(connection.Namespace)); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for _, project := range projects.Items {
		if projectReferencesConnection(&project, connection.Name) {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&project)})
		}
	}
	return requests
}

func projectReferencesConnection(project *triggersv1alpha1.Project, connectionName string) bool {
	for _, trigger := range project.Spec.Triggers {
		switch trigger.Type {
		case triggersv1alpha1.ProjectTriggerTypeGitHub:
			if trigger.GitHub != nil && trigger.GitHub.ConnectionRef.Name == connectionName {
				return true
			}
		case triggersv1alpha1.ProjectTriggerTypeSlack:
			if trigger.Slack != nil && trigger.Slack.ConnectionRef.Name == connectionName {
				return true
			}
		case triggersv1alpha1.ProjectTriggerTypeLinear:
			if trigger.Linear != nil && trigger.Linear.ConnectionRef.Name == connectionName {
				return true
			}
		}
	}
	return false
}
