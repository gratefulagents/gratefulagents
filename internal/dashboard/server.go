package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/agentrun"
	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/internal/githubapp"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// Server implements the PlatformService business logic.
// It reads from the controller-runtime cache (zero extra K8s API load).
type Server struct {
	k8sClient         client.Client
	scheme            *runtime.Scheme
	s3Reader          *s3ActivityReader
	s3Diff            *s3DiffReader
	clientset         *kubernetes.Clientset // for pod exec (activity log streaming)
	restConfig        *rest.Config          // for pod exec SPDY transport
	teamService       agentrun.TeamService
	teamModeEnabled   bool
	stateStore        store.StateStore // optional Postgres state backend
	jaeger            *jaegerClient    // optional Jaeger query client
	presence          *PresenceTracker // in-memory presence tracker
	authStore         auth.Store       // integrated auth store (user search)
	githubApp         GitHubAppConfig  // platform GitHub App onboarding config
	githubAppMinter   *githubapp.KeyedMinter
	githubHTTP        *http.Client
	githubAPIBase     string
	skillsHTTP        *http.Client
	skillsCatalogURL  string
	providerOAuthHTTP *http.Client
	// providerOAuthKube stores browser OAuth sessions in per-user Kubernetes
	// Secrets so Start and Complete/Poll can land on different manager replicas.
	providerOAuthKube kubernetes.Interface

	// providerOAuthSessions is the in-memory fallback used by unit tests that do
	// not configure a Kubernetes clientset.
	providerOAuthMu       sync.Mutex
	providerOAuthSessions map[string]providerOAuthSession

	// metricsCache caches aggregated resource metrics per namespace for a
	// short TTL: every project/cron/repo get/list/watch tick otherwise lists
	// all AgentRuns and scans all Postgres session metadata.
	metricsCacheMu sync.Mutex
	metricsCache   map[string]*resourceMetricsCacheEntry

	// activityMemo caches the last built activity-log response per run so the
	// 500ms watch tick can skip rebuilding when no new events arrived.
	activityMemoMu sync.Mutex
	activityMemo   map[string]*activityMemoEntry

	// execFailAt records when the pod-exec activity-log fallback last failed
	// (or returned nothing) per sandbox pod, so 500ms watch ticks don't
	// re-exec into a pod that has no data yet.
	execFailMu sync.Mutex
	execFailAt map[string]time.Time

	// probes coalesces the cheap Postgres probes issued by concurrent watch
	// streams (summary versions, session fingerprints, latest event IDs, ACL
	// bulk loads) so query load scales with distinct resources, not with the
	// number of open dashboard pages.
	probes probeCache
}

type resourceMetricsCacheEntry struct {
	at   time.Time
	data map[resourceMetricsKey]*platform.ProjectMetrics
}

type activityMemoEntry struct {
	lastEventID int64
	isTerminal  bool
	entries     []*platform.ActivityEntry
	resp        *platform.GetActivityLogResponse
	lastAccess  time.Time
}

const openAIApiModeAnnotation = "platform.gratefulagents.dev/openai-api-mode"

const cancelRequestedAnnotation = "platform.gratefulagents.dev/cancel-requested"

// promoteSucceededAnnotation asks the AgentRun controller to tear the run
// down like a cancellation but record the terminal phase as Succeeded — the
// user explicitly promoted the run to success.
const promoteSucceededAnnotation = "platform.gratefulagents.dev/promote-succeeded-requested"

const awaitingUserStep = "awaiting-user"

func boolPtr(v bool) *bool { return &v }

// NewServer creates a new dashboard server backed by the given K8s client.
func NewServer(c client.Client, scheme *runtime.Scheme, clientset *kubernetes.Clientset, restConfig *rest.Config, teamModeEnabled bool, opts ...ServerOption) *Server {
	ar := newS3ActivityReader()
	s := &Server{
		k8sClient:             c,
		scheme:                scheme,
		s3Reader:              ar,
		s3Diff:                newS3DiffReader(ar),
		clientset:             clientset,
		restConfig:            restConfig,
		teamModeEnabled:       teamModeEnabled,
		jaeger:                newJaegerClient(),
		presence:              NewPresenceTracker(),
		githubAppMinter:       githubapp.NewKeyedMinter(),
		providerOAuthHTTP:     &http.Client{Timeout: 20 * time.Second},
		providerOAuthKube:     clientset,
		providerOAuthSessions: make(map[string]providerOAuthSession),
	}
	for _, opt := range opts {
		opt(s)
	}
	var githubMinterOpts []githubapp.Option
	if s.githubHTTP != nil {
		githubMinterOpts = append(githubMinterOpts, githubapp.WithHTTPClient(s.githubHTTP))
	}
	if s.githubAPIBase != "" {
		githubMinterOpts = append(githubMinterOpts, githubapp.WithBaseURL(s.githubAPIBase))
	}
	if len(githubMinterOpts) > 0 {
		s.githubAppMinter = githubapp.NewKeyedMinter(githubMinterOpts...)
	}
	// Build team service after opts so stateStore is wired.
	ts := agentrun.NewKubeTeamService(c, scheme)
	if s.stateStore != nil {
		ts.WithStateStore(s.stateStore)
	}
	s.teamService = ts
	return s
}

