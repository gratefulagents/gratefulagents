package securitytoolpacks

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gratefulagents/gratefulagents/internal/security"
)

type Redactor struct{ sensitive map[string]bool }

func NewRedactor(fields ...string) Redactor {
	r := Redactor{sensitive: map[string]bool{"authorization": true, "proxy-authorization": true, "cookie": true, "set-cookie": true}}
	for _, f := range fields {
		r.sensitive[strings.ToLower(strings.TrimSpace(f))] = true
	}
	return r
}

var (
	headerLine = regexp.MustCompile(`(?im)^(authorization|proxy-authorization|cookie|set-cookie)\s*:\s*.*$`)
	pemBlock   = regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----`)
	jwtValue   = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
)

func (r Redactor) Text(s string) string {
	s = headerLine.ReplaceAllStringFunc(s, func(line string) string { p := strings.Index(line, ":"); return line[:p+1] + " [REDACTED]" })
	s = pemBlock.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	s = jwtValue.ReplaceAllString(s, "[REDACTED JWT]")
	for field := range r.sensitive {
		quoted := regexp.MustCompile(`(?i)("?` + regexp.QuoteMeta(field) + `"?\s*[:=]\s*")([^"]*)(")`)
		s = quoted.ReplaceAllString(s, `${1}[REDACTED]${3}`)
		plain := regexp.MustCompile(`(?i)(` + regexp.QuoteMeta(field) + `\s*[:=]\s*)([^\s,}\]]+)`)
		s = plain.ReplaceAllString(s, `${1}[REDACTED]`)
	}
	return s
}

func DefaultAdapters() map[string]Adapter {
	generic := jsonRecordsAdapter{}
	return map[string]Adapter{
		"authorization-matrix": authzAdapter{}, "crypto-vectors": cryptoAdapter{}, "zeek-jsonl": zeekAdapter{}, "suricata-eve": suricataAdapter{}, "nmap-xml": nmapAdapter{},
		"json-records": generic, "zap-json": zapAdapter{}, "schemathesis-json": schemathesisAdapter{}, "restler-json": restlerAdapter{}, "nuclei-jsonl": nucleiAdapter{}, "naabu-jsonl": naabuAdapter{}, "sslyze-json": sslyzeAdapter{}, "testssl-json": testsslAdapter{}, "openssl-json": opensslAdapter{}, "tshark-json": tsharkAdapter{},
		"har": harAdapter{}, "junit": junitAdapter{}, "sarif": sarifAdapter{},
		"slither-json": slitherAdapter{}, "echidna-json": echidnaAdapter{}, "mythril-json": mythrilAdapter{}, "halmos-json": halmosAdapter{},
	}
}

type jsonRecordsAdapter struct{}

func (jsonRecordsAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var records []security.ScannerRecord
	if err := json.Unmarshal(native, &records); err != nil {
		return nil, err
	}
	out := make([]securityRecord, 0, len(records))
	for _, rec := range records {
		rec.Tool = tool.Name
		rec.ToolVersion = tool.Version
		rec = redactPipelineRecord(rec, r)
		out = append(out, securityRecord{Record: fromPipelineRecord(rec), Asset: rec.FilePath})
	}
	return out, nil
}
func fromPipelineRecord(r security.ScannerRecord) ScannerRecord {
	return ScannerRecord{Tool: r.Tool, ToolVersion: r.ToolVersion, RuleID: r.RuleID, RuleName: r.RuleName, Message: r.Message, Severity: r.Severity, Category: r.Category, FilePath: r.FilePath, StartLine: r.StartLine, EndLine: r.EndLine, Symbol: r.Symbol, CWE: r.CWE, References: r.References, RawEvidence: r.RawEvidence, Extra: r.Extra}
}

func redactPipelineRecord(rec security.ScannerRecord, r Redactor) security.ScannerRecord {
	rec.RuleName, rec.Message, rec.FilePath, rec.Symbol = r.Text(rec.RuleName), r.Text(rec.Message), r.Text(rec.FilePath), r.Text(rec.Symbol)
	rec.RawEvidence = r.Text(rec.RawEvidence)
	for i := range rec.References {
		rec.References[i] = r.Text(rec.References[i])
	}
	if rec.Extra != nil {
		extra := make(map[string]string, len(rec.Extra))
		for k, v := range rec.Extra {
			extra[r.Text(k)] = r.Text(v)
		}
		rec.Extra = extra
	}
	return rec
}

func requireJSONObject(native []byte, dst any, format string) error {
	trimmed := bytes.TrimSpace(native)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("%s output must be a JSON object", format)
	}
	if err := json.Unmarshal(trimmed, dst); err != nil {
		return fmt.Errorf("%s output: %w", format, err)
	}
	return nil
}

func nativeSeverity(value, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "critical", "fatal":
		return "critical"
	case "high", "error", "err":
		return "high"
	case "medium", "moderate", "warning", "warn":
		return "medium"
	case "low":
		return "low"
	case "info", "informational", "note", "chat", "unknown":
		return "info"
	default:
		return fallback
	}
}

func rawStrings(raw json.RawMessage) []string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return many
	}
	var one string
	if json.Unmarshal(raw, &one) == nil && one != "" {
		return []string{one}
	}
	return nil
}

type sarifDocument struct {
	Version string `json:"version"`
	Runs    []struct {
		Tool struct {
			Driver struct {
				Name string `json:"name"`
			} `json:"driver"`
		} `json:"tool"`
		Results []struct {
			RuleID  string `json:"ruleId"`
			Level   string `json:"level"`
			Message struct {
				Text string `json:"text"`
			} `json:"message"`
			Locations []struct {
				PhysicalLocation struct {
					ArtifactLocation struct {
						URI string `json:"uri"`
					} `json:"artifactLocation"`
					Region struct {
						StartLine int `json:"startLine"`
						EndLine   int `json:"endLine"`
					} `json:"region"`
				} `json:"physicalLocation"`
			} `json:"locations"`
			Properties map[string]any `json:"properties"`
		} `json:"results"`
	} `json:"runs"`
}
type sarifAdapter struct{}

