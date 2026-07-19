package triggers

// Project is a passive trigger CRD (no reconciler) but the scheme registers
// it so the informer cache watches it.  The controller-manager service account
// needs list/watch (cache), get (dashboard reads), update/patch (status writes
// from dashboard helpers), and create/delete (the dashboard auto-provisions a
// personal default chat Project per user).
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=projects,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=projects/status,verbs=get;update;patch

// The dashboard (served from the controller-manager binary) manages Cron,
// GitHubRepository, and LinearProject triggers through its write RPCs:
// create/update/delete for crons and GitHub repositories (onboarding,
// settings, rollback of partially-created triggers) and update for Linear
// projects (run-defaults editor). The reconcilers' own markers only cover
// get/list/watch/update/patch, so grant the write verbs here.
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=crons;githubrepositories;linearprojects,verbs=get;list;watch;create;update;patch;delete

// The dashboard also manages Connection resources (shared GitHub/Slack/Linear
// credential references used by project triggers) through its
// Create/Update/DeleteConnection RPCs. The Project reconciler's own marker
// only covers get/list/watch, so grant the write verbs here.
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=connections,verbs=get;list;watch;create;update;patch;delete
