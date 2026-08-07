package triggers

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v68/github"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/linear"
	"github.com/gratefulagents/gratefulagents/internal/slack"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// SecurityScanNotifier delivers finding notifications. The default
// implementation posts to Slack incoming webhooks and creates GitHub/Linear
// issues; tests inject fakes.
type SecurityScanNotifier interface {
	SendSlack(ctx context.Context, webhookURL, text string) error
	CreateGitHubIssue(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, title, body string) (string, error)
	CreateLinearIssue(ctx context.Context, apiKey, teamID, title, body string) (string, error)
}

type defaultSecurityScanNotifier struct {
	client client.Client
	minter gitHubAppTokenMinter
}

func (n *defaultSecurityScanNotifier) SendSlack(ctx context.Context, webhookURL, text string) error {
	return slack.PostWebhookMessage(ctx, nil, webhookURL, text)
}

func (n *defaultSecurityScanNotifier) CreateGitHubIssue(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, title, body string) (string, error) {
	token, err := resolveGitHubToken(ctx, n.client, gh, n.minter)
	if err != nil {
		return "", err
	}
	ghc := github.NewClient(nil).WithAuthToken(token)
	issue, _, err := ghc.Issues.Create(ctx, gh.Spec.Owner, gh.Spec.Repo, &github.IssueRequest{
		Title: &title,
		Body:  &body,
	})
	if err != nil {
		return "", fmt.Errorf("creating GitHub issue: %w", err)
	}
	return issue.GetHTMLURL(), nil
}

func (n *defaultSecurityScanNotifier) CreateLinearIssue(ctx context.Context, apiKey, teamID, title, body string) (string, error) {
	created, err := linear.NewClient(apiKey).CreateIssue(ctx, linear.CreateIssueInput{
		TeamID:      teamID,
		Title:       title,
		Description: body,
	})
	if err != nil {
		return "", fmt.Errorf("creating Linear issue: %w", err)
	}
	return created.URL, nil
}

func (r *SecurityScanReconciler) notifier() SecurityScanNotifier {
	if r.Notifier != nil {
		return r.Notifier
	}
	return &defaultSecurityScanNotifier{client: r.Client, minter: nil}
}

// securitySeverityAtLeast reports whether severity is at or above min.
func securitySeverityAtLeast(severity, min string) bool {
	return securitySeverityRank(severity) >= securitySeverityRank(min) && securitySeverityRank(severity) > 0
}

// notificationRuleMatches reports whether the finding is in scope for the
// rule: open, non-duplicate, at or above the rule severity, and in one of the
// rule's baseline states (new and/or regressed — reopened counts as
// regressed).
func notificationRuleMatches(rule triggersv1alpha1.SecurityScanNotificationRule, f store.SecurityFindingRecord) bool {
	if f.Status != store.SecurityFindingStatusOpen || f.DuplicateOf != nil {
		return false
	}
	if !securitySeverityAtLeast(f.Severity, rule.EffectiveMinSeverity()) {
		return false
	}
	switch rule.EffectiveNotifyOn() {
	case "new":
		return f.BaselineState == store.SecurityFindingBaselineNew
	case "regressed":
		return f.BaselineState == store.SecurityFindingBaselineRegressed || f.BaselineState == store.SecurityFindingBaselineReopened
	default:
		return f.BaselineState == store.SecurityFindingBaselineNew ||
			f.BaselineState == store.SecurityFindingBaselineRegressed ||
			f.BaselineState == store.SecurityFindingBaselineReopened
	}
}

// securityFindingNotificationLocation renders "path:line" or "".
func securityFindingNotificationLocation(f store.SecurityFindingRecord) string {
	if f.FilePath == "" {
		return ""
	}
	if f.StartLine > 0 {
		return fmt.Sprintf("%s:%d", f.FilePath, f.StartLine)
	}
	return f.FilePath
}

// securityFindingDashboardPath is the dashboard finding detail path embedded
// in notifications instead of evidence.
func securityFindingDashboardPath(f store.SecurityFindingRecord) string {
	return fmt.Sprintf("/security/%s/%s/findings/%s", f.Namespace, f.RunName, f.ID)
}

