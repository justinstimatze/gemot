package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestJsonErrorShimLeavesExistingJSONErrorsAlone: an error the inner handler
// already served as JSON (e.g. a JSON-RPC error object) must pass through
// verbatim — the shim should never double-wrap an already-correct body.
func TestJsonErrorShimLeavesExistingJSONErrorsAlone(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"Invalid Request"}}`)
	})
	rec := httptest.NewRecorder()
	jsonErrorShim(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	want := `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"Invalid Request"}}`
	if rec.Body.String() != want {
		t.Errorf("body = %q, want untouched %q", rec.Body.String(), want)
	}
}

// TestJsonErrorShimPassesThroughSuccess guards the other half of the
// contract: a normal 200 response — including one that streams multiple
// writes with explicit Flush calls, as the real MCP Streamable HTTP success
// path does — must reach the client byte-for-byte unbuffered and unaltered.
// Buffering a successful stream would break the real-time protocol behavior
// MCP clients depend on.
func TestJsonErrorShimPassesThroughSuccess(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("shimResponseWriter must implement http.Flusher for the streaming success path")
		}
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\"}\n\n")
		flusher.Flush()
		fmt.Fprint(w, ": keepalive\n\n")
		flusher.Flush()
	})
	rec := httptest.NewRecorder()
	jsonErrorShim(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q, want text/event-stream unchanged", ct)
	}
	want := "event: message\ndata: {\"jsonrpc\":\"2.0\"}\n\n: keepalive\n\n"
	if rec.Body.String() != want {
		t.Errorf("body = %q, want %q (streaming success must pass through untouched)", rec.Body.String(), want)
	}
}

// TestJsonErrorShimRewritesPlainTextError is the regression for the actual
// production bug: the MCP Go SDK's own Accept-header/body validation fails
// with a raw http.Error (text/plain), which an agent's JSON-RPC parser can't
// read. jsonErrorShim must rewrite it into gemot's standard JSON error body
// without changing the status code.
func TestJsonErrorShimRewritesPlainTextError(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mirrors exactly what the SDK does: http.Error sets Content-Type to
		// text/plain internally, regardless of what the caller might prefer.
		http.Error(w, "Accept must contain both 'application/json' and 'text/event-stream'", http.StatusBadRequest)
	})
	rec := httptest.NewRecorder()
	jsonErrorShim(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (must not change the status code)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json (this is the actual bug being fixed)", ct)
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
		Hint  string `json:"hint"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v (raw: %q)", err, rec.Body.String())
	}
	if !strings.Contains(body.Error, "Accept must contain") {
		t.Errorf("error message lost the original detail: %q", body.Error)
	}
	if body.Code == "" {
		t.Error("code is empty")
	}
}
