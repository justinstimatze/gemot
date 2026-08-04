package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// These tests exercise llm.go against a mock Anthropic endpoint. Before them the
// LLM path had never executed — NewLLM/complete/Argue/Vote/Reconsider compiled
// and were wired in, but no request had ever been made. An httptest server
// standing in for /v1/messages lets every line run without a real key, catching
// the plumbing bugs a first keyed run would otherwise hit.

// testLLM returns an LLM pointed at a mock server, with backoff disabled so
// retry tests do not actually sleep.
func testLLM(t *testing.T, handler http.HandlerFunc) *LLM {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &LLM{
		apiKey:    "test-key",
		baseURL:   srv.URL,
		model:     "claude-test",
		client:    srv.Client(),
		retryBase: 0,
	}
}

// anthropicMessage renders a single-block text response body.
func anthropicMessage(text string) string {
	return anthropicBlocks(text)
}

// anthropicBlocks renders a response with one text block per argument.
func anthropicBlocks(texts ...string) string {
	var b strings.Builder
	b.WriteString(`{"content":[`)
	for i, tx := range texts {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"type":"text","text":` + strconv.Quote(tx) + `}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

func testPersonality() Personality {
	return Personality{
		ID:          "aggressor",
		Name:        "Aggressor",
		Style:       "relentlessly attacking",
		Interests:   "initiative over material",
		Reservation: "never walk into a forced mate",
	}
}

func testCandidate(san, uci string, cp, bias, utility int) Candidate {
	return Candidate{
		UCI:     uci,
		SAN:     san,
		Eval:    Eval{CP: cp},
		Bias:    bias,
		Utility: utility,
		PV:      []string{san},
	}
}

func TestLLMCompleteConcatenatesTextBlocks(t *testing.T) {
	// A response may lead with a non-text or empty block; taking only Content[0]
	// would drop the answer. complete must stitch every text block together.
	l := testLLM(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(anthropicBlocks("", "Hello ", "world"))) //nolint:errcheck
	})
	got, err := l.complete("sys", "user", 64)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got != "Hello world" {
		t.Errorf("got %q, want %q", got, "Hello world")
	}
}

func TestLLMCompleteSetsAuthHeaders(t *testing.T) {
	l := testLLM(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing x-api-key: %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Error("missing anthropic-version header")
		}
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %q, want /v1/messages", r.URL.Path)
		}
		w.Write([]byte(anthropicMessage("ok"))) //nolint:errcheck
	})
	if _, err := l.complete("sys", "user", 64); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

func TestLLMCompleteRetriesThenSucceeds(t *testing.T) {
	var n int32
	l := testLLM(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) <= 2 {
			w.WriteHeader(529)                        // overloaded, twice
			w.Write([]byte(`{"error":"overloaded"}`)) //nolint:errcheck
			return
		}
		w.Write([]byte(anthropicMessage("recovered"))) //nolint:errcheck
	})
	got, err := l.complete("sys", "user", 64)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got != "recovered" {
		t.Errorf("got %q, want %q", got, "recovered")
	}
	if l.Calls != 3 {
		t.Errorf("Calls = %d, want 3 (two retries then success)", l.Calls)
	}
}

func TestLLMCompleteGivesUpAfterRetries(t *testing.T) {
	l := testLLM(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // 503, always
		w.Write([]byte(`{"error":"unavailable"}`))   //nolint:errcheck
	})
	_, err := l.complete("sys", "user", 64)
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "giving up") {
		t.Errorf("error = %v, want it to mention giving up", err)
	}
	if l.Calls != 4 {
		t.Errorf("Calls = %d, want 4 (maxAttempts)", l.Calls)
	}
}

func TestLLMCompleteDoesNotRetryClientError(t *testing.T) {
	l := testLLM(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)       // 400, our fault
		w.Write([]byte(`{"error":"bad request"}`)) //nolint:errcheck
	})
	_, err := l.complete("sys", "user", 64)
	if err == nil {
		t.Fatal("expected an error for a 400")
	}
	if l.Calls != 1 {
		t.Errorf("Calls = %d, want 1 (4xx must not retry)", l.Calls)
	}
}

