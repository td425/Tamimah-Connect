// Package api wires the HTTP surface of the TPBX GUI backend: a JSON API under
// /api, the live-events WebSocket under /ws, and the static single-page app
// everywhere else.
package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/td425/tpbx/internal/ari"
	"github.com/td425/tpbx/internal/db"
	"github.com/td425/tpbx/internal/store"
	"github.com/td425/tpbx/internal/ws"
)

// Server holds the dependencies shared across HTTP handlers.
type Server struct {
	DB             *db.DB
	ARI            *ari.Client
	Hub            *ws.Hub
	Ext            *store.Extensions
	Trunks         *store.Trunks
	Routes         *store.Routes
	IVRs           *store.IVRs
	Lists          *store.Lists
	Leads          *store.Leads
	Campaigns      *store.Campaigns
	AgentAccounts  *store.AgentAccounts
	LeadCalls      *store.LeadCalls
	Work           *store.AgentWork
	Hopper         *store.Hopper
	Ingroups       *store.Ingroups
	Transports     *store.Transports
	PJSIP          *store.PJSIPSettingsStore
	Users          *store.Users
	Roles          *store.Roles
	Agents         *store.Agents
	Settings       *store.Settings
	System         *store.System
	Analytics      *store.Analytics
	Softphone      *store.SoftphoneStore
	Dashboard      *store.Dashboard
	CDR            *store.CDR
	ApiTokens      *store.ApiTokens
	DialplanFile   string // generated routing dialplan Asterisk #includes
	TransportsFile string // generated PJSIP transports include Asterisk loads
	PJSIPFile      string // generated PJSIP [global]/[system] include Asterisk loads
	WebDir         string // built admin frontend (index.html, assets/)
	AgentWebDir    string // built agent softphone frontend, served under /phone
	SoundsDir      string // where uploaded IVR prompts are stored (under Asterisk sounds)
	SoundsPrefix   string // dialplan reference prefix for uploaded prompts (e.g. "tpbx")

	// WebRTC/signalling parameters handed to the browser softphone.
	Domain     string        // public FQDN/IP for WSS + TURN ("" = derive from request)
	WSSPort    string        // Asterisk secure-WebSocket port (default 8089)
	TURNSecret string        // coturn static-auth-secret ("" disables TURN)
	TURNTTL    time.Duration // lifetime of a minted TURN credential

	// Infra carries the bootstrap/credential config (DB URL, ARI/AMI, file
	// paths) that cannot safely move to the DB. It is displayed read-only and
	// masked on the System settings tab; never editable from the UI.
	Infra InfraInfo

	// ReloadQueues asks Asterisk to re-read realtime queue membership. Injected
	// by main so the api package stays decoupled from AMI. Nil when
	// unavailable, in which case membership still takes effect on the next
	// queue load — it is a nudge, not a requirement.
	ReloadQueues func(context.Context) error

	// RestartAsterisk performs a full Asterisk restart (to re-bind transports).
	// Injected by main so the api package stays decoupled from AMI/config. Nil
	// when unavailable, in which case the restart endpoint reports so.
	RestartAsterisk func(context.Context) error
}

// InfraInfo is the set of infrastructure values surfaced (read-only) on the
// System settings tab. Secrets embedded in these (e.g. the DB password) are
// masked before the values ever leave the server; see handleGetInfra.
type InfraInfo struct {
	HTTPAddr       string
	DatabaseURL    string
	ARIURL         string
	ARIUser        string
	AMIAddr        string
	AMIUser        string
	AsteriskConf   string
	DialplanFile   string
	TransportsFile string
	PJSIPFile      string
	SoundsDir      string
	WSSPort        string
}

