package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/td425/tpbx/internal/store"
)

// The agent softphone is a separate app (served at /phone) with its own
// session cookie, distinct from the admin console's tpbx_session. An agent
// authenticates with a SIP extension + secret, so the session gates only the
// softphone's own endpoints -- never the admin API.
const agentSessionCookie = "tpbx_agent"

type agentCtxKey string

const agentKey agentCtxKey = "agent_ext"

func agentFrom(r *http.Request) string {
	if ext, ok := r.Context().Value(agentKey).(string); ok {
		return ext
	}
	return ""
}

// agentToken extracts the session token from either a Bearer header (browser
// extension / cross-origin clients, which cannot ride the cookie) or the
// session cookie (the hosted same-origin app).
func agentToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[len("Bearer "):])
	}
	if c, err := r.Cookie(agentSessionCookie); err == nil {
		return c.Value
	}
	return ""
}

// requireAgent rejects requests without a valid agent session (cookie or token).
func (s *Server) requireAgent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := agentToken(r)
		if token == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		ext, ok := s.Agents.LookupSession(ctx, token)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session expired"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), agentKey, ext)))
	})
}

// agentCORS allows the browser extension (a cross-origin caller) to reach the
// agent API. Auth is by bearer token, not cookie, so reflecting the origin
// without credentials is safe.
func (s *Server) agentCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleAgentLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Extension string `json:"extension"`
		Password  string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	a, err := s.Agents.Authenticate(ctx, strings.TrimSpace(body.Extension), body.Password)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid extension or password"})
		return
	}
	token, err := s.Agents.CreateSession(ctx, a.Extension)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Resolve the device login to an agent identity, minting the account on
	// first sight (phase 3). This is what lets an agent gain a persistent
	// identity — campaigns, pause time, statistics — while every shipped
	// softphone keeps signing in with an extension and its SIP secret.
	// Best-effort: a phone that can register must never be blocked from
	// registering because the desk features could not be set up.
	agentUser := ""
	if acct, err := s.AgentAccounts.EnsureForExtension(ctx, a.Extension, a.DisplayName); err == nil {
		agentUser = acct.Username
		_ = s.Work.Log(ctx, acct.Username, acct.Extension, 0, "login", "")
	} else {
		slog.Warn("agent account provisioning failed", "extension", a.Extension, "err", err)
	}

	http.SetCookie(w, s.agentCookie(r, token))
	writeJSON(w, http.StatusOK, map[string]any{
		"extension":   a.Extension,
		"displayName": a.DisplayName,
		"agent":       agentUser, // "" when the desk features are unavailable
		"token":       token,     // for token-based clients (browser extension)
	})
}

func (s *Server) handleAgentLogout(w http.ResponseWriter, r *http.Request) {
	// Close the shift before the session goes: the log is what the agent's
	// time is reconstructed from, and a logout that leaves no mark reads as a
	// still-open shift for the rest of the day.
	//
	// Logout is deliberately outside requireAgent — signing out must work even
	// with a session the server no longer likes — so the agent is resolved from
	// the token here rather than from the request context, which carries
	// nothing on this route.
	if ext, ok := s.Agents.LookupSession(r.Context(), agentToken(r)); ok && ext != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		if acct, err := s.AgentAccounts.ByExtension(ctx, ext); err == nil {
			_ = s.Work.Log(ctx, acct.Username, acct.Extension, 0, "logout", "")
			_ = s.Work.SetPaused(ctx, acct.ID, true, "LOGOUT")
			_ = s.Work.SetCurrent(ctx, acct.ID, 0, 0)
		}
		cancel()
	}
	if c, err := r.Cookie(agentSessionCookie); err == nil {
		s.Agents.DeleteSession(r.Context(), c.Value)
	}
	clear := s.agentCookie(r, "")
	clear.MaxAge = -1
	http.SetCookie(w, clear)
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// handleAgentTelemetry ingests a softphone telemetry event (DND toggle,
// registration, or a completed call) for the authenticated agent. The extension
// always comes from the session, never the body, so an agent can only report
// events for itself. Best-effort: a store error is reported but the softphone
// treats telemetry as fire-and-forget.
func (s *Server) handleAgentTelemetry(w http.ResponseWriter, r *http.Request) {
	var ev store.SoftphoneEvent
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&ev); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	ev.Extension = agentFrom(r)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.Softphone.Record(ctx, ev); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleAgentCalls returns the agent's persisted call log (Recents), so it
// survives re-login/restart and syncs across devices. The extension is taken
// from the session.
func (s *Server) handleAgentCalls(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	calls, err := s.Softphone.AgentCalls(ctx, agentFrom(r), 200)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"calls": calls})
}

