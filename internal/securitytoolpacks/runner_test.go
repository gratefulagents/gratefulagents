/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package securitytoolpacks

import (
	"fmt"
	"strings"
	"testing"
)

func TestCoverageSummaryStaysWithinTheStatusBound(t *testing.T) {
	t.Parallel()
	// A suite with hundreds of assets would otherwise produce a coverage
	// string the API server rejects, and a rejected status update loses the
	// whole run.
	many := make([]string, 400)
	for i := range many {
		many[i] = fmt.Sprintf("test/contracts/Vault%03d.t.sol:testWithdrawRevertsForNonOwner", i)
	}
	summary := summarizeCoverage(many)
	if len(summary) > maxCoverageSummaryBytes+64 {
		t.Fatalf("coverage summary is %d bytes, want it bounded", len(summary))
	}
	if !strings.HasPrefix(summary, "400 examined") {
		t.Fatalf("summary must state the true total, got %q", summary)
	}
	if summarizeCoverage(nil) != "" {
		t.Fatal("no coverage must render as no summary")
	}
	if got := summarizeCoverage([]string{"a", "b"}); got != "2 examined: a, b" {
		t.Fatalf("small coverage = %q", got)
	}
}