// Router builds the chi router with all routes mounted.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Route("/api", func(r chi.Router) {
		// Public endpoints (no session required).
		r.Get("/health", s.handleHealth)
		r.Get("/branding", s.handleBranding) // brand name + default theme for the login screen
		r.Post("/login", s.handleLogin)
		r.Post("/logout", s.handleLogout)

		// Machine-to-machine control plane (/api/v1). Authenticated by an API
		// token (Bearer / X-API-Token / ?api_token=), independent of the
		// browser session. The docs + OpenAPI spec are public so the "API docs"
		// link opens without a login.
		r.Route("/v1", func(r chi.Router) {
			r.Use(noStore)
			r.Get("/docs", s.handleAPIDocs)
			r.Get("/openapi.json", s.handleOpenAPI)

			r.Group(func(r chi.Router) {
				r.Use(s.requireAPIToken)
				r.Get("/ping", s.handleV1Ping)

				r.Get("/extensions", s.handleV1ListExtensions)
				r.Post("/extensions", s.handleV1CreateExtension)
				r.Get("/extensions/{id}", s.handleV1GetExtension)
				r.Delete("/extensions/{id}", s.handleV1DeleteExtension)

				r.Get("/trunks", s.handleV1ListTrunks)

				r.Get("/reports/overview", s.handleV1ReportsOverview)
				r.Get("/reports/queues", s.handleV1ReportsQueues)
				r.Get("/reports/agents", s.handleV1ReportsAgents)

				r.Get("/calls", s.handleV1Calls)
				r.Post("/calls/originate", s.handleV1Originate)
				r.Delete("/calls/{id}", s.handleV1Hangup)
			})
		})

		// Agent softphone: its own login + session, separate from the admin
		// console. Agents authenticate with a SIP extension + secret.
		r.Route("/agent", func(r chi.Router) {
			r.Use(s.agentCORS) // allow the cross-origin browser extension
			r.Options("/*", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
			r.Post("/login", s.handleAgentLogin)
			r.Post("/logout", s.handleAgentLogout)
			r.Group(func(r chi.Router) {
				r.Use(s.requireAgent)
				r.Get("/config", s.handleAgentConfig)
				r.Post("/telemetry", s.handleAgentTelemetry)
				r.Get("/calls", s.handleAgentCalls)
				r.Delete("/calls", s.handleAgentClearCalls)

				// The agent desktop (phase 3). Every one of these resolves the
				// agent from the session, never from the body, so an agent can
				// only ever act as themselves.
				r.Get("/session", s.handleAgentSession)
				r.Get("/next-lead", s.handleAgentNextLead)
				r.Get("/callbacks", s.handleAgentCallbacks)
				r.Post("/campaign", s.handleAgentSetCampaign)
				r.Post("/pause", s.handleAgentPause)
				r.Post("/dial", s.handleAgentDial)
				r.Post("/disposition", s.handleAgentDisposition)
				r.Post("/lead", s.handleAgentUpdateLead)
				r.Post("/take-lead", s.handleAgentTakeLead)

				// In-groups the agent takes calls for this shift.
				r.Get("/ingroups", s.handleAgentIngroups)
				r.Post("/ingroups", s.handleAgentSetIngroups)
			})
		})

		// Everything else requires an authenticated session (Phase 8) and is
		// audited (who changed what).
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Use(s.audit)

			r.Get("/me", s.handleMe)
			r.Post("/change-password", s.handleChangePassword)

			// Whether the Windows softphone installer has been provisioned, so
			// the console can show a real download vs. a "not published yet"
			// state instead of a raw 404.
			r.Get("/softphone", s.handleSoftphoneInfo)

			// Self-service two-factor (TOTP) enrolment for the logged-in user.
			r.Post("/totp/enroll", s.handleTOTPEnroll)
			r.Post("/totp/activate", s.handleTOTPActivate)
			r.Post("/totp/disable", s.handleTOTPDisable)

			// Dashboard: live snapshot + control actions. Available to every
			// authenticated user (the landing page); finer control lives in the
			// per-feature permissions below.
			r.Get("/status", s.handleStatus)
			r.Get("/endpoints", s.handleEndpoints)
			r.Get("/rtp", s.handleRTP)
			r.Get("/asterisk/info", s.handleAsteriskInfo)
			r.Post("/originate", s.handleOriginate)
			r.Delete("/channels/{id}", s.handleHangup)
			r.Post("/reload", s.handleReload)

			// Extensions (CRUD over realtime tables). Static sub-paths are
			// registered before /{id}; chi prioritises static segments anyway.
			r.Group(func(r chi.Router) {
				r.Use(s.requirePerm("extensions"))
				r.Get("/extensions", s.handleListExtensions)
				r.Get("/extensions/status", s.handleExtensionStatus)
				r.Post("/extensions/bulk", s.handleBulkExtensions)
				r.Post("/extensions", s.handleCreateExtension)
				r.Get("/extensions/{id}", s.handleGetExtension)
				r.Put("/extensions/{id}", s.handleUpdateExtension)
				r.Delete("/extensions/{id}", s.handleDeleteExtension)
				r.Post("/extensions/{id}/password", s.handleResetExtPassword)
			})

			// Trunks (connection to an upstream SIP provider).
			r.Group(func(r chi.Router) {
				r.Use(s.requirePerm("trunks"))
				r.Get("/trunks", s.handleListTrunks)
				r.Post("/trunks", s.handleCreateTrunk)
				r.Get("/trunks/{id}", s.handleGetTrunk)
				r.Put("/trunks/{id}", s.handleUpdateTrunk)
				r.Delete("/trunks/{id}", s.handleDeleteTrunk)
			})

			// Routing (compiled into a generated dialplan include).
			r.Group(func(r chi.Router) {
				r.Use(s.requirePerm("routing"))
				r.Get("/routes/outbound", s.handleListOutbound)
				r.Post("/routes/outbound", s.handleCreateOutbound)
				r.Put("/routes/outbound/{id}", s.handleUpdateOutbound)
				r.Delete("/routes/outbound/{id}", s.handleDeleteOutbound)
				// In-groups: ACD queues an inbound route or IVR can point at.
				r.Get("/ingroups", s.handleListIngroups)
				r.Post("/ingroups", s.handleCreateIngroup)
				r.Get("/ingroups/{name}", s.handleGetIngroup)
				r.Put("/ingroups/{name}", s.handleUpdateIngroup)
				r.Delete("/ingroups/{name}", s.handleDeleteIngroup)
				r.Put("/ingroups/{name}/agents", s.handleSetIngroupAgents)

				r.Get("/routes/inbound", s.handleListInbound)
				r.Post("/routes/inbound", s.handleCreateInbound)
				r.Put("/routes/inbound/{id}", s.handleUpdateInbound)
				r.Delete("/routes/inbound/{id}", s.handleDeleteInbound)
			})

			// IVR menus + the prompt library share the "ivr" feature.
			r.Group(func(r chi.Router) {
				r.Use(s.requirePerm("ivr"))
				r.Get("/ivrs", s.handleListIVRs)
				r.Post("/ivrs", s.handleCreateIVR)
				r.Get("/ivrs/{id}", s.handleGetIVR)
				r.Put("/ivrs/{id}", s.handleUpdateIVR)
				r.Delete("/ivrs/{id}", s.handleDeleteIVR)
				r.Get("/sounds", s.handleListSounds)
				r.Post("/sounds", s.handleUploadSound)
				r.Get("/sounds/{name}/audio", s.handleSoundAudio)
				r.Delete("/sounds/{name}", s.handleDeleteSound)
			})

			// Lead lists and leads: the CRM half of the dialer. Static
			// sub-paths come before /{id} for readability; chi prioritises
			// static segments regardless.
			r.Group(func(r chi.Router) {
				r.Use(s.requirePerm("leads"))
				r.Get("/lists", s.handleListLists)
				r.Post("/lists", s.handleCreateList)
				r.Get("/lists/{id}", s.handleGetList)
				r.Put("/lists/{id}", s.handleUpdateList)
				r.Delete("/lists/{id}", s.handleDeleteList)
				// A reset rewrites every lead in the list, so it is an edit.
				r.Put("/lists/{id}/reset", s.handleResetList)

				r.Get("/leads", s.handleSearchLeads)
				r.Get("/leads/statuses", s.handleLeadStatuses)
				r.Post("/leads", s.handleCreateLead)
				r.Post("/leads/bulk", s.handleBulkLeads)
				r.Put("/leads/status", s.handleSetLeadStatus)
				r.Get("/leads/{id}", s.handleGetLead)
				r.Put("/leads/{id}", s.handleUpdateLead)
				r.Delete("/leads/{id}", s.handleDeleteLead)

				// Working a lead: place the call, record what happened, read
				// the history. Dialing and dispositioning are edits to the
				// lead's state, not new objects, so they sit behind "edit".
				r.Get("/leads/{id}/calls", s.handleLeadCalls)
				r.Put("/leads/{id}/dial", s.handleDialLead)
				r.Put("/leads/{id}/disposition", s.handleDispositionLead)
			})

			// Campaigns, their dispositions and pause codes, and agent
			// accounts: one operational concern, one permission.
			r.Group(func(r chi.Router) {
				r.Use(s.requirePerm("campaigns"))
				r.Get("/campaigns", s.handleListCampaigns)
				r.Post("/campaigns", s.handleCreateCampaign)
				r.Get("/campaigns/{id}", s.handleGetCampaign)
				r.Put("/campaigns/{id}", s.handleUpdateCampaign)
				r.Delete("/campaigns/{id}", s.handleDeleteCampaign)
				r.Get("/campaigns/{id}/next-lead", s.handleNextPreviewLead)

				// Dialer control and live state. Starting the machine is an
				// edit to the campaign, not a new object.
				r.Get("/campaigns/{id}/dialer", s.handleDialerStatus)
				r.Put("/campaigns/{id}/dialer", s.handleSetDialerRunning)
				r.Get("/campaigns/{id}/hopper", s.handleListHopper)
				r.Delete("/campaigns/{id}/hopper", s.handlePurgeHopper)

				// Do-Not-Call. Suppression is campaign administration, and it
				// is what makes automatic dialing lawful.
				r.Get("/dnc", s.handleListDNC)
				r.Post("/dnc", s.handleAddDNC)
				r.Delete("/dnc/{id}", s.handleRemoveDNC)

				r.Get("/dispositions", s.handleListDispositions)
				r.Post("/dispositions", s.handleSaveDisposition)
				r.Put("/dispositions", s.handleSaveDisposition)
				r.Delete("/dispositions/{id}", s.handleDeleteDisposition)

				r.Get("/pause-codes", s.handleListPauseCodes)
				r.Post("/pause-codes", s.handleSavePauseCode)
				r.Put("/pause-codes", s.handleSavePauseCode)
				r.Delete("/pause-codes/{id}", s.handleDeletePauseCode)

				r.Get("/agents", s.handleListAgentAccounts)
				r.Post("/agents", s.handleCreateAgentAccount)
				r.Put("/agents/{id}", s.handleUpdateAgentAccount)
				r.Delete("/agents/{id}", s.handleDeleteAgentAccount)
			})

			// PJSIP transports + global PJSIP/TLS settings (load-time objects
			// compiled to a static #include; bind changes need a restart).
			r.Group(func(r chi.Router) {
				r.Use(s.requirePerm("transports"))
				r.Get("/transports", s.handleListTransports)
				r.Post("/transports", s.handleCreateTransport)
				r.Get("/transports/{name}", s.handleGetTransport)
				r.Put("/transports/{name}", s.handleUpdateTransport)
				r.Delete("/transports/{name}", s.handleDeleteTransport)
				r.Post("/asterisk/restart", s.handleRestartAsterisk)

				// Global PJSIP + TLS/SSL/SRTP settings.
				r.Get("/pjsip/settings", s.handleGetPJSIPSettings)
				r.Put("/pjsip/settings", s.handleUpdatePJSIPSettings)
			})

			// Call history (CDR).
			r.Group(func(r chi.Router) {
				r.Use(s.requirePerm("cdr"))
				r.Get("/cdr", s.handleListCDR)
			})

			// Analytics.
			r.Group(func(r chi.Router) {
				r.Use(s.requirePerm("analytics"))
				r.Get("/analytics/agents", s.handleAgentAnalytics)
				r.Get("/analytics/softphone", s.handleSoftphoneAnalytics)
				r.Get("/analytics/overview", s.handleAnalyticsOverview)
				r.Get("/analytics/reports", s.handleAnalyticsReports)
				r.Get("/analytics/extension/{ext}", s.handleAnalyticsExtension)
				// Web wrap-up panel: tag recent calls (any softphone) with a
				// disposition that feeds Nature/Resolution/Hangup-cause analytics.
				r.Get("/wrapup/calls", s.handleWrapupCalls)
				r.Post("/wrapup/tag", s.handleWrapupTag)
			})

			// Runtime configuration (System/Branding/WebRTC) lives under the
			// "settings" feature. The System tab also surfaces read-only infra.
			r.Group(func(r chi.Router) {
				r.Use(s.requirePerm("settings"))
				r.Get("/settings/system", s.handleGetSystemSettings)
				r.Put("/settings/system", s.handleUpdateSystemSettings)
				r.Get("/settings/infra", s.handleGetInfra)
				r.Get("/settings/webrtc", s.handleGetWebRTCSettings)
				r.Put("/settings/webrtc", s.handleUpdateWebRTCSettings)

				// API token administration (mint/revoke the /api/v1 bearer
				// tokens). The plaintext is returned once, at creation.
				r.Get("/settings/tokens", s.handleListAPITokens)
				r.Post("/settings/tokens", s.handleCreateAPIToken)
				r.Post("/settings/tokens/{id}/revoke", s.handleRevokeAPIToken)
				r.Delete("/settings/tokens/{id}", s.handleDeleteAPIToken)
			})

			// User management is gated by the "users" feature.
			r.Group(func(r chi.Router) {
				r.Use(s.requirePerm("users"))
				r.Get("/users", s.handleListUsers)
				r.Post("/users", s.handleCreateUser)
				r.Put("/users/{username}", s.handleUpdateUser)
				r.Delete("/users/{username}", s.handleDeleteUser)
				r.Post("/users/{username}/password", s.handleResetUserPassword)
				r.Post("/users/{username}/totp/reset", s.handleResetUserTOTP)
			})

			// Role management is admin-only: only an admin can invent roles and
			// decide which features each role may use.
			r.Group(func(r chi.Router) {
				r.Use(s.requireAdmin)
				r.Get("/roles", s.handleListRoles)
				r.Post("/roles", s.handleCreateRole)
				r.Put("/roles/{name}", s.handleUpdateRole)
				r.Delete("/roles/{name}", s.handleDeleteRole)
			})
		})
	})

	// Live event stream for the dashboard (authenticated).
	r.Handle("/ws", s.requireAuth(http.HandlerFunc(s.Hub.ServeHTTP)))

	// Agent softphone SPA (separate build) under /phone.
	r.Get("/phone", s.serveAgentSPA)
	r.Get("/phone/*", s.serveAgentSPA)

	// Packaged downloads (extension zips). 404 when absent instead of falling
	// through to the SPA, so a not-yet-built file never masquerades as HTML.
	r.Get("/downloads/*", s.serveDownload)

	// Everything else is the admin SPA.
	r.NotFound(s.serveSPA)
	r.Get("/", s.serveSPA)

	return r
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	health := map[string]any{"status": "ok", "time": time.Now().UTC()}
	if err := s.DB.Pool.Ping(ctx); err != nil {
		health["database"] = "down: " + err.Error()
		health["status"] = "degraded"
	} else {
		health["database"] = "up"
	}
	writeJSON(w, http.StatusOK, health)
}

