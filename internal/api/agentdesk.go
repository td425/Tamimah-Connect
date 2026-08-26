package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/td425/tpbx/internal/ari"
	"github.com/td425/tpbx/internal/store"
)

// The agent desktop: the endpoints that turn the softphone from a phone into an
// agent screen (docs/VICIDIAL_PARITY.md, phase 3).
//
// Every one of these resolves the caller's agent identity from their softphone
// session — never from the request body — so an agent can only ever act as
// themselves. That is the same rule the telemetry endpoint already follows.

// agentAccount resolves the signed-in softphone session to an agent record,
// creating it on first sight. Returns false having already written the error
// response.
func (s *Server) agentAccount(w http.ResponseWriter, r *http.Request) (store.AgentAccount, bool) {
	ext := agentFrom(r)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	display := ""
	if a, err := s.Agents.Get(ctx, ext); err == nil {
		display = a.DisplayName
	}
	acct, err := s.AgentAccounts.EnsureForExtension(ctx, ext, display)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return acct, false
	}
	if !acct.Active {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "this agent account is disabled — ask a supervisor to re-enable it",
		})
		return acct, false
	}
	return acct, true
}

// handleAgentSession returns everything the agent screen renders itself from:
// who the agent is, which campaigns they may work, what they are doing right
// now, and the vocabularies (dispositions, pause codes) for the campaign they
// are on.
//
// It is one call on purpose. The screen needs all of it before it can show
// anything, and an agent reconnecting mid-shift must land back exactly where
// they were.
func (s *Server) handleAgentSession(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.agentAccount(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	state, err := s.Work.State(ctx, acct.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	campaigns := []store.Campaign{}
	for _, id := range acct.Campaigns {
		if c, err := s.Campaigns.Get(ctx, id); err == nil {
			campaigns = append(campaigns, c)
		}
	}

	campaignID := int64(0)
	if state.CampaignID != nil {
		campaignID = *state.CampaignID
	}
	dispositions, _ := s.Campaigns.ListDispositions(ctx, campaignID)
	pauseCodes, _ := s.Campaigns.ListPauseCodes(ctx, campaignID)

	// The lead on screen, if the agent left one there.
	var lead any
	if state.CurrentLead != nil {
		if d, err := s.Leads.Get(ctx, *state.CurrentLead); err == nil {
			lead = d
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"agent": map[string]any{
			"id":          acct.ID,
			"username":    acct.Username,
			"displayName": acct.DisplayName,
			"extension":   acct.Extension,
		},
		"state":        state,
		"campaigns":    campaigns,
		"dispositions": dispositions,
		"pauseCodes":   pauseCodes,
		"lead":         lead,
	})
}

// handleAgentSetCampaign puts the agent on one of their assigned campaigns.
func (s *Server) handleAgentSetCampaign(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.agentAccount(w, r)
	if !ok {
		return
	}
	var body struct {
		CampaignID int64 `json:"campaignId"`
	}
	if !decodeJSON(w, r, &body, 4096) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := s.Work.SetCampaign(ctx, acct.ID, body.CampaignID); err != nil {
		writeExtError(w, err)
		return
	}
	_ = s.Work.Log(ctx, acct.Username, acct.Extension, body.CampaignID, "campaign", "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleAgentPause pauses or resumes the agent. Pausing needs a reason code so
// the not-ready time can be accounted for.
func (s *Server) handleAgentPause(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.agentAccount(w, r)
	if !ok {
		return
	}
	var body struct {
		Paused bool   `json:"paused"`
		Code   string `json:"code"`
	}
	if !decodeJSON(w, r, &body, 4096) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := s.Work.SetPaused(ctx, acct.ID, body.Paused, body.Code); err != nil {
		writeExtError(w, err)
		return
	}
	state, _ := s.Work.State(ctx, acct.ID)
	campaign := int64(0)
	if state.CampaignID != nil {
		campaign = *state.CampaignID
	}
	event := "resume"
	if body.Paused {
		event = "pause"
	}
	_ = s.Work.Log(ctx, acct.Username, acct.Extension, campaign, event, body.Code)

	// The desk's pause state and the agent's ACD availability must not
	// disagree: an agent who stepped away should not still be rung by a queue.
	if s.Ingroups != nil {
		_ = s.Ingroups.SetPaused(ctx, acct.Extension, body.Paused)
		s.reloadQueueMembers(ctx)
	}
	writeJSON(w, http.StatusOK, state)
}

// handleAgentNextLead hands the agent the next lead on their campaign and puts
// it on their screen, without calling anyone: preview dialing is the agent
// deciding, having seen the record.
func (s *Server) handleAgentNextLead(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.agentAccount(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	state, err := s.Work.State(ctx, acct.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if state.CampaignID == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "choose a campaign first"})
		return
	}

	leadID, err := s.LeadCalls.NextPreviewLead(ctx, *state.CampaignID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{
			"lead": nil,
			"note": "no leads left to call on this campaign",
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	lead, err := s.Leads.Get(ctx, leadID)
	if err != nil {
		writeExtError(w, err)
		return
	}
	if err := s.Work.SetCurrent(ctx, acct.ID, leadID, 0); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lead": lead})
}

// handleAgentDial calls the lead on the agent's screen. Asterisk rings the
// agent's own extension first; when they answer it dials the customer through
// the outbound routes already configured.
//
// altPhone dials the lead's alternate number instead of the primary — ViciDial's
// alt-dial, and the reason a lead carries three numbers at all.
func (s *Server) handleAgentDial(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.agentAccount(w, r)
	if !ok {
		return
	}
	var body struct {
		LeadID   int64  `json:"leadId"`   // 0 = whatever is on screen
		AltPhone string `json:"altPhone"` // "" | "alt" | "alt2"
	}
	if !decodeJSON(w, r, &body, 4096) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	state, err := s.Work.State(ctx, acct.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	leadID := body.LeadID
	if leadID == 0 && state.CurrentLead != nil {
		leadID = *state.CurrentLead
	}
	if leadID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no lead on screen"})
		return
	}
	if acct.Extension == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "this agent is not bound to an extension",
		})
		return
	}

	lead, err := s.Leads.Get(ctx, leadID)
	if err != nil {
		writeExtError(w, err)
		return
	}

	number := lead.PhoneCode + lead.PhoneNumber
	switch body.AltPhone {
	case "alt":
		number = lead.AltPhone
	case "alt2":
		number = lead.AltPhoneTwo
	}
	if number == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "that number is empty on this lead"})
		return
	}
	// Re-validate at dial time: the lead may have been edited since import, and
	// this is the last point before a call actually goes out.
	dialed, err := store.CheckPhoneNumber(number)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	callerID := ""
	var campaignID *int64
	if state.CampaignID != nil {
		if c, err := s.Campaigns.Get(ctx, *state.CampaignID); err == nil {
			callerID = c.OutboundCID
			campaignID = &c.ID
		}
	}

	ch, err := s.ARI.Originate(ctx, ari.OriginateParams{
		Endpoint:  "PJSIP/" + acct.Extension,
		Context:   "from-internal",
		Extension: dialed,
		CallerID:  callerID,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	call, err := s.LeadCalls.Start(ctx, store.LeadCall{
		LeadID:     leadID,
		CampaignID: campaignID,
		Agent:      acct.Username,
		Extension:  acct.Extension,
		Direction:  "out",
		ChannelID:  ch.ID,
		Dialed:     dialed,
	})
	if err != nil {
		// The call is up; a logging failure must not read as a failure to dial.
		writeJSON(w, http.StatusOK, map[string]any{"dialed": dialed, "logError": err.Error()})
		return
	}
	_ = s.Work.SetCurrent(ctx, acct.ID, leadID, call.ID)
	writeJSON(w, http.StatusOK, map[string]any{"dialed": dialed, "call": call})
}

// handleAgentDisposition records the outcome of the call, writes the status back
// to the lead, optionally schedules a callback, and clears the agent's screen.
func (s *Server) handleAgentDisposition(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.agentAccount(w, r)
	if !ok {
		return
	}
	var body struct {
		Status     string `json:"status"`
		Note       string `json:"note"`
		LeadID     int64  `json:"leadId"`
		CallbackAt string `json:"callbackAt"` // RFC3339; set when the disposition is a callback
		CallbackTo string `json:"callbackTo"` // ANYONE | USERONLY
	}
	if !decodeJSON(w, r, &body, 1<<16) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	state, err := s.Work.State(ctx, acct.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	leadID := body.LeadID
	if leadID == 0 && state.CurrentLead != nil {
		leadID = *state.CurrentLead
	}
	if leadID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no lead on screen"})
		return
	}

	campaignID := int64(0)
	if state.CampaignID != nil {
		campaignID = *state.CampaignID
	}

	// The code must exist in this campaign's vocabulary, so a stale client
	// cannot invent a status no report or dial rule will ever match.
	dispo, err := s.Campaigns.GetDisposition(ctx, campaignID, body.Status)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "unknown disposition " + body.Status,
		})
		return
	}

	callID := int64(0)
	if state.CurrentCall != nil {
		callID = *state.CurrentCall
	}
	call, err := s.LeadCalls.Apply(ctx, store.DispositionInput{
		CallID: callID,
		LeadID: leadID,
		Status: dispo.Code,
		Note:   body.Note,
		Agent:  acct.Username,
	})
	if err != nil {
		writeExtError(w, err)
		return
	}

	// A promise that has been kept stops resurfacing; a new one replaces it.
	_ = s.Work.CloseCallbacks(ctx, leadID)

	var callback *store.Callback
	if body.CallbackAt != "" {
		cb, err := s.Work.ScheduleCallback(ctx, store.Callback{
			LeadID:     leadID,
			CampaignID: state.CampaignID,
			Agent:      acct.Username,
			Recipient:  body.CallbackTo,
			CallbackAt: body.CallbackAt,
			Note:       body.Note,
		})
		if err != nil {
			// The disposition is already applied and must not be lost; report
			// the scheduling problem alongside it so the agent can retry.
			writeJSON(w, http.StatusOK, map[string]any{
				"call": call, "callbackError": err.Error(),
			})
			return
		}
		callback = &cb
	} else if dispo.Callback {
		// The disposition means "call them back" but no time came with it.
		// Saying so beats silently dropping the promise.
		writeJSON(w, http.StatusOK, map[string]any{
			"call":          call,
			"callbackError": "this disposition schedules a callback, but no time was given",
		})
		_ = s.Work.SetCurrent(ctx, acct.ID, 0, 0)
		return
	}

	if err := s.Work.SetCurrent(ctx, acct.ID, 0, 0); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"call": call, "callback": callback})
}

