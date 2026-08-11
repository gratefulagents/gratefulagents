package postgres

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

const (
	// securityPostureActivityDefault is how many recent completed runs feed
	// each configuration's activity series when the caller does not name a
	// limit.
	securityPostureActivityDefault = int32(12)
	// securityPostureActivityMax caps the per-configuration activity series.
	securityPostureActivityMax = int32(50)
)

// ListSecurityConfigPostures aggregates per-scan-configuration posture: the
// same grouped finding counts as SummarizeSecurityFindings but keyed by
// scan_name, the latest persisted run per configuration, and per-run
// observation counts over each configuration's newest completed runs.
func (s *Store) ListSecurityConfigPostures(ctx context.Context, namespace string, activityLimit int32, excludedScanNames []string) ([]store.SecurityConfigPosture, error) {
	if err := requireSecurityNamespace(namespace); err != nil {
		return nil, err
	}
	if activityLimit <= 0 {
		activityLimit = securityPostureActivityDefault
	}
	if activityLimit > securityPostureActivityMax {
		activityLimit = securityPostureActivityMax
	}

	exclude := func(column string, args *[]any) string {
		if len(excludedScanNames) == 0 {
			return ""
		}
		*args = append(*args, excludedScanNames)
		return fmt.Sprintf(" AND NOT (%s = ANY($%d))", column, len(*args))
	}

	byName := map[string]*store.SecurityConfigPosture{}
	posture := func(scanName string) *store.SecurityConfigPosture {
		p := byName[scanName]
		if p == nil {
			p = &store.SecurityConfigPosture{ScanName: scanName, Counts: newSecurityFindingSummary()}
			byName[scanName] = p
		}
		return p
	}

	// Grouped finding counts, one summary per scan_name.
	countArgs := []any{namespace}
	countWhere := "WHERE namespace = $1 AND duplicate_of IS NULL" + exclude("scan_name", &countArgs)
	rows, err := s.pool.Query(ctx, `
		SELECT scan_name, severity, status, COALESCE(baseline_state, ''), source_kind,
			cardinality(correlated_fingerprints) > 0, suppressed_by IS NOT NULL, COUNT(*)
		FROM security_findings
		`+countWhere+`
		GROUP BY scan_name, severity, status, baseline_state, source_kind,
			cardinality(correlated_fingerprints) > 0, suppressed_by IS NOT NULL`, countArgs...)
	if err != nil {
		return nil, fmt.Errorf("summarizing security findings per configuration: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var scanName, severity, status, baseline, sourceKind string
		var correlated, suppressed bool
		var count int64
		if err := rows.Scan(&scanName, &severity, &status, &baseline, &sourceKind, &correlated, &suppressed, &count); err != nil {
			return nil, fmt.Errorf("scanning security posture summary: %w", err)
		}
		addSecurityFindingSummaryCount(posture(scanName).Counts, severity, status, baseline, sourceKind, correlated, suppressed, false, int32(count))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("summarizing security findings per configuration: %w", err)
	}
	rows.Close()

	// Latest persisted run per configuration, so configurations whose runs
	// have not reported findings yet still get a posture row.
	lastArgs := []any{namespace}
	lastWhere := "WHERE namespace = $1" + exclude("scan_name", &lastArgs)
	rows, err = s.pool.Query(ctx, `
		SELECT DISTINCT ON (scan_name) scan_name, run_name, repository, status, started_at, completed_at
		FROM security_scans
		`+lastWhere+`
		ORDER BY scan_name, created_at DESC, run_name DESC`, lastArgs...)
	if err != nil {
		return nil, fmt.Errorf("listing latest security scan runs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var scanName, runName, repository, status string
		var startedAt, completedAt *time.Time
		if err := rows.Scan(&scanName, &runName, &repository, &status, &startedAt, &completedAt); err != nil {
			return nil, fmt.Errorf("scanning latest security scan run: %w", err)
		}
		p := posture(scanName)
		p.LastRunName = runName
		p.Repository = repository
		p.LastRunStatus = status
		p.LastStartedAt = startedAt
		p.LastCompletedAt = completedAt
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing latest security scan runs: %w", err)
	}
	rows.Close()

	// Per-run observed finding counts over each configuration's newest
	// completed runs. The LEFT JOIN keeps clean runs (zero observations) in
	// the series so a drop to zero findings is visible.
	actArgs := []any{namespace}
	actWhere := "WHERE namespace = $1 AND completed_at IS NOT NULL" + exclude("scan_name", &actArgs)
	actArgs = append(actArgs, activityLimit)
	rows, err = s.pool.Query(ctx, `
		WITH recent AS (
			SELECT scan_name, run_name, completed_at,
				row_number() OVER (PARTITION BY scan_name ORDER BY completed_at DESC, created_at DESC, run_name DESC) AS rn
			FROM security_scans
			`+actWhere+`
		)
		SELECT r.scan_name, r.run_name, r.completed_at, COALESCE(o.severity, ''), COUNT(o.id)
		FROM recent r
		LEFT JOIN security_finding_observations o
			ON o.namespace = $1 AND o.scan_name = r.scan_name AND o.run_name = r.run_name
		WHERE r.rn <= $`+fmt.Sprint(len(actArgs))+`
		GROUP BY r.scan_name, r.run_name, r.completed_at, o.severity
		ORDER BY r.scan_name, r.completed_at, r.run_name`, actArgs...)
	if err != nil {
		return nil, fmt.Errorf("aggregating security run activity: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var scanName, runName, severity string
		var completedAt time.Time
		var count int64
		if err := rows.Scan(&scanName, &runName, &completedAt, &severity, &count); err != nil {
			return nil, fmt.Errorf("scanning security run activity: %w", err)
		}
		p := posture(scanName)
		var point *store.SecurityRunActivityPoint
		if n := len(p.Activity); n > 0 && p.Activity[n-1].RunName == runName {
			point = &p.Activity[n-1]
		} else {
			p.Activity = append(p.Activity, store.SecurityRunActivityPoint{
				RunName:        runName,
				CompletedAt:    completedAt,
				SeverityCounts: map[string]int32{},
			})
			point = &p.Activity[len(p.Activity)-1]
		}
		if severity != "" {
			point.SeverityCounts[severity] += int32(count)
			point.Total += int32(count)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("aggregating security run activity: %w", err)
	}

	out := make([]store.SecurityConfigPosture, 0, len(byName))
	for _, p := range byName {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ScanName < out[j].ScanName })
	return out, nil
}
