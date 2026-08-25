package configtest

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

func TestEVMOrderbookSettlementReviewGatesHuntLanes(t *testing.T) {
	t.Parallel()

	var workflow triggersv1alpha1.SecurityWorkflow
	readBootstrapAsset(t, "securityworkflows", "evm-orderbook-settlement-review", &workflow)

	byName := make(map[string]triggersv1alpha1.SecurityScanTask, len(workflow.Spec.Tasks))
	for _, task := range workflow.Spec.Tasks {
		byName[task.Name] = task
	}

	pin := byName["pin-target-and-toolchain"]
	var pinSchema struct {
		Required   []string `json:"required"`
		Properties struct {
			Capabilities struct {
				MinItems int `json:"minItems"`
				MaxItems int `json:"maxItems"`
				Items    struct {
					Required   []string `json:"required"`
					Properties map[string]struct {
						Type string   `json:"type"`
						Enum []string `json:"enum"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"capabilities"`
			HuntLanes struct {
				Required   []string `json:"required"`
				Properties map[string]struct {
					Type string `json:"type"`
				} `json:"properties"`
			} `json:"hunt_lanes"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(pin.OutputSchema), &pinSchema); err != nil {
		t.Fatalf("decode pin output schema: %v", err)
	}
	for _, field := range []string{"capabilities", "hunt_lanes"} {
		if !slices.Contains(pinSchema.Required, field) {
			t.Errorf("pin output schema must require %q", field)
		}
	}
	capabilities := pinSchema.Properties.Capabilities
	if capabilities.MinItems != 4 || capabilities.MaxItems != 4 {
		t.Errorf("capabilities cardinality = %d..%d, want 4..4", capabilities.MinItems, capabilities.MaxItems)
	}
	for _, field := range []string{"capability", "status", "evidence"} {
		if !slices.Contains(capabilities.Items.Required, field) {
			t.Errorf("capability record must require %q", field)
		}
	}
	if got := capabilities.Items.Properties["status"].Enum; !slices.Equal(got, []string{"detected", "not_detected", "inconclusive"}) {
		t.Errorf("capability statuses = %v", got)
	}
	if capabilities.Items.Properties["evidence"].Type != "array" {
		t.Error("capability evidence must be an array")
	}
	for _, marker := range []string{"detected or inconclusive", "false only when", "Uncertainty must route to review"} {
		if !strings.Contains(pin.Objective, marker) {
			t.Errorf("pin objective is missing conservative gating rule %q", marker)
		}
	}

	lanes := map[string]string{
		"hunt-signatures-domain-and-replay":          "signatures-domain-and-replay",
		"hunt-settlement-extensions-and-callbacks":   "settlement-extensions-and-callbacks",
		"hunt-amount-fee-and-plugin-accounting":      "amount-fee-and-plugin-accounting",
		"hunt-escrow-hashlock-and-timelocks":         "escrow-hashlock-and-timelocks",
	}
	validator := byName["validate-candidates-in-harness"]
	for taskName, lane := range lanes {
		if !slices.Contains(pinSchema.Properties.HuntLanes.Required, taskName) || pinSchema.Properties.HuntLanes.Properties[taskName].Type != "boolean" {
			t.Errorf("hunt_lanes must require boolean %q", taskName)
		}
		task := byName[taskName]
		if task.When == nil {
			t.Errorf("%s has no condition", taskName)
			continue
		}
		if task.When.Task != "pin-target-and-toolchain" || task.When.Path != "hunt_lanes."+taskName || task.When.Equals != "true" {
			t.Errorf("%s condition = %#v", taskName, task.When)
		}
		if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(task.OutputSchema, task.When.OtherwiseOutput); err != nil {
			t.Errorf("%s otherwiseOutput does not satisfy outputSchema: %v", taskName, err)
		}
		if !strings.Contains(task.When.OtherwiseOutput, `"lane":"`+lane+`"`) {
			t.Errorf("%s skipped output does not identify lane %q", taskName, lane)
		}
		if !strings.Contains(validator.Objective, "{{tasks."+taskName+".output}}") {
			t.Errorf("validator does not consume %s output", taskName)
		}
	}
	if !strings.Contains(validator.Objective, "exclude skipped-lane") {
		t.Error("validator must exclude skipped lanes from candidate reconciliation")
	}
}
