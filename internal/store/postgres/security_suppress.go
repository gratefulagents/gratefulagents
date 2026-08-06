package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

// securityGlobToLike converts a suppression pathGlob ('*' matches any run of
// characters, '?' matches one) into a Postgres LIKE pattern, escaping LIKE
// metacharacters in the literal parts.
func securityGlobToLike(glob string) string {
	var b strings.Builder
	for _, r := range glob {
		switch r {
		case '*':
			b.WriteByte('%')
		case '?':
			b.WriteByte('_')
		case '%', '_', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// securitySuppressionMatchSQL builds the parameterized WHERE fragment for one
// rule's matcher, appending to args. All set matcher fields must match.
func securitySuppressionMatchSQL(m store.SecuritySuppressionMatcher, args *[]any) string {
	var conds []string
	add := func(format string, val any) {
		*args = append(*args, val)
		conds = append(conds, fmt.Sprintf(format, len(*args)))
	}
	if m.Category != "" {
		add("category = $%d", m.Category)
	}
	if m.CWE != "" {
		add("$%d = ANY(cwe)", m.CWE)
	}
	if m.PathGlob != "" {
		add("file_path LIKE $%d", securityGlobToLike(m.PathGlob))
	}
	if m.Fingerprint != "" {
		add("fingerprint = $%d", m.Fingerprint)
	}
	return strings.Join(conds, " AND ")
}

// ApplySecuritySuppressions marks the scan's findings matched by any rule as
// suppressed. Findings are never deleted: suppression only sets the
// suppressed_* columns and appends a "suppressed" audit event, so triage
// history and the row itself are preserved. A finding already suppressed by
// another rule is left alone; one already suppressed by the SAME rule id has
// its reason/owner/expiry refreshed without a new event, so the sweep is
// idempotent and converges after rule edits. Returns the number of findings
// newly suppressed.
func (s *Store) ApplySecuritySuppressions(ctx context.Context, namespace, scanName string, rules []store.SecuritySuppressionRule) (int32, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return 0, err
	}
	if scanName == "" {
		return 0, errors.New("security suppression requires a scan name")
	}
	if len(rules) == 0 {
		return 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning suppression sweep: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var suppressed int32
	for _, rule := range rules {
		if rule.ID == "" || rule.Matcher.IsZero() {
			return 0, fmt.Errorf("suppression rule %q needs an id and at least one matcher field", rule.ID)
		}
		args := []any{namespace, scanName, rule.ID, rule.Reason, rule.Owner, rule.ExpiresAt}
		match := securitySuppressionMatchSQL(rule.Matcher, &args)

		// Refresh metadata for findings already suppressed by this rule so
		// rule edits (e.g. an extended expiry) converge without new events.
		if _, err := tx.Exec(ctx, `
			UPDATE security_findings
			SET suppressed_reason = $4, suppressed_owner = $5, suppression_expires_at = $6
			WHERE namespace = $1 AND scan_name = $2 AND suppressed_by = $3
			  AND (suppressed_reason IS DISTINCT FROM $4
			    OR suppressed_owner IS DISTINCT FROM $5
			    OR suppression_expires_at IS DISTINCT FROM $6)
			  AND `+match, args...); err != nil {
			return 0, fmt.Errorf("refreshing suppressions for rule %q: %w", rule.ID, err)
		}

		rows, err := tx.Query(ctx, `
			UPDATE security_findings
			SET suppressed_by = $3, suppressed_reason = $4, suppressed_owner = $5,
			    suppression_expires_at = $6, suppressed_at = now()
			WHERE namespace = $1 AND scan_name = $2 AND suppressed_by IS NULL
			  AND `+match+`
			RETURNING id`, args...)
		if err != nil {
			return 0, fmt.Errorf("applying suppression rule %q: %w", rule.ID, err)
		}
		var ids []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return 0, fmt.Errorf("scanning suppressed finding: %w", err)
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, fmt.Errorf("applying suppression rule %q: %w", rule.ID, err)
		}
		if len(ids) == 0 {
			continue
		}
		detailMap := map[string]string{"rule": rule.ID, "owner": rule.Owner, "reason": rule.Reason}
		if rule.ExpiresAt != nil {
			detailMap["expires_at"] = rule.ExpiresAt.UTC().Format(time.RFC3339)
		}
		detail, err := json.Marshal(detailMap)
		if err != nil {
			return 0, fmt.Errorf("encoding suppression detail: %w", err)
		}
		for _, id := range ids {
			if _, err := tx.Exec(ctx, `
				INSERT INTO security_finding_events (finding_id, event_type, actor, detail)
				VALUES ($1, 'suppressed', 'system', $2)`, id, detail); err != nil {
				return 0, fmt.Errorf("recording suppression event: %w", err)
			}
		}
		suppressed += int32(len(ids)) //nolint:gosec // finding counts stay far below int32 bounds
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing suppression sweep: %w", err)
	}
	return suppressed, nil
}

// RevokeSecuritySuppressions clears every suppression on the scan's findings
// whose governing rule has been revoked: the finding's suppressed_by rule id
// is not in activeRules (the rule was deleted or the pack swapped/removed —
// pass no rules to revoke everything the scan's pack once suppressed), or
// the finding no longer matches its rule's current matcher. Finding rows and
// their audit history are preserved; each revocation appends a
// "suppression_revoked" event recording the previous rule, owner, and
// reason, distinct from the "suppression_expired" event of a natural expiry.
// Idempotent and scan-scoped; returns the number of suppressions revoked.
func (s *Store) RevokeSecuritySuppressions(ctx context.Context, namespace, scanName string, activeRules []store.SecuritySuppressionRule) (int32, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return 0, err
	}
	if scanName == "" {
		return 0, errors.New("security suppression revocation requires a scan name")
	}
	activeIDs := make([]string, 0, len(activeRules))
	for _, rule := range activeRules {
		if rule.ID == "" || rule.Matcher.IsZero() {
			return 0, fmt.Errorf("suppression rule %q needs an id and at least one matcher field", rule.ID)
		}
		activeIDs = append(activeIDs, rule.ID)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning suppression-revocation sweep: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	type revoked struct {
		id                  uuid.UUID
		rule, owner, reason string
	}
	var revocations []revoked
	collect := func(cond string, args []any) error {
		rows, err := tx.Query(ctx, `
			SELECT id, suppressed_by, COALESCE(suppressed_owner, ''), COALESCE(suppressed_reason, '')
			FROM security_findings
			WHERE namespace = $1 AND scan_name = $2 AND suppressed_by IS NOT NULL
			  AND `+cond, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r revoked
			if err := rows.Scan(&r.id, &r.rule, &r.owner, &r.reason); err != nil {
				return err
			}
			revocations = append(revocations, r)
		}
		return rows.Err()
	}

	// Suppressions whose rule id is no longer in the active set: the rule
	// was deleted, or the whole pack was swapped or removed.
	if err := collect("suppressed_by <> ALL($3::text[])", []any{namespace, scanName, activeIDs}); err != nil {
		return 0, fmt.Errorf("finding revoked-rule suppressions: %w", err)
	}
	// Suppressions whose rule still exists but whose current matcher no
	// longer selects the finding. A NULL match result (e.g. a CWE matcher
	// against a finding without CWEs) counts as non-matching.
	for _, rule := range activeRules {
		args := []any{namespace, scanName, rule.ID}
		match := securitySuppressionMatchSQL(rule.Matcher, &args)
		if err := collect("suppressed_by = $3 AND NOT COALESCE(("+match+"), FALSE)", args); err != nil {
			return 0, fmt.Errorf("finding stale suppressions for rule %q: %w", rule.ID, err)
		}
	}

	for _, r := range revocations {
		if _, err := tx.Exec(ctx, `
			UPDATE security_findings
			SET suppressed_by = NULL, suppressed_reason = NULL, suppressed_owner = NULL,
			    suppression_expires_at = NULL, suppressed_at = NULL
			WHERE id = $1`, r.id); err != nil {
			return 0, fmt.Errorf("revoking suppression: %w", err)
		}
		detail, err := json.Marshal(map[string]string{"rule": r.rule, "owner": r.owner, "reason": r.reason})
		if err != nil {
			return 0, fmt.Errorf("encoding suppression-revocation detail: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO security_finding_events (finding_id, event_type, actor, detail)
			VALUES ($1, 'suppression_revoked', 'system', $2)`, r.id, detail); err != nil {
			return 0, fmt.Errorf("recording suppression-revocation event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing suppression-revocation sweep: %w", err)
	}
	return int32(len(revocations)), nil //nolint:gosec // finding counts stay far below int32 bounds
}

// ExpireSecuritySuppressions clears every suppression in the namespace whose
// expiry has passed, so the sweep is cheap and idempotent no matter how often
// it runs. The finding rows and their audit history are preserved; each
// expiry appends a "suppression_expired" event.
func (s *Store) ExpireSecuritySuppressions(ctx context.Context, namespace string) (int32, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning suppression-expiry sweep: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		UPDATE security_findings f
		SET suppressed_by = NULL, suppressed_reason = NULL, suppressed_owner = NULL,
		    suppression_expires_at = NULL, suppressed_at = NULL
		FROM security_findings prev
		WHERE f.namespace = $1
		  AND f.suppressed_by IS NOT NULL
		  AND f.suppression_expires_at IS NOT NULL
		  AND f.suppression_expires_at <= now()
		  AND prev.id = f.id
		RETURNING f.id, prev.suppressed_by`, namespace)
	if err != nil {
		return 0, fmt.Errorf("expiring suppressions: %w", err)
	}
	type expired struct {
		id   uuid.UUID
		rule string
	}
	var expiredRows []expired
	for rows.Next() {
		var e expired
		if err := rows.Scan(&e.id, &e.rule); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scanning expired suppression: %w", err)
		}
		expiredRows = append(expiredRows, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("expiring suppressions: %w", err)
	}
	if len(expiredRows) == 0 {
		return 0, nil
	}
	for _, e := range expiredRows {
		detail, err := json.Marshal(map[string]string{"rule": e.rule})
		if err != nil {
			return 0, fmt.Errorf("encoding suppression-expiry detail: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO security_finding_events (finding_id, event_type, actor, detail)
			VALUES ($1, 'suppression_expired', 'system', $2)`, e.id, detail); err != nil {
			return 0, fmt.Errorf("recording suppression-expiry event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing suppression-expiry sweep: %w", err)
	}
	return int32(len(expiredRows)), nil //nolint:gosec // finding counts stay far below int32 bounds
}
