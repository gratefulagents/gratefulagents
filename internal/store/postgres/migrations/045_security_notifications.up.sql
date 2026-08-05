-- Persisted notification dedupe markers: one row per
-- (namespace, scan, rule/channel, finding fingerprint) that has been
-- notified. Claim-before-send with release-on-failure guarantees a finding
-- never notifies twice for the same rule/channel.
CREATE TABLE IF NOT EXISTS security_notification_markers (
    namespace   TEXT NOT NULL,
    scan_name   TEXT NOT NULL,
    rule_key    TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (namespace, scan_name, rule_key, fingerprint)
);