func (sarifAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	if start := bytes.Index(native, []byte("STDOUT START")); start >= 0 {
		native = native[start+len("STDOUT START"):]
		if end := bytes.Index(native, []byte("STDOUT END")); end >= 0 {
			native = native[:end]
		}
	}
	var document sarifDocument
	if err := requireJSONObject(native, &document, "SARIF"); err != nil {
		return nil, err
	}
	if document.Version != "2.1.0" || len(document.Runs) == 0 {
		return nil, fmt.Errorf("SARIF output requires version 2.1.0 and at least one run")
	}
	var records []securityRecord
	for runIndex, run := range document.Runs {
		if run.Tool.Driver.Name == "" {
			return nil, fmt.Errorf("SARIF run %d requires tool.driver.name", runIndex)
		}
		for resultIndex, result := range run.Results {
			if result.RuleID == "" || result.Message.Text == "" || len(result.Locations) == 0 {
				return nil, fmt.Errorf("SARIF run %d result %d requires ruleId, message, and location", runIndex, resultIndex)
			}
			location := result.Locations[0].PhysicalLocation
			if location.ArtifactLocation.URI == "" {
				return nil, fmt.Errorf("SARIF run %d result %d requires artifact URI", runIndex, resultIndex)
			}
			extra := map[string]string{"sarif_driver": run.Tool.Driver.Name}
			if swc, ok := result.Properties["swc"].(string); ok && swc != "" {
				extra["swc"] = r.Text(swc)
			}
			records = append(records, securityRecord{Asset: location.ArtifactLocation.URI, Record: ScannerRecord{
				Tool: tool.Name, ToolVersion: tool.Version, RuleID: result.RuleID, RuleName: result.RuleID,
				Message: r.Text(result.Message.Text), Severity: nativeSeverity(result.Level, "info"), Category: "logic-flaw",
				FilePath: r.Text(location.ArtifactLocation.URI), StartLine: location.Region.StartLine, EndLine: location.Region.EndLine, Extra: extra,
			}})
		}
	}
	sortSecurityRecords(records)
	return records, nil
}

type slitherDocument struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Results struct {
		Detectors []struct {
			Check       string `json:"check"`
			Impact      string `json:"impact"`
			Confidence  string `json:"confidence"`
			Description string `json:"description"`
			ID          string `json:"id"`
			Elements    []struct {
				Name          string `json:"name"`
				Type          string `json:"type"`
				SourceMapping struct {
					FilenameRelative string `json:"filename_relative"`
					Lines            []int  `json:"lines"`
				} `json:"source_mapping"`
			} `json:"elements"`
		} `json:"detectors"`
	} `json:"results"`
}

type slitherAdapter struct{}

func (slitherAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var document slitherDocument
	if err := requireJSONObject(native, &document, "Slither JSON"); err != nil {
		return nil, err
	}
	if !document.Success {
		if document.Error == "" {
			return nil, fmt.Errorf("slither reported unsuccessful analysis without an error")
		}
		return nil, fmt.Errorf("slither analysis failed: %s", r.Text(document.Error))
	}
	var records []securityRecord
	for i, detector := range document.Results.Detectors {
		if detector.Check == "" || detector.Description == "" || len(detector.Elements) == 0 {
			return nil, fmt.Errorf("slither detector %d requires check, description, and elements", i)
		}
		element := detector.Elements[0]
		path := element.SourceMapping.FilenameRelative
		if path == "" {
			return nil, fmt.Errorf("slither detector %d requires a source filename", i)
		}
		start, end := 0, 0
		if len(element.SourceMapping.Lines) > 0 {
			start = element.SourceMapping.Lines[0]
			end = element.SourceMapping.Lines[len(element.SourceMapping.Lines)-1]
		}
		ruleID := detector.Check
		if detector.ID != "" {
			ruleID = detector.Check + ":" + detector.ID
		}
		records = append(records, securityRecord{Asset: path, Record: ScannerRecord{
			Tool: tool.Name, ToolVersion: tool.Version, RuleID: ruleID, RuleName: detector.Check,
			Message: r.Text(strings.TrimSpace(detector.Description)), Severity: nativeSeverity(detector.Impact, "info"), Category: "logic-flaw",
			FilePath: r.Text(path), StartLine: start, EndLine: end, Symbol: r.Text(element.Name),
			RawEvidence: r.Text(strings.TrimSpace(detector.Description)), Extra: map[string]string{"confidence": r.Text(detector.Confidence), "element_type": r.Text(element.Type)},
		}})
	}
	sortSecurityRecords(records)
	return records, nil
}

type echidnaDocument struct {
	Success bool            `json:"success"`
	Error   json.RawMessage `json:"error"`
	Seed    int64           `json:"seed"`
	Tests   []struct {
		Contract     string          `json:"contract"`
		Name         string          `json:"name"`
		Status       string          `json:"status"`
		Error        json.RawMessage `json:"error"`
		Type         string          `json:"type"`
		TestType     string          `json:"testType"`
		Transactions []struct {
			Contract  string   `json:"contract"`
			Function  string   `json:"function"`
			Arguments []string `json:"arguments"`
			Gas       string   `json:"gas"`
			GasPrice  string   `json:"gasprice"`
			Value     string   `json:"value"`
		} `json:"transactions"`
	} `json:"tests"`
}

type echidnaAdapter struct{}

func (echidnaAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var document echidnaDocument
	if err := requireJSONObject(native, &document, "Echidna JSON"); err != nil {
		return nil, err
	}
	if !document.Success {
		return nil, fmt.Errorf("echidna analysis failed: %s", r.Text(strings.TrimSpace(string(document.Error))))
	}
	var records []securityRecord
	var testErrors []string
	for i, test := range document.Tests {
		testType := test.Type
		if testType == "" {
			testType = test.TestType
		}
		if test.Status == "" || testType == "" {
			return nil, fmt.Errorf("echidna test %d requires status and type", i)
		}
		name := test.Name
		if name == "" {
			name = fmt.Sprintf("%s-%d", testType, i+1)
		}
		asset := "echidna-property:" + name
		if test.Contract != "" {
			asset = "echidna-property:" + test.Contract + "." + name
		}
		status := strings.ToLower(strings.TrimSpace(test.Status))
		switch status {
		case "passed", "verified":
			records = append(records, securityRecord{Asset: r.Text(asset), Examined: true})
			continue
		case "solved":
			records = append(records, securityRecord{Asset: r.Text(asset), Examined: true})
		case "shrinking":
			// Preserve the current counterexample as a partial finding, but do
			// not claim complete property coverage until shrinking terminates.
			records = append(records, securityRecord{Asset: r.Text(asset), Uncovered: true})
			testErrors = append(testErrors, fmt.Sprintf("test %d (%s) ended while shrinking a counterexample", i, name))
		case "error", "fuzzing":
			records = append(records, securityRecord{Asset: r.Text(asset), Uncovered: true})
			testErrors = append(testErrors, fmt.Sprintf("test %d (%s) ended in %s state: %s", i, name, status, strings.TrimSpace(string(test.Error))))
			continue
		default:
			records = append(records, securityRecord{Asset: r.Text(asset), Uncovered: true})
			testErrors = append(testErrors, fmt.Sprintf("test %d (%s) returned unsupported status %q", i, name, test.Status))
			continue
		}
		evidence, _ := json.Marshal(test.Transactions)
		message := fmt.Sprintf("Echidna %s %s", testType, test.Status)
		if len(test.Error) > 0 && string(test.Error) != "null" {
			message += ": " + string(test.Error)
		}
		records = append(records, securityRecord{Asset: target.Locator, Record: ScannerRecord{
			Tool: tool.Name, ToolVersion: tool.Version, RuleID: "ECHIDNA-" + strings.ToUpper(testType), RuleName: name,
			Message: r.Text(message), Severity: "high", Category: "logic-flaw", FilePath: r.Text(target.Locator), Symbol: r.Text(name),
			RawEvidence: r.Text(string(evidence)), Extra: map[string]string{"status": test.Status, "seed": strconv.FormatInt(document.Seed, 10)},
		}})
	}
	sortSecurityRecords(records)
	if len(testErrors) > 0 {
		sort.Strings(testErrors)
		return records, fmt.Errorf("echidna incomplete coverage: %s", r.Text(strings.Join(testErrors, "; ")))
	}
	return records, nil
}