// handleStatus returns a live snapshot pulled straight from Asterisk via ARI:
// currently known endpoints and active channels. The dashboard renders this on
// first load and then keeps it fresh from the /ws event stream.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	resp := map[string]any{"time": time.Now().UTC()}

	if eps, err := s.ARI.Endpoints(ctx); err != nil {
		resp["endpoints_error"] = err.Error()
	} else {
		resp["endpoints"] = eps
	}
	if chans, err := s.ARI.Channels(ctx); err != nil {
		resp["channels_error"] = err.Error()
	} else {
		resp["channels"] = chans
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleRTP returns per-channel audio RTP counters (rx/tx packet totals) for
// every active channel, so the UI can show which leg is actually sending or
// receiving audio (one-way-audio diagnosis).
func (s *Server) handleRTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	chans, err := s.ARI.Channels(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"rtp": map[string]ari.RTPStats{}})
		return
	}
	out := make(map[string]ari.RTPStats, len(chans))
	for _, ch := range chans {
		out[ch.ID] = s.ARI.ChannelRTP(ctx, ch.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rtp": out})
}

// handleEndpoints lists provisioned PJSIP endpoints from the realtime tables.
// This is configuration (what SHOULD exist), as opposed to /status which is
// live state (what IS registered right now).
func (s *Server) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := s.DB.Pool.Query(ctx,
		`SELECT id, COALESCE(context,''), COALESCE(disallow,''), COALESCE(allow,''), COALESCE(transport,'')
		   FROM ps_endpoints ORDER BY id`)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	type endpoint struct {
		ID        string `json:"id"`
		Context   string `json:"context"`
		Disallow  string `json:"disallow"`
		Allow     string `json:"allow"`
		Transport string `json:"transport"`
	}
	out := []endpoint{}
	for rows.Next() {
		var e endpoint
		if err := rows.Scan(&e.ID, &e.Context, &e.Disallow, &e.Allow, &e.Transport); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"endpoints": out})
}

