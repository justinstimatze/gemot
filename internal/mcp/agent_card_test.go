package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestAgentCardVersion is a guard against the version drift that bit us
// before agent-card.json was Go-rendered (it sat at 0.5.0 through six
// minor releases). With the current architecture this should be
// structurally impossible — the test exists as a tripwire if anyone
// reintroduces a hard-coded version.
func TestAgentCardVersion(t *testing.T) {
	got, ok := AgentCard()["version"].(string)
	if !ok {
		t.Fatalf("agent card version field missing or not a string: %v", AgentCard()["version"])
	}
	if got != Version {
		t.Errorf("agent card version = %q, Version constant = %q", got, Version)
	}
}

// TestChangelogHasCurrentVersion asserts that CHANGELOG.md documents the
// release pinned by the Version constant. Catches the "bumped the version
// but forgot to write release notes" path.
func TestChangelogHasCurrentVersion(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locating repo root: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatalf("reading CHANGELOG.md: %v", err)
	}
	// Match either historical format (`## v0.10.2 (...)`) or current
	// format (`## 0.11.0 — ...`). Anchored to a heading line so we don't
	// false-match a version mentioned in a description.
	headingRe := regexp.MustCompile(`(?m)^##\s+v?` + regexp.QuoteMeta(Version) + `\b`)
	if !headingRe.Match(body) {
		t.Errorf("CHANGELOG.md has no heading for version %s — promote the Unreleased section before tagging the release", Version)
	}
}

// TestServerJSONVersion guards the MCP-registry submission file
// (server.json) against drifting from the Version constant.
func TestServerJSONVersion(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locating repo root: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, "server.json"))
	if err != nil {
		t.Fatalf("reading server.json: %v", err)
	}
	var doc struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("parsing server.json: %v", err)
	}
	if doc.Version != Version {
		t.Errorf("server.json version = %q, Version constant = %q (run scripts/bump-version.sh to fix)", doc.Version, Version)
	}
}

// TestAgentCardActionCoverage asserts every MCP tool action defined in
// server.go is mentioned by at least one agent-card skill description.
// Scrapes the action list from the source file's switch cases so it
// stays accurate as actions are added or removed — no hand-maintained
// list to drift.
//
// New actions added without a matching agent-card mention will fail this
// test, prompting the developer to either describe the new capability or
// explicitly exempt it (see ignoredActions below for genuine exceptions).
func TestAgentCardActionCoverage(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locating repo root: %v", err)
	}
	// Scrape every grouped handler's action cases. server.go holds the six
	// original tools; account.go holds the account tool — both must be covered.
	var src []byte
	for _, f := range []string{"internal/mcp/server.go", "internal/mcp/account.go"} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		src = append(src, b...)
	}
	// Match `case "<lowercase_with_underscores>":` patterns inside the
	// action-dispatch switches. The scope is intentionally broad — any
	// case statement that looks like an action gets included.
	caseRe := regexp.MustCompile(`(?m)^\s*case "([a-z][a-z0-9_]*)":`)
	matches := caseRe.FindAllSubmatch(src, -1)
	if len(matches) == 0 {
		t.Fatalf("no action cases found in server.go — regex out of date?")
	}
	actions := map[string]struct{}{}
	for _, m := range matches {
		actions[string(m[1])] = struct{}{}
	}

	// Build a corpus from every skill name + description + tag, then
	// search for each action name as a *whole token* (bounded by
	// non-word characters). Token-bounded matching prevents a short
	// action like `get` or `up` from accidentally matching inside
	// "together" or "groups" — substring matching gave false positives
	// in early drafts of this test, which silently allowed coverage
	// gaps to slip past.
	var corpus strings.Builder
	for _, skill := range AgentCard()["skills"].([]map[string]any) {
		fmt.Fprintln(&corpus, skill["name"], "|", skill["description"])
		for _, tag := range skill["tags"].([]string) {
			fmt.Fprintln(&corpus, tag)
		}
	}
	body := strings.ToLower(corpus.String())

	// Actions whose absence from the agent card is intentional. Empty
	// today; populate with justification if needed. Prefer adding the
	// action name to a skill description over silencing it here — the
	// whole point of this test is that A2A discovery describes every
	// MCP capability.
	ignoredActions := map[string]string{}

	var missing []string
	for action := range actions {
		if _, skip := ignoredActions[action]; skip {
			continue
		}
		// Token-bounded match: action surrounded by non-word characters
		// or string boundaries. `\b` works for snake_case names because
		// underscore IS a word character in Go regex — so `get_context`
		// is matched as a single token, not split at the underscore.
		tokenRe := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(action) + `\b`)
		if !tokenRe.MatchString(body) {
			missing = append(missing, action)
		}
	}
	if len(missing) > 0 {
		t.Errorf("agent card skills don't mention %d MCP actions: %v\n"+
			"Either add the action name to a skill description in agent_card.go or "+
			"add it to ignoredActions with a justification.", len(missing), missing)
	}
}

