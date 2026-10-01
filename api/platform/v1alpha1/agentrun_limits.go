package v1alpha1

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// CostCapUSD parses spec.limits.maxCostUsd. set reports whether a cap was
// configured at all; a configured value that is not a finite positive decimal
// returns set=true with an error so callers can treat it as a hard stop
// rather than silently running without a ceiling.
func (l *AgentRunLimits) CostCapUSD() (capUSD float64, set bool, err error) {
	if l == nil {
		return 0, false, nil
	}
	raw := strings.TrimSpace(l.MaxCostUsd)
	if raw == "" {
		return 0, false, nil
	}
	capUSD, err = strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(capUSD) || math.IsInf(capUSD, 0) || capUSD <= 0 {
		return 0, true, fmt.Errorf("maxCostUsd %q must be a finite positive decimal", raw)
	}
	return capUSD, true, nil
}
