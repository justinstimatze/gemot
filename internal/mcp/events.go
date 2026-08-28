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

func EventsHandler(svc *deliberation.Service, creditStore *payments.CreditStore, apiSecret string, rateLimiter *payments.RateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		events := svc.Events()
		if events == nil {
			jsonError(w, http.StatusServiceUnavailable, "events_disabled", "events not enabled", "")
			return
		}

		var isAdmin bool
		var keyID string
		filterDelibID := r.URL.Query().Get("deliberation_id")
		preScopedAuth := false
		var groupDelibIDs map[string]bool // for share token auth: set of deliberation IDs in the group

		// Auth path 1: join_code query param (anonymous, scoped to one deliberation)
		ctx := r.Context()
		if jc := r.URL.Query().Get("join_code"); jc != "" {
			_, d, err := svc.LookupJoinCode(ctx, jc)
			if err != nil || d == nil {
				jsonError(w, http.StatusNotFound, "join_code_not_found", "invalid or expired join code", "request a new join code")
				return
			}
			filterDelibID = d.ID
			preScopedAuth = true
		} else if st := r.URL.Query().Get("share_token"); st != "" {
			// Auth path 2: share_token query param (anonymous, scoped to a group of deliberations)
			groupID, err := svc.LookupShareToken(ctx, st)
			if err != nil || groupID == "" {
				jsonError(w, http.StatusNotFound, "share_token_not_found", "invalid or expired share token", "")
				return
			}
			delibs, err := svc.ListByGroup(ctx, groupID, 500, 0, "")
			if err != nil || len(delibs) == 0 {
				jsonError(w, http.StatusNotFound, "group_not_found", "group not found", "")
				return
			}
			groupDelibIDs = make(map[string]bool, len(delibs))
			for _, d := range delibs {
				groupDelibIDs[d.ID] = true
			}
			preScopedAuth = true // reuse the "pre-scoped" flag to skip per-event access checks
		} else {
			// Auth path 3: Bearer token from header, or an API key (only, never
			// the admin secret) via ?token= for browser EventSource clients
			// that can't set custom headers. The admin secret is accepted
			// ONLY from the Authorization header, deliberately never from a
			// query parameter: a query string ends up in server access logs,
			// browser history, and any Referer header a client sends onward
			// -- an acceptable exposure for a scoped, revocable API key, not
			// for the one credential that grants admin access to everything.
			headerToken := bearerToken(r.Header.Get("Authorization"))
			queryToken := r.URL.Query().Get("token")
			token := headerToken
			if token == "" {
				token = queryToken
			}
			if apiSecret == "" {
				// Dev mode: no auth required
				isAdmin = true
			} else if token == "" {
				jsonError(w, http.StatusUnauthorized, "missing_credential", "authorization required (Bearer header, ?token=, or ?join_code=)", "")
				return
			} else {
				isAdmin = headerToken != "" && isAdminToken(headerToken, apiSecret)
				if !isAdmin {
					if creditStore == nil || !strings.HasPrefix(token, "gmt_") {
						jsonError(w, http.StatusUnauthorized, "invalid_api_key", "invalid API key", "")
						return
					}
					if valid, _ := creditStore.KeyActive(token); !valid {
						jsonError(w, http.StatusUnauthorized, "invalid_api_key", "invalid or expired API key", "")
						return
					}
					keyID = payments.KeyID(token)
				}
			}

			// Rate limit: initial check only (SSE is long-lived)
			if !isAdmin && keyID != "" {
				if !rateLimiter.Allow(keyID) {
					jsonError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded", "retry after a short delay")
					return
				}
			}
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			jsonError(w, http.StatusInternalServerError, "streaming_unsupported", "streaming unsupported", "")
			return
		}

		// If filtering to a specific deliberation, verify access upfront (skip for join_code — already scoped)
		if filterDelibID != "" && !isAdmin && !preScopedAuth {
			if err := svc.CheckAccess(ctx, filterDelibID, keyID); err != nil {
				jsonError(w, http.StatusForbidden, "access_denied", "access denied", "")
				return
			}
		}

		// Cache accessible deliberation IDs for non-admin unfiltered streams.
		// Refreshed every 60s to pick up new access grants.
		var accessCache map[string]bool
		var accessCacheTime time.Time
		checkAccess := func(delibID string) bool {
			if isAdmin || preScopedAuth || filterDelibID != "" {
				return true // already verified at connection time
			}
			if time.Since(accessCacheTime) > 60*time.Second || accessCache == nil {
				accessCache = make(map[string]bool)
				accessCacheTime = time.Now()
			}
			if allowed, cached := accessCache[delibID]; cached {
				return allowed
			}
			allowed := svc.CheckAccess(ctx, delibID, keyID) == nil
			accessCache[delibID] = allowed
			return allowed
		}

		// Atomically check limit and subscribe in one lock acquisition (no TOCTOU race).
		ch, unsub, err := events.SubscribeIfUnder(100, 64)
		if err != nil {
			jsonError(w, http.StatusServiceUnavailable, "connection_limit", err.Error(), "retry shortly, or use a share_token stream")
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
		fmt.Fprintf(w, "data: %s\n\n", connected) //nolint:errcheck
		flusher.Flush()

		for {
			select {
			case event, ok := <-ch:
				if !ok {
					// Event bus shut down (or, for a plain Subscribe caller,
					// explicitly closed) -- without this check, reading from
					// a closed channel never blocks and returns the zero
					// Event forever, busy-spinning this loop until the
					// request context ends instead of ending the stream now.
					return
				}
				if filterDelibID != "" && event.DeliberationID != filterDelibID {
					continue
				}
				if groupDelibIDs != nil && !groupDelibIDs[event.DeliberationID] {
					continue
				}
				if !checkAccess(event.DeliberationID) {
					continue
				}
				data, err := deliberation.MarshalEvent(event)
				if err != nil {
					continue
				}
				fmt.Fprintf(w, "data: %s\n\n", data) //nolint:errcheck
				flusher.Flush()
			case <-ping.C:
				fmt.Fprintf(w, "data: {\"type\":\"ping\"}\n\n") //nolint:errcheck
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