// handleAgentClearCalls clears the agent's own call log (manual clear).
func (s *Server) handleAgentClearCalls(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.Softphone.ClearAgentCalls(ctx, agentFrom(r)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

// handleAgentConfig returns everything the browser softphone needs to register
// and place calls: the SIP identity (with secret, since it is the agent's own),
// the WSS signalling URL, and ICE (STUN/TURN) servers -- all derived from the
// admin-editable WebRTC settings so it adapts per deployment.
func (s *Server) handleAgentConfig(w http.ResponseWriter, r *http.Request) {
	ext := agentFrom(r)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	a, err := s.Agents.Get(ctx, ext)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := s.Settings.GetWebRTC(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	host := s.resolveHost(r, cfg)
	policy := cfg.ICETransportPolicy
	if policy != "relay" {
		policy = "all"
	}

	// A reverse proxy terminating TLS exposes the WebSocket on its own host/path,
	// so an explicit override wins over the derived wss://<host>:<port>/ws.
	wsURL := strings.TrimSpace(cfg.WSSURL)
	if wsURL == "" {
		wssPort := cfg.WSSPort
		if wssPort == "" {
			wssPort = "8089"
		}
		wsURL = fmt.Sprintf("wss://%s:%s/ws", host, wssPort)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"extension":          a.Extension,
		"displayName":        a.DisplayName,
		"password":           a.Password, // the agent's own SIP secret, over their session
		"domain":             host,
		"wsUrl":              wsURL,
		"iceServers":         s.iceServers(cfg, host, ext),
		"iceTransportPolicy": policy,
	})
}

// resolveHost picks the address clients should reach signalling/media at, in
// order of precedence:
//  1. the WebRTC-specific public host (a per-WebRTC override),
//  2. the admin-editable System public domain (DB — the global default),
//  3. the install-time TPBX_DOMAIN env value (first-boot seed / fallback),
//  4. the request Host (correct for LAN installs the admin browses to).
//
// Steps 2–3 are why a domain change no longer needs an env edit + reinstall:
// the admin sets it once on the System settings tab and it wins over the env.
func (s *Server) resolveHost(r *http.Request, cfg store.WebRTCSettings) string {
	if cfg.PublicHost != "" {
		return cfg.PublicHost
	}
	if s.System != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if sys, err := s.System.Get(ctx); err == nil && sys.PublicDomain != "" {
			return sys.PublicDomain
		}
	}
	if s.Domain != "" {
		return s.Domain
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host
}

// iceServers builds the ICE server list from the WebRTC settings.
//
//   - STUN: offered when enabled.
//   - TURN "builtin": short-lived HMAC credentials minted from the coturn
//     shared secret (TURN REST API) so the secret never reaches the browser.
//   - TURN "static": fixed username/password for an external TURN service.
//   - TURN "none"/disabled: omitted.
func (s *Server) iceServers(cfg store.WebRTCSettings, host, ext string) []map[string]any {
	turnHost := cfg.TURNHost
	if turnHost == "" {
		turnHost = host
	}
	servers := []map[string]any{}
	if cfg.STUNEnabled {
		stun := splitCSV(cfg.STUNURLs) // explicit STUN servers (e.g. a public fallback)
		if len(stun) == 0 {
			stun = []string{"stun:" + hostPort(turnHost, "3478")}
		}
		servers = append(servers, map[string]any{"urls": stun})
	}
	if !cfg.TURNEnabled || cfg.TURNMode == "none" {
		return servers
	}

	// Explicit URLs win; otherwise derive standard coturn URLs from turnHost.
	urls := splitCSV(cfg.TURNURLs)
	if len(urls) == 0 {
		urls = []string{
			"turn:" + hostPort(turnHost, "3478") + "?transport=udp",
			"turn:" + hostPort(turnHost, "3478") + "?transport=tcp",
		}
		if cfg.TURNTLS {
			urls = append(urls, "turns:"+hostPort(turnHost, "5349")+"?transport=tcp")
		}
	}

	switch cfg.TURNMode {
	case "static":
		if cfg.TURNStaticUser == "" {
			return servers // misconfigured; fall back to STUN only
		}
		servers = append(servers, map[string]any{
			"urls": urls, "username": cfg.TURNStaticUser, "credential": cfg.TURNStaticPassword,
		})
	default: // "builtin"
		if s.TURNSecret == "" {
			return servers // no coturn secret provisioned
		}
		ttl := s.TURNTTL
		if ttl <= 0 {
			ttl = time.Hour
		}
		username := strconv.FormatInt(time.Now().Add(ttl).Unix(), 10) + ":" + ext
		mac := hmac.New(sha1.New, []byte(s.TURNSecret))
		mac.Write([]byte(username))
		servers = append(servers, map[string]any{
			"urls":       urls,
			"username":   username,
			"credential": base64.StdEncoding.EncodeToString(mac.Sum(nil)),
		})
	}
	return servers
}

// hostPort appends :port to host unless host already carries a port, so a
// value like "stun.l.google.com:19302" is used verbatim instead of being
// double-ported into an invalid "stun.l.google.com:19302:3478".
func hostPort(host, port string) string {
	if strings.Contains(host, ":") {
		return host
	}
	return host + ":" + port
}

func splitCSV(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// agentCookie builds the agent session cookie, marking it Secure when the
// request arrived over TLS (directly or via a terminating proxy).
func (s *Server) agentCookie(r *http.Request, token string) *http.Cookie {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	return &http.Cookie{
		Name:     agentSessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((16 * time.Hour).Seconds()),
	}
}
