package securitytoolpacks

import "testing"

func TestNativeAdaptersRejectCanonicalScannerRecordArrays(t *testing.T) {
	adapters := DefaultAdapters()
	for _, name := range []string{"zap-json", "schemathesis-json", "restler-json", "nuclei-jsonl", "sslyze-json", "testssl-json", "openssl-json", "tshark-json", "har", "junit"} {
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