// handleAsteriskInfo returns the running PBX version and lifecycle times.
func (s *Server) handleAsteriskInfo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	info, err := s.ARI.AsteriskInfo(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleOriginate places a new call via ARI and returns the created channel.
func (s *Server) handleOriginate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Endpoint  string `json:"endpoint"`
		Extension string `json:"extension"`
		Context   string `json:"context"`
		CallerID  string `json:"callerId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if body.Endpoint == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "endpoint is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	ch, err := s.ARI.Originate(ctx, ari.OriginateParams{
		Endpoint:  body.Endpoint,
		Extension: body.Extension,
		Context:   body.Context,
		CallerID:  body.CallerID,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channel": ch})
}

// handleHangup terminates an active channel by id.
func (s *Server) handleHangup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "channel id is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.ARI.Hangup(ctx, id, r.URL.Query().Get("reason")); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "hung up", "channel": id})
}

// handleReload reloads an Asterisk module (e.g. res_pjsip.so) via ARI.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Module string `json:"module"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if body.Module == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "module is required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.ARI.ReloadModule(ctx, body.Module); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded", "module": body.Module})
}

// --- Phase 3: extensions -----------------------------------------------

func (s *Server) handleListExtensions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	exts, err := s.Ext.List(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"extensions": exts})
}

