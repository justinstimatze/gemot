package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/justinstimatze/gemot/internal/auth"
	"github.com/justinstimatze/gemot/internal/deliberation"
)

// EnvelopeMode controls how strictly the request-envelope signing middleware
// enforces signatures. The modes parallel DMARC's none/quarantine/reject so
// operators can deploy envelope signing in stages without breaking existing
// unsigned clients.
type EnvelopeMode int

const (
	// EnvelopeOff disables envelope verification entirely. The middleware is a
	// pass-through. This is the default for existing deployments so introducing
	// the feature does not break any client.
	EnvelopeOff EnvelopeMode = iota

	// EnvelopeAdvisory is the rollout mode: unsigned requests (no envelope
	// headers at all) pass through unchanged so existing clients keep working.
	// Any request that DOES include envelope headers is held to the full
	// required-mode contract — partial headers, stale timestamps, invalid
	// signatures, and replayed nonces are all rejected with 401. This surfaces
	// client-side signing bugs during rollout without blocking the long tail
	// of unsigned callers. Intended for operator soak-testing before flipping
	// to required mode.
	EnvelopeAdvisory

	// EnvelopeRequired rejects any request to this middleware's routes unless a
	// valid envelope signature is attached. Use after confirming all legitimate
	// clients are upgraded.
	EnvelopeRequired
)

// ParseEnvelopeMode maps the GEMOT_ENVELOPE_MODE env string to a mode. Unknown
// values fall back to EnvelopeOff with a warning so a typo doesn't silently
// relax a stricter intent.
func ParseEnvelopeMode(s string) (EnvelopeMode, error) {
	switch s {
	case "", "off":
		return EnvelopeOff, nil
	case "advisory":
		return EnvelopeAdvisory, nil
	case "required":
		return EnvelopeRequired, nil
	default:
		return EnvelopeOff, fmt.Errorf("unknown envelope mode %q (use off|advisory|required)", s)
	}
}

// HTTP headers carrying envelope signature metadata. Kept short and
// hyphen-cased to match existing X-* conventions; clients emit base64 for the
// signature bytes.
const (
	HeaderEnvelopeAgentID   = "X-Gemot-Agent-Id"
	HeaderEnvelopeNonce     = "X-Gemot-Nonce"
	HeaderEnvelopeTimestamp = "X-Gemot-Timestamp"
	HeaderEnvelopeSignature = "X-Gemot-Signature"
	HeaderEnvelopeAlgo      = "X-Gemot-Algo"
)

