package securitytoolpacks

import "testing"

func TestJUnitFailureIsNotHiddenBySkippedElement(t *testing.T) {
	native := []byte(`<testsuites><testsuite name="schemathesis"><testcase name="GET /"><failure message="server error"/><skipped>No examples</skipped></testcase></testsuite></testsuites>`)
	records, err := (junitAdapter{}).Normalize(Tool{Name: "schemathesis", Version: "4.0.16"}, Target{Locator: "fixture"}, native, NewRedactor())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Skipped || records[0].Record.RuleID != "JUNIT-FAILURE" {
		t.Fatalf("records=%+v", records)
	}
}

func TestSSLyzeCommandFailureReturnsCoverageError(t *testing.T) {
	native := []byte(`{"server_scan_results":[{"connectivity_status":"COMPLETED","scan_status":"COMPLETED","server_location":{"hostname":"example.test","port":443},"scan_result":{"tls_1_0_cipher_suites":{"status":"ERROR","result":{"is_tls_version_supported":false}}}}]}`)
	records, err := (sslyzeAdapter{}).Normalize(Tool{Name: "sslyze", Version: "6.1.0"}, Target{}, native, NewRedactor())
	if err == nil || len(records) != 0 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
}
