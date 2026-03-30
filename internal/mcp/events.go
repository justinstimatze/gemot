package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/payments"
)

// EventsHandler returns an SSE endpoint that streams deliberation events.
//
// Query params:
//
//	deliberation_id — filter to a specific deliberation (optional)
//
// Auth: Bearer token (API key or admin secret), same as A2A.
func EventsHandler(svc *deliberation.Service, creditStore *payments.CreditStore, apiSecret string, rateLimiter *payments.RateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET required", http.StatusMethodNotAllowed)
			return
		}

		events := svc.Events()
		if events == nil {
			http.Error(w, "events not enabled", http.StatusServiceUnavailable)
			return
		}

		// Auth: same as A2A
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "Authorization: Bearer <api_key> required", http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")

		isAdmin := apiSecret != "" && token == apiSecret
		var keyID string
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

		// Rate limit check (but SSE is a single long-lived connection, so this is just the initial check)
		if !isAdmin && keyID != "" {
			if !rateLimiter.Allow(keyID) {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		// Optional filter
		filterDelibID := r.URL.Query().Get("deliberation_id")

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*") // vis.gemot.dev needs this

		ch, unsub := events.Subscribe(64)
		defer unsub()

		ping := time.NewTicker(15 * time.Second)
		defer ping.Stop()

		// Send initial connected event
		connected, _ := json.Marshal(map[string]string{"type": "connected"})
		fmt.Fprintf(w, "data: %s\n\n", connected)
		flusher.Flush()

		for {
			select {
			case event := <-ch:
				// Apply filter
				if filterDelibID != "" && event.DeliberationID != filterDelibID {
					continue
				}
				// Access check: non-admin users can only see deliberations they have access to
				if !isAdmin && filterDelibID == "" {
					if err := svc.CheckAccess(event.DeliberationID, keyID); err != nil {
						continue // skip events for deliberations this key can't access
					}
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
