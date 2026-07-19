package triggers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const PRLoopKeyAnnotation = "triggers.gratefulagents.dev/pr-loop-key"

type prLoopRecord struct {
	Key        string
	Repository string
	Number     int
	URL        string
	BaseRef    string
}

func loopRecordForEvent(event PullRequestEvent) prLoopRecord {
	repository := normalizeRepositoryName(event.Repository)
	if repository == "" {
		repository = repositoryFromPullRequestURL(event.URL)
	}
	return prLoopRecord{
		Key:        prLoopKey(repository, event.Number),
		Repository: repository,
		Number:     event.Number,
		URL:        event.URL,
		BaseRef:    event.BaseRef,
	}
}

func prLoopKey(repository string, number int) string {
	return fmt.Sprintf("%s#%d", normalizeRepositoryName(repository), number)
}

func loopMarkerKey(base, loopKey string) string {
	if loopKey == "" {
		return base
	}
	sum := sha256.Sum256([]byte(strings.ToLower(loopKey)))
	return base + "-" + hex.EncodeToString(sum[:6])
}

func setLoopLabel(run *platformv1alpha1.AgentRun, loopKey, base, value string) {
	if run.Labels == nil {
		run.Labels = map[string]string{}
	}
	run.Labels[base] = value // compatibility/current-loop view
	if loopKey != "" {
		run.Labels[loopMarkerKey(base, loopKey)] = value
	}
}

func setLoopAnnotation(run *platformv1alpha1.AgentRun, loopKey, base, value string) {
	if run.Annotations == nil {
		run.Annotations = map[string]string{}
	}
	run.Annotations[base] = value // compatibility/current-loop view
	if loopKey != "" {
		run.Annotations[loopMarkerKey(base, loopKey)] = value
	}
}

func loopState(run *platformv1alpha1.AgentRun, loopKey, base string) string {
	if run == nil {
		return ""
	}
	if loopKey != "" {
		if value := run.Labels[loopMarkerKey(base, loopKey)]; value != "" {
			return value
		}
		if value := run.Annotations[loopMarkerKey(base, loopKey)]; value != "" {
			return value
		}
		// Only the current loop may fall back to compatibility markers.
		current := strings.TrimSpace(run.Annotations[PRLoopKeyAnnotation])
		if current != "" && current != loopKey {
			return ""
		}
	}
	if value := run.Labels[base]; value != "" {
		return value
	}
	return run.Annotations[base]
}

func loopAnnotation(run *platformv1alpha1.AgentRun, loopKey, base string) string {
	if run == nil {
		return ""
	}
	if loopKey != "" {
		if value := run.Annotations[loopMarkerKey(base, loopKey)]; value != "" {
			return value
		}
	}
	return run.Annotations[base]
}

func loopRecordFromRun(run *platformv1alpha1.AgentRun, loopKey string) prLoopRecord {
	getAnnotation := func(base string) string {
		if loopKey != "" {
			if value := run.Annotations[loopMarkerKey(base, loopKey)]; value != "" {
				return value
			}
		}
		return run.Annotations[base]
	}
	getLabel := func(base string) string {
		if loopKey != "" {
			if value := run.Labels[loopMarkerKey(base, loopKey)]; value != "" {
				return value
			}
		}
		return run.Labels[base]
	}
	record := prLoopRecord{
		Key:     loopKey,
		Number:  annotationInt(getLabel(PRLoopNumberLabel), 0),
		URL:     getAnnotation(PRLoopURLAnnotation),
		BaseRef: getAnnotation(PRLoopBaseRefAnnotation),
	}
	record.Repository = repositoryFromPullRequestURL(record.URL)
	if record.Key == "" && record.Number > 0 {
		record.Key = prLoopKey(record.Repository, record.Number)
	}
	return record
}

func hasLoopRecord(run *platformv1alpha1.AgentRun, loopKey string) bool {
	for _, record := range loopRecords(run) {
		if record.Key == loopKey {
			return true
		}
	}
	return false
}