type mythrilDocument struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Issues  []struct {
		Title       string          `json:"title"`
		SWC         string          `json:"swc-id"`
		Contract    string          `json:"contract"`
		Description string          `json:"description"`
		Function    string          `json:"function"`
		Severity    string          `json:"severity"`
		Address     int             `json:"address"`
		TXSequence  json.RawMessage `json:"tx_sequence"`
		SourceMap   string          `json:"sourceMap"`
		Filename    string          `json:"filename"`
		Line        int             `json:"lineno"`
		Code        string          `json:"code"`
	} `json:"issues"`
}

type mythrilAdapter struct{}

func (mythrilAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var document mythrilDocument
	if err := requireJSONObject(native, &document, "Mythril JSON"); err != nil {
		return nil, err
	}
	if !document.Success {
		if document.Error == "" {
			return nil, fmt.Errorf("mythril reported unsuccessful analysis without an error")
		}
		return nil, fmt.Errorf("mythril analysis failed: %s", r.Text(document.Error))
	}
	var records []securityRecord
	for i, issue := range document.Issues {
		if issue.Title == "" || issue.SWC == "" || issue.Description == "" {
			return nil, fmt.Errorf("mythril issue %d requires title, swc-id, and description", i)
		}
		path := issue.Filename
		if path == "" {
			path = target.Locator
		}
		evidence := strings.TrimSpace(issue.Code)
		if len(issue.TXSequence) > 0 && string(issue.TXSequence) != "null" {
			evidence += "\ntx_sequence=" + string(issue.TXSequence)
		}
		records = append(records, securityRecord{Asset: path, Record: ScannerRecord{
			Tool: tool.Name, ToolVersion: tool.Version, RuleID: "SWC-" + strings.TrimPrefix(strings.ToUpper(issue.SWC), "SWC-"), RuleName: issue.Title,
			Message: r.Text(issue.Description), Severity: nativeSeverity(issue.Severity, "info"), Category: "logic-flaw", FilePath: r.Text(path),
			StartLine: issue.Line, EndLine: issue.Line, Symbol: r.Text(issue.Function), CWE: "", RawEvidence: r.Text(strings.TrimSpace(evidence)),
			Extra: map[string]string{"contract": r.Text(issue.Contract), "source_map": r.Text(issue.SourceMap), "instruction_address": strconv.Itoa(issue.Address)},
		}})
	}
	sortSecurityRecords(records)
	return records, nil
}

type halmosDocument struct {
	ExitCode    int `json:"exitcode"`
	TestResults map[string][]struct {
		Name      string `json:"name"`
		ExitCode  int    `json:"exitcode"`
		NumModels int    `json:"num_models"`
	} `json:"test_results"`
}

type halmosAdapter struct{}

func (halmosAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var document halmosDocument
	if err := requireJSONObject(native, &document, "Halmos JSON"); err != nil {
		return nil, err
	}
	if document.TestResults == nil {
		return nil, fmt.Errorf("halmos JSON requires test_results")
	}
	var records []securityRecord
	var incomplete []string
	for suite, tests := range document.TestResults {
		for i, test := range tests {
			if test.Name == "" {
				return nil, fmt.Errorf("halmos test %s[%d] requires name", suite, i)
			}
			if test.ExitCode == 0 {
				continue
			}
			if test.ExitCode != 1 {
				incomplete = append(incomplete, fmt.Sprintf("%s.%s(exitcode=%d)", suite, test.Name, test.ExitCode))
				continue
			}
			evidence := fmt.Sprintf("suite=%s exitcode=%d models=%d", suite, test.ExitCode, test.NumModels)
			records = append(records, securityRecord{Asset: suite, Record: ScannerRecord{
				Tool: tool.Name, ToolVersion: tool.Version, RuleID: "HALMOS-COUNTEREXAMPLE", RuleName: test.Name,
				Message: r.Text("Halmos found a symbolic counterexample for " + test.Name), Severity: "high", Category: "logic-flaw",
				FilePath: r.Text(suite), Symbol: r.Text(test.Name), RawEvidence: r.Text(evidence), Extra: map[string]string{"models": strconv.Itoa(test.NumModels)},
			}})
		}
	}
	if len(incomplete) != 0 {
		sort.Strings(incomplete)
		sortSecurityRecords(records)
		return records, fmt.Errorf("halmos returned incomplete test states: %s", strings.Join(incomplete, ", "))
	}
	if document.ExitCode != 0 && len(records) == 0 {
		return nil, fmt.Errorf("halmos exited %d without a test counterexample", document.ExitCode)
	}
	sortSecurityRecords(records)
	return records, nil
}

type nucleiResult struct {
	TemplateID  string   `json:"template-id"`
	MatcherName string   `json:"matcher-name"`
	Type        string   `json:"type"`
	Host        string   `json:"host"`
	MatchedAt   string   `json:"matched-at"`
	Request     string   `json:"request"`
	Response    string   `json:"response"`
	Extracted   []string `json:"extracted-results"`
	Info        struct {
		Name           string          `json:"name"`
		Severity       string          `json:"severity"`
		Reference      json.RawMessage `json:"reference"`
		Classification struct {
			CWE json.RawMessage `json:"cwe-id"`
		} `json:"classification"`
	} `json:"info"`
}

type nucleiAdapter struct{}