// ServerOption configures the dashboard server.
type ServerOption func(*Server)

// WithStateStore sets the optional Postgres state backend.
func WithStateStore(ss store.StateStore) ServerOption {
	return func(s *Server) {
		s.stateStore = ss
		// Also wire the store into the team service for child message routing.
		if ts, ok := s.teamService.(*agentrun.KubeTeamService); ok {
			ts.WithStateStore(ss)
		}
	}
}

// WithAuthStore sets the integrated auth store for user search.
func WithAuthStore(store auth.Store) ServerOption {
	return func(s *Server) {
		s.authStore = store
	}
}

// WithGitHubAppConfig sets the platform GitHub App used for repository onboarding.
func WithGitHubAppConfig(appID int64, appSlug, privateKeySecret, namespace string) ServerOption {
	return func(s *Server) {
		s.githubApp = GitHubAppConfig{
			AppID:            appID,
			AppSlug:          strings.TrimSpace(appSlug),
			PrivateKeySecret: strings.TrimSpace(privateKeySecret),
			Namespace:        strings.TrimSpace(namespace),
		}
	}
}

// WithGitHubAppHTTPClient sets the GitHub HTTP client used by onboarding.
func WithGitHubAppHTTPClient(client *http.Client) ServerOption {
	return func(s *Server) {
		s.githubHTTP = client
	}
}

// WithGitHubAppAPIBaseURL sets an alternate GitHub API base URL, primarily for tests.
func WithGitHubAppAPIBaseURL(baseURL string) ServerOption {
	return func(s *Server) {
		s.githubAPIBase = strings.TrimSpace(baseURL)
		s.githubAppMinter = githubapp.NewKeyedMinter(githubapp.WithHTTPClient(s.githubHTTP), githubapp.WithBaseURL(s.githubAPIBase))
	}
}

// WithSkillsCatalogHTTPClient sets the HTTP client used for skills.sh catalog
// requests and catalog skill resolution.
func WithSkillsCatalogHTTPClient(httpClient *http.Client) ServerOption {
	return func(s *Server) { s.skillsHTTP = httpClient }
}

// WithSkillsCatalogURL overrides the skills.sh base URL, primarily for tests.
func WithSkillsCatalogURL(baseURL string) ServerOption {
	return func(s *Server) { s.skillsCatalogURL = strings.TrimRight(strings.TrimSpace(baseURL), "/") }
}

// filterListByAccess narrows list results to resources owned by or shared with
// the calling actor when the request asks for those views. Without a state
// store or an authenticated actor the list is returned unchanged, matching the
// visibility-filter posture.
func filterListByAccess[T any](ctx context.Context, s *Server, resourceType string, ownedByMe, sharedWithMe bool, items []T, key func(T) string) []T {
	if s.stateStore == nil {
		return items
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return items
	}
	keep := func(allowed map[string]bool) []T {
		var filtered []T
		for _, item := range items {
			if allowed[key(item)] {
				filtered = append(filtered, item)
			}
		}
		return filtered
	}
	if ownedByMe {
		ownedSet := make(map[string]bool)
		owned, _ := s.stateStore.ListOwnedResources(ctx, actor.Subject, resourceType)
		for _, o := range owned {
			ownedSet[o.ResourceNamespace+"/"+o.ResourceID] = true
		}
		items = keep(ownedSet)
	}
	if sharedWithMe {
		sharedSet := make(map[string]bool)
		shares, _ := s.stateStore.ListSharedWithMe(ctx, actor.Subject, resourceType)
		for _, sh := range shares {
			sharedSet[sh.ResourceNamespace+"/"+sh.ResourceID] = true
		}
		items = keep(sharedSet)
	}
	return items
}

func isTerminalAgentRunPhase(phase platformv1alpha1.AgentRunPhase) bool {
	return phase == platformv1alpha1.AgentRunPhaseSucceeded ||
		phase == platformv1alpha1.AgentRunPhaseFailed ||
		phase == platformv1alpha1.AgentRunPhaseCancelled
}

func parsePositiveDashboardDuration(field, value string) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is required", field))
	}
	var duration metav1.Duration
	if err := duration.UnmarshalJSON([]byte(strconv.Quote(trimmed))); err != nil {
		return 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid %s %q: %w", field, trimmed, err))
	}
	if duration.Duration <= 0 {
		return 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s must be greater than zero", field))
	}
	return duration.Duration, nil
}

func (s *Server) requireAgentRunCollaborator(ctx context.Context, namespace, name, action string) error {
	return s.requireAgentRunAccess(ctx, namespace, name, AccessCollaborator, action+" this run")
}

