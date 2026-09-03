ALTER TABLE security_research_submissions
    DROP CONSTRAINT IF EXISTS security_research_submissions_status_check,
    DROP CONSTRAINT IF EXISTS security_research_submissions_lifecycle_check;

-- The pre-062 schema had no packaged state; fold packaged rows back into the
-- old agent-marked 'submitted' meaning so the original CHECK holds.
UPDATE security_research_submissions
SET status = 'submitted', submitted_at = COALESCE(submitted_at, packaged_at, now())
WHERE status = 'packaged';

ALTER TABLE security_research_submissions
    ADD CONSTRAINT security_research_submissions_status_check
        CHECK (status IN ('candidate', 'reserved', 'submitted', 'resolved')),
    ADD CONSTRAINT security_research_submissions_check CHECK (
        (status IN ('submitted', 'resolved') AND submitted_at IS NOT NULL)
        OR (status IN ('candidate', 'reserved') AND submitted_at IS NULL)
    );

DROP INDEX IF EXISTS idx_security_research_submissions_finding;

ALTER TABLE security_research_submissions
    DROP COLUMN IF EXISTS program,
    DROP COLUMN IF EXISTS external_reference,
    DROP COLUMN IF EXISTS submitted_by,
    DROP COLUMN IF EXISTS packaged_at;