// buildSecurityNotificationSlackText renders one Slack message per rule per
// run: severity, title, and location per finding plus dashboard paths — never
// evidence, impact analysis, or attack vectors.
func buildSecurityNotificationSlackText(scan *triggersv1alpha1.SecurityScan, rule triggersv1alpha1.SecurityScanNotificationRule, findings []store.SecurityFindingRecord, dashboardBaseURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, ":rotating_light: Security scan *%s* reported %d finding(s) matching rule *%s* (>= %s, %s):\n",
		scan.Name, len(findings), rule.Name, rule.EffectiveMinSeverity(), rule.EffectiveNotifyOn())
	for _, f := range findings {
		line := fmt.Sprintf("• [%s] %s", f.Severity, f.Title)
		if loc := securityFindingNotificationLocation(f); loc != "" {
			line += " (`" + loc + "`)"
		}
		path := securityFindingDashboardPath(f)
		if dashboardBaseURL != "" {
			line += " — " + strings.TrimRight(dashboardBaseURL, "/") + path
		} else {
			line += " — `" + path + "`"
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// buildSecurityNotificationIssueBody renders the non-secret issue body:
// identifying metadata and a dashboard link, never raw evidence.
func buildSecurityNotificationIssueBody(f store.SecurityFindingRecord, dashboardBaseURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Severity:** %s\n", f.Severity)
	if f.Category != "" {
		fmt.Fprintf(&b, "**Category:** %s\n", f.Category)
	}
	if loc := securityFindingNotificationLocation(f); loc != "" {
		fmt.Fprintf(&b, "**Location:** `%s`\n", loc)
	}
	if f.Repository != "" {
		fmt.Fprintf(&b, "**Repository:** %s\n", f.Repository)
	}
	if f.BaselineState != "" {
		fmt.Fprintf(&b, "**State:** %s\n", f.BaselineState)
	}
	path := securityFindingDashboardPath(f)
	if dashboardBaseURL != "" {
		fmt.Fprintf(&b, "\nFull details (evidence, impact, remediation) are in the security dashboard:\n%s%s\n", strings.TrimRight(dashboardBaseURL, "/"), path)
	} else {
		fmt.Fprintf(&b, "\nFull details (evidence, impact, remediation) are in the security dashboard:\n`%s`\n", path)
	}
	return b.String()
}

type securityNotificationChannel struct {
	name string
	// send delivers the batch and returns the fingerprints of findings that
	// were actually delivered. On error, delivered may be a non-empty prefix
	// of the batch (per-finding channels can fail partway through); only the
	// undelivered remainder is safe to retry.
	send func(ctx context.Context, findings []store.SecurityFindingRecord) (delivered []string, err error)
}

func matchingSecurityNotificationFindings(rule triggersv1alpha1.SecurityScanNotificationRule, findings []store.SecurityFindingRecord) []store.SecurityFindingRecord {
	matched := make([]store.SecurityFindingRecord, 0, len(findings))
	for _, finding := range findings {
		if notificationRuleMatches(rule, finding) {
			matched = append(matched, finding)
		}
	}
	return matched
}

func (r *SecurityScanReconciler) securityNotificationChannels(scan *triggersv1alpha1.SecurityScan, rule triggersv1alpha1.SecurityScanNotificationRule) []securityNotificationChannel {
	var channels []securityNotificationChannel
	if rule.Slack != nil {
		slackRule := rule
		channels = append(channels, securityNotificationChannel{name: "slack", send: func(ctx context.Context, batch []store.SecurityFindingRecord) ([]string, error) {
			webhookURL, err := ReadSecretValue(ctx, r.Client, scan.Namespace, slackRule.Slack.WebhookSecretRef, "url")
			if err != nil {
				return nil, fmt.Errorf("reading slack webhook secret: %w", err)
			}
			if err := r.notifier().SendSlack(ctx, strings.TrimSpace(webhookURL), buildSecurityNotificationSlackText(scan, slackRule, batch, r.DashboardBaseURL)); err != nil {
				return nil, err
			}
			delivered := make([]string, 0, len(batch))
			for _, finding := range batch {
				delivered = append(delivered, finding.Fingerprint)
			}
			return delivered, nil
		}})
	}
	if rule.GitHubIssues != nil {
		ghRule := rule
		channels = append(channels, securityNotificationChannel{name: "github", send: func(ctx context.Context, batch []store.SecurityFindingRecord) ([]string, error) {
			gh, err := r.notificationRepository(ctx, scan, ghRule.GitHubIssues.RepositoryRef)
			if err != nil {
				return nil, err
			}
			var delivered []string
			for _, finding := range batch {
				title := fmt.Sprintf("[security][%s] %s", finding.Severity, finding.Title)
				if _, err := r.notifier().CreateGitHubIssue(ctx, gh, title, buildSecurityNotificationIssueBody(finding, r.DashboardBaseURL)); err != nil {
					return delivered, err
				}
				delivered = append(delivered, finding.Fingerprint)
			}
			return delivered, nil
		}})
	}
	if rule.Linear != nil {
		linRule := rule
		channels = append(channels, securityNotificationChannel{name: "linear", send: func(ctx context.Context, batch []store.SecurityFindingRecord) ([]string, error) {
			apiKey, err := ReadSecretValue(ctx, r.Client, scan.Namespace, linRule.Linear.APIKeySecretRef, "api-key")
			if err != nil {
				return nil, fmt.Errorf("reading Linear API key secret: %w", err)
			}
			var delivered []string
			for _, finding := range batch {
				title := fmt.Sprintf("[security][%s] %s", finding.Severity, finding.Title)
				if _, err := r.notifier().CreateLinearIssue(ctx, strings.TrimSpace(apiKey), linRule.Linear.TeamID, title, buildSecurityNotificationIssueBody(finding, r.DashboardBaseURL)); err != nil {
					return delivered, err
				}
				delivered = append(delivered, finding.Fingerprint)
			}
			return delivered, nil
		}})
	}
	return channels
}

// notifyRunFindings evaluates the scan's notification rules against the last
// run's findings once the run has terminated successfully. Duplicate noise is
// suppressed with persisted (scan, rule/channel, fingerprint) markers claimed
// through the findings store before sending; a failed delivery releases only
// the claims the channel did not deliver, so the next reconcile retries
// exactly the undelivered findings without ever double-notifying a delivered
// one. Failures are recorded in status.lastNotifications plus a
// Warning event and never corrupt other scan state. The returned flag asks
// the caller to requeue for a retry.
func (r *SecurityScanReconciler) notifyRunFindings(ctx context.Context, scan *triggersv1alpha1.SecurityScan) bool {
	rules := scan.Spec.Notifications
	if len(rules) == 0 || r.Findings == nil || scan.Status.LastRunName == "" {
		return false
	}
	log := logf.FromContext(ctx)
	runName := scan.Status.LastRunName
	if s := scan.Status.LastNotifications; s != nil && s.LastRunName == runName && s.LastError == "" {
		return false
	}
	run := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: scan.Namespace, Name: runName}, run); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "failed to get scan AgentRun for notifications", "run", runName)
		}
		return false
	}
	if run.Status.Phase != platformv1alpha1.AgentRunPhaseSucceeded {
		return false
	}

	findings, err := r.Findings.ListSecurityFindings(ctx, store.SecurityFindingFilter{
		Namespace: scan.Namespace, ScanName: scan.Name, RunName: runName, Limit: 1000,
	})
	if err != nil {
		r.recordNotificationResult(ctx, scan, runName, 0, 0, "listing findings: "+err.Error())
		return true
	}

	sent, suppressed, failures := r.deliverSecurityNotifications(ctx, scan, findings)

	r.recordNotificationResult(ctx, scan, runName, sent, suppressed, strings.Join(failures, "; "))
	return len(failures) > 0
}