// handleExtensionStatus returns live registration state per extension (online,
// address, device class, last-seen) for the presence dots/illustrations.
func (s *Server) handleExtensionStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	st, err := s.Ext.Status(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": st})
}

// handleResetExtPassword sets a new SIP secret for one extension. If the body
// omits a password, a strong random one is generated and returned so the
// operator can hand it to the user.
func (s *Server) handleResetExtPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
	if strings.TrimSpace(body.Password) == "" {
		body.Password = randomSecret(18)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.Ext.SetPassword(ctx, chi.URLParam(r, "id"), body.Password); err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset", "password": body.Password})
}

// handleBulkExtensions creates many extensions in one request. Each row is
// attempted independently; the response reports per-row success/failure so a
// partially-bad upload still creates the good rows.
func (s *Server) handleBulkExtensions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Extensions []store.Extension `json:"extensions"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if len(body.Extensions) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no extensions supplied"})
		return
	}
	type rowResult struct {
		ID    string `json:"id"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	results := make([]rowResult, 0, len(body.Extensions))
	created := 0
	for _, e := range body.Extensions {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		err := s.Ext.Create(ctx, e)
		cancel()
		if err != nil {
			results = append(results, rowResult{ID: e.ID, OK: false, Error: err.Error()})
			continue
		}
		created++
		results = append(results, rowResult{ID: e.ID, OK: true})
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": created, "results": results})
}

