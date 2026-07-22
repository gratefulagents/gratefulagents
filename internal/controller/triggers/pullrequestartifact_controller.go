package triggers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

type PullRequestArtifactReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentruns,verbs=get;list;watch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=githubrepositories,verbs=get;list;watch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=pullrequestmonitors,verbs=get;list;watch;create

func (r *PullRequestArtifactReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	run := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, req.NamespacedName, run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if run.Labels[triggersv1alpha1.PRLoopRoleLabelKey] == triggersv1alpha1.PRLoopRoleReviewerValue {
		return ctrl.Result{}, nil
	}

	for _, pullRequest := range artifactPullRequests(run) {
		if !runDeclaresRepository(run, pullRequest.Repository) {
			continue
		}
		monitor := &triggersv1alpha1.PullRequestMonitor{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pullRequestMonitorName(run.UID, pullRequest.URL),
				Namespace: run.Namespace,
			},
			Spec: triggersv1alpha1.PullRequestMonitorSpec{
				URL:            pullRequest.URL,
				Repository:     pullRequest.Repository,
				Number:         pullRequest.Number,
				ImplementerRef: corev1.LocalObjectReference{Name: run.Name},
				DiscoveredAt:   metav1.Now(),
			},
		}
		repository, err := r.matchingGitHubRepository(ctx, run.Namespace, pullRequest.Repository)
		if err != nil {
			return ctrl.Result{}, err
		}
		if repository != nil {
			monitor.Spec.GitHubRepositoryRef = &corev1.LocalObjectReference{Name: repository.Name}
		}
		if err := ctrl.SetControllerReference(run, monitor, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting AgentRun owner on PullRequestMonitor: %w", err)
		}
		if err := r.Create(ctx, monitor); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return ctrl.Result{}, err
			}
			if err := r.validateExistingMonitor(ctx, monitor); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{}, nil
}

type artifactPullRequest struct {
	URL        string
	Repository string
	Number     int32
}

func artifactPullRequests(run *platformv1alpha1.AgentRun) []artifactPullRequest {
	if run == nil || run.Status.Artifacts == nil {
		return nil
	}
	raw := make([]string, 0, len(run.Status.Artifacts.PullRequestURLs)+1)
	raw = append(raw, run.Status.Artifacts.PullRequestURLs...)
	raw = append(raw, run.Status.Artifacts.PullRequestURL)

	byURL := make(map[string]artifactPullRequest, len(raw))
	for _, value := range raw {
		pullRequest, ok := parseArtifactPullRequestURL(value)
		if ok {
			byURL[pullRequest.URL] = pullRequest
		}
	}
	urls := make([]string, 0, len(byURL))
	for canonicalURL := range byURL {
		urls = append(urls, canonicalURL)
	}
	sort.Strings(urls)
	result := make([]artifactPullRequest, 0, len(urls))
	for _, canonicalURL := range urls {
		result = append(result, byURL[canonicalURL])
	}
	return result
}

func parseArtifactPullRequestURL(value string) (artifactPullRequest, bool) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed == nil {
		return artifactPullRequest{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if !strings.EqualFold(parsed.Scheme, "https") || (host != "github.com" && host != "www.github.com") || parsed.User != nil || parsed.Port() != "" {
		return artifactPullRequest{}, false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) < 4 || parts[0] == "" || parts[1] == "" || !strings.EqualFold(parts[2], "pull") {
		return artifactPullRequest{}, false
	}
	owner, err := url.PathUnescape(parts[0])
	if err != nil || owner == "" || strings.Contains(owner, "/") {
		return artifactPullRequest{}, false
	}
	repository, err := url.PathUnescape(parts[1])
	if err != nil || repository == "" || strings.Contains(repository, "/") {
		return artifactPullRequest{}, false
	}
	number, err := strconv.ParseInt(parts[3], 10, 32)
	if err != nil || number <= 0 {
		return artifactPullRequest{}, false
	}
	repositoryName := strings.ToLower(owner + "/" + repository)
	canonicalURL := fmt.Sprintf("https://github.com/%s/pull/%d", repositoryName, number)
	return artifactPullRequest{
		URL:        canonicalURL,
		Repository: repositoryName,
		Number:     int32(number),
	}, true
}

func runRecordsPullRequestURL(run *platformv1alpha1.AgentRun, rawURL string) bool {
	want, ok := parseArtifactPullRequestURL(rawURL)
	if !ok {
		return false
	}
	for _, recorded := range artifactPullRequests(run) {
		if strings.EqualFold(recorded.URL, want.URL) {
			return true
		}
	}
	return false
}

func pullRequestMonitorName(uid types.UID, canonicalURL string) string {
	digest := sha256.Sum256([]byte(string(uid) + "\x00" + canonicalURL))
	return "pr-monitor-" + hex.EncodeToString(digest[:12])
}

func (r *PullRequestArtifactReconciler) matchingGitHubRepository(ctx context.Context, namespace, repository string) (*triggersv1alpha1.GitHubRepository, error) {
	list := &triggersv1alpha1.GitHubRepositoryList{}
	if err := r.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	var match *triggersv1alpha1.GitHubRepository
	for i := range list.Items {
		candidate := &list.Items[i]
		if !strings.EqualFold(candidate.Spec.Owner+"/"+candidate.Spec.Repo, repository) {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("multiple GitHubRepositories in namespace %s match %s", namespace, repository)
		}
		match = candidate
	}
	return match, nil
}

func (r *PullRequestArtifactReconciler) validateExistingMonitor(ctx context.Context, desired *triggersv1alpha1.PullRequestMonitor) error {
	existing := &triggersv1alpha1.PullRequestMonitor{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing); err != nil {
		return err
	}
	owner := metav1.GetControllerOf(existing)
	desiredOwner := metav1.GetControllerOf(desired)
	if owner == nil || desiredOwner == nil || owner.APIVersion != desiredOwner.APIVersion || owner.Kind != desiredOwner.Kind || owner.Name != desiredOwner.Name || owner.UID != desiredOwner.UID {
		return fmt.Errorf("PullRequestMonitor %s/%s already exists with a different controller owner", existing.Namespace, existing.Name)
	}

	existingSpec := existing.Spec.DeepCopy()
	desiredSpec := desired.Spec.DeepCopy()
	existingSpec.DiscoveredAt = desiredSpec.DiscoveredAt
	if !reflect.DeepEqual(existingSpec, desiredSpec) {
		return fmt.Errorf("PullRequestMonitor %s/%s already exists with a different spec", existing.Namespace, existing.Name)
	}
	return nil
}

func pullRequestArtifactPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			run, ok := e.Object.(*platformv1alpha1.AgentRun)
			return ok && len(artifactPullRequests(run)) > 0
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldRun, oldOK := e.ObjectOld.(*platformv1alpha1.AgentRun)
			newRun, newOK := e.ObjectNew.(*platformv1alpha1.AgentRun)
			return oldOK && newOK && !reflect.DeepEqual(artifactPullRequests(oldRun), artifactPullRequests(newRun))
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

func (r *PullRequestArtifactReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.AgentRun{}).
		Named("pull-request-artifacts").
		WithEventFilter(pullRequestArtifactPredicate()).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}
