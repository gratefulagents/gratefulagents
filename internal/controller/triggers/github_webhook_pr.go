package triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// PullRequestEventType classifies normalized pull-request webhook events.
type PullRequestEventType string

// githubActionCreated is the webhook action value for newly created comments.
const githubActionCreated = "created"

const (
	// PREventOpened fires when a pull request is opened or reopened.
	PREventOpened PullRequestEventType = "opened"
	// PREventSynchronize fires when new commits are pushed to a PR head.
	PREventSynchronize PullRequestEventType = "synchronize"
	// PREventReviewSubmitted fires when a PR review is submitted.
	PREventReviewSubmitted PullRequestEventType = "review_submitted"
	// PREventReviewComment fires when an inline review comment is created.
	PREventReviewComment PullRequestEventType = "review_comment"
	// PREventComment fires when a plain conversation comment is created on a PR.
	PREventComment PullRequestEventType = "pr_comment"
)

// PullRequestEvent is the normalized payload handed to the PR loop engine.
type PullRequestEvent struct {
	Type PullRequestEventType
	// Repository is the GitHub owner/name from repository.full_name. It lets
	// app-level ingress route loop events even when no GitHubRepository exists.
	Repository  string
	Number      int
	Title       string
	URL         string
	HeadRef     string
	BaseRef     string
	AuthorLogin string
	// SenderLogin is the user whose action produced this delivery.
	SenderLogin string
	// SenderAuthorAssociation is the sender's relationship to the repository
	// from GitHub author_association.
	SenderAuthorAssociation string
	// ReviewState is approved | changes_requested | commented for
	// review_submitted events.
	ReviewState string
	// Body carries the review body, review-comment body, or PR-comment body
	// depending on Type.
	Body string
	// CommentPath/CommentLine locate inline review comments.
	CommentPath string
	CommentLine int
	// SourceID and SourceCreatedAt identify the durable GitHub object. Polling
	// always sets them; webhooks set them when present so webhook/poll races
	// share the same idempotency key.
	SourceID        int64
	SourceCreatedAt time.Time
	// TargetImplementer pins artifact-derived polling events to the validated
	// owner run. Webhook-normalized events leave these empty and use matching.
	TargetImplementerNamespace string
	TargetImplementerName      string
}

// PullRequestEventSink receives normalized pull-request lifecycle events from
// the GitHub webhook ingress. The autonomous PR loop engine implements this to
// start reviewer runs and wake implementer runs. The handled result reports
// whether the event was consumed by the loop; callers may fall back to legacy
// behavior when it is false. A nil sink drops the events.
type PullRequestEventSink interface {
	// repo is optional for app-level deliveries to repositories that have no
	// GitHubRepository CR. In that case event.Repository carries owner/name.
	HandlePullRequestEvent(ctx context.Context, repo *triggersv1alpha1.GitHubRepository, event PullRequestEvent) (handled bool, err error)
}

// Minimal payload structs for PR-related webhook deliveries.

type githubPullRequest struct {
	Number  int        `json:"number"`
	Title   string     `json:"title"`
	HTMLURL string     `json:"html_url"`
	User    githubUser `json:"user"`
	Head    githubRef  `json:"head"`
	Base    githubRef  `json:"base"`
}

type githubRef struct {
	Ref string `json:"ref"`
}

type githubRepositoryRef struct {
	FullName string `json:"full_name"`
}

type githubPullRequestEvent struct {
	Action      string              `json:"action"`
	Repository  githubRepositoryRef `json:"repository"`
	PullRequest githubPullRequest   `json:"pull_request"`
	Sender      githubUser          `json:"sender"`
}

type githubPullRequestReviewEvent struct {
	Action      string              `json:"action"`
	Repository  githubRepositoryRef `json:"repository"`
	PullRequest githubPullRequest   `json:"pull_request"`
	Review      githubReview        `json:"review"`
	Sender      githubUser          `json:"sender"`
}

type githubReview struct {
	ID                int64      `json:"id"`
	State             string     `json:"state"`
	Body              string     `json:"body"`
	User              githubUser `json:"user"`
	AuthorAssociation string     `json:"author_association"`
	SubmittedAt       time.Time  `json:"submitted_at"`
}

type githubPullRequestReviewCommentEvent struct {
	Action      string                `json:"action"`
	Repository  githubRepositoryRef   `json:"repository"`
	PullRequest githubPullRequest     `json:"pull_request"`
	Comment     githubPRReviewComment `json:"comment"`
	Sender      githubUser            `json:"sender"`
}

type githubPRReviewComment struct {
	Body string     `json:"body"`
	Path string     `json:"path"`
	Line int        `json:"line"`
	User githubUser `json:"user"`
}

