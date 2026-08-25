package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// jsonErrorShim wraps a handler gemot doesn't fully own (the MCP Go SDK's
// Streamable HTTP handler) so any error response it produces with a
// non-JSON Content-Type is rewritten into gemot's standard {error,code,hint}
// body before it reaches the client. The SDK's own validation failures — a
// malformed JSON-RPC body, or an Accept header that omits either
// application/json or text/event-stream (Streamable HTTP requires both) —
// surface as plain-text http.Error calls internal to the library; agents
// parsing gemot's responses should see one consistent JSON error envelope
// regardless of which layer produced the failure.
//
// Only intercepts non-2xx responses whose Content-Type is not already JSON.
// A successful response — including the streaming text/event-stream 200 this
// same handler produces on the happy path — passes through unbuffered via
// Flush, so real-time MCP behavior real clients depend on is untouched.
func jsonErrorShim(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &shimResponseWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		sw.finalize()
	})
}

// Flush finalizes any buffered error first (so a handler that tries to
// stream after an early plain-text http.Error doesn't hang waiting for
// bytes that were diverted into our buffer), then passes through so the
// streaming success path is unaffected.
func (w *shimResponseWriter) Flush() {
	w.finalize()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *shimResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.intercept {
		return w.buf.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

// WriteHeader decides whether to intercept: a non-2xx status with a
// non-JSON Content-Type is held back (buffered) until finalize rewrites it;
// everything else — success, or an error the caller already made JSON —
// passes straight through.
func (w *shimResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	ct := w.Header().Get("Content-Type")
	if status >= 400 && !strings.Contains(ct, "application/json") {
		w.intercept = true
		return // hold the real WriteHeader until finalize decides the body
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *shimResponseWriter) finalize() {
	if !w.intercept {
		return
	}
	w.intercept = false
	message := strings.TrimSpace(w.buf.String())
	if message == "" {
		message = http.StatusText(w.status)
	}
	w.ResponseWriter.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.ResponseWriter.WriteHeader(w.status)
	body, _ := json.Marshal(map[string]string{
		"error": message,
		"code":  "mcp_transport_error",
		"hint":  "malformed request, or Accept must include both application/json and text/event-stream per the MCP Streamable HTTP spec",
	})
	_, _ = w.ResponseWriter.Write(body)
}

type shimResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	buf         bytes.Buffer
	intercept   bool
}