func loopRecords(run *platformv1alpha1.AgentRun) []prLoopRecord {
	if run == nil {
		return nil
	}
	seen := map[string]bool{}
	var records []prLoopRecord
	prefix := PRLoopKeyAnnotation + "-"
	for marker, key := range run.Annotations {
		if !strings.HasPrefix(marker, prefix) || strings.TrimSpace(key) == "" || seen[key] {
			continue
		}
		seen[key] = true
		records = append(records, loopRecordFromRun(run, key))
	}
	currentKey := strings.TrimSpace(run.Annotations[PRLoopKeyAnnotation])
	if currentKey != "" && !seen[currentKey] {
		seen[currentKey] = true
		records = append(records, loopRecordFromRun(run, currentKey))
	}
	// Upgrade legacy single-PR markers lazily. The URL supplies repository
	// identity, avoiding collisions where two repos both have PR #42.
	if len(records) == 0 {
		legacy := loopRecordFromRun(run, "")
		if legacy.Number > 0 {
			records = append(records, legacy)
		}
	}
	return records
}

func normalizeRepositoryName(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "/")
	value = strings.TrimSuffix(value, ".git")
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return strings.ToLower(parts[0] + "/" + parts[1])
}

func repositoryFromPullRequestURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return normalizeRepositoryName(parts[0] + "/" + parts[1])
}

func repositoryFromCloneURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "git@github.com:") {
		return normalizeRepositoryName(strings.TrimPrefix(raw, "git@github.com:"))
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
		return ""
	}
	return normalizeRepositoryName(u.Path)
}

func runDeclaresRepository(run *platformv1alpha1.AgentRun, repository string) bool {
	repository = normalizeRepositoryName(repository)
	if run == nil || repository == "" {
		return false
	}
	urls := append([]string{run.Spec.Repository.URL}, run.Spec.Repository.AdditionalRepos...)
	for _, raw := range urls {
		if repositoryFromCloneURL(raw) == repository {
			return true
		}
	}
	return false
}

func declaredRepositoryURL(run *platformv1alpha1.AgentRun, repository string) string {
	if run == nil {
		return ""
	}
	urls := append([]string{run.Spec.Repository.URL}, run.Spec.Repository.AdditionalRepos...)
	for _, raw := range urls {
		if repositoryFromCloneURL(raw) == normalizeRepositoryName(repository) {
			return raw
		}
	}
	return ""
}

func reviewerOwnerReferences(implementer *platformv1alpha1.AgentRun) []metav1.OwnerReference {
	if implementer == nil {
		return nil
	}
	owners := make([]metav1.OwnerReference, 0, len(implementer.OwnerReferences))
	for _, owner := range implementer.OwnerReferences {
		// AgentRun ownership has orchestration semantics: the platform treats
		// the owned run as a delegated team child. Reviewers are independent.
		if owner.APIVersion == platformv1alpha1.GroupVersion.String() && owner.Kind == "AgentRun" {
			continue
		}
		owners = append(owners, owner)
	}
	return owners
}

func reviewerAdditionalRepositories(run *platformv1alpha1.AgentRun, primaryURL string, configured []string) []string {
	primaryRepository := repositoryFromCloneURL(primaryURL)
	declared := append([]string(nil), configured...)
	if run != nil {
		declared = append(declared, run.Spec.Repository.URL)
		declared = append(declared, run.Spec.Repository.AdditionalRepos...)
	}
	seen := map[string]bool{primaryRepository: true}
	var additional []string
	for _, raw := range declared {
		repository := repositoryFromCloneURL(raw)
		if repository == "" || seen[repository] {
			continue
		}
		seen[repository] = true
		additional = append(additional, raw)
	}
	return additional
}

func runHasPullRequestURL(run *platformv1alpha1.AgentRun, prURL string) bool {
	if run == nil || run.Status.Artifacts == nil || strings.TrimSpace(prURL) == "" {
		return false
	}
	for _, value := range append(append([]string(nil), run.Status.Artifacts.PullRequestURLs...), run.Status.Artifacts.PullRequestURL) {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(prURL)) {
			return true
		}
	}
	return false
}

func (e *PRLoopEngine) implementerForPullRequestEvent(ctx context.Context, event PullRequestEvent, requireLoop bool) (*platformv1alpha1.AgentRun, error) {
	namespace := strings.TrimSpace(event.TargetImplementerNamespace)
	name := strings.TrimSpace(event.TargetImplementerName)
	if namespace == "" && name == "" {
		return nil, nil
	}
	if namespace == "" || name == "" {
		return nil, fmt.Errorf("target implementer namespace and name must both be set")
	}
	run := &platformv1alpha1.AgentRun{}
	if err := e.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, run); err != nil {
		return nil, err
	}
	repository := normalizeRepositoryName(event.Repository)
	if run.Labels[PRLoopRoleLabel] == PRLoopRoleReviewer || !runDeclaresRepository(run, repository) || !runRecordsPullRequestURL(run, event.URL) {
		return nil, fmt.Errorf("target implementer %s/%s does not own %s", namespace, name, event.URL)
	}
	if requireLoop && !hasLoopRecord(run, prLoopKey(repository, event.Number)) {
		return nil, fmt.Errorf("target implementer %s/%s has no loop record for %s#%d", namespace, name, repository, event.Number)
	}
	return run, nil
}

