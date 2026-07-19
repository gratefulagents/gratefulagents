package triggers

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/linear"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	defaultPollInterval  = 30 * time.Second
	defaultApprovedLabel = "ai-approved"
	labelInProgress      = "ai-in-progress"
	runModeAnnotation    = "platform.gratefulagents.dev/run-mode"
)

var nonAlphaNum = regexp.MustCompile(`[^a-z0-9-]`)

type LinearProjectReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	StateStore          store.StateStore
	LinearClientFactory func(apiKey string) linear.LinearClient
}

// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=linearprojects,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=linearprojects/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentruns,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *LinearProjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	lp := &triggersv1alpha1.LinearProject{}
	if err := r.Get(ctx, req.NamespacedName, lp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	apiKey, err := ReadSecretValue(ctx, r.Client, lp.Namespace, lp.Spec.LinearAPIKeySecret, "api-key")
	if err != nil {
		_ = retryLinearProjectStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(lp), func(fresh *triggersv1alpha1.LinearProject) {
			fresh.Status.LastError = err.Error()
		})
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	approvedLabel := strings.TrimSpace(lp.Spec.ApprovedLabel)
	if approvedLabel == "" {
		approvedLabel = defaultApprovedLabel
	}
	newLinearClient := r.LinearClientFactory
	if newLinearClient == nil {
		newLinearClient = linear.NewClient
	}
	lc := newLinearClient(apiKey)
	issues, err := lc.FetchIssuesByLabel(ctx, lp.Spec.ProjectID, approvedLabel)
	if err != nil {
		log.Error(err, "failed to fetch issues from Linear")
		_ = retryLinearProjectStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(lp), func(fresh *triggersv1alpha1.LinearProject) {
			fresh.Status.LastError = err.Error()
		})
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	if !lp.Spec.AutoCreateTasks {
		return r.updateStatusAndRequeue(ctx, lp, 0)
	}

	existing, err := ExistingTriggerIssueIDs(ctx, r.Client, lp.Namespace, "LinearProject", lp.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	var inProgressLabelID, approvedLabelID string
	if len(issues) > 0 {
		inProgressLabelID, _ = lc.GetLabelID(ctx, lp.Spec.TeamID, labelInProgress)
		approvedLabelID, _ = lc.GetLabelID(ctx, lp.Spec.TeamID, approvedLabel)
	}

	created := 0
	modeExists := ModeExistsFromK8s(ctx, r.Client)
	for _, issue := range issues {
		if _, ok := existing[issue.ID]; ok {
			continue
		}
		if err := r.createAgentRunWithModeExists(ctx, lp, issue, modeExists); err != nil {
			log.Error(err, "failed to create AgentRun", "issueID", issue.ID)
			continue
		}
		if !hasLabel(issue, labelInProgress) {
			if approvedLabelID != "" {
				if err := lc.RemoveLabel(ctx, issue.ID, approvedLabelID); err != nil {
					log.Error(err, "failed to remove Linear approved label", "issueID", issue.ID, "labelID", approvedLabelID)
				}
			}
			if inProgressLabelID != "" {
				if err := lc.AddLabel(ctx, issue.ID, inProgressLabelID); err != nil {
					log.Error(err, "failed to add Linear in-progress label", "issueID", issue.ID, "labelID", inProgressLabelID)
				}
			}
		}
		created++
	}

	return r.updateStatusAndRequeue(ctx, lp, created)
}

func (r *LinearProjectReconciler) createAgentRun(ctx context.Context, lp *triggersv1alpha1.LinearProject, issue linear.Issue) error {
	return r.createAgentRunWithModeExists(ctx, lp, issue, ModeExistsFromK8s(ctx, r.Client))
}

func (r *LinearProjectReconciler) createAgentRunWithModeExists(ctx context.Context, lp *triggersv1alpha1.LinearProject, issue linear.Issue, modeExists ModeExistsFunc) error {
	runName := issueName(issue.ID)
	d := lp.Spec.Defaults
	provider := triggersv1alpha1.NormalizeProvider(d.Provider)
	if err := validateTriggerRunDefaults(TriggerRunSpec{
		Namespace:                      lp.Namespace,
		TriggerKind:                    "LinearProject",
		TriggerName:                    lp.Name,
		Defaults:                       d,
		DetailedCredentialErrorContext: true,
	}); err != nil {
		return err
	}
	annotations := map[string]string{
		runModeAnnotation: string(d.ResolveWorkflowMode()),
	}
	if strings.TrimSpace(d.CustomInstructions) != "" {
		instructionsName := runName + "-instructions"
		instructions := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: instructionsName, Namespace: lp.Namespace}, Data: map[string]string{"instructions.md": d.CustomInstructions}}
		if err := ctrl.SetControllerReference(lp, instructions, r.Scheme); err != nil {
			return fmt.Errorf("setting owner reference on instructions ConfigMap: %w", err)
		}
		if err := r.Create(ctx, instructions); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating instructions ConfigMap: %w", err)
		}
		annotations["platform.gratefulagents.dev/instructions-configmap-ref"] = instructionsName
	}
	if triggersv1alpha1.IsOpenAICompatibleProvider(provider) {
		annotations["platform.gratefulagents.dev/openai-api-mode"] = triggersv1alpha1.NormalizeOpenAIAPIForProvider(provider, d.OpenAIAPI)
	}
	runContext := &platformv1alpha1.AgentRunContext{
		ProjectRef: &platformv1alpha1.ProjectRef{Kind: "LinearProject", Name: lp.Name},
	}
	modeRef := d.ModeRef
	var issueLabels []string
	for _, l := range issue.Labels.Nodes {
		issueLabels = append(issueLabels, l.Name)
	}
	if labelMode := ResolveModeFromLabels(issueLabels, modeExists); labelMode != nil {
		modeRef = MergeModeRef(nil, labelMode, modeRef)
	}
	_, _, err := CreateTriggerRun(ctx, r.Client, r.StateStore, TriggerRunSpec{
		RunName:                        runName,
		Namespace:                      lp.Namespace,
		TriggerKind:                    "LinearProject",
		TriggerName:                    lp.Name,
		ExternalID:                     issue.ID,
		ExternalIdentifier:             issue.Identifier,
		SeedMessage:                    issue.Title,
		Defaults:                       d,
		OwnerRef:                       lp,
		Scheme:                         r.Scheme,
		Annotations:                    annotations,
		Context:                        runContext,
		ModeRef:                        modeRef,
		SeedLogPrefix:                  "linearproject",
		SeedOnAlreadyExists:            true,
		DetailedCredentialErrorContext: true,
	})
	return err
}

func (r *LinearProjectReconciler) updateStatusAndRequeue(ctx context.Context, lp *triggersv1alpha1.LinearProject, created int) (ctrl.Result, error) {
	if err := retryLinearProjectStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(lp), func(fresh *triggersv1alpha1.LinearProject) {
		now := metav1.Now()
		fresh.Status.LastPollTime = &now
		fresh.Status.LastError = ""
		fresh.Status.IssuesProcessed += int32(created)
	}); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("updating LinearProject status: %w", err)
	}
	pollInterval := lp.Spec.PollInterval.Duration
	if pollInterval == 0 {
		pollInterval = defaultPollInterval
	}
	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

func hasLabel(issue linear.Issue, name string) bool {
	for _, l := range issue.Labels.Nodes {
		if strings.EqualFold(l.Name, name) {
			return true
		}
	}
	return false
}

func issueName(issueID string) string {
	sanitized := nonAlphaNum.ReplaceAllString(strings.ToLower(issueID), "-")
	name := "linear-" + sanitized
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimRight(name, "-")
}

func (r *LinearProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&triggersv1alpha1.LinearProject{}).
		Owns(&platformv1alpha1.AgentRun{}).
		Named("linearproject").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}