func (s *Server) requireAgentRunOwner(ctx context.Context, namespace, name, action string) error {
	actor, recorded := requestActorFromContextOK(ctx)
	if !recorded {
		return nil // trusted internal invocation; external RPCs always carry an actor
	}
	if actor.Role == "admin" || actor.Role == "owner" {
		return nil
	}
	if actor.Subject == "" {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required to %s this run", action))
	}
	if s.stateStore == nil {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only the run owner or an admin may %s this run", action))
	}
	ownership, err := s.stateStore.GetResourceOwner(ctx, "agent_run", name, namespace)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("checking run ownership: %w", err))
	}
	if ownership == nil || ownership.OwnerID == "" {
		run := &platformv1alpha1.AgentRun{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, run); err != nil {
			return mapK8sError(fmt.Sprintf("get AgentRun %s/%s", namespace, name), err)
		}
		if projectName := agentRunProjectName(run); projectName != "" {
			ownership, err = s.stateStore.GetResourceOwner(ctx, projectResourceType, projectName, namespace)
			if err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("checking Project ownership: %w", err))
			}
		}
		resourceType := agentRunTriggerResourceTypes[run.Spec.Trigger.Kind]
		triggerName := strings.TrimSpace(run.Spec.Trigger.Name)
		if (ownership == nil || ownership.OwnerID == "") && resourceType != "" && triggerName != "" {
			ownership, err = s.stateStore.GetResourceOwner(ctx, resourceType, triggerName, namespace)
			if err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("checking trigger ownership: %w", err))
			}
		}
	}
	if ownership == nil || ownership.OwnerID != actor.Subject {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("only the run owner or an admin may %s this run", action))
	}
	return nil
}

func agentRunSendReadiness(runPhase string, sess *store.Session) (bool, string) {
	const notReadyReason = "Session is still starting up. Try again in a moment."

	phase := platformv1alpha1.AgentRunPhase(runPhase)
	if isTerminalAgentRunPhase(phase) {
		return false, "Run has ended. Retry the run to send another message."
	}
	if phase == platformv1alpha1.AgentRunPhasePaused {
		return false, "Run is paused. Resume the run to send another message."
	}
	// For active runs, the Postgres session row is the only hard prerequisite.
	// Session phase/current_step are bootstrap metadata and are not updated
	// consistently during the live run.
	if sess == nil {
		return false, notReadyReason
	}
	return true, ""
}

func agentRunMessageReadiness(run *platformv1alpha1.AgentRun, sess *store.Session) (bool, string) {
	if run == nil {
		return false, "Run is unavailable."
	}
	if ready, reason := agentRunSendReadiness(string(run.Status.Phase), sess); !ready {
		return false, reason
	}
	if run.Annotations[cancelRequestedAnnotation] != "" || run.Annotations[promoteSucceededAnnotation] != "" {
		return false, "Run is stopping and cannot accept new messages."
	}
	return true, ""
}

func resolveActorLabel(ctx context.Context) string {
	actor := requestActorFromContext(ctx)
	if actor.Role == "" && actor.Subject == "" {
		return "system"
	}
	if subject := strings.TrimSpace(actor.Subject); subject != "" {
		return subject
	}
	if actor.Role != "" {
		return actor.Role
	}
	return "anonymous"
}

func queueAdmittedAt(queue *platformv1alpha1.AgentRunQueueStatus) *metav1.Time {
	if queue == nil {
		return nil
	}
	return queue.AdmittedAt
}

func ensureRunMetrics(status *platformv1alpha1.AgentRunStatus) *platformv1alpha1.AgentRunMetrics {
	if status.Metrics == nil {
		status.Metrics = &platformv1alpha1.AgentRunMetrics{}
	}
	return status.Metrics
}

func effectiveAgentRunTimeout(run *platformv1alpha1.AgentRun, fallback metav1.Duration) metav1.Duration {
	if run != nil && run.Spec.Limits != nil && run.Spec.Limits.MaxRuntime.Duration > 0 {
		return run.Spec.Limits.MaxRuntime
	}
	return fallback
}

// mapK8sError maps Kubernetes API errors to Connect error codes.
func mapK8sError(op string, err error) error {
	switch {
	case k8serrors.IsNotFound(err):
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s: not found", op))
	case k8serrors.IsAlreadyExists(err):
		return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("%s: already exists", op))
	case k8serrors.IsInvalid(err), k8serrors.IsBadRequest(err):
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s: %w", op, err))
	case k8serrors.IsUnauthorized(err):
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("%s: %w", op, err))
	case k8serrors.IsForbidden(err):
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s: %w", op, err))
	case k8serrors.IsConflict(err):
		return connect.NewError(connect.CodeAborted, fmt.Errorf("%s: resource changed; reload and retry: %w", op, err))
	case k8serrors.IsTooManyRequests(err):
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("%s: %w", op, err))
	case k8serrors.IsServerTimeout(err), k8serrors.IsServiceUnavailable(err):
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("%s: %w", op, err))
	default:
		return connect.NewError(connect.CodeInternal, fmt.Errorf("%s: %w", op, err))
	}
}
