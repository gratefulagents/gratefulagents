package dashboard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
)

func TestAccessLogMiddlewarePassesThrough(t *testing.T) {
	handler := AccessLogMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("short and stout"))
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/platform.v1.PlatformService/ListAvailableModels", nil))

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if rec.Body.String() != "short and stout" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestIsAPIPath(t *testing.T) {
	for path, want := range map[string]bool{
		"/platform.v1.PlatformService/ListAvailableModels": true,
		"/auth.AuthService/Login":                          true,
		"/api/config":                                      true,
		"/assets/index-abc123.js":                          false,
		"/":                                                false,
	} {
		if got := isAPIPath(path); got != want {
			t.Errorf("isAPIPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestLoggingInterceptorPassesThroughErrors(t *testing.T) {
	wantErr := connect.NewError(connect.CodeUnavailable, errors.New("provider unreachable"))
	next := connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, wantErr
	})

	wrapped := LoggingInterceptor().WrapUnary(next)
	req := connect.NewRequest(&struct{}{})
	_, err := wrapped(context.Background(), req)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}
