package security

import (
	"fmt"
	"sort"
	"strings"
)

// Cluster groups a canonical Finding with the duplicates merged into it.
type Cluster struct {
	Canonical  Finding
	Duplicates []Finding
	Reason     string
}

const defaultDedupeThreshold = 0.82

// Similarity scores how alike two findings are, from 0 (unrelated) to 1
// (identical). It is a token-set Jaccard over title+description, boosted
// when the findings are in the same file with overlapping line ranges and
// when they share a CWE.
func Similarity(a, b Finding) float64 {
	sa := sortedTokenSet(a.Title + " " + a.Description)
	sb := sortedTokenSet(b.Title + " " + b.Description)
	score := jaccard(sa, sb)

	if a.FilePath != "" && normalizePath(a.FilePath, a.Repository) == normalizePath(b.FilePath, b.Repository) &&
		linesOverlap(a.StartLine, a.EndLine, b.StartLine, b.EndLine) {
		score += 0.15
	}
	if sharesCWE(a.CWE, b.CWE) {
		score += 0.10
	}
	if score > 1 {
		score = 1
	}
	return score
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	set := make(map[string]bool, len(a))
	for _, t := range a {
		set[t] = true
	}
	inter := 0
	for _, t := range b {
		if set[t] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func linesOverlap(aStart, aEnd, bStart, bEnd int) bool {
	if aStart <= 0 || bStart <= 0 {
		return false
	}
	if aEnd < aStart {
		aEnd = aStart
	}
	if bEnd < bStart {
		bEnd = bStart
	}
	return aStart <= bEnd && bStart <= aEnd
}

func sharesCWE(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, c := range a {
		set[normalizeCWE(c)] = true
	}
	for _, c := range b {
		if set[normalizeCWE(c)] {
			return true
		}
	}
	return false
}

func confidenceRank(c string) int {
	switch c {
	case ConfidenceConfirmed:
		return 2
	case ConfidenceFirm:
		return 1
	case ConfidenceTentative:
		return 0
	}
	return -1
}

// canonicalBefore reports whether a should be preferred over b as the
// canonical finding of a cluster.
func canonicalBefore(a, b Finding) bool {
	if ra, rb := SeverityRank(a.Severity), SeverityRank(b.Severity); ra != rb {
		return ra > rb
	}
	if ra, rb := confidenceRank(a.Confidence), confidenceRank(b.Confidence); ra != rb {
		return ra > rb
	}
	if len(a.Evidence) != len(b.Evidence) {
		return len(a.Evidence) > len(b.Evidence)
	}
	if len(a.Description) != len(b.Description) {
		return len(a.Description) > len(b.Description)
	}
	if a.Title != b.Title {
		return a.Title < b.Title
	}
	return a.Fingerprint < b.Fingerprint
}

// Dedupe merges duplicate findings into Clusters. Findings with the same
// Fingerprint always merge; otherwise findings merge when Similarity meets
// threshold (<= 0 means the default of 0.82). The canonical finding of each
// cluster is the one with the highest severity, then highest confidence,
// then most evidence, then longest description. Clusters are returned in a
// deterministic order: severity descending, then title ascending.
func Dedupe(findings []Finding, threshold float64) []Cluster {
	if threshold <= 0 {
		threshold = defaultDedupeThreshold
	}
	work := make([]Finding, len(findings))
	copy(work, findings)
	for i := range work {
		if work[i].Fingerprint == "" {
			work[i].Fingerprint = Fingerprint(work[i])
		}
	}
	sort.SliceStable(work, func(i, j int) bool { return canonicalBefore(work[i], work[j]) })

	type merge struct {
		fingerprint int
		similar     int
	}
	var clusters []Cluster
	merges := make(map[int]*merge)
	for _, f := range work {
		matched := -1
		byFingerprint := false
		for i := range clusters {
			if clusters[i].Canonical.Fingerprint == f.Fingerprint {
				matched, byFingerprint = i, true
				break
			}
		}
		if matched < 0 {
			for i := range clusters {
				// Similarity never merges across source kinds: an agent
				// finding and a scanner finding describing the same issue
				// are correlated (Correlate), keeping both provenances,
				// rather than silently collapsed into one record.
				if clusters[i].Canonical.IsScannerFinding() != f.IsScannerFinding() {
					continue
				}
				if Similarity(clusters[i].Canonical, f) >= threshold {
					matched = i
					break
				}
			}
		}
		if matched < 0 {
			clusters = append(clusters, Cluster{Canonical: f})
			continue
		}
		clusters[matched].Duplicates = append(clusters[matched].Duplicates, f)
		m := merges[matched]
		if m == nil {
			m = &merge{}
			merges[matched] = m
		}
		if byFingerprint {
			m.fingerprint++
		} else {
			m.similar++
		}
	}

	for i := range clusters {
		m := merges[i]
		if m == nil {
			clusters[i].Reason = "no duplicates"
			continue
		}
		var parts []string
		if m.fingerprint > 0 {
			parts = append(parts, fmt.Sprintf("%d by exact fingerprint match", m.fingerprint))
		}
		if m.similar > 0 {
			parts = append(parts, fmt.Sprintf("%d by similarity >= %.2f", m.similar, threshold))
		}
		clusters[i].Reason = fmt.Sprintf("merged %d duplicate(s): %s", len(clusters[i].Duplicates), strings.Join(parts, ", "))
	}

	sort.SliceStable(clusters, func(i, j int) bool {
		a, b := clusters[i].Canonical, clusters[j].Canonical
		if ra, rb := SeverityRank(a.Severity), SeverityRank(b.Severity); ra != rb {
			return ra > rb
		}
		return a.Title < b.Title
	})
	return clusters
}
