/*
Copyright 2026.

SPDX-License-Identifier: GPL-3.0-only
*/

package platform

import (
	"context"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-github/v68/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

const (
	// skillMaxContentBytes caps fetched SKILL.md content so resolved
	// instructions stay well under etcd object limits.
	skillMaxContentBytes = 256 * 1024
	// skillFetchRetryInterval spaces out re-fetch attempts after failures
	// (the unauthenticated GitHub API is rate limited).
	skillFetchRetryInterval = 5 * time.Minute
)

// skillNameRe accepts lowercase
// alphanumeric with single hyphen separators.
var skillNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// SkillContentsFetcher fetches the SKILL.md content and a content SHA for a
// git skill source. Implemented by githubSkillFetcher; tests inject fakes.
type SkillContentsFetcher interface {
	FetchSkillMD(ctx context.Context, src platformv1alpha1.SkillGitSource) (content, sha string, err error)
}

// SkillReconciler reconciles Skill objects: it validates the spec, fetches
// git-sourced SKILL.md content, and materializes the resolved instructions
// into status for the agent pipeline to consume.
type SkillReconciler struct {
	client.Client
	// Fetcher resolves git sources. Nil uses the public GitHub API.
	Fetcher SkillContentsFetcher
}

// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=skills,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=skills/status,verbs=get;update;patch

func (r *SkillReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	skill := &platformv1alpha1.Skill{}
	if err := r.Get(ctx, req.NamespacedName, skill); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	refresh := skill.Annotations[platformv1alpha1.SkillRefreshAnnotation]
	upToDate := skill.Status.ObservedGeneration == skill.Generation &&
		skill.Status.LastRefresh == refresh &&
		skill.Status.Resolved != nil
	if upToDate {
		return ctrl.Result{}, nil
	}

	status, requeueAfter := r.resolveSkill(ctx, skill, refresh)
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.Skill{}
		if err := r.Get(ctx, req.NamespacedName, fresh); err != nil {
			return err
		}
		patch := client.MergeFrom(fresh.DeepCopy())
		fresh.Status = status
		return r.Status().Patch(ctx, fresh, patch)
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating Skill status: %w", err)
	}
	log.Info("Skill status updated", "name", skill.Name, "phase", status.Phase)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// resolveSkill computes the full status for the current spec generation. A
// non-zero requeue interval is returned for retryable fetch failures.
func (r *SkillReconciler) resolveSkill(ctx context.Context, skill *platformv1alpha1.Skill, refresh string) (platformv1alpha1.SkillStatus, time.Duration) {
	status := platformv1alpha1.SkillStatus{
		ObservedGeneration: skill.Generation,
		LastRefresh:        refresh,
		Conditions:         skill.Status.Conditions,
	}
	fail := func(phase, reason, message string, requeueAfter time.Duration) (platformv1alpha1.SkillStatus, time.Duration) {
		status.Phase = phase
		setCondition(&status.Conditions, "Resolved", metav1.ConditionFalse, reason, message)
		return status, requeueAfter
	}

	inline, git := skill.Spec.Source.Inline, skill.Spec.Source.Git
	switch {
	case inline == nil && git == nil:
		return fail("Invalid", "MissingSource", "source requires exactly one of inline or git", 0)
	case inline != nil && git != nil:
		return fail("Invalid", "AmbiguousSource", "source allows only one of inline or git", 0)
	case inline != nil:
		if !skillNameRe.MatchString(skill.Name) || len(skill.Name) > 64 {
			return fail("Invalid", "InvalidName",
				"skill name must be lowercase alphanumeric with single hyphen separators (max 64 chars)", 0)
		}
		status.Phase = "Ready"
		status.Resolved = &platformv1alpha1.SkillResolved{
			Name:         skill.Name,
			Description:  strings.TrimSpace(skill.Spec.Description),
			Instructions: strings.TrimSpace(inline.Instructions),
			SyncedAt:     ptrTime(metav1.Now()),
		}
		setCondition(&status.Conditions, "Resolved", metav1.ConditionTrue, "Reconciled", "Inline instructions validated")
		return status, 0
	}

	fetcher := r.Fetcher
	if fetcher == nil {
		fetcher = githubSkillFetcher{}
	}
	content, sha, err := fetcher.FetchSkillMD(ctx, *git)
	if err != nil {
		return fail("Error", "FetchFailed", fmt.Sprintf("fetching SKILL.md: %v", err), skillFetchRetryInterval)
	}
	if len(content) > skillMaxContentBytes {
		return fail("Invalid", "ContentTooLarge",
			fmt.Sprintf("SKILL.md is %d bytes (max %d)", len(content), skillMaxContentBytes), 0)
	}
	fm, body, err := parseSkillMD(content)
	if err != nil {
		return fail("Invalid", "ParseFailed", fmt.Sprintf("parsing SKILL.md: %v", err), 0)
	}
	if err := validateSkillFrontmatter(fm, git.Path); err != nil {
		return fail("Invalid", "InvalidFrontmatter", err.Error(), 0)
	}

	status.Phase = "Ready"
	status.Resolved = &platformv1alpha1.SkillResolved{
		Name:         fm.Name,
		Description:  fm.Description,
		Instructions: body,
		SHA:          sha,
		SyncedAt:     ptrTime(metav1.Now()),
	}
	setCondition(&status.Conditions, "Resolved", metav1.ConditionTrue, "Reconciled",
		fmt.Sprintf("Fetched SKILL.md %q at %s", fm.Name, shortSHA(sha)))
	return status, 0
}

func (r *SkillReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Skill{}).
		Named("skill").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}