func (h *GitHubWebhookHandler) dispatchPREvent(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, event PullRequestEvent) error {
	if h.PRSink == nil {
		return nil
	}
	if _, err := h.PRSink.HandlePullRequestEvent(ctx, gh, event); err != nil {
		logf.FromContext(ctx).WithName("github-webhook").Error(err, "pull request event sink failed",
			"type", event.Type, "pr", event.Number)
		return err
	}
	return nil
}

func (h *GitHubWebhookHandler) handlePullRequestEvent(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, payload []byte) error {
	var event githubPullRequestEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return errBadWebhookPayload
	}

	var eventType PullRequestEventType
	switch event.Action {
	case "opened", "reopened", "ready_for_review":
		eventType = PREventOpened
	case "synchronize":
		eventType = PREventSynchronize
	default:
		return nil
	}

	return h.dispatchPREvent(ctx, gh, PullRequestEvent{
		Type:        eventType,
		Repository:  event.Repository.FullName,
		Number:      event.PullRequest.Number,
		Title:       event.PullRequest.Title,
		URL:         event.PullRequest.HTMLURL,
		HeadRef:     event.PullRequest.Head.Ref,
		BaseRef:     event.PullRequest.Base.Ref,
		AuthorLogin: event.PullRequest.User.Login,
		SenderLogin: event.Sender.Login,
	})
}

func (h *GitHubWebhookHandler) handlePullRequestReviewEvent(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, payload []byte) error {
	var event githubPullRequestReviewEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return errBadWebhookPayload
	}
	if event.Action != "submitted" {
		return nil
	}

	senderLogin := event.Review.User.Login
	if senderLogin == "" {
		senderLogin = event.Sender.Login
	}
	return h.dispatchPREvent(ctx, gh, PullRequestEvent{
		Type:                    PREventReviewSubmitted,
		Repository:              event.Repository.FullName,
		Number:                  event.PullRequest.Number,
		Title:                   event.PullRequest.Title,
		URL:                     event.PullRequest.HTMLURL,
		HeadRef:                 event.PullRequest.Head.Ref,
		BaseRef:                 event.PullRequest.Base.Ref,
		AuthorLogin:             event.PullRequest.User.Login,
		SenderLogin:             senderLogin,
		SenderAuthorAssociation: event.Review.AuthorAssociation,
		ReviewState:             event.Review.State,
		Body:                    event.Review.Body,
		SourceID:                event.Review.ID,
		SourceCreatedAt:         event.Review.SubmittedAt,
	})
}

func (h *GitHubWebhookHandler) handlePullRequestReviewCommentEvent(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, payload []byte) error {
	var event githubPullRequestReviewCommentEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return errBadWebhookPayload
	}
	if event.Action != githubActionCreated {
		return nil
	}

	return h.dispatchPREvent(ctx, gh, PullRequestEvent{
		Type:        PREventReviewComment,
		Repository:  event.Repository.FullName,
		Number:      event.PullRequest.Number,
		Title:       event.PullRequest.Title,
		URL:         event.PullRequest.HTMLURL,
		HeadRef:     event.PullRequest.Head.Ref,
		BaseRef:     event.PullRequest.Base.Ref,
		AuthorLogin: event.PullRequest.User.Login,
		SenderLogin: event.Sender.Login,
		Body:        event.Comment.Body,
		CommentPath: event.Comment.Path,
		CommentLine: event.Comment.Line,
	})
}

// deliveryDeduper drops webhook redeliveries based on the X-GitHub-Delivery
// GUID. Entries expire after a TTL so the map stays bounded.
type deliveryDeduper struct {
	mu      sync.Mutex
	seen    map[string]time.Time
	ttl     time.Duration
	maxSize int
	now     func() time.Time
}

func newDeliveryDeduper() *deliveryDeduper {
	return &deliveryDeduper{
		seen:    make(map[string]time.Time),
		ttl:     30 * time.Minute,
		maxSize: 10000,
		now:     time.Now,
	}
}

// isDuplicate records the delivery ID and reports whether it was already seen
// within the TTL window. Empty IDs are never treated as duplicates.
func (d *deliveryDeduper) isDuplicate(id string) bool {
	if id == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.now()
	if t, ok := d.seen[id]; ok && now.Sub(t) < d.ttl {
		return true
	}
	// Opportunistic expiry; also hard-cap the map size.
	if len(d.seen) >= d.maxSize {
		for k, t := range d.seen {
			if now.Sub(t) >= d.ttl {
				delete(d.seen, k)
			}
		}
		if len(d.seen) >= d.maxSize {
			d.seen = make(map[string]time.Time)
		}
	}
	d.seen[id] = now
	return false
}

func (d *deliveryDeduper) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return fmt.Sprintf("deliveryDeduper(%d entries)", len(d.seen))
}
