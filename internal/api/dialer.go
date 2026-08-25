package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/td425/tpbx/internal/dialer"
	"github.com/td425/tpbx/internal/store"
)

// Dialer control and the Do-Not-Call list (docs/VICIDIAL_PARITY.md, phase 4).

// handleDialerStatus reports what the engine is doing for one campaign: whether
// it is running, how deep the queue is, how many agents are free, and the two
// rates the pacing governor is reading.
//
// The drop rate is surfaced next to its ceiling on purpose. It is the number
// that decides whether the engine is allowed to keep dialing, and a supervisor
// should never have to go looking for it.
func (s *Server) handleDialerStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	c, err := s.Campaigns.Get(ctx, id)
	if err != nil {
		writeExtError(w, err)
		return
	}
	depth, _ := s.Hopper.Depth(ctx, id)
	ready, _ := s.Work.ReadyAgents(ctx, id)
	recent, _ := s.Work.RecentCalls(ctx, id, 30*time.Minute)

	answerRate := 0.0
	if recent.Placed > 0 {
		answerRate = float64(recent.Answered) / float64(recent.Placed)
	}
	dropRate := dialer.DropRatePercent(recent.Answered, recent.Dropped)

	// Say plainly why a started dialer is not dialing. Every one of these is a
	// configuration mistake or a condition that otherwise looks like a broken
	// product.
	//
	// Order matters. Configuration comes first because nothing else can be
	// fixed until it is right; the abandoned-call brake comes next, ahead of
	// staffing, because it is the one condition that persists no matter how
	// many agents sign in — telling a supervisor "no agents are ready" while
	// the real answer is "you are over your abandon limit" sends them to fix
	// the wrong thing.
	blocked := ""
	switch {
	case !dialer.Automatic(c.DialMethod):
		blocked = "dial method " + c.DialMethod + " is agent-driven; agents dial it themselves"
	case c.Trunk == "":
		blocked = "no trunk set on the campaign, so automatic calls have no route out"
	case !c.Active:
		blocked = "campaign is paused"
	case c.DropRateTarget > 0 && recent.Placed >= dialer.MinSample && dropRate >= c.DropRateTarget:
		blocked = "abandoned-call rate is at the campaign ceiling; dialing is held until it recovers"
	case len(ready) == 0:
		blocked = "no agents are ready on this campaign"
	case depth == 0:
		blocked = "no dialable leads: check the lists, their statuses, and the do-not-call list"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"campaignId":   c.ID,
		"code":         c.Code,
		"running":      c.DialerRunning,
		"dialMethod":   c.DialMethod,
		"automatic":    dialer.Automatic(c.DialMethod),
		"hopperDepth":  depth,
		"hopperLevel":  c.HopperLevel,
		"readyAgents":  ready,
		"recent":       recent,
		"answerRate":   answerRate,
		"dropRate":     dropRate,
		"dropCeiling":  c.DropRateTarget,
		"blockedBy":    blocked,
		"measuredOver": "30m",
	})
}

// handleSetDialerRunning starts or stops automatic dialing for a campaign.
// Stopping also empties the queue: leads held in a hopper nobody is dialing are
// invisible to every other part of the system, and would be stale when it
// restarted anyway.
func (s *Server) handleSetDialerRunning(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body struct {
		Running bool `json:"running"`
	}
	if !decodeJSON(w, r, &body, 4096) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	c, err := s.Campaigns.Get(ctx, id)
	if err != nil {
		writeExtError(w, err)
		return
	}
	// Refuse to start a dialer that cannot place a call. Starting it anyway
	// would look like it was working and quietly do nothing.
	if body.Running {
		if !dialer.Automatic(c.DialMethod) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "dial method " + c.DialMethod + " is agent-driven — there is no dialer to start",
			})
			return
		}
		if c.Trunk == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "set the campaign's trunk first: an automatic call has no agent to pick a route for it",
			})
			return
		}
		if !c.Active {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "campaign " + c.Code + " is paused",
			})
			return
		}
	}

	if err := s.Work.SetDialerRunning(ctx, id, body.Running); err != nil {
		writeExtError(w, err)
		return
	}
	purged := int64(0)
	if !body.Running {
		purged, _ = s.Hopper.Purge(ctx, id)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running": body.Running, "hopperCleared": purged,
	})
}

// handleListHopper shows what a campaign is about to call.
func (s *Server) handleListHopper(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := s.Hopper.List(ctx, id, 100)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hopper": list})
}

// handlePurgeHopper empties a campaign's queue. The filler rebuilds it on the
// next tick if the dialer is running, so this is a way to re-select leads after
// changing the campaign's rules, not a way to stop it.
func (s *Server) handlePurgeHopper(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	n, err := s.Hopper.Purge(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": n})
}

// Do-Not-Call -----------------------------------------------------------------

func (s *Server) handleListDNC(w http.ResponseWriter, r *http.Request) {
	campaign, _ := strconv.ParseInt(r.URL.Query().Get("campaign"), 10, 64)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := s.Hopper.ListDNC(ctx, campaign, r.URL.Query().Get("q"), 200)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dnc": list})
}

// handleAddDNC suppresses a number. A blank campaign means globally — the
// difference between "stop calling me about this" and "never call me again".
func (s *Server) handleAddDNC(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PhoneNumber string `json:"phoneNumber"`
		CampaignID  *int64 `json:"campaignId"`
		Reason      string `json:"reason"`
	}
	if !decodeJSON(w, r, &body, 1<<16) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	sess := sessionFrom(r)
	e, err := s.Hopper.AddDNC(ctx, store.DNCEntry{
		PhoneNumber: body.PhoneNumber,
		CampaignID:  body.CampaignID,
		Reason:      body.Reason,
		AddedBy:     sess.Username,
	})
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) handleRemoveDNC(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Hopper.RemoveDNC(ctx, id); err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}
