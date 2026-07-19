package mode

import "fmt"

// Gate result/denial vocabulary the operator stores on CRD status and returns
// through dashboard RPCs. (Owned here since SDK v0.0.36 removed workflow
// gates; the operator's evaluator and authz produce these natively.)

// Stable denial reason codes.
const (
	DenyGateFailed       = "GATE_FAILED"
	DenyEdgeNotFound     = "EDGE_NOT_FOUND"
	DenyRBACDenied       = "RBAC_DENIED"
	DenyTemplateNotFound = "TEMPLATE_NOT_FOUND"
)

// GateResult is the outcome of evaluating a single gate.
type GateResult struct {
	Gate     string
	Passed   bool
	Reason   string
	DenyCode string
}

// FormatDenialReason renders a human-readable denial string.
func FormatDenialReason(result *GateResult) string {
	if result == nil {
		return ""
	}
	if result.DenyCode != "" {
		return fmt.Sprintf("[%s] %s: %s", result.DenyCode, result.Gate, result.Reason)
	}
	return fmt.Sprintf("%s: %s", result.Gate, result.Reason)
}