// handleAgentUpdateLead applies an in-call correction to the lead on screen —
// a misspelled name, a better address. Only contact detail is editable; the
// list, dial state and provenance are not an agent's to change mid-call.
func (s *Server) handleAgentUpdateLead(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.agentAccount(w, r)
	if !ok {
		return
	}
	var body struct {
		LeadID int64             `json:"leadId"`
		Fields map[string]string `json:"fields"`
	}
	if !decodeJSON(w, r, &body, 1<<16) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	leadID := body.LeadID
	if leadID == 0 {
		if state, err := s.Work.State(ctx, acct.ID); err == nil && state.CurrentLead != nil {
			leadID = *state.CurrentLead
		}
	}
	if leadID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no lead on screen"})
		return
	}
	if err := s.Leads.UpdateFields(ctx, leadID, body.Fields); err != nil {
		writeExtError(w, err)
		return
	}
	lead, err := s.Leads.Get(ctx, leadID)
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lead)
}

// handleAgentCallbacks lists the callbacks now due for this agent: their own
// reserved ones, plus unreserved ones on the campaign they are working.
func (s *Server) handleAgentCallbacks(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.agentAccount(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	campaignID := int64(0)
	if state, err := s.Work.State(ctx, acct.ID); err == nil && state.CampaignID != nil {
		campaignID = *state.CampaignID
	}
	// A short look-ahead: a callback due in a few minutes is worth showing now,
	// so an agent can pick it up rather than being handed it late.
	list, err := s.Work.DueCallbacks(ctx, acct.Username, campaignID, 15*time.Minute)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"callbacks": list})
}

// handleAgentTakeLead puts a specific lead on the agent's screen — used to pick
// up a callback.
func (s *Server) handleAgentTakeLead(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.agentAccount(w, r)
	if !ok {
		return
	}
	var body struct {
		LeadID int64 `json:"leadId"`
	}
	if !decodeJSON(w, r, &body, 4096) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	lead, err := s.Leads.Get(ctx, body.LeadID)
	if err != nil {
		writeExtError(w, err)
		return
	}
	if err := s.Work.SetCurrent(ctx, acct.ID, lead.ID, 0); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lead": lead})
}