func (nucleiAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	s := bufio.NewScanner(bytes.NewReader(native))
	var records []securityRecord
	for line := 1; s.Scan(); line++ {
		if len(bytes.TrimSpace(s.Bytes())) == 0 {
			continue
		}
		var result nucleiResult
		if err := requireJSONObject(s.Bytes(), &result, "nuclei JSONL line"); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if result.TemplateID == "" || (result.MatchedAt == "" && result.Host == "") {
			return nil, fmt.Errorf("line %d: nuclei result requires template-id and matched-at or host", line)
		}
		name := result.Info.Name
		if name == "" {
			name = result.TemplateID
		}
		location := result.MatchedAt
		if location == "" {
			location = result.Host
		}
		cwes := rawStrings(result.Info.Classification.CWE)
		cwe := ""
		if len(cwes) > 0 {
			cwe = cwes[0]
		}
		evidence := strings.Join([]string{result.Request, result.Response, strings.Join(result.Extracted, "\n")}, "\n")
		records = append(records, securityRecord{Asset: location, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: result.TemplateID, RuleName: r.Text(name), Message: r.Text(name + " matched at " + location), Severity: nativeSeverity(result.Info.Severity, "info"), Category: nucleiCategory(result.Type), FilePath: "targets/web/" + safePath(location), CWE: cwe, References: rawStrings(result.Info.Reference), RawEvidence: r.Text(strings.TrimSpace(evidence)), Extra: map[string]string{"matcher": r.Text(result.MatcherName)}}})
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	sortSecurityRecords(records)
	return records, nil
}

func nucleiCategory(nativeType string) string {
	switch strings.ToLower(nativeType) {
	case "xss":
		return "xss"
	case "ssrf":
		return "ssrf"
	case "sqli", "injection":
		return "injection"
	default:
		return "misconfiguration"
	}
}

type naabuResult struct {
	Host     string `json:"host"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	TLS      bool   `json:"tls"`
}
type naabuAdapter struct{}

func (naabuAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	scanner := bufio.NewScanner(bytes.NewReader(native))
	var records []securityRecord
	seen := map[string]bool{}
	for line := 1; scanner.Scan(); line++ {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var result naabuResult
		if err := requireJSONObject(scanner.Bytes(), &result, "naabu JSONL line"); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if result.Port < 1 || result.Port > 65535 || (result.Host == "" && result.IP == "") {
			return nil, fmt.Errorf("line %d: naabu result requires host or ip and a valid port", line)
		}
		host := result.Host
		if host == "" {
			host = result.IP
		}
		asset := net.JoinHostPort(host, strconv.Itoa(result.Port))
		if seen[asset] {
			continue
		}
		seen[asset] = true
		evidence := fmt.Sprintf("host=%s ip=%s port=%d protocol=%s tls=%t", result.Host, result.IP, result.Port, result.Protocol, result.TLS)
		records = append(records, securityRecord{Asset: asset, Record: ScannerRecord{
			Tool: tool.Name, ToolVersion: tool.Version, RuleID: "NAABU-OPEN-PORT", RuleName: "Discovered open network service",
			Message: r.Text("Open service discovered at " + asset), Severity: "info", Category: "misconfiguration",
			FilePath: "targets/network/" + safePath(asset), RawEvidence: r.Text(evidence),
		}})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sortSecurityRecords(records)
	return records, nil
}

type junitIssue struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Body    string `xml:",chardata"`
}
type junitCase struct {
	Name      string       `xml:"name,attr"`
	ClassName string       `xml:"classname,attr"`
	File      string       `xml:"file,attr"`
	Failures  []junitIssue `xml:"failure"`
	Errors    []junitIssue `xml:"error"`
	Skipped   []junitIssue `xml:"skipped"`
	SystemOut string       `xml:"system-out"`
	SystemErr string       `xml:"system-err"`
}
type junitSuite struct {
	Name   string       `xml:"name,attr"`
	Cases  []junitCase  `xml:"testcase"`
	Suites []junitSuite `xml:"testsuite"`
}
type junitSuites struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []junitSuite `xml:"testsuite"`
}
type junitAdapter struct{}

func (junitAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var root struct{ XMLName xml.Name }
	if err := xml.Unmarshal(native, &root); err != nil {
		return nil, fmt.Errorf("junit output: %w", err)
	}
	var suites []junitSuite
	switch root.XMLName.Local {
	case "testsuites":
		var document junitSuites
		if err := xml.Unmarshal(native, &document); err != nil {
			return nil, err
		}
		suites = document.Suites
	case "testsuite":
		var suite junitSuite
		if err := xml.Unmarshal(native, &suite); err != nil {
			return nil, err
		}
		suites = []junitSuite{suite}
	default:
		return nil, fmt.Errorf("junit output root must be testsuite or testsuites")
	}
	var records []securityRecord
	var visit func(junitSuite)
	visit = func(suite junitSuite) {
		for _, test := range suite.Cases {
			asset := strings.Trim(strings.Join([]string{test.ClassName, test.Name}, "/"), "/")
			if asset == "" {
				asset = suite.Name
			}
			if len(test.Skipped) > 0 && len(test.Failures) == 0 && len(test.Errors) == 0 {
				records = append(records, securityRecord{Asset: asset, Skipped: true})
				continue
			}
			appendIssues := func(kind string, issues []junitIssue, severity string) {
				for _, issue := range issues {
					message := strings.TrimSpace(issue.Message)
					if message == "" {
						message = strings.TrimSpace(issue.Type)
					}
					if message == "" {
						message = test.Name + " " + kind
					}
					file := test.File
					if file == "" {
						file = "tests/" + safePath(test.ClassName) + "/" + safePath(test.Name)
					}
					evidence := strings.Join([]string{issue.Body, test.SystemOut, test.SystemErr}, "\n")
					records = append(records, securityRecord{Asset: asset, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: "JUNIT-" + strings.ToUpper(kind), RuleName: r.Text(message), Message: r.Text(message), Severity: severity, Category: "other", FilePath: file, RawEvidence: r.Text(strings.TrimSpace(evidence))}})
				}
			}
			appendIssues("failure", test.Failures, "medium")
			appendIssues("error", test.Errors, "high")
		}
		for _, nested := range suite.Suites {
			visit(nested)
		}
	}
	for _, suite := range suites {
		visit(suite)
	}
	sortSecurityRecords(records)
	return records, nil
}

type harDocument struct {
	Log *struct {
		Version string `json:"version"`
		Entries []struct {
			Request struct {
				Method  string `json:"method"`
				URL     string `json:"url"`
				Headers []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"headers"`
				PostData struct {
					Text string `json:"text"`
				} `json:"postData"`
			} `json:"request"`
			Response struct {
				Status     int    `json:"status"`
				StatusText string `json:"statusText"`
				Headers    []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"headers"`
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"response"`
		} `json:"entries"`
	} `json:"log"`
}
type harAdapter struct{}

