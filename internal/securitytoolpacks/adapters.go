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
		"json-records": generic, "zap-json": generic, "schemathesis-json": generic, "restler-json": generic, "nuclei-jsonl": generic, "sslyze-json": generic, "testssl-json": generic, "openssl-json": generic, "tshark-json": generic,
		"har": generic, "junit": generic,
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
		if c.Actual == c.Expected || !(c.Actual >= 200 && c.Actual < 300) || !(c.Expected == 401 || c.Expected == 403 || c.Expected == 404) {
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
		if v.Passed || v.Skipped {
			continue
		}
		ev := r.Text(fmt.Sprintf("suite=%s vector=%s expected=%s actual=%s context=%v", v.Suite, v.ID, v.Expected, v.Actual, v.Context))
		records = append(records, securityRecord{Asset: v.ID, Record: ScannerRecord{Tool: tool.Name, ToolVersion: tool.Version, RuleID: "CRYPTO-KAT-MISMATCH", RuleName: "Cryptographic known-answer mismatch", Message: "implementation failed known-answer vector " + v.ID, Severity: "high", Category: "crypto", FilePath: "vectors/" + safePath(v.Suite) + "/" + safePath(v.ID) + ".json", CWE: "CWE-327", RawEvidence: ev}})
	}
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
	sort.Slice(rs, func(i, j int) bool { return rs[i].Asset < rs[j].Asset })
}
