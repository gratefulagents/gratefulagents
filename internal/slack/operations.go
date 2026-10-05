package slack

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"time"

	slackgo "github.com/slack-go/slack"
)

// OperationalActionPrefix namespaces every Block Kit action and view callback
// that belongs to the App Home run controls, so the connector can route them
// (including to a commander of a shared-workspace member) before any other
// interaction handling.
const OperationalActionPrefix = "slack_ops_"

// Block Kit action IDs for the App Home run controls.
const (
	ActionRunStart   = OperationalActionPrefix + "start"
	ActionRunRefresh = OperationalActionPrefix + "refresh"
	ActionRunStop    = OperationalActionPrefix + "stop"
	ActionRunResume  = OperationalActionPrefix + "resume"
)

// View callback IDs and input block/action IDs for the run modals.
const (
	CallbackRunStart   = OperationalActionPrefix + "start"
	CallbackRunResume  = OperationalActionPrefix + "resume"
	BlockRunRepository = "ops_repository"
	BlockRunBranch     = "ops_branch"
	BlockRunTask       = "ops_task"
	ActionRunInput     = "value"
)

// IsOperationalAction reports whether an action or callback ID belongs to the
// App Home run controls.
func IsOperationalAction(id string) bool {
	return strings.HasPrefix(id, OperationalActionPrefix)
}

// Slack limits applied to the operational Home view.
const (
	// homeRunCardLimit caps the run cards on the Home tab so the view stays
	// scannable and well inside Slack's 100-block ceiling.
	homeRunCardLimit = 10
	// homePullRequestLimit caps the PR lines shown per run card.
	homePullRequestLimit = 3
	// optionTextMaxRunes is Slack's limit for a select option label.
	optionTextMaxRunes = 75
	// RepositorySelectLimit is Slack's static-select option limit, and so the
	// most repositories the New Run form can offer.
	RepositorySelectLimit = 100
	// HomeApprovalCountCap is the largest exact approval backlog the Home tab
	// reports; anything beyond shows as "N+".
	HomeApprovalCountCap = 100
)

// HomePullRequest is one monitored pull request shown under a run card.
type HomePullRequest struct {
	URL   string
	Title string
	// Lifecycle is the GitHub lifecycle: open, draft, merged, or closed.
	Lifecycle string
	// Checks summarizes the current-head check rollup: success, failure,
	// pending, none, or unknown.
	Checks string
}

// HomeRun is one run card on the operational Home tab.
type HomeRun struct {
	Name       string
	Phase      string
	Repository string
	BaseBranch string
	// StartedAt is when the run started (or was created); zero hides the field.
	StartedAt    time.Time
	PullRequests []HomePullRequest
	CanStop      bool
	CanResume    bool
}

// HomeOperations is everything the operational Home tab renders for an
// authorized owner or commander. It is a plain view model so the connector
// gathers state and this package only renders it.
type HomeOperations struct {
	// Repositories are the configured repository URLs New Run can target.
	Repositories []string
	// BaseBranch is the configured default base branch ("" = repository default).
	BaseBranch string
	// ShowApprovals renders the pending-approval line (owner only).
	ShowApprovals bool
	// PendingApprovals is the number of held replies awaiting the owner.
	PendingApprovals int
	// Runs are the cards to render, most relevant first; only the first
	// homeRunCardLimit are shown.
	Runs []HomeRun
	// RunsUnavailable replaces the run list with a transient-error note.
	RunsUnavailable bool
	// PullRequestsUnavailable notes that PR/check state could not be read.
	PullRequestsUnavailable bool
}

