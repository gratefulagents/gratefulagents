package securitytoolpacks

import (
	"fmt"
	"testing"
)

func TestGoFuzzAdapterPreservesFailureEvidence(t *testing.T) {
	native := []byte("{\"Action\":\"output\",\"Package\":\"example/parser\",\"Test\":\"FuzzDecode\",\"Output\":\"panic: bad frame\\n\"}\n{\"Action\":\"fail\",\"Package\":\"example/parser\",\"Test\":\"FuzzDecode\"}\n")
	records, err := DefaultAdapters()["go-test-json"].Normalize(Tool{Name: "go-fuzz-tests", Version: "go1.26"}, Target{Locator: "fixture"}, native, NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Record.RuleID != "go-fuzz-failure" || records[0].Record.Symbol != "FuzzDecode" || records[0].Record.RawEvidence != "panic: bad frame" {
		t.Fatalf("unexpected records: %+v", records)
	}
}

func TestNativeAdaptersRejectCanonicalScannerRecordArrays(t *testing.T) {
	adapters := DefaultAdapters()
	for _, name := range []string{"zap-json", "schemathesis-json", "restler-json", "nuclei-jsonl", "sslyze-json", "testssl-json", "openssl-json", "tshark-json", "har", "junit", "slither-json", "echidna-json", "halmos-json"} {
		t.Run(name, func(t *testing.T) {
			adapter := adapters[name]
			if adapter == nil {
				t.Fatalf("adapter %q is not registered", name)
			}
			if _, err := adapter.Normalize(Tool{Name: name, Version: "fixture"}, Target{Locator: "fixture"}, []byte(`[{"tool":"fake","rule_id":"R","message":"not native","severity":"high","file_path":"x"}]`), NewRedactor()); err == nil {
				t.Fatalf("%s accepted a canonical ScannerRecord array as native output", name)
			}
		})
	}
}

func TestEVMNativeAdapters(t *testing.T) {
	tests := []struct {
		name       string
		native     string
		wantRuleID string
	}{
		{
			name:       "slither-json",
			native:     `{"success":true,"error":null,"results":{"detectors":[{"check":"reentrancy-eth","impact":"High","confidence":"Medium","description":"State write follows external call","id":"fixture-id","elements":[{"type":"function","name":"withdraw","source_mapping":{"filename_relative":"src/Vault.sol","lines":[12,18]}}]}]}}`,
			wantRuleID: "reentrancy-eth:fixture-id",
		},
		{
			name:       "echidna-json",
			native:     `{"success":true,"error":null,"seed":42,"tests":[{"contract":"Vault","name":"echidna_solvency","status":"solved","error":null,"events":[],"type":"property","transactions":[{"contract":"Vault","function":"withdraw(uint256)","arguments":["1"],"gas":"1","gasprice":"0","value":"0"}]}],"coverage":{}}`,
			wantRuleID: "ECHIDNA-PROPERTY",
		},

		{
			name:       "halmos-json",
			native:     `{"exitcode":1,"test_results":{"test/Vault.t.sol:VaultTest":[{"name":"check_solvency(uint256)","exitcode":1,"num_models":1}]}}`,
			wantRuleID: "HALMOS-COUNTEREXAMPLE",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			adapter := DefaultAdapters()[tc.name]
			records, err := adapter.Normalize(Tool{Name: tc.name, Version: "fixture"}, Target{Locator: "fixture"}, []byte(tc.native), NewRedactor())
			if err != nil {
				t.Fatal(err)
			}
			var gotRule bool
			for _, record := range records {
				if record.Record.RuleID == tc.wantRuleID {
					gotRule = true
				}
			}
			if !gotRule {
				t.Fatalf("records=%+v, want rule %s", records, tc.wantRuleID)
			}
			if tc.name == "echidna-json" {
				var gotCoverage bool
				for _, record := range records {
					gotCoverage = gotCoverage || record.Examined && record.Asset == "echidna-property:Vault.echidna_solvency"
				}
				if !gotCoverage {
					t.Fatalf("echidna records lack property coverage: %+v", records)
				}
			}
		})
	}
}

