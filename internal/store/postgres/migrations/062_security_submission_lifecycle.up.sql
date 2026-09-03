-- Split "the agent packaged a bounty bundle" (packaged) from "a human filed
-- the report with a program" (submitted). Before this migration the
-- post-script marked every stored bundle 'submitted', so precision feedback
-- counted reports nobody had filed.
ALTER TABLE security_research_submissions
    ADD COLUMN IF NOT EXISTS program TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS external_reference TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS submitted_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS packaged_at TIMESTAMPTZ;

ALTER TABLE security_research_submissions
    DROP CONSTRAINT IF EXISTS security_research_submissions_status_check,
    DROP CONSTRAINT IF EXISTS security_research_submissions_check;

-- Agent-marked rows never received an adjudicated outcome; rows that did were
-- filed by a human and keep their submitted status.
UPDATE security_research_submissions s
SET status = 'packaged', packaged_at = s.submitted_at, submitted_at = NULL
WHERE s.status = 'submitted'
  AND NOT EXISTS (SELECT 1 FROM security_research_submission_outcomes o WHERE o.submission_id = s.id);

ALTER TABLE security_research_submissions
    ADD CONSTRAINT security_research_submissions_status_check
        CHECK (status IN ('candidate', 'reserved', 'packaged', 'submitted', 'resolved')),
    ADD CONSTRAINT security_research_submissions_lifecycle_check CHECK (
        (status IN ('submitted', 'resolved') AND submitted_at IS NOT NULL)
        OR (status = 'packaged' AND packaged_at IS NOT NULL AND submitted_at IS NULL)
        OR (status IN ('candidate', 'reserved') AND submitted_at IS NULL)
    );

CREATE INDEX IF NOT EXISTS idx_security_research_submissions_finding
    ON security_research_submissions (finding_id) WHERE finding_id IS NOT NULL;