// BuildHomeOperationsBlocks renders the run controls appended to the App Home
// placeholder for an authorized user: the New Run / Refresh actions, the
// configured repositories, the owner's approval backlog, and one card per
// run with Stop / Resume controls.
func BuildHomeOperationsBlocks(ops HomeOperations) []slackgo.Block {
	blocks := []slackgo.Block{
		slackgo.NewDividerBlock(),
		mrkdwnSection("*Operations*\nStart a run in a configured repository, or stop and resume the runs below. " +
			"Results and notifications arrive privately in your DM with this app."),
		slackgo.NewActionBlock("slack_ops",
			slackgo.NewButtonBlockElement(ActionRunStart, "start", plainText("New Run")).
				WithStyle(slackgo.StylePrimary),
			slackgo.NewButtonBlockElement(ActionRunRefresh, "refresh", plainText("Refresh")),
		),
		slackgo.NewContextBlock("slack_ops_targets", mrkdwnText(repositoryContextLine(ops))),
	}
	if ops.ShowApprovals {
		blocks = append(blocks, slackgo.NewContextBlock("slack_ops_approvals", mrkdwnText(approvalsLine(ops.PendingApprovals))))
	}
	blocks = append(blocks, slackgo.NewDividerBlock())

	switch {
	case ops.RunsUnavailable:
		return append(blocks, mrkdwnSection(":warning: Run status is temporarily unavailable. Try *Refresh* in a moment."))
	case len(ops.Runs) == 0:
		return append(blocks, mrkdwnSection(":sparkles: *No runs yet.* Select *New Run* to pick a repository and describe the task."))
	}

	runs := ops.Runs
	heading := "*Runs*"
	if len(runs) > homeRunCardLimit {
		heading = fmt.Sprintf("*Runs* · showing the %d most recent of %d", homeRunCardLimit, len(runs))
		runs = runs[:homeRunCardLimit]
	}
	blocks = append(blocks, mrkdwnSection(heading))
	if ops.PullRequestsUnavailable {
		blocks = append(blocks, slackgo.NewContextBlock("slack_ops_pr_warning",
			mrkdwnText(":warning: PR and check status is temporarily unavailable.")))
	}
	for i := range runs {
		blocks = append(blocks, runCardBlocks(runs[i])...)
	}
	return blocks
}

// runCardBlocks renders one run: a section with the status line and metadata
// fields, an optional context line per pull request, and the controls that
// apply to the run's phase.
func runCardBlocks(run HomeRun) []slackgo.Block {
	fields := []*slackgo.TextBlockObject{
		mrkdwnText("*Repository*\n" + codeSpan(RepositoryLabel(run.Repository))),
		mrkdwnText("*Base branch*\n" + codeSpan(branchOrDefault(run.BaseBranch))),
	}
	if !run.StartedAt.IsZero() {
		fields = append(fields, mrkdwnText("*Started*\n"+slackDate(run.StartedAt)))
	}
	blocks := []slackgo.Block{
		slackgo.NewSectionBlock(mrkdwnText(runStatusLine(run)), fields, nil),
	}
	if len(run.PullRequests) > 0 {
		elements := make([]slackgo.MixedElement, 0, homePullRequestLimit+1)
		for i, pr := range run.PullRequests {
			if i == homePullRequestLimit {
				elements = append(elements, mrkdwnText(fmt.Sprintf("+%d more", len(run.PullRequests)-homePullRequestLimit)))
				break
			}
			elements = append(elements, mrkdwnText(pullRequestLine(pr)))
		}
		blocks = append(blocks, slackgo.NewContextBlock("ops_pr_"+run.Name, elements...))
	}
	var controls []slackgo.BlockElement
	if run.CanStop {
		stop := slackgo.NewButtonBlockElement(ActionRunStop, run.Name, plainText("Stop")).
			WithStyle(slackgo.StyleDanger).
			WithConfirm(slackgo.NewConfirmationBlockObject(
				plainText("Stop this run?"),
				mrkdwnText("The current turn of "+codeSpan(run.Name)+" is interrupted. "+
					"The conversation stays resumable from Home."),
				plainText("Stop"),
				plainText("Keep running"),
			).WithStyle(slackgo.StyleDanger))
		controls = append(controls, stop)
	}
	if run.CanResume {
		controls = append(controls, slackgo.NewButtonBlockElement(ActionRunResume, run.Name, plainText("Resume")))
	}
	if len(controls) > 0 {
		blocks = append(blocks, slackgo.NewActionBlock("ops_"+run.Name, controls...))
	}
	return append(blocks, slackgo.NewDividerBlock())
}