func (e *PRLoopEngine) findImplementerByHead(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, repository, prURL, headRef string) (*platformv1alpha1.AgentRun, error) {
	repository = normalizeRepositoryName(repository)
	if repository == "" && gh != nil {
		repository = normalizeRepositoryName(gh.Spec.Owner + "/" + gh.Spec.Repo)
	}
	if strings.TrimSpace(headRef) == "" || repository == "" {
		return nil, nil
	}
	list := &platformv1alpha1.AgentRunList{}
	if err := e.List(ctx, list); err != nil {
		return nil, fmt.Errorf("listing AgentRuns for PR head %q: %w", headRef, err)
	}
	var matches []*platformv1alpha1.AgentRun
	for i := range list.Items {
		run := &list.Items[i]
		if run.Name == headRef && run.Labels[PRLoopRoleLabel] != PRLoopRoleReviewer && runDeclaresRepository(run, repository) {
			matches = append(matches, run)
		}
	}
	if len(matches) > 1 && strings.TrimSpace(prURL) != "" {
		var artifactMatches []*platformv1alpha1.AgentRun
		for _, run := range matches {
			if runHasPullRequestURL(run, prURL) {
				artifactMatches = append(artifactMatches, run)
			}
		}
		if len(artifactMatches) == 1 {
			return artifactMatches[0], nil
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("PR head %q in repository %s matches AgentRuns in multiple namespaces", headRef, repository)
	}
	return nil, nil
}

func (e *PRLoopEngine) findImplementerByPR(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, repository string, prNumber int) (*platformv1alpha1.AgentRun, string, error) {
	if prNumber <= 0 {
		return nil, "", nil
	}
	repository = normalizeRepositoryName(repository)
	if repository == "" && gh != nil {
		repository = normalizeRepositoryName(gh.Spec.Owner + "/" + gh.Spec.Repo)
	}
	key := prLoopKey(repository, prNumber)
	list := &platformv1alpha1.AgentRunList{}
	if err := e.List(ctx, list); err != nil {
		return nil, key, fmt.Errorf("listing AgentRuns for PR %s: %w", key, err)
	}
	var matches []*platformv1alpha1.AgentRun
	for i := range list.Items {
		run := &list.Items[i]
		if run.Labels[PRLoopRoleLabel] == PRLoopRoleReviewer || !runDeclaresRepository(run, repository) {
			continue
		}
		for _, record := range loopRecords(run) {
			if record.Key == key || (record.Number == prNumber && record.Repository == repository) {
				matches = append(matches, run)
				break
			}
		}
	}
	if len(matches) == 1 {
		return matches[0], key, nil
	}
	if len(matches) > 1 {
		return nil, key, fmt.Errorf("PR %s is linked to AgentRuns in multiple namespaces", key)
	}
	return nil, key, nil
}

func (e *PRLoopEngine) lookupRepositoryForLoop(ctx context.Context, implementer *platformv1alpha1.AgentRun, repository string) *triggersv1alpha1.GitHubRepository {
	repository = normalizeRepositoryName(repository)
	list := &triggersv1alpha1.GitHubRepositoryList{}
	if err := e.List(ctx, list); err != nil {
		return nil
	}
	var fallback *triggersv1alpha1.GitHubRepository
	for i := range list.Items {
		gh := &list.Items[i]
		if normalizeRepositoryName(gh.Spec.Owner+"/"+gh.Spec.Repo) != repository {
			continue
		}
		if implementer != nil && gh.Namespace == implementer.Namespace {
			return gh
		}
		if fallback == nil {
			fallback = gh
		}
	}
	return fallback
}

func reviewerRunName(implementerName, loopKey string, round int) string {
	sum := sha256.Sum256([]byte(strings.ToLower(loopKey)))
	return ghIssueName("rev", implementerName, hex.EncodeToString(sum[:4])+"-r"+strconv.Itoa(round))
}
