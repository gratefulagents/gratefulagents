-- Automated bug-squasher fix tracking. When a human moves a report to
-- in_progress, the platform launches a fix AgentRun from the namespace's
-- bug-squasher Project; fix_run_name records that run and fix_pr_url the pull
-- request it opened. The report auto-resolves when the PR merges.
ALTER TABLE agent_bug_reports
    ADD COLUMN IF NOT EXISTS fix_run_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS fix_pr_url   TEXT NOT NULL DEFAULT '';