// runStatusLine is the card's first line: a phase glyph, the run name, and a
// human-readable phase.
func runStatusLine(run HomeRun) string {
	return PhaseEmoji(run.Phase) + " *" + EscapeMrkdwn(run.Name) + "* · " + PhaseLabel(run.Phase)
}

// pullRequestLine renders one PR as a linked label, its lifecycle, and the
// check rollup for the current head.
func pullRequestLine(pr HomePullRequest) string {
	label := PullRequestLabel(pr.URL)
	if title := strings.TrimSpace(pr.Title); title != "" {
		label += " — " + truncateRunes(EscapeMrkdwn(title), 80)
	}
	parts := []string{":arrow_heading_up: <" + pr.URL + "|" + label + ">"}
	if lifecycle := LifecycleLabel(pr.Lifecycle); lifecycle != "" {
		parts = append(parts, lifecycle)
	}
	parts = append(parts, ChecksLabel(pr.Checks))
	return strings.Join(parts, " · ")
}

// repositoryContextLine lists the configured repositories and default base.
func repositoryContextLine(ops HomeOperations) string {
	repos := make([]string, 0, len(ops.Repositories))
	for _, repo := range ops.Repositories {
		if repo = strings.TrimSpace(repo); repo != "" {
			repos = append(repos, codeSpan(RepositoryLabel(repo)))
		}
	}
	repoText := "none configured — set one on the Project"
	if len(repos) > 0 {
		repoText = strings.Join(repos, ", ")
	}
	return ":file_folder: *Repositories:* " + repoText +
		"   ·   :twisted_rightwards_arrows: *Default base:* " + codeSpan(branchOrDefault(ops.BaseBranch))
}

// approvalsLine is the owner-only backlog of held channel replies.
func approvalsLine(pending int) string {
	switch {
	case pending <= 0:
		return ":white_check_mark: No replies are waiting for your approval."
	case pending == 1:
		return ":hourglass_flowing_sand: *1 reply is waiting for your approval* — review the card in your DM with this app."
	case pending > HomeApprovalCountCap:
		return fmt.Sprintf(":hourglass_flowing_sand: *%d+ replies are waiting for your approval* — review the cards in your DM with this app.", HomeApprovalCountCap)
	default:
		return fmt.Sprintf(":hourglass_flowing_sand: *%d replies are waiting for your approval* — review the cards in your DM with this app.", pending)
	}
}

// PhaseEmoji maps an AgentRun phase to a status glyph.
func PhaseEmoji(phase string) string {
	switch phase {
	case "Running":
		return ":large_green_circle:"
	case "Paused", "Question", "Blocked", "WaitingApproval":
		return ":large_yellow_circle:"
	case "Succeeded":
		return ":white_check_mark:"
	case "Failed":
		return ":x:"
	case "Cancelled":
		return ":no_entry_sign:"
	default:
		return ":hourglass_flowing_sand:"
	}
}

// PhaseLabel maps an AgentRun phase to the wording shown to Slack users.
func PhaseLabel(phase string) string {
	switch phase {
	case "":
		return "Pending"
	case "Question":
		return "Needs your input"
	case "WaitingApproval":
		return "Waiting for approval"
	case "Admitted", "Provisioning":
		return "Starting"
	default:
		return phase
	}
}

// LifecycleLabel renders a PR lifecycle, or "" when unknown.
func LifecycleLabel(lifecycle string) string {
	switch lifecycle {
	case "open":
		return "open"
	case "draft":
		return "draft"
	case "merged":
		return ":tada: merged"
	case "closed":
		return "closed"
	default:
		return ""
	}
}

