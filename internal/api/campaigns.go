package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/td425/tpbx/internal/ari"
	"github.com/td425/tpbx/internal/store"
)

// Campaigns ------------------------------------------------------------------

func (s *Server) handleListCampaigns(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := s.Campaigns.List(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"campaigns": list,
		// The console marks automatic methods as pending so an operator never
		// believes RATIO is already placing calls; the dialer arrives in P4.
		"dialMethods": store.DialMethods,
		"leadOrders":  store.LeadOrders,
	})
}

func (s *Server) handleGetCampaign(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleCreateCampaign(w http.ResponseWriter, r *http.Request) {
	var body store.Campaign
	if !decodeJSON(w, r, &body, 1<<18) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	c, err := s.Campaigns.Create(ctx, body)
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleUpdateCampaign(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body store.Campaign
	if !decodeJSON(w, r, &body, 1<<18) {
		return
	}
	body.ID = id
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Campaigns.Update(ctx, body); err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteCampaign(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Campaigns.Delete(ctx, id); err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// Dispositions and pause codes -----------------------------------------------

// handleListDispositions returns the vocabulary available to a campaign: its
// own rows plus the system-wide ones. ?campaign=0 (or absent) gives just the
// system-wide set.
func (s *Server) handleListDispositions(w http.ResponseWriter, r *http.Request) {
	campaign, _ := strconv.ParseInt(r.URL.Query().Get("campaign"), 10, 64)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := s.Campaigns.ListDispositions(ctx, campaign)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dispositions": list})
}

func (s *Server) handleSaveDisposition(w http.ResponseWriter, r *http.Request) {
	var body store.Disposition
	if !decodeJSON(w, r, &body, 1<<16) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	d, err := s.Campaigns.SaveDisposition(ctx, body)
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleDeleteDisposition(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Campaigns.DeleteDisposition(ctx, id); err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleListPauseCodes(w http.ResponseWriter, r *http.Request) {
	campaign, _ := strconv.ParseInt(r.URL.Query().Get("campaign"), 10, 64)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := s.Campaigns.ListPauseCodes(ctx, campaign)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pauseCodes": list})
}

func (s *Server) handleSavePauseCode(w http.ResponseWriter, r *http.Request) {
	var body store.PauseCode
	if !decodeJSON(w, r, &body, 1<<16) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	p, err := s.Campaigns.SavePauseCode(ctx, body)
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeletePauseCode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Campaigns.DeletePauseCode(ctx, id); err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// Agent accounts -------------------------------------------------------------

func (s *Server) handleListAgentAccounts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := s.AgentAccounts.List(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": list})
}

func (s *Server) handleCreateAgentAccount(w http.ResponseWriter, r *http.Request) {
	var body store.AgentAccount
	if !decodeJSON(w, r, &body, 1<<16) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	a, err := s.AgentAccounts.Create(ctx, body)
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) handleUpdateAgentAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body store.AgentAccount
	if !decodeJSON(w, r, &body, 1<<16) {
		return
	}
	body.ID = id
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.AgentAccounts.Update(ctx, body); err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteAgentAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.AgentAccounts.Delete(ctx, id); err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// Dialing --------------------------------------------------------------------

// handleDialLead places a manual call to a lead: Asterisk rings the agent's
// own extension first and, when they pick up, continues in from-internal to
// dial the lead — so the number goes out through the outbound routes that are
// already configured, with no dialer-specific dialplan.
//
// This is phase 2's dialing model in full: agent-paced, one call at a time.
// Automatic pacing (RATIO/ADAPT_*) needs the P4 engine and is refused here
// rather than pretended at.
func (s *Server) handleDialLead(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body struct {
		Extension  string `json:"extension"`  // agent's device; required
		CampaignID int64  `json:"campaignId"` // supplies caller ID and wrap-up
		Agent      string `json:"agent"`      // agent username, for the record
	}
	if !decodeJSON(w, r, &body, 4096) {
		return
	}
	if body.Extension == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "which extension should we ring first? Pass the agent's extension.",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	lead, err := s.Leads.Get(ctx, id)
	if err != nil {
		writeExtError(w, err)
		return
	}

	// Re-validate at dial time. The number was checked on import, but a lead
	// can be edited between then and now, and this is the last point before a
	// call actually goes out.
	dialed := lead.PhoneCode + lead.PhoneNumber
	clean, err := store.CheckPhoneNumber(dialed)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	callerID := ""
	var campaignID *int64
	if body.CampaignID > 0 {
		c, err := s.Campaigns.Get(ctx, body.CampaignID)
		if err != nil {
			writeExtError(w, err)
			return
		}
		if !c.Active {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "campaign " + c.Code + " is not active",
			})
			return
		}
		callerID = c.OutboundCID
		campaignID = &c.ID
	}

	ch, err := s.ARI.Originate(ctx, ari.OriginateParams{
		Endpoint:  "PJSIP/" + body.Extension,
		Context:   "from-internal",
		Extension: clean,
		CallerID:  callerID,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	call, err := s.LeadCalls.Start(ctx, store.LeadCall{
		LeadID:     lead.ID,
		CampaignID: campaignID,
		Agent:      body.Agent,
		Extension:  body.Extension,
		Direction:  "out",
		ChannelID:  ch.ID,
		Dialed:     clean,
	})
	if err != nil {
		// The call is already up; failing to log it must not read as a failure
		// to dial, so report the call and the logging problem together.
		writeJSON(w, http.StatusOK, map[string]any{
			"channel": ch, "dialed": clean, "logError": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channel": ch, "dialed": clean, "call": call})
}

// handleDispositionLead records what happened on a call and writes the outcome
// back to the lead.
func (s *Server) handleDispositionLead(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body struct {
		Status     string `json:"status"`
		Note       string `json:"note"`
		CallID     int64  `json:"callId"`
		CampaignID int64  `json:"campaignId"`
	}
	if !decodeJSON(w, r, &body, 1<<16) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Check the code against the campaign's vocabulary so a typo cannot invent
	// a status that no report or dial rule will ever match.
	if _, err := s.Campaigns.GetDisposition(ctx, body.CampaignID, body.Status); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "unknown disposition " + body.Status + " — define it on the campaign first",
		})
		return
	}

	sess := sessionFrom(r)
	call, err := s.LeadCalls.Apply(ctx, store.DispositionInput{
		CallID: body.CallID,
		LeadID: id,
		Status: body.Status,
		Note:   body.Note,
		Agent:  sess.Username,
	})
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, call)
}

// handleLeadCalls returns a lead's attempt history.
func (s *Server) handleLeadCalls(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	calls, err := s.LeadCalls.ForLead(ctx, id, 50)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"calls": calls})
}

// handleNextPreviewLead hands back the next lead a campaign would dial, for
// preview dialing: the agent sees the record before deciding to call.
func (s *Server) handleNextPreviewLead(w http.ResponseWriter, r *http.Request) {
	campaign, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || campaign <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid campaign"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	leadID, err := s.LeadCalls.NextPreviewLead(ctx, campaign)
	if err != nil {
		if err == store.ErrNotFound {
			writeJSON(w, http.StatusOK, map[string]any{
				"lead": nil,
				"note": "no dialable leads: check the campaign is active, has active lists, and that its dial statuses match leads in them",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	lead, err := s.Leads.Get(ctx, leadID)
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lead": lead})
}
