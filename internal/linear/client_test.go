package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClientSetsHTTPTimeout(t *testing.T) {
	client := NewClient("key").(*httpLinearClient)
	if client.httpClient.Timeout != 30*time.Second {
		t.Fatalf("timeout = %v, want 30s", client.httpClient.Timeout)
	}
}

func TestLabelMutationsUseAtomicLinearMutations(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req graphqlRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		queries = append(queries, req.Query)
		switch {
		case strings.Contains(req.Query, "issueAddLabel"):
			_, _ = w.Write([]byte(`{"data":{"issueAddLabel":{"success":true}}}`))
		case strings.Contains(req.Query, "issueRemoveLabel"):
			_, _ = w.Write([]byte(`{"data":{"issueRemoveLabel":{"success":true}}}`))
		default:
			t.Fatalf("unexpected query: %s", req.Query)
		}
	}))
	defer server.Close()
	oldEndpoint := linearGraphQLEndpoint
	linearGraphQLEndpoint = server.URL
	defer func() { linearGraphQLEndpoint = oldEndpoint }()

	client := &httpLinearClient{apiKey: "key", httpClient: server.Client()}
	if err := client.AddLabel(context.Background(), "issue-1", "label-1"); err != nil {
		t.Fatalf("AddLabel() error = %v", err)
	}
	if err := client.RemoveLabel(context.Background(), "issue-1", "label-1"); err != nil {
		t.Fatalf("RemoveLabel() error = %v", err)
	}
	for _, query := range queries {
		if strings.Contains(query, "issueUpdate") || strings.Contains(query, "labels {") {
			t.Fatalf("mutation used read-modify-write path: %s", query)
		}
	}
}

func TestLinearMutationsReturnSuccessFalseErrors(t *testing.T) {
	tests := []struct {
		name    string
		call    func(*httpLinearClient) error
		resp    string
		wantErr string
	}{
		{
			name:    "add label",
			call:    func(c *httpLinearClient) error { return c.AddLabel(context.Background(), "issue", "label") },
			resp:    `{"data":{"issueAddLabel":{"success":false}}}`,
			wantErr: "issueAddLabel returned success=false",
		},
		{
			name:    "remove label",
			call:    func(c *httpLinearClient) error { return c.RemoveLabel(context.Background(), "issue", "label") },
			resp:    `{"data":{"issueRemoveLabel":{"success":false}}}`,
			wantErr: "issueRemoveLabel returned success=false",
		},
		{
			name:    "comment",
			call:    func(c *httpLinearClient) error { return c.AddComment(context.Background(), "issue", "body") },
			resp:    `{"data":{"commentCreate":{"success":false}}}`,
			wantErr: "commentCreate returned success=false",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.resp))
			}))
			defer server.Close()
			oldEndpoint := linearGraphQLEndpoint
			linearGraphQLEndpoint = server.URL
			defer func() { linearGraphQLEndpoint = oldEndpoint }()

			client := &httpLinearClient{apiKey: "key", httpClient: server.Client()}
			err := tt.call(client)
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
