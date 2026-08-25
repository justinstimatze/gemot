package payments

import (
	"encoding/json"
	"net/http"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// WriteJSONError writes a structured JSON error body: a human-readable
// message, a stable machine-readable code, and an optional resolution hint
// telling the caller what to do next. Canonical error shape for every
// API-surfaced endpoint (MCP payment middleware, A2A auth, checkout, the
// gemot.dev HTTP routes) — agents parsing responses see one consistent
// shape instead of a mix of plain-text and ad-hoc JSON strings.
//
// Sets Content-Type explicitly before WriteHeader: net/http's http.Error
// always stamps "text/plain" regardless of the body you pass it, so a call
// site that did http.Error(w, `{"error":"..."}`, code) was serving JSON
// text mislabeled as text/plain — undetectable by an agent's Accept-based
// parser. WriteJSONError is the fix, used everywhere that mislabeling
// occurred.
func WriteJSONError(w http.ResponseWriter, status int, code, message, hint string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	body := struct {
		Error string `json:"error"`
		Code  string `json:"code"`
		Hint  string `json:"hint,omitempty"`
	}{Error: message, Code: code, Hint: hint}
	_ = json.NewEncoder(w).Encode(body)
}
