package mcp

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/payments"
)

var sseConnectionCount atomic.Int64

const maxSSEConnections = 100

// EventsHandler returns an SSE endpoint that streams deliberation events.
//
// Query params:
//
//	deliberation_id — filter to a specific deliberation (optional)
//	token           — Bearer token (alternative to Authorization header, for browser EventSource)
//	join_code       — join code for anonymous read-only access (scoped to one deliberation)
//
// Auth: Bearer token via header or query param, OR join_code query param.
func EventsHandler(svc *deliberation.Service, creditStore *payments.CreditStore, apiSecret string, rateLimiter *payments.RateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		events := svc.Events()
		if events == nil {
			http.Error(w, "events not enabled", http.StatusServiceUnavailable)
			return
		}

		// Connection limit
		current := sseConnectionCount.Add(1)
		defer sseConnectionCount.Add(-1)
		if current > maxSSEConnections {
			http.Error(w, "too many connections", http.StatusServiceUnavailable)
			return
		}

		var isAdmin bool
		var keyID string
		filterDelibID := r.URL.Query().Get("deliberation_id")
		joinCodeAuth := false

		// Auth path 1: join_code query param (anonymous, scoped to one deliberation)
		if jc := r.URL.Query().Get("join_code"); jc != "" {
			_, d, err := svc.LookupJoinCode(jc)
			if err != nil || d == nil {
				http.Error(w, "invalid or expired join code", http.StatusNotFound)
				return
			}
			filterDelibID = d.ID
			joinCodeAuth = true
		} else {
			// Auth path 2: Bearer token from header or ?token= query param
			token := ""
			if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
				token = strings.TrimPrefix(auth, "Bearer ")
			} else if t := r.URL.Query().Get("token"); t != "" {
				token = t
			}
			if token == "" {
				http.Error(w, "authorization required (Bearer header, ?token=, or ?join_code=)", http.StatusUnauthorized)
				return
			}

			isAdmin = apiSecret != "" && subtle.ConstantTimeCompare([]byte(token), []byte(apiSecret)) == 1
			if !isAdmin {
				if creditStore == nil || !strings.HasPrefix(token, "gmt_") {
					http.Error(w, "invalid API key", http.StatusUnauthorized)
					return
				}
				if valid, _ := creditStore.ValidateKey(token); !valid {
					http.Error(w, "invalid or expired API key", http.StatusUnauthorized)
					return
				}
				keyID = payments.KeyID(token)
			}

			// Rate limit: initial check only (SSE is long-lived)
			if !isAdmin && keyID != "" {
				if !rateLimiter.Allow(keyID) {
					http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
					return
				}
			}
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		// If filtering to a specific deliberation, verify access upfront (skip for join_code — already scoped)
		if filterDelibID != "" && !isAdmin && !joinCodeAuth {
			if err := svc.CheckAccess(filterDelibID, keyID); err != nil {
				http.Error(w, "access denied", http.StatusForbidden)
				return
			}
		}

		// Cache accessible deliberation IDs for non-admin unfiltered streams.
		// Refreshed every 60s to pick up new access grants.
		var accessCache map[string]bool
		var accessCacheTime time.Time
		checkAccess := func(delibID string) bool {
			if isAdmin || joinCodeAuth || filterDelibID != "" {
				return true // already verified at connection time
			}
			if time.Since(accessCacheTime) > 60*time.Second || accessCache == nil {
				accessCache = make(map[string]bool)
				accessCacheTime = time.Now()
			}
			if allowed, cached := accessCache[delibID]; cached {
				return allowed
			}
			allowed := svc.CheckAccess(delibID, keyID) == nil
			accessCache[delibID] = allowed
			return allowed
		}

		// Atomically check limit and subscribe in one lock acquisition (no TOCTOU race).
		ch, unsub, err := events.SubscribeIfUnder(100, 64)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		defer unsub()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		if origin := allowedCORSOrigin(r); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}

		ping := time.NewTicker(15 * time.Second)
		defer ping.Stop()

		// Send initial connected event
		connected, _ := json.Marshal(map[string]string{"type": "connected"})
		fmt.Fprintf(w, "data: %s\n\n", connected)
		flusher.Flush()

		for {
			select {
			case event := <-ch:
				if filterDelibID != "" && event.DeliberationID != filterDelibID {
					continue
				}
				if !checkAccess(event.DeliberationID) {
					continue
				}
				data, err := deliberation.MarshalEvent(event)
				if err != nil {
					continue
				}
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			case <-ping.C:
				fmt.Fprintf(w, "data: {\"type\":\"ping\"}\n\n")
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
}

// allowedCORSOrigin returns the request Origin if it matches a known allowed origin,
// or empty string if it should not be reflected.
func allowedCORSOrigin(r *http.Request) string {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return ""
	}
	allowed := map[string]bool{
		"https://gemot.dev":     true,
		"https://vis.gemot.dev": true,
		"http://localhost":      true,
	}
	// Check exact match or localhost with port
	if allowed[origin] {
		return origin
	}
	if strings.HasPrefix(origin, "http://localhost:") {
		return origin
	}
	return ""
}