func (s *Server) handleGetExtension(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	ext, err := s.Ext.Get(ctx, chi.URLParam(r, "id"))
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ext)
}

func (s *Server) handleCreateExtension(w http.ResponseWriter, r *http.Request) {
	ext, ok := decodeExtension(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.Ext.Create(ctx, ext); err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "created", "id": ext.ID})
}

func (s *Server) handleUpdateExtension(w http.ResponseWriter, r *http.Request) {
	ext, ok := decodeExtension(w, r)
	if !ok {
		return
	}
	// The path id is authoritative.
	ext.ID = chi.URLParam(r, "id")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.Ext.Update(ctx, ext); err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "id": ext.ID})
}

func (s *Server) handleDeleteExtension(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.Ext.Delete(ctx, chi.URLParam(r, "id")); err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// randomSecret returns an n-character SIP secret from an unambiguous alphabet
// (no 0/O/1/l/I) so it is safe to read aloud when handing it to a user.
func randomSecret(n int) string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Extremely unlikely; fall back to a fixed-length deterministic-ish value.
		return "changeme-" + fmt.Sprint(time.Now().UnixNano())
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func decodeExtension(w http.ResponseWriter, r *http.Request) (store.Extension, bool) {
	var ext store.Extension
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&ext); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return ext, false
	}
	return ext, true
}