// ChecksLabel renders the current-head check rollup.
func ChecksLabel(checks string) string {
	switch checks {
	case "success":
		return ":white_check_mark: checks passed"
	case "failure":
		return ":x: checks failed"
	case "pending":
		return ":hourglass_flowing_sand: checks running"
	case "none":
		return "no checks"
	default:
		return "checks unknown"
	}
}

// RepositoryLabel shortens a repository URL to owner/repo for display. Anything
// that does not look like https://host/owner/repo is returned unchanged.
func RepositoryLabel(repo string) string {
	repo = strings.TrimSpace(repo)
	trimmed := repo
	if i := strings.Index(trimmed, "://"); i >= 0 {
		trimmed = trimmed[i+3:]
		if slash := strings.Index(trimmed, "/"); slash >= 0 {
			trimmed = trimmed[slash+1:]
		} else {
			return repo
		}
	} else if at := strings.Index(trimmed, "@"); at >= 0 && strings.Contains(trimmed, ":") {
		// git@github.com:owner/repo.git
		trimmed = trimmed[strings.Index(trimmed, ":")+1:]
	}
	trimmed = strings.TrimSuffix(strings.Trim(trimmed, "/"), ".git")
	if trimmed == "" || strings.Count(trimmed, "/") != 1 {
		return repo
	}
	return trimmed
}

// PullRequestLabel shortens a GitHub PR URL to owner/repo#number.
func PullRequestLabel(url string) string {
	url = strings.TrimSpace(url)
	marker := "/pull/"
	idx := strings.LastIndex(url, marker)
	if idx < 0 {
		return url
	}
	number := strings.Trim(url[idx+len(marker):], "/")
	repo := RepositoryLabel(url[:idx])
	if number == "" || repo == url[:idx] {
		return url
	}
	return repo + "#" + number
}

// EscapeMrkdwn escapes the three characters Slack's mrkdwn treats specially so
// free text (titles, custom copy) never turns into a link or mention.
func EscapeMrkdwn(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}

// RepositoryOptionValue is the stable, length-bounded select value for a
// repository URL (Slack caps option values at 150 characters). Submissions are
// resolved back to the live configuration by recomputing this value.
func RepositoryOptionValue(repository string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(repository)))
}

// BuildRunStartModal is the New Run form: a repository select over the
// configured repositories (owner/repo labels, URL-bound values), the base
// branch prefilled with the configured default, and the task. agentKey rides in
// private_metadata so the submission can be bound back to this agent.
func BuildRunStartModal(agentKey string, repositories []string, branch string) slackgo.ModalViewRequest {
	options := repositoryOptions(repositories)
	selection := slackgo.NewOptionsSelectBlockElement(slackgo.OptTypeStatic, plainText("Choose a repository"),
		ActionRunInput, options...)
	if len(options) > 0 {
		selection.InitialOption = options[0]
	}
	repositoryBlock := slackgo.NewInputBlock(BlockRunRepository, plainText("Repository"),
		plainText("Only repositories configured on this agent's Project are listed."), selection)

	branchInput := slackgo.NewPlainTextInputBlockElement(plainText(branchOrDefault(branch)), ActionRunInput)
	branchInput.InitialValue = strings.TrimSpace(branch)
	branchInput.MaxLength = 255
	branchBlock := slackgo.NewInputBlock(BlockRunBranch, plainText("Base branch"),
		plainText("The branch the run checks out and targets. Leave empty for the repository default."), branchInput)
	branchBlock.Optional = true

	task := slackgo.NewPlainTextInputBlockElement(
		plainText("e.g. Fix the flaky login test, add a regression test, and open a PR."), ActionRunInput)
	task.Multiline = true
	task.MaxLength = 3000
	task.FocusOnLoad = true
	taskBlock := slackgo.NewInputBlock(BlockRunTask, plainText("Task"),
		plainText("Describe the goal, any constraints, and how you want the result delivered."), task)

	return slackgo.ModalViewRequest{
		Type:            slackgo.VTModal,
		CallbackID:      CallbackRunStart,
		PrivateMetadata: agentKey,
		Title:           plainText("New Run"),
		Submit:          plainText("Start run"),
		Close:           plainText("Cancel"),
		Blocks: slackgo.Blocks{BlockSet: []slackgo.Block{
			slackgo.NewContextBlock("slack_ops_start_intro",
				mrkdwnText(":rocket: Starts a fresh run. Progress and results arrive in your DM with this app; "+
					"stop or resume it from Home.")),
			repositoryBlock,
			branchBlock,
			taskBlock,
		}},
	}
}