// deliverSecurityNotifications evaluates every notification rule against the
// given findings and delivers the matches, claiming persisted (scan,
// rule/channel, fingerprint) dedupe markers before sending and releasing
// only undelivered claims on failure. Shared by the run-scoped coordinator
// path and the execution-scoped deterministic path.
func (r *SecurityScanReconciler) deliverSecurityNotifications(ctx context.Context, scan *triggersv1alpha1.SecurityScan, findings []store.SecurityFindingRecord) (sent, suppressed int32, failures []string) {
	for _, rule := range scan.Spec.Notifications {
		matched := matchingSecurityNotificationFindings(rule, findings)
		if len(matched) == 0 {
			continue
		}

		channels := r.securityNotificationChannels(scan, rule)

		fingerprints := make([]string, 0, len(matched))
		byFingerprint := make(map[string]store.SecurityFindingRecord, len(matched))
		for _, f := range matched {
			if f.Fingerprint == "" {
				continue
			}
			fingerprints = append(fingerprints, f.Fingerprint)
			byFingerprint[f.Fingerprint] = f
		}
		if len(fingerprints) == 0 {
			continue
		}

		for _, ch := range channels {
			ruleKey := rule.Name + "/" + ch.name
			claimed, err := r.Findings.ClaimSecurityNotifications(ctx, scan.Namespace, scan.Name, ruleKey, fingerprints)
			if err != nil {
				failures = append(failures, fmt.Sprintf("rule %q %s: claiming dedupe markers: %v", rule.Name, ch.name, err))
				continue
			}
			suppressed += int32(len(fingerprints) - len(claimed))
			if len(claimed) == 0 {
				continue
			}
			batch := make([]store.SecurityFindingRecord, 0, len(claimed))
			for _, fp := range claimed {
				batch = append(batch, byFingerprint[fp])
			}
			delivered, sendErr := ch.send(ctx, batch)
			sent += int32(len(delivered))
			if sendErr != nil {
				failures = append(failures, fmt.Sprintf("rule %q %s: %v", rule.Name, ch.name, sendErr))
				deliveredSet := make(map[string]bool, len(delivered))
				for _, fp := range delivered {
					deliveredSet[fp] = true
				}
				undelivered := make([]string, 0, len(claimed))
				for _, fp := range claimed {
					if !deliveredSet[fp] {
						undelivered = append(undelivered, fp)
					}
				}
				if len(undelivered) > 0 {
					if releaseErr := r.Findings.ReleaseSecurityNotifications(ctx, scan.Namespace, scan.Name, ruleKey, undelivered); releaseErr != nil {
						// The claim stays: the finding will never notify twice,
						// but this delivery is lost. Surface it.
						failures = append(failures, fmt.Sprintf("rule %q %s: releasing dedupe markers after failed delivery: %v", rule.Name, ch.name, releaseErr))
					}
				}
				continue
			}
		}
	}

	return sent, suppressed, failures
}

