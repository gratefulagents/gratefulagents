package securitytoolpacks

import "testing"

func TestNativeAdaptersRejectCanonicalScannerRecordArrays(t *testing.T) {
	adapters := DefaultAdapters()
	for _, name := range []string{"zap-json", "schemathesis-json", "restler-json", "nuclei-jsonl", "sslyze-json", "testssl-json", "openssl-json", "tshark-json", "har", "junit", "slither-json", "echidna-json", "mythril-json", "halmos-json"} {
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
			name:       "mythril-json",
			native:     `{"success":true,"error":null,"issues":[{"title":"External Call","swc-id":"107","contract":"Vault","description":"External call before state update","function":"withdraw()","severity":"High","address":12,"tx_sequence":{"steps":["withdraw"]},"sourceMap":"1:2:3","filename":"src/Vault.sol","lineno":12,"code":"callee.call()"}]}`,
			wantRuleID: "SWC-107",
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
			if len(records) != 1 || records[0].Record.RuleID != tc.wantRuleID {
				t.Fatalf("records=%+v", records)
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
	if len(records) != 1 || records[0].Record.RuleID != "ECHIDNA-PROPERTY" {
		t.Fatalf("records=%+v", records)
	}
}

func TestEVMNativeAdaptersRejectToolErrors(t *testing.T) {
	for name, native := range map[string]string{
		"slither-json": `{"success":false,"error":"compile failed","results":{"detectors":[]}}`,
		"echidna-json": `{"success":false,"error":"compile failed","tests":[],"seed":1}`,
		"mythril-json": `{"success":false,"error":"analysis failed","issues":[]}`,
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