func TestLLMVoteParsesFencedFloatAndClamps(t *testing.T) {
	own := testCandidate("e4", "e2e4", 30, 10, 40)
	peerMove := testCandidate("d4", "d2d4", 25, 0, 25)
	peer := Proposal{AgentID: "Defender", Move: peerMove, Argument: "solid center"}
	peerSeen := testCandidate("d4", "d2d4", 20, -5, 15)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"plain integer", `{"value": 2, "caveat": "convinced"}`, 2},
		{"fenced", "```json\n{\"value\": 1, \"caveat\": \"ok\"}\n```", 1},
		{"float 2.0", `{"value": 2.0, "caveat": "yes"}`, 2},
		{"rounds down", `{"value": 1.4, "caveat": "meh"}`, 1},
		{"rounds negative", `{"value": -1.6, "caveat": "no"}`, -2},
		{"clamps high", `{"value": 5, "caveat": "over"}`, 2},
		{"clamps low", `{"value": -7, "caveat": "under"}`, -2},
		{"prose wrapped", `Sure: {"value": 0, "caveat": "mixed"}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := testLLM(t, func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(anthropicMessage(tc.body))) //nolint:errcheck
			})
			got, caveat, err := l.Vote(testPersonality(), "White to move", own, peer, peerSeen)
			if err != nil {
				t.Fatalf("Vote: %v", err)
			}
			if got != tc.want {
				t.Errorf("value = %d, want %d", got, tc.want)
			}
			if caveat == "" {
				t.Error("expected a caveat to be parsed")
			}
		})
	}
}

func TestLLMVoteSurfacesMalformedJSON(t *testing.T) {
	own := testCandidate("e4", "e2e4", 30, 10, 40)
	peer := Proposal{AgentID: "Defender", Move: own, Argument: "x"}
	l := testLLM(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(anthropicMessage("no json here at all"))) //nolint:errcheck
	})
	if _, _, err := l.Vote(testPersonality(), "White to move", own, peer, own); err == nil {
		t.Fatal("expected an error parsing a response with no JSON")
	}
}

func TestLLMReconsiderNormalizesUCICase(t *testing.T) {
	// The options table is keyed by canonical lowercase UCI including the
	// promotion suffix. A model that echoes E7E8Q must still resolve.
	own := testCandidate("e8=Q", "e7e8q", 900, 0, 900)
	options := map[string]Candidate{
		"e7e8q": own,
		"e2e4":  testCandidate("e4", "e2e4", 30, 0, 30),
	}
	analysis := &Analysis{Consensus: []string{"promote"}}
	l := testLLM(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(anthropicMessage(`{"uci": " E7E8Q ", "reason": "queen"}`))) //nolint:errcheck
	})
	uci, _, err := l.Reconsider(testPersonality(), "White to move", own, options, analysis, "")
	if err != nil {
		t.Fatalf("Reconsider: %v", err)
	}
	if uci != "e7e8q" {
		t.Errorf("uci = %q, want normalized %q", uci, "e7e8q")
	}
}

func TestLLMReconsiderRejectsOffTableMove(t *testing.T) {
	own := testCandidate("e4", "e2e4", 30, 0, 30)
	options := map[string]Candidate{"e2e4": own}
	analysis := &Analysis{}
	l := testLLM(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(anthropicMessage(`{"uci": "a1a8", "reason": "invented"}`))) //nolint:errcheck
	})
	if _, _, err := l.Reconsider(testPersonality(), "White to move", own, options, analysis, ""); err == nil {
		t.Fatal("expected rejection of a move that is not on the table")
	}
}

func TestLLMArgueReturnsText(t *testing.T) {
	shortlist := []Candidate{
		testCandidate("e4", "e2e4", 30, 10, 40),
		testCandidate("d4", "d2d4", 25, 0, 25),
	}
	l := testLLM(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(anthropicMessage("1.e4 grabs the center and opens lines."))) //nolint:errcheck
	})
	got, err := l.Argue(testPersonality(), "White to move", "startpos", shortlist)
	if err != nil {
		t.Fatalf("Argue: %v", err)
	}
	if !strings.Contains(got, "e4") {
		t.Errorf("argument = %q, want it to mention the move", got)
	}
}

func TestLLMArgueRejectsEmptyShortlist(t *testing.T) {
	l := testLLM(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("Argue must not call the API with an empty shortlist")
	})
	if _, err := l.Argue(testPersonality(), "White to move", "startpos", nil); err == nil {
		t.Fatal("expected an error for an empty shortlist")
	}
}

func TestResolveSelection(t *testing.T) {
	options := map[string]Candidate{
		"e7e8q": testCandidate("e8=Q", "e7e8q", 900, 0, 900),
		"g1f3":  testCandidate("Nf3", "g1f3", 20, 0, 20),
		"e8g8":  testCandidate("O-O", "e8g8", 10, 0, 10),
	}
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"g1f3", "g1f3", true},     // exact UCI
		{" E7E8Q ", "e7e8q", true}, // UCI case + whitespace
		{"Nf3", "g1f3", true},      // SAN in the uci field
		{"O-O", "e8g8", true},      // castling SAN
		{"0-0", "e8g8", true},      // castling written with zeros
		{"a1a8", "", false},        // genuinely off the table
	}
	for _, tc := range cases {
		got, ok := resolveSelection(tc.in, options)
		if ok != tc.ok || got != tc.want {
			t.Errorf("resolveSelection(%q) = (%q, %v); want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestLLMReconsiderAcceptsSANInUCIField(t *testing.T) {
	// Live runs showed models returning SAN ("e5") in the uci field, which the
	// strict lookup rejected as off-table. It must resolve to canonical UCI.
	own := testCandidate("Nf6", "g8f6", 20, 0, 20)
	options := map[string]Candidate{
		"g8f6": own,
		"e7e5": testCandidate("e5", "e7e5", 15, 0, 15),
	}
	analysis := &Analysis{Consensus: []string{"strike the center"}}
	l := testLLM(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(anthropicMessage(`{"uci": "e5", "reason": "central break"}`))) //nolint:errcheck
	})
	uci, _, err := l.Reconsider(testPersonality(), "Black to move", own, options, analysis, "")
	if err != nil {
		t.Fatalf("Reconsider: %v", err)
	}
	if uci != "e7e5" {
		t.Errorf("uci = %q, want SAN 'e5' resolved to 'e7e5'", uci)
	}
}