// notifyExecutionFindings is the deterministic-execution counterpart of
// notifyRunFindings: it evaluates the notification rules against the
// findings persisted by every task run of a SUCCEEDED execution and delivers
// them exactly once per execution (gated on
// status.lastNotifications.lastRunName carrying the execution key, with the
// same persisted per-fingerprint dedupe claims underneath). Failed
// executions never notify, mirroring the run-succeeded requirement of the
// coordinator path.
func (r *SecurityScanReconciler) notifyExecutionFindings(ctx context.Context, scan *triggersv1alpha1.SecurityScan, exec *triggersv1alpha1.SecurityScanExecutionStatus) bool {
	if len(scan.Spec.Notifications) == 0 || r.Findings == nil {
		return false
	}
	if exec.Phase != triggersv1alpha1.SecurityScanExecutionPhaseSucceeded {
		return false
	}
	executionKey := "execution/" + exec.ID
	if s := scan.Status.LastNotifications; s != nil && s.LastRunName == executionKey && s.LastError == "" {
		return false
	}

	var findings []store.SecurityFindingRecord
	for _, runName := range securityScanExecutionRunNames(exec) {
		batch, err := r.Findings.ListSecurityFindings(ctx, store.SecurityFindingFilter{
			Namespace: scan.Namespace, ScanName: scan.Name, RunName: runName, Limit: 1000,
		})
		if err != nil {
			r.recordNotificationResult(ctx, scan, executionKey, 0, 0, "listing findings: "+err.Error())
			return true
		}
		findings = append(findings, batch...)
	}

	sent, suppressed, failures := r.deliverSecurityNotifications(ctx, scan, findings)

	r.recordNotificationResult(ctx, scan, executionKey, sent, suppressed, strings.Join(failures, "; "))
	return len(failures) > 0
}

// notificationRepository resolves the GitHubRepository used by a
// githubIssues channel: the rule's own ref or the trigger repository.
func (r *SecurityScanReconciler) notificationRepository(ctx context.Context, scan *triggersv1alpha1.SecurityScan, ref *triggersv1alpha1.SecurityResourceRef) (*triggersv1alpha1.GitHubRepository, error) {
	if ref != nil && strings.TrimSpace(ref.Name) != "" {
		gh := &triggersv1alpha1.GitHubRepository{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: scan.Namespace, Name: ref.Name}, gh); err != nil {
			return nil, fmt.Errorf("getting GitHubRepository %s/%s: %w", scan.Namespace, ref.Name, err)
		}
		return gh, nil
	}
	return r.triggerRepository(ctx, scan)
}

// recordNotificationResult persists the delivery outcome without touching any
// other scan state.
func (r *SecurityScanReconciler) recordNotificationResult(ctx context.Context, scan *triggersv1alpha1.SecurityScan, runName string, sent, suppressed int32, failure string) {
	if failure != "" {
		r.recordScanEvent(scan, corev1.EventTypeWarning, "NotificationFailed", failure)
	}
	now := metav1.NewTime(r.now())
	if err := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
		prev := fresh.Status.LastNotifications
		next := &triggersv1alpha1.SecurityScanNotificationStatus{LastRunName: runName, LastError: failure}
		if prev != nil {
			next.Sent = prev.Sent
			next.Suppressed = prev.Suppressed
			next.LastNotifiedAt = prev.LastNotifiedAt
		}
		next.Sent += sent
		next.Suppressed += suppressed
		if sent > 0 {
			next.LastNotifiedAt = &now
		}
		fresh.Status.LastNotifications = next
	}); err != nil {
		logf.FromContext(ctx).Error(err, "failed to record notification result", "scan", scan.Name)
	}
}
