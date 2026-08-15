ALTER TABLE agent_bug_reports
    DROP COLUMN IF EXISTS fix_run_name,
    DROP COLUMN IF EXISTS fix_pr_url;