// repositoryOptions builds the select options: owner/repo labels when they are
// unambiguous, the (truncated) URL otherwise, each bound to the URL's hash.
func repositoryOptions(repositories []string) []*slackgo.OptionBlockObject {
	labels := make(map[string]int, len(repositories))
	for _, repo := range repositories {
		labels[RepositoryLabel(repo)]++
	}
	options := make([]*slackgo.OptionBlockObject, 0, len(repositories))
	for i, repo := range repositories {
		if i == RepositorySelectLimit {
			break
		}
		label := RepositoryLabel(repo)
		if labels[label] > 1 {
			label = repo
		}
		options = append(options, slackgo.NewOptionBlockObject(RepositoryOptionValue(repo),
			plainText(truncateRunes(label, optionTextMaxRunes)), nil))
	}
	return options
}

// BuildRunResumeModal is the Resume form for one run: a summary of what is
// being resumed and optional new instructions. runKey rides in
// private_metadata so the submission can be bound back to this run; the form
// deliberately carries no repository or branch field, because resuming never
// retargets a run.
func BuildRunResumeModal(runKey string, run HomeRun) slackgo.ModalViewRequest {
	task := slackgo.NewPlainTextInputBlockElement(
		plainText("e.g. Address the review comments and re-run the tests."), ActionRunInput)
	task.Multiline = true
	task.MaxLength = 3000
	task.FocusOnLoad = true
	input := slackgo.NewInputBlock(BlockRunTask, plainText("Instructions (optional)"),
		plainText("Leave empty to continue the previous task from where it stopped."), task)
	input.Optional = true

	summary := runStatusLine(run) + "\n" + codeSpan(RepositoryLabel(run.Repository)) +
		" on " + codeSpan(branchOrDefault(run.BaseBranch))
	return slackgo.ModalViewRequest{
		Type:            slackgo.VTModal,
		CallbackID:      CallbackRunResume,
		PrivateMetadata: runKey,
		Title:           plainText("Resume run"),
		Submit:          plainText("Resume"),
		Close:           plainText("Cancel"),
		Blocks: slackgo.Blocks{BlockSet: []slackgo.Block{
			mrkdwnSection(summary),
			slackgo.NewDividerBlock(),
			input,
		}},
	}
}

// branchOrDefault names the branch a run uses when none is configured.
func branchOrDefault(branch string) string {
	if branch = strings.TrimSpace(branch); branch != "" {
		return branch
	}
	return "repository default"
}

// slackDate renders a timestamp with Slack's date formatting so it shows in the
// viewer's timezone, with a UTC fallback for clients that cannot render it.
func slackDate(t time.Time) string {
	return "<!date^" + strconv.FormatInt(t.Unix(), 10) + "^{date_short_pretty} at {time}|" +
		t.UTC().Format("2006-01-02 15:04 UTC") + ">"
}

func codeSpan(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "'") + "`"
}

func plainText(text string) *slackgo.TextBlockObject {
	return slackgo.NewTextBlockObject(slackgo.PlainTextType, text, false, false)
}

func mrkdwnText(text string) *slackgo.TextBlockObject {
	return slackgo.NewTextBlockObject(slackgo.MarkdownType, truncateRunes(text, homeTextMaxRunes), false, false)
}

func mrkdwnSection(text string) *slackgo.SectionBlock {
	return slackgo.NewSectionBlock(mrkdwnText(text), nil, nil)
}
