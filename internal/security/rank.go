package security

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// RankWeights weighs the four scoring dimensions used by Rank.
type RankWeights struct {
	Severity       float64
	Confidence     float64
	Exploitability float64
	Exposure       float64
}

// DefaultRankWeights returns the standard scoring weights.
func DefaultRankWeights() RankWeights {
	return RankWeights{Severity: 0.5, Confidence: 0.2, Exploitability: 0.15, Exposure: 0.15}
}

// RankRules steers Rank. It is typically produced by ParseRankRules from
// operator-authored ranker text.
type RankRules struct {
	// Text is the prose left over after directive lines were parsed out; it
	// can be handed to a triage agent verbatim.
	Text              string
	Weights           RankWeights
	SeverityFloors    map[string]string
	SeverityCeilings  map[string]string
	ExcludeCategories []string
	MinSeverity       string
}

// ParseRankRules extracts directive lines from operator-authored ranker
// text. Recognized directives (one per line, case-insensitive):
//
//	severity-floor: <category>=<severity>
//	severity-ceiling: <category>=<severity>
//	exclude: <category>[,<category>...]
//	min-severity: <severity>
//	weight: <severity|confidence|exploitability|exposure>=<float>[,<name>=<float>...]
//
// Lines that are not valid directives are retained verbatim in Text.
func ParseRankRules(text string) RankRules {
	rules := RankRules{Weights: DefaultRankWeights()}
	var prose []string
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		directive, rest, ok := strings.Cut(trimmed, ":")
		if !ok {
			prose = append(prose, line)
			continue
		}
		rest = strings.TrimSpace(rest)
		switch strings.ToLower(strings.TrimSpace(directive)) {
		case "severity-floor":
			if cat, sev, ok := parseCategorySeverity(rest); ok {
				if rules.SeverityFloors == nil {
					rules.SeverityFloors = map[string]string{}
				}
				rules.SeverityFloors[cat] = sev
				continue
			}
		case "severity-ceiling":
			if cat, sev, ok := parseCategorySeverity(rest); ok {
				if rules.SeverityCeilings == nil {
					rules.SeverityCeilings = map[string]string{}
				}
				rules.SeverityCeilings[cat] = sev
				continue
			}
		case "exclude":
			cats := parseCategoryList(rest)
			if len(cats) > 0 {
				rules.ExcludeCategories = append(rules.ExcludeCategories, cats...)
				continue
			}
		case "min-severity":
			sev := normalizeSeverity(rest)
			if SeverityRank(sev) >= 0 {
				rules.MinSeverity = sev
				continue
			}
		case "weight":
			if applyWeightDirective(&rules.Weights, rest) {
				continue
			}
		}
		prose = append(prose, line)
	}
	rules.ExcludeCategories = normalizeStringSet(rules.ExcludeCategories, nil)
	rules.Text = strings.TrimSpace(strings.Join(prose, "\n"))
	return rules
}

func parseCategorySeverity(s string) (category, severity string, ok bool) {
	cat, sev, found := strings.Cut(s, "=")
	if !found {
		return "", "", false
	}
	cat = normalizeCategory(cat)
	sev = normalizeSeverity(sev)
	if !knownCategories[cat] || SeverityRank(sev) < 0 {
		return "", "", false
	}
	return cat, sev, true
}

func parseCategoryList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		cat := normalizeCategory(part)
		if knownCategories[cat] {
			out = append(out, cat)
		}
	}
	return out
}

func applyWeightDirective(w *RankWeights, s string) bool {
	applied := false
	for part := range strings.SplitSeq(s, ",") {
		name, val, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err != nil || v < 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "severity":
			w.Severity = v
		case "confidence":
			w.Confidence = v
		case "exploitability":
			w.Exploitability = v
		case "exposure":
			w.Exposure = v
		default:
			continue
		}
		applied = true
	}
	return applied
}

// RankedFinding is a Finding with its priority score and the reasons behind it.
type RankedFinding struct {
	Finding Finding
	Score   float64
	Reasons []string
}

var severityScores = map[string]float64{
	SeverityCritical: 1.0,
	SeverityHigh:     0.8,
	SeverityMedium:   0.55,
	SeverityLow:      0.3,
	SeverityInfo:     0.1,
}

var confidenceScores = map[string]float64{
	ConfidenceConfirmed: 1.0,
	ConfidenceFirm:      0.7,
	ConfidenceTentative: 0.4,
}

var exploitabilityBoosts = []struct {
	keyword string
	boost   float64
}{
	{"unauthenticated", 0.2},
	{"pre-auth", 0.2},
	{"preauth", 0.2},
	{"rce", 0.2},
	{"remote code execution", 0.2},
	{"remote", 0.15},
	{"network", 0.1},
	{"internet", 0.1},
	{"public", 0.1},
}

var exploitabilityDampeners = []struct {
	keyword string
	damp    float64
}{
	{"local", 0.1},
	{"authenticated", 0.1},
	{"physical", 0.15},
}