// skillFrontmatter carries the recognized SKILL.md frontmatter fields
// (standard agent-skill format). Unknown fields are ignored.
type skillFrontmatter struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// parseSkillMD splits SKILL.md into YAML frontmatter and the instruction body.
func parseSkillMD(content string) (skillFrontmatter, string, error) {
	var fm skillFrontmatter
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return fm, "", fmt.Errorf("SKILL.md must start with YAML frontmatter (---)")
	}
	rest := normalized[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return fm, "", fmt.Errorf("SKILL.md frontmatter is not terminated (---)")
	}
	header := rest[:end]
	body := rest[end+len("\n---"):]
	if i := strings.Index(body, "\n"); i >= 0 {
		body = body[i+1:]
	} else {
		body = ""
	}
	if err := yaml.Unmarshal([]byte(header), &fm); err != nil {
		return fm, "", fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	return fm, strings.TrimSpace(body), nil
}

// validateSkillFrontmatter applies the supported skill rules.
func validateSkillFrontmatter(fm skillFrontmatter, srcPath string) error {
	if fm.Name == "" {
		return fmt.Errorf("frontmatter name is required")
	}
	if len(fm.Name) > 64 || !skillNameRe.MatchString(fm.Name) {
		return fmt.Errorf("frontmatter name %q must be lowercase alphanumeric with single hyphen separators (max 64 chars)", fm.Name)
	}
	if fm.Description == "" {
		return fmt.Errorf("frontmatter description is required")
	}
	if len(fm.Description) > 1024 {
		return fmt.Errorf("frontmatter description exceeds 1024 characters")
	}
	if dir := skillFolderName(srcPath); dir != "" && dir != fm.Name {
		return fmt.Errorf("frontmatter name %q must match its folder name %q", fm.Name, dir)
	}
	return nil
}

// skillFolderName returns the folder a SKILL.md path points into ("" for the
// repository root).
func skillFolderName(srcPath string) string {
	p := strings.Trim(strings.TrimSpace(srcPath), "/")
	if p == "" {
		return ""
	}
	if strings.EqualFold(path.Base(p), "SKILL.md") {
		p = path.Dir(p)
		if p == "." || p == "/" {
			return ""
		}
	}
	return path.Base(p)
}

// githubSkillFetcher fetches SKILL.md from public github.com repositories via
// the contents API (no clone). Requests are authenticated with GITHUB_TOKEN
// when the operator has one — unauthenticated GitHub API calls are limited to
// 60/hour per IP, which stalls skill syncs on busy clusters.
type githubSkillFetcher struct{}

func (githubSkillFetcher) FetchSkillMD(ctx context.Context, src platformv1alpha1.SkillGitSource) (string, string, error) {
	owner, repo, err := parseGitHubRepoURL(src.URL)
	if err != nil {
		return "", "", err
	}
	skillPath := strings.Trim(strings.TrimSpace(src.Path), "/")
	if !strings.EqualFold(path.Base(skillPath), "SKILL.md") {
		skillPath = path.Join(skillPath, "SKILL.md")
	}
	opts := &github.RepositoryContentGetOptions{Ref: strings.TrimSpace(src.Ref)}
	client := github.NewClient(nil)
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		client = client.WithAuthToken(token)
	}
	file, _, _, err := client.Repositories.GetContents(ctx, owner, repo, skillPath, opts)
	if err != nil {
		return "", "", fmt.Errorf("fetching %s/%s %s: %w", owner, repo, skillPath, err)
	}
	if file == nil {
		return "", "", fmt.Errorf("%s is a directory, expected a SKILL.md file", skillPath)
	}
	content, err := file.GetContent()
	if err != nil {
		return "", "", fmt.Errorf("decoding %s: %w", skillPath, err)
	}
	return content, file.GetSHA(), nil
}

// parseGitHubRepoURL extracts owner/repo from a github.com repository URL.
func parseGitHubRepoURL(raw string) (owner, repo string, err error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "git@github.com:")
	s = strings.TrimPrefix(s, "github.com/")
	s = strings.TrimSuffix(strings.Trim(s, "/"), ".git")
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("git url %q is not a github.com repository (expected https://github.com/owner/repo)", raw)
	}
	return parts[0], parts[1], nil
}

// setCondition upserts a status condition preserving LastTransitionTime for
// unchanged states.
func setCondition(conditions *[]metav1.Condition, condType string, condStatus metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i, c := range *conditions {
		if c.Type == condType {
			if c.Status == condStatus && c.Reason == reason && c.Message == message {
				now = c.LastTransitionTime
			}
			(*conditions)[i].Status = condStatus
			(*conditions)[i].Reason = reason
			(*conditions)[i].Message = message
			(*conditions)[i].LastTransitionTime = now
			return
		}
	}
	*conditions = append(*conditions, metav1.Condition{
		Type:               condType,
		Status:             condStatus,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

func ptrTime(t metav1.Time) *metav1.Time { return &t }

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