func (harAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var document harDocument
	if err := requireJSONObject(native, &document, "HAR"); err != nil {
		return nil, err
	}
	if document.Log == nil || document.Log.Version == "" {
		return nil, fmt.Errorf("HAR output requires log.version")
	}
	var records []securityRecord
	for _, entry := range document.Log.Entries {
		if entry.Request.Method == "" || entry.Request.URL == "" {
			return nil, fmt.Errorf("HAR entry requires request.method and request.url")
		}
		if entry.Response.Status < 400 {
			continue
		}
		severity := "low"
		if entry.Response.Status >= 500 {
			severity = "medium"
		}
		var evidence []string
		for _, header := range entry.Request.Headers {
			evidence = append(evidence, header.Name+": "+header.Value)
		}
		for _, header := range entry.Response.Headers {
			evidence = append(evidence, header.Name+": "+header.Value)
		}
		sort.Strings(evidence)
		evidence = append(evidence, entry.Request.PostData.Text, entry.Response.Content.Text)
		message := fmt.Sprintf("%s %s returned %d %s", entry.Request.Method, entry.Request.URL, entry.Response.Status, entry.Response.StatusText)
		records = append(records, securityRecord{Asset: entry.Request.URL, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: "HTTP-STATUS-" + strconv.Itoa(entry.Response.Status), RuleName: "HTTP error response", Message: r.Text(strings.TrimSpace(message)), Severity: severity, Category: "other", FilePath: "targets/web/" + safePath(entry.Request.URL), RawEvidence: r.Text(strings.TrimSpace(strings.Join(evidence, "\n")))}})
	}
	sortSecurityRecords(records)
	return records, nil
}

type zapDocument struct {
	Sites *[]struct {
		Name   string `json:"@name"`
		Host   string `json:"@host"`
		Alerts []struct {
			PluginID  string `json:"pluginid"`
			Alert     string `json:"alert"`
			Name      string `json:"name"`
			RiskCode  string `json:"riskcode"`
			RiskDesc  string `json:"riskdesc"`
			Desc      string `json:"desc"`
			Solution  string `json:"solution"`
			Reference string `json:"reference"`
			CWEID     string `json:"cweid"`
			Instances []struct {
				URI       string `json:"uri"`
				Method    string `json:"method"`
				Param     string `json:"param"`
				Attack    string `json:"attack"`
				Evidence  string `json:"evidence"`
				OtherInfo string `json:"otherinfo"`
			} `json:"instances"`
		} `json:"alerts"`
	} `json:"site"`
}
type zapAdapter struct{}

func (zapAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var document zapDocument
	if err := requireJSONObject(native, &document, "ZAP"); err != nil {
		return nil, err
	}
	if document.Sites == nil {
		return nil, fmt.Errorf("ZAP output requires site")
	}
	var records []securityRecord
	for _, site := range *document.Sites {
		for _, alert := range site.Alerts {
			if alert.PluginID == "" || (alert.Alert == "" && alert.Name == "") {
				return nil, fmt.Errorf("ZAP alert requires pluginid and alert or name")
			}
			name := alert.Alert
			if name == "" {
				name = alert.Name
			}
			severity := map[string]string{"0": "info", "1": "low", "2": "medium", "3": "high", "4": "critical"}[alert.RiskCode]
			if severity == "" {
				fields := strings.Fields(alert.RiskDesc)
				if len(fields) > 0 {
					severity = nativeSeverity(fields[0], "info")
				} else {
					severity = "info"
				}
			}
			instanceURI := site.Name
			var evidence []string
			for _, instance := range alert.Instances {
				if instanceURI == "" {
					instanceURI = instance.URI
				}
				evidence = append(evidence, strings.Join([]string{instance.Method, instance.URI, instance.Param, instance.Attack, instance.Evidence, instance.OtherInfo}, " "))
			}
			if instanceURI == "" {
				instanceURI = site.Host
			}
			evidence = append(evidence, alert.Desc, alert.Solution)
			refs := strings.Fields(alert.Reference)
			sort.Strings(refs)
			cwe := ""
			if alert.CWEID != "" {
				cwe = "CWE-" + strings.TrimPrefix(alert.CWEID, "CWE-")
			}
			records = append(records, securityRecord{Asset: instanceURI, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: alert.PluginID, RuleName: r.Text(name), Message: r.Text(name), Severity: severity, Category: "other", FilePath: "targets/web/" + safePath(instanceURI), CWE: cwe, References: refs, RawEvidence: r.Text(strings.TrimSpace(strings.Join(evidence, "\n")))}})
		}
	}
	sortSecurityRecords(records)
	return records, nil
}