// writeExtError maps store errors to HTTP status codes.
func writeExtError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	case errors.Is(err, store.ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already exists"})
	default:
		// Validation errors and everything else.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
}

// --- Phase 4: trunks ---------------------------------------------------

// reloadPJSIP asks Asterisk to reload PJSIP so realtime OUTBOUND REGISTRATIONS
// and IDENTIFIES take effect. Endpoints/AORs are fetched from realtime on
// demand, but registrations and identifies are only read at load/reload time,
// so a trunk written to the DB does nothing until this runs. Best-effort: the
// config is already persisted; a reload failure is logged, not fatal.
func (s *Server) reloadPJSIP(ctx context.Context) {
	if err := s.ARI.ReloadModule(ctx, "res_pjsip.so"); err != nil {
		slog.Warn("pjsip reload after trunk change failed", "err", err)
	}
}

func (s *Server) handleListTrunks(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	trunks, err := s.Trunks.List(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Enrich with live reachability from ARI (best-effort). The trunk's AOR is
	// qualified, so the endpoint state reflects whether the provider responds.
	if eps, err := s.ARI.Endpoints(ctx); err == nil {
		state := make(map[string]string, len(eps))
		for _, e := range eps {
			if strings.EqualFold(e.Technology, "PJSIP") {
				state[e.Resource] = e.State
			}
		}
		for i := range trunks {
			if st, ok := state[trunks[i].Name]; ok {
				trunks[i].State = st
			} else {
				trunks[i].State = "unknown"
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"trunks": trunks})
}

func (s *Server) handleGetTrunk(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	t, err := s.Trunks.Get(ctx, chi.URLParam(r, "id"))
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleCreateTrunk(w http.ResponseWriter, r *http.Request) {
	t, ok := decodeTrunk(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.Trunks.Create(ctx, t); err != nil {
		writeExtError(w, err)
		return
	}
	s.reloadPJSIP(ctx)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "created", "name": t.Name})
}

func (s *Server) handleUpdateTrunk(w http.ResponseWriter, r *http.Request) {
	t, ok := decodeTrunk(w, r)
	if !ok {
		return
	}
	t.Name = chi.URLParam(r, "id")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.Trunks.Update(ctx, t); err != nil {
		writeExtError(w, err)
		return
	}
	s.reloadPJSIP(ctx)
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "name": t.Name})
}

func (s *Server) handleDeleteTrunk(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.Trunks.Delete(ctx, chi.URLParam(r, "id")); err != nil {
		writeExtError(w, err)
		return
	}
	s.reloadPJSIP(ctx)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func decodeTrunk(w http.ResponseWriter, r *http.Request) (store.Trunk, bool) {
	var t store.Trunk
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&t); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return t, false
	}
	return t, true
}

// serveSPA serves the built frontend from WebDir. Unknown paths fall back to
// index.html so client-side routing works. If no build is present it serves a
// themed placeholder so the operator can confirm the backend is running.
// serveAgentSPA serves the agent softphone build (rooted at /phone). Its assets
// are referenced as /phone/assets/..., so the /phone prefix is stripped before
// looking a file up under AgentWebDir; unknown paths fall back to its index.
func (s *Server) serveAgentSPA(w http.ResponseWriter, r *http.Request) {
	if s.AgentWebDir != "" {
		rel := strings.TrimPrefix(r.URL.Path, "/phone")
		clean := filepath.Clean(strings.TrimPrefix(rel, "/"))
		candidate := filepath.Join(s.AgentWebDir, clean)
		if clean != "." && withinDir(s.AgentWebDir, candidate) {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				http.ServeFile(w, r, candidate)
				return
			}
		}
		index := filepath.Join(s.AgentWebDir, "index.html")
		if _, err := os.Stat(index); err == nil {
			http.ServeFile(w, r, index)
			return
		}
	}
	http.Error(w, "agent softphone not built", http.StatusNotFound)
}

// Softphone installer filenames under WebDir/downloads (produced by the CI
// build jobs and provisioned into the console by the deploy).
const (
	softphoneInstaller = "xelovoice-softphone-setup.exe" // Windows
	softphoneAPK       = "xelovoice-softphone.apk"       // Android
)

