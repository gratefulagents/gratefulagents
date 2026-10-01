package v1alpha1

import "testing"

func TestAgentRunLimitsCostCapUSD(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		limits  *AgentRunLimits
		wantCap float64
		wantSet bool
		wantErr bool
	}{
		{name: "nil limits", limits: nil},
		{name: "empty", limits: &AgentRunLimits{}},
		{name: "whitespace", limits: &AgentRunLimits{MaxCostUsd: "  "}},
		{name: "valid", limits: &AgentRunLimits{MaxCostUsd: " 2.50 "}, wantCap: 2.5, wantSet: true},
		{name: "zero", limits: &AgentRunLimits{MaxCostUsd: "0"}, wantSet: true, wantErr: true},
		{name: "negative", limits: &AgentRunLimits{MaxCostUsd: "-1"}, wantSet: true, wantErr: true},
		{name: "garbage", limits: &AgentRunLimits{MaxCostUsd: "five"}, wantSet: true, wantErr: true},
		{name: "infinite", limits: &AgentRunLimits{MaxCostUsd: "Inf"}, wantSet: true, wantErr: true},
		{name: "nan", limits: &AgentRunLimits{MaxCostUsd: "NaN"}, wantSet: true, wantErr: true},
	}
	for _, tc := range cases {
		gotCap, gotSet, err := tc.limits.CostCapUSD()
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
		if gotCap != tc.wantCap || gotSet != tc.wantSet {
			t.Errorf("%s: CostCapUSD() = (%v, %v), want (%v, %v)", tc.name, gotCap, gotSet, tc.wantCap, tc.wantSet)
		}
	}
}