// TestAgentCardHandler covers the method-gating, ETag, and If-None-Match
// branches that replaced the prior http.FileServer behavior. The card
// content is asserted by other tests in this file; here we just check
// the wire-protocol bits.
func TestAgentCardHandler(t *testing.T) {
	t.Run("GET returns 200 with body and ETag", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
		AgentCardHandler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want application/json", ct)
		}
		etag := rec.Header().Get("ETag")
		if etag == "" || etag[0] != '"' {
			t.Errorf("ETag missing or unquoted: %q", etag)
		}
		if rec.Body.Len() == 0 {
			t.Error("body is empty")
		}
	})

	t.Run("HEAD returns 200 with no body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodHead, "/.well-known/agent-card.json", nil)
		AgentCardHandler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("HEAD response leaked %d bytes of body", rec.Body.Len())
		}
		if rec.Header().Get("ETag") == "" {
			t.Error("HEAD response missing ETag")
		}
		// HEAD should carry the same headers as GET so caches and clients
		// can pre-flight (RFC 7231 §4.3.2).
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("HEAD content-type = %q, want application/json", ct)
		}
	})

	t.Run("If-None-Match returns 304", func(t *testing.T) {
		// First call to capture the ETag.
		rec1 := httptest.NewRecorder()
		AgentCardHandler(rec1, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
		etag := rec1.Header().Get("ETag")
		if etag == "" {
			t.Fatal("no ETag on first call")
		}
		// Second call with If-None-Match should be 304.
		rec2 := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
		req.Header.Set("If-None-Match", etag)
		AgentCardHandler(rec2, req)
		if rec2.Code != http.StatusNotModified {
			t.Errorf("status = %d, want 304", rec2.Code)
		}
		if rec2.Body.Len() != 0 {
			t.Errorf("304 response leaked %d bytes of body", rec2.Body.Len())
		}
	})

	t.Run("If-None-Match honors wildcard and lists per RFC 7232", func(t *testing.T) {
		// Capture the ETag for list-form testing.
		warm := httptest.NewRecorder()
		AgentCardHandler(warm, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
		etag := warm.Header().Get("ETag")
		if etag == "" {
			t.Fatal("no ETag on warm-up call")
		}
		cases := []struct {
			name   string
			header string
			want   int
		}{
			{"wildcard", `*`, http.StatusNotModified},
			{"list with our etag last", `"deadbeefdeadbeef", ` + etag, http.StatusNotModified},
			{"list with our etag first", etag + `, "feedfacefeedface"`, http.StatusNotModified},
			{"list of unknown etags", `"deadbeefdeadbeef", "feedfacefeedface"`, http.StatusOK},
			{"single unknown etag", `"deadbeefdeadbeef"`, http.StatusOK},
			{"empty header", "", http.StatusOK},
		}
		for _, tc := range cases {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
			if tc.header != "" {
				req.Header.Set("If-None-Match", tc.header)
			}
			AgentCardHandler(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s: status = %d, want %d", tc.name, rec.Code, tc.want)
			}
		}
	})

	t.Run("non-GET/HEAD returns 405 with Allow header", func(t *testing.T) {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/.well-known/agent-card.json", nil)
			AgentCardHandler(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s: status = %d, want 405", method, rec.Code)
			}
			if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
				t.Errorf("%s: Allow header = %q, want GET, HEAD", method, allow)
			}
		}
	})
}

// repoRoot walks up from the current test working directory looking for
// the go.mod file, so tests can locate top-level files like server.json
// regardless of which package they run in.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found walking up from working dir")
		}
		dir = parent
	}
}