// EnvelopeMiddleware returns an http.Handler middleware that verifies optional
// request-envelope signatures. It buffers request bodies up to maxBody bytes
// so it can hash them before handing off to the next handler — requests larger
// than maxBody are rejected (a signed body must be fully readable for the hash
// to be reproducible).
//
// Mode EnvelopeOff makes the middleware a zero-cost pass-through: no header
// reads, no body buffering. This is important for /mcp where request volume
// is high and most clients won't sign.
func EnvelopeMiddleware(svc *deliberation.Service, cache auth.NonceCache, mode EnvelopeMode, maxBody int64) func(http.Handler) http.Handler {
	if maxBody <= 0 {
		maxBody = 1 << 20 // 1 MiB default — generous for JSON-RPC payloads
	}
	return func(next http.Handler) http.Handler {
		if mode == EnvelopeOff {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Non-POST requests (SSE GET for subscriptions, etc.) carry no
			// state-changing body to sign. Exempting them keeps the existing
			// SSE establishment path working even in required mode. If a use
			// case ever needs signed GETs, add a second middleware branded
			// for that purpose rather than complicating this one.
			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}

			agentID := r.Header.Get(HeaderEnvelopeAgentID)
			nonce := r.Header.Get(HeaderEnvelopeNonce)
			tsStr := r.Header.Get(HeaderEnvelopeTimestamp)
			sigB64 := r.Header.Get(HeaderEnvelopeSignature)
			algo := r.Header.Get(HeaderEnvelopeAlgo)
			if algo == "" {
				algo = auth.AlgoEd25519
			}
			present := agentID != "" || nonce != "" || tsStr != "" || sigB64 != ""

			switch {
			case !present && mode == EnvelopeAdvisory:
				// No envelope provided, advisory mode — pass through without warning
				// (advisory is specifically for a mixed unsigned/signed fleet).
				next.ServeHTTP(w, r)
				return
			case !present && mode == EnvelopeRequired:
				envelopeReject(w, "envelope signature required but no signature headers present")
				return
			}

			// Partial headers are always an error — a missing piece means either
			// client bug or MITM tampering. Fail closed regardless of mode.
			if agentID == "" || nonce == "" || tsStr == "" || sigB64 == "" {
				envelopeReject(w, "envelope headers incomplete — all of agent_id, nonce, timestamp, signature required")
				return
			}

			ts, err := strconv.ParseInt(tsStr, 10, 64)
			if err != nil {
				envelopeReject(w, "envelope timestamp is not an integer")
				return
			}
			now := time.Now()
			if err := auth.ValidateTimestamp(ts, now); err != nil {
				envelopeReject(w, "envelope timestamp outside replay window")
				return
			}

			// Buffer the body so we can hash it and still hand the original bytes
			// to downstream handlers. The LimitReader caps adversarial size.
			body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
			if err != nil {
				envelopeReject(w, "failed to read request body")
				return
			}
			if int64(len(body)) > maxBody {
				envelopeReject(w, "request body exceeds envelope size limit")
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			bodyHash := sha256.Sum256(body)

			sig, err := base64.StdEncoding.DecodeString(sigB64)
			if err != nil {
				envelopeReject(w, "envelope signature is not valid base64")
				return
			}

			// Key lookup uses the scoped form (keys are stored under
			// "<keyID>:alice" in hosted mode), but the canonical payload uses
			// the unscoped form the client signed with. This mirrors the
			// per-action-signature fix in SubmitPositionWithSigningID. When
			// no keyID is in context (admin mode, open federation, sandbox),
			// scopeAgentID is a no-op and both forms coincide.
			storedAgentID := scopeAgentID(r.Context(), agentID)
			pubkey, regAlgo, keyErr := svc.GetActiveAgentKey(r.Context(), storedAgentID)
			if errors.Is(keyErr, deliberation.ErrAgentKeyNotFound) {
				envelopeReject(w, fmt.Sprintf("no active key registered for agent %q", agentID))
				return
			}
			if keyErr != nil {
				// Real DB error — don't leak internals to the client but log.
				slog.Error("envelope: key lookup failed", "agent", agentID, "err", keyErr)
				envelopeReject(w, "envelope verification unavailable")
				return
			}
			if regAlgo != "" && regAlgo != algo {
				envelopeReject(w, fmt.Sprintf("envelope algo mismatch: header says %q, registered key uses %q", algo, regAlgo))
				return
			}

			// Sign the full request target (path + query) so an attacker can't
			// reroute a captured envelope to a different query-parameter
			// variant of the same path. RequestURI() yields "/path?query" if
			// a query is present, otherwise just "/path".
			method := r.Method + " " + r.URL.RequestURI()
			msg := auth.EnvelopePayload(agentID, method, bodyHash[:], nonce, ts)
			if err := auth.Verify(algo, pubkey, msg, sig); err != nil {
				slog.Warn("envelope: signature verify failed", "agent", agentID, "method", method)
				envelopeReject(w, "envelope signature did not verify")
				return
			}

			// Signature is valid — now claim the nonce. Done after verify so that
			// an invalid signature doesn't cause legitimate retries (same nonce +
			// fixed signature) to fail with a replay error.
			if err := cache.Observe(nonce, now); err != nil {
				envelopeReject(w, "nonce already seen within replay window")
				return
			}

			if mode == EnvelopeAdvisory {
				slog.Info("envelope: signature verified (advisory mode)", "agent", agentID, "method", method)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// envelopeReject returns a uniform 401 Unauthorized. The reason is logged but
// also surfaced in the response body for dev-ergonomics — production deployments
// behind a reverse proxy may still want to strip the body.
func envelopeReject(w http.ResponseWriter, reason string) {
	slog.Info("envelope: rejected", "reason", reason)
	http.Error(w, "envelope: "+reason, http.StatusUnauthorized)
}
