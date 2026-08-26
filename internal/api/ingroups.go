package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/td425/tpbx/internal/store"
)

// In-groups: skill-based inbound queues served by app_queue
// (docs/VICIDIAL_PARITY.md, phase 5).

// reloadQueueMembers nudges Asterisk to re-read realtime queue membership.
//
// Membership rows are written straight into Asterisk's own `queue_members`
// table, so no file changes and no reload is needed for the queue to exist —
// but app_queue caches members, so a change an agent just made would otherwise
// take effect only on the next queue load. Best-effort by design: the database
// is already correct, this only makes it visible sooner.
func (s *Server) reloadQueueMembers(ctx context.Context) {
	if s.ReloadQueues == nil {
		return
	}
	if err := s.ReloadQueues(ctx); err != nil {
		slog.Debug("queue member reload failed; membership still takes effect on the next queue load", "err", err)
	}
}

func (s *Server) handleListIngroups(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := s.Ingroups.List(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ingroups":   list,
		"strategies": store.QueueStrategies,
	})
}

func (s *Server) handleGetIngroup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	g, err := s.Ingroups.Get(ctx, chi.URLParam(r, "name"))
	if err != nil {
		writeExtError(w, err)
		return
	}
	agents, _ := s.Ingroups.Agents(ctx, g.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ingroup": g, "agents": agents})
}

func (s *Server) handleCreateIngroup(w http.ResponseWriter, r *http.Request) {
	var body store.Ingroup
	if !decodeJSON(w, r, &body, 1<<16) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Ingroups.Save(ctx, body, true); err != nil {
		writeExtError(w, err)
		return
	}
	// A new queue changes what an inbound route or IVR may point at, so the
	// dialplan is regenerated to keep the pickers and the dialplan in step.
	if err := s.applyDialplan(ctx); err != nil {
		slog.Warn("dialplan regenerate after in-group create", "err", err)
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "created"})
}

func (s *Server) handleUpdateIngroup(w http.ResponseWriter, r *http.Request) {
	var body store.Ingroup
	if !decodeJSON(w, r, &body, 1<<16) {
		return
	}
	body.Name = chi.URLParam(r, "name")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Ingroups.Save(ctx, body, false); err != nil {
		writeExtError(w, err)
		return
	}
	// The queue's own settings are read live from realtime, but the give-up
	// destination is compiled into the dialplan, so regenerate.
	if err := s.applyDialplan(ctx); err != nil {
		slog.Warn("dialplan regenerate after in-group update", "err", err)
	}
	s.reloadQueueMembers(ctx)
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteIngroup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Ingroups.Delete(ctx, chi.URLParam(r, "name")); err != nil {
		writeExtError(w, err)
		return
	}
	if err := s.applyDialplan(ctx); err != nil {
		slog.Warn("dialplan regenerate after in-group delete", "err", err)
	}
	s.reloadQueueMembers(ctx)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleSetIngroupAgents replaces who may take an in-group's calls.
func (s *Server) handleSetIngroupAgents(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Agents []store.IngroupAgent `json:"agents"`
	}
	if !decodeJSON(w, r, &body, 1<<18) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.Ingroups.SetAgents(ctx, chi.URLParam(r, "name"), body.Agents); err != nil {
		writeExtError(w, err)
		return
	}
	s.reloadQueueMembers(ctx)
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// Agent-facing ----------------------------------------------------------------

// handleAgentIngroups lists the in-groups this agent may take, and which they
// are signed in to right now.
func (s *Server) handleAgentIngroups(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.agentAccount(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := s.Ingroups.AllowedFor(ctx, acct.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ingroups": list})
}

// handleAgentSetIngroups is ViciDial's change_ingroups: the agent chooses which
// queues they are taking for this shift. An in-group they are not permitted is
// refused rather than ignored, so an agent who believes they are on sales calls
// actually is.
func (s *Server) handleAgentSetIngroups(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.agentAccount(w, r)
	if !ok {
		return
	}
	var body struct {
		Ingroups []string `json:"ingroups"`
	}
	if !decodeJSON(w, r, &body, 1<<16) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := s.Ingroups.SelectIngroups(ctx, acct.ID, body.Ingroups); err != nil {
		writeExtError(w, err)
		return
	}
	// An agent who is paused at the desk must be paused in the queues too, or
	// the ACD will ring someone who has stepped away.
	if state, err := s.Work.State(ctx, acct.ID); err == nil {
		_ = s.Ingroups.SetPaused(ctx, acct.Extension, state.Paused)
	}
	s.reloadQueueMembers(ctx)

	list, _ := s.Ingroups.AllowedFor(ctx, acct.ID)
	writeJSON(w, http.StatusOK, map[string]any{"ingroups": list})
}