func TestEchidnaAdapterAcceptsLegacyTestTypeKey(t *testing.T) {
	native := []byte(`{"success":true,"error":null,"seed":42,"tests":[{"name":"solvency","status":"solved","testType":"property","transactions":[]}]}`)
	records, err := DefaultAdapters()["echidna-json"].Normalize(
		Tool{Name: "echidna", Version: "2.3.0"}, Target{Locator: "fixture"}, native, NewRedactor(),
	)
	if err != nil {
		t.Fatal(err)
	}
	var finding, coverage bool
	for _, record := range records {
		finding = finding || record.Record.RuleID == "ECHIDNA-PROPERTY"
		coverage = coverage || record.Examined && record.Asset == "echidna-property:solvency"
	}
	if !finding || !coverage {
		t.Fatalf("records=%+v", records)
	}
}

func TestEchidnaAdapterMarksErroredPropertiesUncovered(t *testing.T) {
	native := []byte(`{"success":true,"error":null,"seed":42,"tests":[{"contract":"Vault","name":"echidna_solvency","status":"error","type":"property","error":"reproducer failed","transactions":[]}]}`)
	records, err := DefaultAdapters()["echidna-json"].Normalize(
		Tool{Name: "echidna", Version: "2.3.0"}, Target{Locator: "fixture"}, native, NewRedactor(),
	)
	if err == nil {
		t.Fatal("expected incomplete coverage error")
	}
	if len(records) != 1 || !records[0].Uncovered || records[0].Asset != "echidna-property:Vault.echidna_solvency" {
		t.Fatalf("records=%+v", records)
	}
}

func TestHalmosIncompleteStatesAreNotCounterexamples(t *testing.T) {
	for _, exitCode := range []int{2, 3, 4, 5} {
		native := fmt.Appendf(nil, `{"exitcode":%d,"test_results":{"test/Vault.t.sol:VaultTest":[{"name":"check_solvency(uint256)","exitcode":%d,"num_models":0}]}}`, exitCode, exitCode)
		if _, err := DefaultAdapters()["halmos-json"].Normalize(Tool{Name: "halmos"}, Target{}, native, NewRedactor()); err == nil {
			t.Fatalf("Halmos exit code %d was accepted as a counterexample", exitCode)
		}
	}
}

func TestHalmosPreservesCounterexamplesFromMixedIncompleteRun(t *testing.T) {
	native := []byte(`{"exitcode":2,"test_results":{"test/Vault.t.sol:VaultTest":[{"name":"check_counterexample(uint256)","exitcode":1,"num_models":1},{"name":"check_timeout(uint256)","exitcode":2,"num_models":0}]}}`)
	records, err := DefaultAdapters()["halmos-json"].Normalize(Tool{Name: "halmos"}, Target{}, native, NewRedactor())
	if err == nil {
		t.Fatal("mixed incomplete run must return a normalization error")
	}
	if len(records) != 1 || records[0].Record.RuleID != "HALMOS-COUNTEREXAMPLE" {
		t.Fatalf("confirmed counterexample was lost: %+v", records)
	}
}

func TestEVMNativeAdaptersRejectToolErrors(t *testing.T) {
	for name, native := range map[string]string{
		"slither-json": `{"success":false,"error":"compile failed","results":{"detectors":[]}}`,
		"echidna-json": `{"success":false,"error":"compile failed","tests":[],"seed":1}`,
		"halmos-json":  `{"exitcode":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DefaultAdapters()[name].Normalize(Tool{Name: name}, Target{}, []byte(native), NewRedactor()); err == nil {
				t.Fatal("expected native tool error to fail normalization")
			}
		})
	}
}

func TestSkippedCryptoVectorBecomesCoverageGap(t *testing.T) {
	adapter := cryptoAdapter{}
	records, err := adapter.Normalize(Tool{Name: "wycheproof", Version: "fixture"}, Target{}, []byte(`{"vectors":[{"id":"skipped-vector","suite":"fixture","skipped":true}]}`), NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || !records[0].Skipped || records[0].Asset != "skipped-vector" {
		t.Fatalf("records=%+v", records)
	}
}
