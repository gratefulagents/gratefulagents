package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// jaegerClient queries the Jaeger HTTP API for trace data.
type jaegerClient struct {
	baseURL    string
	httpClient *http.Client
}

func newJaegerClient() *jaegerClient {
	endpoint := os.Getenv("JAEGER_QUERY_URL")
	if endpoint == "" {
		endpoint = deriveJaegerQueryURL(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	}
	if endpoint == "" {
		return nil
	}
	return &jaegerClient{
		baseURL:    strings.TrimRight(endpoint, "/"),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// deriveJaegerQueryURL derives the Jaeger query API URL from an OTLP
// endpoint: if OTLP is jaeger.jaeger.svc:4317 (or http://jaeger.jaeger.svc:4317),
// the query API is at http://jaeger.jaeger.svc:16686.
func deriveJaegerQueryURL(otelEndpoint string) string {
	host := strings.TrimSpace(otelEndpoint)
	if host == "" {
		return ""
	}
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+len("://"):]
	}
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return ""
	}
	return "http://" + host + ":16686"
}

// jaegerTrace is the top-level Jaeger API response.
type jaegerAPIResponse struct {
	Data []jaegerTrace `json:"data"`
}

type jaegerTrace struct {
	TraceID   string                   `json:"traceID"`
	Spans     []jaegerSpan             `json:"spans"`
	Processes map[string]jaegerProcess `json:"processes"`
}

type jaegerSpan struct {
	TraceID       string            `json:"traceID"`
	SpanID        string            `json:"spanID"`
	OperationName string            `json:"operationName"`
	References    []jaegerReference `json:"references"`
	StartTime     int64             `json:"startTime"` // microseconds
	Duration      int64             `json:"duration"`  // microseconds
	Tags          []jaegerTag       `json:"tags"`
	Logs          []jaegerLog       `json:"logs"`
	ProcessID     string            `json:"processID"`
}

type jaegerReference struct {
	RefType string `json:"refType"` // CHILD_OF, FOLLOWS_FROM
	TraceID string `json:"traceID"`
	SpanID  string `json:"spanID"`
}

type jaegerTag struct {
	Key   string      `json:"key"`
	Type  string      `json:"type"`
	Value interface{} `json:"value"`
}

type jaegerLog struct {
	Timestamp int64       `json:"timestamp"`
	Fields    []jaegerTag `json:"fields"`
}

type jaegerProcess struct {
	ServiceName string      `json:"serviceName"`
	Tags        []jaegerTag `json:"tags"`
}

// traceIDPattern matches valid Jaeger/OTel trace IDs (hex, up to 128 bits).
// The trace ID comes from AgentRun status written by agent pods, so validate
// before interpolating it into a URL.
var traceIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{1,32}$`)

// FetchTrace retrieves a trace by ID from Jaeger and converts to proto.
func (j *jaegerClient) FetchTrace(traceID string) (*platform.GetAgentTraceResponse, error) {
	if !traceIDPattern.MatchString(traceID) {
		return nil, fmt.Errorf("invalid trace ID %q: must be 1-32 hex characters", traceID)
	}
	url := fmt.Sprintf("%s/api/traces/%s", j.baseURL, traceID)
	resp, err := j.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("jaeger request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &platform.GetAgentTraceResponse{TraceId: traceID}, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("jaeger returned %d: %s", resp.StatusCode, body)
	}

	var apiResp jaegerAPIResponse
	// Bound the decoded body: a huge or hostile trace store response must not
	// balloon dashboard memory (64 MiB is far above any legitimate trace).
	const maxTraceResponseBytes = 64 << 20
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxTraceResponseBytes)).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decode jaeger response: %w", err)
	}
	if len(apiResp.Data) == 0 {
		return &platform.GetAgentTraceResponse{TraceId: traceID}, nil
	}

	trace := apiResp.Data[0]
	return convertJaegerTrace(trace), nil
}

func convertJaegerTrace(jt jaegerTrace) *platform.GetAgentTraceResponse {
	// Build child count map.
	childCount := make(map[string]int32)
	for _, js := range jt.Spans {
		if pid := parentSpanID(js); pid != "" {
			childCount[pid]++
		}
	}

	// Prefer the root span's process for the service name; the Processes
	// map iteration order is nondeterministic.
	serviceName := ""
	rootProcessID := ""
	var rootStart int64
	for _, js := range jt.Spans {
		if parentSpanID(js) == "" {
			rootProcessID = js.ProcessID
			break
		}
		if rootProcessID == "" || js.StartTime < rootStart {
			rootProcessID = js.ProcessID
			rootStart = js.StartTime
		}
	}
	if p, ok := jt.Processes[rootProcessID]; ok {
		serviceName = p.ServiceName
	} else {
		smallestKey := ""
		for k := range jt.Processes {
			if smallestKey == "" || k < smallestKey {
				smallestKey = k
			}
		}
		serviceName = jt.Processes[smallestKey].ServiceName
	}

	var minStart, maxEnd int64
	spans := make([]*platform.TraceSpan, 0, len(jt.Spans))
	for _, js := range jt.Spans {
		if minStart == 0 || js.StartTime < minStart {
			minStart = js.StartTime
		}
		end := js.StartTime + js.Duration
		if end > maxEnd {
			maxEnd = end
		}

		tags := make([]*platform.TraceSpanTag, 0, len(js.Tags))
		hasError := false
		for _, t := range js.Tags {
			v := tagValueString(t)
			tags = append(tags, &platform.TraceSpanTag{Key: t.Key, Value: v})
			switch t.Key {
			case "error":
				if v == "true" {
					hasError = true
				}
			case "otel.status_code":
				if v == "ERROR" {
					hasError = true
				}
			case "gen.success":
				if v == "false" {
					hasError = true
				}
			}
		}

		spans = append(spans, &platform.TraceSpan{
			SpanId:          js.SpanID,
			ParentSpanId:    parentSpanID(js),
			OperationName:   js.OperationName,
			StartTimeUnixUs: js.StartTime,
			DurationUs:      js.Duration,
			Kind:            js.OperationName,
			Tags:            tags,
			IsError:         hasError,
			ChildCount:      childCount[js.SpanID],
		})
	}

	var durationMS int64
	if maxEnd > minStart {
		durationMS = (maxEnd - minStart) / 1000
	}

	return &platform.GetAgentTraceResponse{
		TraceId:     jt.TraceID,
		Spans:       spans,
		DurationMs:  durationMS,
		ServiceName: serviceName,
	}
}

func parentSpanID(js jaegerSpan) string {
	for _, ref := range js.References {
		if ref.RefType == "CHILD_OF" {
			return ref.SpanID
		}
	}
	return ""
}

func tagValueString(t jaegerTag) string {
	switch v := t.Value.(type) {
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", v)
	}
}