func exploitability(f Finding) (float64, []string) {
	text := strings.ToLower(f.AttackVector + " " + strings.Join(f.Tags, " "))
	score := 0.4
	var matched []string
	for _, b := range exploitabilityBoosts {
		if strings.Contains(text, b.keyword) {
			score += b.boost
			matched = append(matched, b.keyword)
		}
	}
	for _, d := range exploitabilityDampeners {
		// "unauthenticated" must not trigger the "authenticated" dampener.
		if d.keyword == "authenticated" && strings.Contains(text, "unauthenticated") {
			continue
		}
		if strings.Contains(text, d.keyword) {
			score -= d.damp
		}
	}
	return clamp01(score), matched
}

var highExposureHints = []string{"handler", "api", "controller", "route", "cmd", "server", "endpoint", "public", "auth", "middleware"}
var lowExposureHints = []string{"test", "fixture", "docs", "doc", "example", "examples", "mock", "vendor", "testdata"}

func exposure(f Finding) (float64, string) {
	p := strings.ToLower(f.FilePath)
	if p == "" {
		return 0.5, ""
	}
	segments := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '_' || r == '.' || r == '-' })
	for _, hint := range lowExposureHints {
		for _, seg := range segments {
			if seg == hint || strings.HasSuffix(seg, hint) {
				return 0.2, hint
			}
		}
	}
	for _, hint := range highExposureHints {
		for _, seg := range segments {
			if strings.HasPrefix(seg, hint) {
				return 0.9, hint
			}
		}
	}
	return 0.5, ""
}

func clamp01(v float64) float64 {
	return math.Min(1, math.Max(0, v))
}

// Rank applies the rules (severity floors/ceilings, category excludes,
// minimum severity) and scores each surviving finding from 0 to 100 using
// the weighted combination of severity, confidence, exploitability
// heuristics, and file-path exposure heuristics. Results are sorted by
// score descending, then severity descending, then title ascending.
func Rank(findings []Finding, rules RankRules) []RankedFinding {
	weights := rules.Weights
	if weights == (RankWeights{}) {
		weights = DefaultRankWeights()
	}
	weightSum := weights.Severity + weights.Confidence + weights.Exploitability + weights.Exposure
	if weightSum <= 0 {
		weights = DefaultRankWeights()
		weightSum = weights.Severity + weights.Confidence + weights.Exploitability + weights.Exposure
	}

	excluded := make(map[string]bool, len(rules.ExcludeCategories))
	for _, c := range rules.ExcludeCategories {
		excluded[normalizeCategory(c)] = true
	}

	ranked := make([]RankedFinding, 0, len(findings))
	for _, f := range findings {
		if excluded[f.Category] {
			continue
		}
		var reasons []string
		if floor, ok := rules.SeverityFloors[f.Category]; ok && SeverityRank(f.Severity) >= 0 && SeverityRank(f.Severity) < SeverityRank(floor) {
			reasons = append(reasons, fmt.Sprintf("severity raised from %s to %s by floor for category %s", f.Severity, floor, f.Category))
			f.Severity = floor
		}
		if ceiling, ok := rules.SeverityCeilings[f.Category]; ok && SeverityRank(f.Severity) > SeverityRank(ceiling) && SeverityRank(ceiling) >= 0 {
			reasons = append(reasons, fmt.Sprintf("severity lowered from %s to %s by ceiling for category %s", f.Severity, ceiling, f.Category))
			f.Severity = ceiling
		}
		if rules.MinSeverity != "" && !SeverityAtLeast(f.Severity, rules.MinSeverity) {
			continue
		}

		sevScore := severityScores[f.Severity]
		confScore, ok := confidenceScores[f.Confidence]
		if !ok {
			confScore = confidenceScores[ConfidenceTentative]
		}
		explScore, explHits := exploitability(f)
		expoScore, expoHint := exposure(f)

		score := 100 * (weights.Severity*sevScore + weights.Confidence*confScore +
			weights.Exploitability*explScore + weights.Exposure*expoScore) / weightSum
		score = math.Round(score*10) / 10

		reasons = append(reasons, fmt.Sprintf("severity %s", orUnknown(f.Severity)))
		reasons = append(reasons, fmt.Sprintf("confidence %s", orUnknown(f.Confidence)))
		if len(explHits) > 0 {
			reasons = append(reasons, "exploitability boosted by: "+strings.Join(explHits, ", "))
		}
		switch {
		case expoScore > 0.5:
			reasons = append(reasons, fmt.Sprintf("high exposure: path %q suggests reachable code (%s)", f.FilePath, expoHint))
		case expoScore < 0.5:
			reasons = append(reasons, fmt.Sprintf("low exposure: path %q suggests non-production code (%s)", f.FilePath, expoHint))
		}

		ranked = append(ranked, RankedFinding{Finding: f, Score: score, Reasons: reasons})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		ri, rj := SeverityRank(ranked[i].Finding.Severity), SeverityRank(ranked[j].Finding.Severity)
		if ri != rj {
			return ri > rj
		}
		return ranked[i].Finding.Title < ranked[j].Finding.Title
	})
	return ranked
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