type apiFailure struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Message    string `json:"message"`
	Severity   string `json:"severity"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	URL        string `json:"url"`
	StatusCode int    `json:"status_code"`
	Request    string `json:"request"`
	Response   string `json:"response"`
	Checker    string `json:"checker_name"`
}
type schemathesisDocument struct {
	Failures *[]apiFailure `json:"failures"`
}
type schemathesisAdapter struct{}

func (schemathesisAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var document schemathesisDocument
	if err := requireJSONObject(native, &document, "Schemathesis"); err != nil {
		return nil, err
	}
	if document.Failures == nil {
		return nil, fmt.Errorf("schemathesis output requires failures")
	}
	return normalizeAPIFailures(tool, *document.Failures, "SCHEMATHESIS", r)
}

type restlerDocument struct {
	Bugs *[]apiFailure `json:"bugs"`
}
type restlerAdapter struct{}

func (restlerAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var document restlerDocument
	if err := requireJSONObject(native, &document, "RESTler"); err != nil {
		return nil, err
	}
	if document.Bugs == nil {
		return nil, fmt.Errorf("RESTler output requires bugs")
	}
	return normalizeAPIFailures(tool, *document.Bugs, "RESTLER", r)
}

func normalizeAPIFailures(tool Tool, failures []apiFailure, prefix string, r Redactor) ([]securityRecord, error) {
	records := make([]securityRecord, 0, len(failures))
	for _, failure := range failures {
		if failure.ID == "" || (failure.Message == "" && failure.Name == "") {
			return nil, fmt.Errorf("%s entry requires id and message or name", prefix)
		}
		message := failure.Message
		if message == "" {
			message = failure.Name
		}
		location := failure.URL
		if location == "" {
			location = failure.Path
		}
		if location == "" {
			location = failure.Method
		}
		name := failure.Name
		if name == "" {
			name = failure.Checker
		}
		if name == "" {
			name = failure.ID
		}
		evidence := fmt.Sprintf("request=%s\nresponse=%s\nstatus=%d", failure.Request, failure.Response, failure.StatusCode)
		records = append(records, securityRecord{Asset: location, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: failure.ID, RuleName: r.Text(name), Message: r.Text(message), Severity: nativeSeverity(failure.Severity, "medium"), Category: "other", FilePath: "targets/api/" + safePath(location), RawEvidence: r.Text(evidence)}})
	}
	sortSecurityRecords(records)
	return records, nil
}

type sslyzeDocument struct {
	ServerScanResults *[]struct {
		ConnectivityStatus     string `json:"connectivity_status"`
		ConnectivityErrorTrace string `json:"connectivity_error_trace"`
		ScanStatus             string `json:"scan_status"`
		ServerLocation         struct {
			Hostname  string `json:"hostname"`
			IPAddress string `json:"ip_address"`
			Port      int    `json:"port"`
		} `json:"server_location"`
		ScanResult map[string]struct {
			Status string          `json:"status"`
			Result json.RawMessage `json:"result"`
		} `json:"scan_result"`
	} `json:"server_scan_results"`
}
type sslyzeAdapter struct{}

func (sslyzeAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var document sslyzeDocument
	if err := requireJSONObject(native, &document, "SSLyze"); err != nil {
		return nil, err
	}
	if document.ServerScanResults == nil {
		return nil, fmt.Errorf("SSLyze output requires server_scan_results")
	}
	var records []securityRecord
	var operationErrors []string
	weakProtocols := map[string]string{"ssl_2_0_cipher_suites": "SSLv2", "ssl_3_0_cipher_suites": "SSLv3", "tls_1_0_cipher_suites": "TLS 1.0", "tls_1_1_cipher_suites": "TLS 1.1"}
	for _, server := range *document.ServerScanResults {
		host := server.ServerLocation.Hostname
		if host == "" {
			host = server.ServerLocation.IPAddress
		}
		asset := net.JoinHostPort(host, strconv.Itoa(server.ServerLocation.Port))
		for label, status := range map[string]string{"connectivity": server.ConnectivityStatus, "scan": server.ScanStatus} {
			if status != "" && !strings.EqualFold(status, "completed") {
				operationErrors = append(operationErrors, asset+" "+label+" status="+status)
			}
		}
		if server.ConnectivityErrorTrace != "" {
			operationErrors = append(operationErrors, asset+" connectivity error="+r.Text(server.ConnectivityErrorTrace))
		}
		keys := make([]string, 0, len(server.ScanResult))
		for key := range server.ScanResult {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			command := server.ScanResult[key]
			if command.Status != "" && !strings.EqualFold(command.Status, "completed") {
				operationErrors = append(operationErrors, asset+" command "+key+" status="+command.Status)
			}
			if protocol, weak := weakProtocols[key]; weak {
				var result struct {
					Supported bool `json:"is_tls_version_supported"`
				}
				if json.Unmarshal(command.Result, &result) == nil && result.Supported {
					message := protocol + " is supported by " + asset
					records = append(records, securityRecord{Asset: asset, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: "WEAK-PROTOCOL-" + strings.ToUpper(strings.ReplaceAll(protocol, " ", "-")), RuleName: "Deprecated TLS protocol enabled", Message: message, Severity: "medium", Category: "crypto", FilePath: "targets/tls/" + safePath(asset), CWE: "CWE-327", RawEvidence: r.Text(message)}})
				}
			}
			if key == "certificate_info" {
				var result struct {
					HostnameMatches       *bool `json:"hostname_matches_certificate"`
					PathValidationResults []struct {
						ValidationError string `json:"validation_error"`
					} `json:"path_validation_results"`
				}
				if json.Unmarshal(command.Result, &result) == nil {
					if result.HostnameMatches != nil && !*result.HostnameMatches {
						message := "certificate does not match " + host
						records = append(records, securityRecord{Asset: asset, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: "CERTIFICATE-HOSTNAME-MISMATCH", RuleName: "TLS certificate hostname mismatch", Message: message, Severity: "high", Category: "crypto", FilePath: "targets/tls/" + safePath(asset), CWE: "CWE-295", RawEvidence: r.Text(message)}})
					}
					for _, validation := range result.PathValidationResults {
						if validation.ValidationError == "" {
							continue
						}
						records = append(records, securityRecord{Asset: asset, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: "CERTIFICATE-VALIDATION-ERROR", RuleName: "TLS certificate validation failed", Message: r.Text(validation.ValidationError), Severity: "high", Category: "crypto", FilePath: "targets/tls/" + safePath(asset), CWE: "CWE-295", RawEvidence: r.Text(validation.ValidationError)}})
					}
				}
			}
		}
	}
	sortSecurityRecords(records)
	if len(operationErrors) > 0 {
		sort.Strings(operationErrors)
		return records, fmt.Errorf("SSLyze incomplete coverage: %s", strings.Join(operationErrors, "; "))
	}
	return records, nil
}

type testsslEntry struct {
	ID       string `json:"id"`
	IP       string `json:"ip"`
	Port     string `json:"port"`
	Severity string `json:"severity"`
	Finding  string `json:"finding"`
}
type testsslAdapter struct{}

func (testsslAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	trimmed := bytes.TrimSpace(native)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, fmt.Errorf("testssl output must be a JSON array")
	}
	var entries []testsslEntry
	if err := json.Unmarshal(trimmed, &entries); err != nil {
		return nil, fmt.Errorf("testssl output: %w", err)
	}
	var records []securityRecord
	for _, entry := range entries {
		if entry.ID == "" || entry.Finding == "" {
			return nil, fmt.Errorf("testssl entry requires id and finding")
		}
		severityToken := strings.ToLower(entry.Severity)
		if severityToken == "ok" || severityToken == "info" {
			continue
		}
		asset := entry.IP
		if entry.Port != "" {
			asset = net.JoinHostPort(entry.IP, entry.Port)
		}
		records = append(records, securityRecord{Asset: asset, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: entry.ID, RuleName: entry.ID, Message: r.Text(entry.Finding), Severity: nativeSeverity(entry.Severity, "medium"), Category: "crypto", FilePath: "targets/tls/" + safePath(asset), RawEvidence: r.Text(entry.Finding)}})
	}
	sortSecurityRecords(records)
	return records, nil
}

type opensslDocument struct {
	Findings *[]struct {
		ID         string   `json:"id"`
		Name       string   `json:"name"`
		Message    string   `json:"message"`
		Severity   string   `json:"severity"`
		Object     string   `json:"object"`
		CWE        string   `json:"cwe"`
		Evidence   string   `json:"evidence"`
		References []string `json:"references"`
	} `json:"findings"`
}
type opensslAdapter struct{}

func (opensslAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var document opensslDocument
	if err := requireJSONObject(native, &document, "OpenSSL inspection"); err != nil {
		return nil, err
	}
	if document.Findings == nil {
		return nil, fmt.Errorf("OpenSSL inspection output requires findings")
	}
	var records []securityRecord
	for _, finding := range *document.Findings {
		if finding.ID == "" || finding.Message == "" {
			return nil, fmt.Errorf("OpenSSL inspection finding requires id and message")
		}
		asset := finding.Object
		if asset == "" {
			asset = target.Locator
		}
		records = append(records, securityRecord{Asset: asset, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: finding.ID, RuleName: r.Text(finding.Name), Message: r.Text(finding.Message), Severity: nativeSeverity(finding.Severity, "medium"), Category: "crypto", FilePath: "crypto/" + safePath(asset), CWE: finding.CWE, References: finding.References, RawEvidence: r.Text(finding.Evidence)}})
	}
	sortSecurityRecords(records)
	return records, nil
}

type tsharkPacket struct {
	Source *struct {
		Layers map[string]any `json:"layers"`
	} `json:"_source"`
}
type tsharkExpert struct {
	Path     string
	Message  string
	Severity string
}
type tsharkAdapter struct{}

func (tsharkAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	trimmed := bytes.TrimSpace(native)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, fmt.Errorf("tshark output must be a JSON array")
	}
	var packets []tsharkPacket
	if err := json.Unmarshal(trimmed, &packets); err != nil {
		return nil, fmt.Errorf("tshark output: %w", err)
	}
	var records []securityRecord
	for i, packet := range packets {
		if packet.Source == nil || packet.Source.Layers == nil {
			return nil, fmt.Errorf("tshark packet %d requires _source.layers", i)
		}
		frame := nestedString(packet.Source.Layers, "frame", "frame.number")
		if frame == "" {
			frame = strconv.Itoa(i + 1)
		}
		src := nestedString(packet.Source.Layers, "ip", "ip.src")
		dst := nestedString(packet.Source.Layers, "ip", "ip.dst")
		var experts []tsharkExpert
		collectTsharkExperts(packet.Source.Layers, "", &experts)
		sort.Slice(experts, func(i, j int) bool {
			return experts[i].Path+"\x00"+experts[i].Message < experts[j].Path+"\x00"+experts[j].Message
		})
		for _, expert := range experts {
			asset := strings.Trim(src+"->"+dst, "->") + "#" + frame
			evidence := fmt.Sprintf("frame=%s source=%s destination=%s message=%s", frame, src, dst, expert.Message)
			records = append(records, securityRecord{Asset: asset, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: "TSHARK-EXPERT", RuleName: "Wireshark expert finding", Message: r.Text(expert.Message), Severity: nativeSeverity(expert.Severity, "low"), Category: "other", FilePath: "network/" + safePath(target.Locator) + "/frame-" + safePath(frame), RawEvidence: r.Text(evidence), Extra: map[string]string{"protocol_path": expert.Path}}})
		}
	}
	sortSecurityRecords(records)
	return records, nil
}

func nestedString(root map[string]any, object, key string) string {
	child, _ := root[object].(map[string]any)
	return anyString(child[key])
}
func anyString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		if len(typed) > 0 {
			return anyString(typed[0])
		}
	}
	return ""
}
func collectTsharkExperts(value any, path string, out *[]tsharkExpert) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	severity := ""
	for _, key := range keys {
		if strings.Contains(key, "expert.severity") {
			severity = anyString(object[key])
		}
	}
	for _, key := range keys {
		next := strings.Trim(path+"/"+key, "/")
		if strings.Contains(key, "expert.message") {
			if message := anyString(object[key]); message != "" {
				*out = append(*out, tsharkExpert{Path: next, Message: message, Severity: severity})
			}
		}
		collectTsharkExperts(object[key], next, out)
	}
}

type authzCase struct {
	ID             string            `json:"id"`
	Actor          string            `json:"actor"`
	ActorTenant    string            `json:"actor_tenant"`
	Resource       string            `json:"resource"`
	ResourceTenant string            `json:"resource_tenant"`
	Method         string            `json:"method"`
	Endpoint       string            `json:"endpoint"`
	Operation      string            `json:"operation"`
	Expected       int               `json:"expected_status"`
	Actual         int               `json:"actual_status"`
	Headers        map[string]string `json:"request_headers,omitempty"`
}
type authzOutput struct {
	Cases []authzCase `json:"cases"`
}
type authzAdapter struct{}

func (authzAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var out authzOutput
	if err := json.Unmarshal(native, &out); err != nil {
		return nil, err
	}
	var records []securityRecord
	for _, c := range out.Cases {
		actualAllowed := c.Actual >= 200 && c.Actual < 300
		expectedDenied := c.Expected == 401 || c.Expected == 403 || c.Expected == 404
		if c.Actual == c.Expected || !actualAllowed || !expectedDenied {
			continue
		}
		headerNames := make([]string, 0, len(c.Headers))
		for name := range c.Headers {
			headerNames = append(headerNames, name+"=[REDACTED]")
		}
		sort.Strings(headerNames)
		evidence := fmt.Sprintf("actor=%s actor_tenant=%s resource=%s resource_tenant=%s request=%s %s expected=%d actual=%d headers=%s", c.Actor, c.ActorTenant, c.Resource, c.ResourceTenant, c.Method, c.Endpoint, c.Expected, c.Actual, strings.Join(headerNames, ","))
		records = append(records, securityRecord{Asset: c.ID, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: "AUTHZ-MATRIX-UNEXPECTED-ALLOW", RuleName: "Authorization matrix allowed forbidden access", Message: fmt.Sprintf("%s received %d for %s %s; expected %d", c.Actor, c.Actual, c.Method, c.Endpoint, c.Expected), Severity: "high", Category: "authz", FilePath: "targets/web/" + safePath(c.ID), CWE: "CWE-639", RawEvidence: r.Text(evidence)}})
	}
	return records, nil
}

type cryptoVector struct {
	ID       string            `json:"id"`
	Suite    string            `json:"suite"`
	Expected string            `json:"expected"`
	Actual   string            `json:"actual"`
	Passed   bool              `json:"passed"`
	Skipped  bool              `json:"skipped,omitempty"`
	Context  map[string]string `json:"context,omitempty"`
}
type cryptoOutput struct {
	Vectors []cryptoVector `json:"vectors"`
}
type cryptoAdapter struct{}

func (cryptoAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var out cryptoOutput
	if err := json.Unmarshal(native, &out); err != nil {
		return nil, err
	}
	var records []securityRecord
	for _, v := range out.Vectors {
		if v.Skipped {
			records = append(records, securityRecord{Asset: v.ID, Skipped: true})
			continue
		}
		if v.Passed {
			continue
		}
		contextKeys := make([]string, 0, len(v.Context))
		for key := range v.Context {
			contextKeys = append(contextKeys, key)
		}
		sort.Strings(contextKeys)
		contextValues := make([]string, 0, len(contextKeys))
		for _, key := range contextKeys {
			contextValues = append(contextValues, key+"="+v.Context[key])
		}
		ev := r.Text(fmt.Sprintf("suite=%s vector=%s expected=%s actual=%s context=%s", v.Suite, v.ID, v.Expected, v.Actual, strings.Join(contextValues, ",")))
		records = append(records, securityRecord{Asset: v.ID, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: "CRYPTO-KAT-MISMATCH", RuleName: "Cryptographic known-answer mismatch", Message: "implementation failed known-answer vector " + v.ID, Severity: "high", Category: "crypto", FilePath: "vectors/" + safePath(v.Suite) + "/" + safePath(v.ID) + ".json", CWE: "CWE-327", RawEvidence: ev}})
	}
	sortSecurityRecords(records)
	return records, nil
}

type zeekAdapter struct{}

func (zeekAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	s := bufio.NewScanner(bytes.NewReader(native))
	var records []securityRecord
	line := 0
	for s.Scan() {
		line++
		if strings.TrimSpace(s.Text()) == "" {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal(s.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		notice := stringValue(e, "note")
		if notice == "" {
			continue
		}
		src := stringValue(e, "id.orig_h")
		dst := stringValue(e, "id.resp_h")
		msg := stringValue(e, "msg")
		if msg == "" {
			msg = notice
		}
		records = append(records, securityRecord{Asset: src + "->" + dst, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: notice, RuleName: notice, Message: r.Text(msg), Severity: "medium", Category: "other", FilePath: "network/" + safePath(dst), RawEvidence: r.Text(fmt.Sprintf("source=%s destination=%s notice=%s", src, dst, notice))}})
	}
	return records, s.Err()
}

type suricataAdapter struct{}

func (suricataAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	s := bufio.NewScanner(bytes.NewReader(native))
	var records []securityRecord
	line := 0
	for s.Scan() {
		line++
		var e struct {
			EventType string `json:"event_type"`
			Src       string `json:"src_ip"`
			Dst       string `json:"dest_ip"`
			Alert     *struct {
				SignatureID int    `json:"signature_id"`
				Signature   string `json:"signature"`
				Severity    int    `json:"severity"`
				Category    string `json:"category"`
			} `json:"alert"`
		}
		if err := json.Unmarshal(s.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if e.EventType != "alert" || e.Alert == nil {
			continue
		}
		sev := "medium"
		if e.Alert.Severity <= 1 {
			sev = "high"
		}
		records = append(records, securityRecord{Asset: e.Src + "->" + e.Dst, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: "SID-" + strconv.Itoa(e.Alert.SignatureID), RuleName: e.Alert.Signature, Message: r.Text(e.Alert.Signature), Severity: sev, Category: "other", FilePath: "network/" + safePath(e.Dst), RawEvidence: r.Text(fmt.Sprintf("source=%s destination=%s category=%s", e.Src, e.Dst, e.Alert.Category))}})
	}
	return records, s.Err()
}

type nmapRun struct {
	Hosts []struct {
		Address struct {
			Addr string `xml:"addr,attr"`
		} `xml:"address"`
		Ports []struct {
			Protocol string `xml:"protocol,attr"`
			Port     int    `xml:"portid,attr"`
			State    struct {
				State string `xml:"state,attr"`
			} `xml:"state"`
			Service struct {
				Name    string `xml:"name,attr"`
				Product string `xml:"product,attr"`
				Version string `xml:"version,attr"`
			} `xml:"service"`
		} `xml:"ports>port"`
	} `xml:"host"`
}
type nmapAdapter struct{}

func (nmapAdapter) Normalize(tool Tool, target Target, native []byte, r Redactor) ([]securityRecord, error) {
	var run nmapRun
	if err := xml.Unmarshal(native, &run); err != nil {
		return nil, err
	}
	var records []securityRecord
	for _, h := range run.Hosts {
		if !inScope(h.Address.Addr, target.Locator) {
			records = append(records, securityRecord{Asset: h.Address.Addr, Skipped: true})
			continue
		}
		for _, p := range h.Ports {
			if p.State.State != "open" {
				continue
			}
			msg := fmt.Sprintf("open %s/%d service=%s product=%s version=%s", p.Protocol, p.Port, p.Service.Name, p.Service.Product, p.Service.Version)
			records = append(records, securityRecord{Asset: h.Address.Addr + ":" + strconv.Itoa(p.Port), Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: "OPEN-SERVICE", RuleName: "Discovered open network service", Message: r.Text(msg), Severity: "info", Category: "misconfiguration", FilePath: "network/" + safePath(h.Address.Addr) + "/" + strconv.Itoa(p.Port), RawEvidence: r.Text(msg)}})
		}
	}
	return records, nil
}

func inScope(ip, scope string) bool {
	scope = strings.TrimSpace(scope)
	if scope == ip {
		return true
	}
	_, n, err := net.ParseCIDR(scope)
	return err == nil && n.Contains(net.ParseIP(ip))
}
func safePath(s string) string {
	s = strings.TrimSpace(s)
	s = regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(s, "_")
	if s == "" {
		return "unknown"
	}
	return s
}
func stringValue(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		return fmt.Sprint(v)
	}
	return ""
}
func sortSecurityRecords(rs []securityRecord) {
	sort.Slice(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		return strings.Join([]string{a.Asset, a.Record.RuleID, a.Record.FilePath, a.Record.Message, strconv.FormatBool(a.Examined), strconv.FormatBool(a.Skipped), strconv.FormatBool(a.Uncovered)}, "\x00") < strings.Join([]string{b.Asset, b.Record.RuleID, b.Record.FilePath, b.Record.Message, strconv.FormatBool(b.Examined), strconv.FormatBool(b.Skipped), strconv.FormatBool(b.Uncovered)}, "\x00")
	})
}