// softphoneEntry reports availability for one platform's installer.
func (s *Server) softphoneEntry(platform, file string) map[string]any {
	e := map[string]any{
		"platform":  platform,
		"available": false,
		"url":       "/downloads/" + file,
		"name":      file,
	}
	if s.WebDir != "" {
		if info, err := os.Stat(filepath.Join(s.WebDir, "downloads", file)); err == nil && !info.IsDir() {
			e["available"] = true
			e["sizeBytes"] = info.Size()
		}
	}
	return e
}

// handleSoftphoneInfo reports which softphone installers (Windows / Android)
// have been provisioned on this server, so the console can show working
// download buttons or a clear "not published yet" state instead of a 404.
func (s *Server) handleSoftphoneInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"installers": []map[string]any{
			s.softphoneEntry("windows", softphoneInstaller),
			s.softphoneEntry("android", softphoneAPK),
		},
	})
}

// serveDownload serves a file from WebDir/downloads (the packaged extension
// zips) and returns 404 when it does not exist, so a missing build never falls
// through to the SPA and gets saved as an HTML "zip".
func (s *Server) serveDownload(w http.ResponseWriter, r *http.Request) {
	if s.WebDir == "" {
		http.NotFound(w, r)
		return
	}
	clean := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/downloads/"))
	if clean == "." || strings.HasPrefix(clean, "..") {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(s.WebDir, "downloads", clean)
	if !withinDir(s.WebDir, p) {
		http.NotFound(w, r)
		return
	}
	if info, err := os.Stat(p); err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(p)+`"`)
	http.ServeFile(w, r, p)
}

func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	if s.WebDir != "" {
		clean := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		candidate := filepath.Join(s.WebDir, clean)
		if clean != "." && withinDir(s.WebDir, candidate) {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				http.ServeFile(w, r, candidate)
				return
			}
		}
		index := filepath.Join(s.WebDir, "index.html")
		if _, err := os.Stat(index); err == nil {
			http.ServeFile(w, r, index)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(placeholderHTML))
}

func withinDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..")
}

// placeholderHTML is a self-contained themed landing page shown when the
// frontend has not been built yet. It matches the sci-fi call-center theme
// (primary green #39a751) so the visual identity is present from day one.
const placeholderHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>TPBX Control</title>
<style>
:root{--green:#39a751;--bg:#05100a;--panel:#0b1f14;--grid:rgba(57,167,81,.12);--text:#c8f7d4;--muted:#5f8f6e}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;font-family:"Segoe UI",system-ui,sans-serif;color:var(--text);
background:radial-gradient(circle at 50% 0%,#0a2417 0%,var(--bg) 60%);
background-image:linear-gradient(var(--grid) 1px,transparent 1px),linear-gradient(90deg,var(--grid) 1px,transparent 1px);
background-size:100% 100%,40px 40px,40px 40px;display:flex;align-items:center;justify-content:center}
.panel{border:1px solid var(--green);border-radius:14px;padding:48px 56px;background:rgba(11,31,20,.75);
box-shadow:0 0 40px rgba(57,167,81,.25),inset 0 0 30px rgba(57,167,81,.06);text-align:center;max-width:560px}
h1{margin:0 0 4px;font-size:34px;letter-spacing:6px;text-transform:uppercase;color:var(--green);text-shadow:0 0 18px rgba(57,167,81,.6)}
.tag{color:var(--muted);letter-spacing:3px;font-size:12px;text-transform:uppercase;margin-bottom:24px}
.dot{display:inline-block;width:9px;height:9px;border-radius:50%;background:var(--green);box-shadow:0 0 10px var(--green);
margin-right:8px;animation:pulse 1.6s infinite}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.3}}
code{color:var(--green)}
.hint{margin-top:22px;font-size:13px;color:var(--muted);line-height:1.7}
</style></head><body>
<div class="panel">
<h1>TPBX</h1>
<div class="tag">Asterisk Control Console</div>
<div><span class="dot"></span>Backend online</div>
<div class="hint">Frontend build not found. Run <code>make web</code> then reload,<br>
or hit the API directly: <code>/api/health</code></div>
</div></body></html>`
