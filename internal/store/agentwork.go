package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AgentWork is the agent desktop's own state: which campaign an agent is on,
// whether they are paused and why, which lead is on their screen, and the
// append-only log of how they spent the shift (docs/VICIDIAL_PARITY.md,
// phase 3).
//
// State lives in the database rather than in memory on purpose: a shift
// outlives a browser tab, a softphone that reconnects after a network blip must
// find itself where it left off, and the supervisor board (P8) has to read the
// same state the agent sees.
type AgentWork struct {
	pool *pgxpool.Pool
}

// NewAgentWork returns an AgentWork store bound to a connection pool.
func NewAgentWork(pool *pgxpool.Pool) *AgentWork {
	return &AgentWork{pool: pool}
}

// AgentState is what an agent is doing right now.
type AgentState struct {
	AgentID     int64  `json:"agentId"`
	CampaignID  *int64 `json:"campaignId"`
	Paused      bool   `json:"paused"`
	PauseCode   string `json:"pauseCode"`
	CurrentLead *int64 `json:"currentLead"`
	CurrentCall *int64 `json:"currentCall"`
	Since       string `json:"since"` // RFC3339: when this state began

	// Resolved for display.
	CampaignCode string `json:"campaignCode,omitempty"`
	CampaignName string `json:"campaignName,omitempty"`
	Script       string `json:"script,omitempty"`
	WrapupSecs   int    `json:"wrapupSeconds,omitempty"`
}

// State returns an agent's working state, creating the row on first sight. A
// new agent starts **paused**: opening the app must never be enough to be
// handed a call — going ready is a deliberate act.
func (s *AgentWork) State(ctx context.Context, agentID int64) (AgentState, error) {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO tpbx_agent_state (agent_id) VALUES ($1) ON CONFLICT DO NOTHING`, agentID); err != nil {
		return AgentState{}, err
	}
	var st AgentState
	var since time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT s.agent_id, s.campaign_id, s.paused, s.pause_code, s.current_lead, s.current_call, s.since,
		       COALESCE(c.code,''), COALESCE(c.name,''), COALESCE(c.script,''), COALESCE(c.wrapup_seconds,0)
		  FROM tpbx_agent_state s LEFT JOIN tpbx_campaigns c ON c.id = s.campaign_id
		 WHERE s.agent_id=$1`, agentID).
		Scan(&st.AgentID, &st.CampaignID, &st.Paused, &st.PauseCode, &st.CurrentLead, &st.CurrentCall,
			&since, &st.CampaignCode, &st.CampaignName, &st.Script, &st.WrapupSecs)
	if err != nil {
		return st, err
	}
	st.Since = since.UTC().Format(time.RFC3339)
	return st, nil
}

// SetCampaign puts the agent on a campaign, verifying they are assigned to it.
// Changing campaign clears the lead on screen: a lead belongs to the campaign
// it was pulled from, and carrying it across would disposition it against the
// wrong one.
func (s *AgentWork) SetCampaign(ctx context.Context, agentID, campaignID int64) error {
	if campaignID > 0 {
		var ok bool
		err := s.pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM tpbx_agent_campaigns WHERE agent_id=$1 AND campaign_id=$2)`,
			agentID, campaignID).Scan(&ok)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("you are not assigned to that campaign")
		}
		var active bool
		if err := s.pool.QueryRow(ctx,
			`SELECT active FROM tpbx_campaigns WHERE id=$1`, campaignID).Scan(&active); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if !active {
			return errors.New("that campaign is paused")
		}
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE tpbx_agent_state
		   SET campaign_id=$2, current_lead=NULL, current_call=NULL, since=now(), updated_at=now()
		 WHERE agent_id=$1`, agentID, nullableID(campaignID))
	return err
}

// SetPaused pauses or resumes an agent. A pause code is required to pause, so
// the not-ready time can be accounted for later; resuming clears it.
func (s *AgentWork) SetPaused(ctx context.Context, agentID int64, paused bool, code string) error {
	code = strings.ToUpper(strings.TrimSpace(code))
	if paused && code == "" {
		return errors.New("choose a reason for pausing")
	}
	if !paused {
		code = ""
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE tpbx_agent_state SET paused=$2, pause_code=$3, since=now(), updated_at=now()
		 WHERE agent_id=$1`, agentID, paused, code)
	return err
}

// SetCurrent records which lead (and optionally which call) is on the agent's
// screen. Passing 0 clears it.
func (s *AgentWork) SetCurrent(ctx context.Context, agentID, leadID, callID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE tpbx_agent_state SET current_lead=$2, current_call=$3, updated_at=now()
		 WHERE agent_id=$1`, agentID, nullableID(leadID), nullableID(callID))
	return err
}

// Log appends one time-and-motion event. It is append-only: a shift can be
// reconstructed from these rows even if the live state row was lost.
func (s *AgentWork) Log(ctx context.Context, agent, extension string, campaignID int64, event, pauseCode string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tpbx_agent_log (agent, extension, campaign_id, event, pause_code)
		VALUES ($1,$2,$3,$4,$5)`,
		agent, extension, nullableID(campaignID), event, strings.ToUpper(strings.TrimSpace(pauseCode)))
	return err
}

// AgentLogEntry is one row of the shift log.
type AgentLogEntry struct {
	Agent     string `json:"agent"`
	Event     string `json:"event"`
	PauseCode string `json:"pauseCode,omitempty"`
	At        string `json:"at"`
}

// RecentLog returns an agent's most recent activity, newest first.
func (s *AgentWork) RecentLog(ctx context.Context, agent string, limit int) ([]AgentLogEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT agent, event, pause_code, at FROM tpbx_agent_log
		 WHERE agent=$1 ORDER BY at DESC LIMIT $2`, agent, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentLogEntry{}
	for rows.Next() {
		var e AgentLogEntry
		var at time.Time
		if err := rows.Scan(&e.Agent, &e.Event, &e.PauseCode, &at); err != nil {
			return nil, err
		}
		e.At = at.UTC().Format(time.RFC3339)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Callbacks ------------------------------------------------------------------

// Callback is a promise to call someone back at a given time.
type Callback struct {
	ID         int64  `json:"id"`
	LeadID     int64  `json:"leadId"`
	CampaignID *int64 `json:"campaignId,omitempty"`
	Agent      string `json:"agent"`
	Recipient  string `json:"recipient"` // ANYONE | USERONLY
	CallbackAt string `json:"callbackAt"`
	Note       string `json:"note"`
	Status     string `json:"status"`

	// Joined for the agent's list.
	LeadName  string `json:"leadName,omitempty"`
	LeadPhone string `json:"leadPhone,omitempty"`
}

// ScheduleCallback records a callback. recipient USERONLY reserves it for the
// agent who promised it; ANYONE returns it to the campaign for whoever is free.
func (s *AgentWork) ScheduleCallback(ctx context.Context, c Callback) (Callback, error) {
	if c.LeadID <= 0 {
		return c, errors.New("a callback needs a lead")
	}
	when, err := time.Parse(time.RFC3339, strings.TrimSpace(c.CallbackAt))
	if err != nil {
		return c, errors.New("callback time must be an RFC3339 timestamp")
	}
	// A callback in the past would be due the instant it is made, which is
	// never what the agent meant — it is a typo in the date, so say so.
	if when.Before(time.Now().Add(-time.Minute)) {
		return c, fmt.Errorf("that callback time (%s) is in the past", when.Format(time.RFC3339))
	}
	c.Recipient = strings.ToUpper(strings.TrimSpace(c.Recipient))
	if c.Recipient != "USERONLY" {
		c.Recipient = "ANYONE"
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO tpbx_callbacks (lead_id, campaign_id, agent, recipient, callback_at, note)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		c.LeadID, c.CampaignID, c.Agent, c.Recipient, when, c.Note).Scan(&c.ID)
	if err != nil && strings.Contains(err.Error(), "violates foreign key") {
		return c, ErrNotFound
	}
	c.Status = "PENDING"
	return c, err
}

// DueCallbacks returns the callbacks an agent should act on: their own reserved
// ones plus any unreserved ones on the campaign, due within the window.
func (s *AgentWork) DueCallbacks(ctx context.Context, agent string, campaignID int64, within time.Duration) ([]Callback, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT cb.id, cb.lead_id, cb.campaign_id, cb.agent, cb.recipient, cb.callback_at,
		       cb.note, cb.status,
		       COALESCE(trim(d.first_name || ' ' || d.last_name), ''), COALESCE(d.phone_number,'')
		  FROM tpbx_callbacks cb JOIN tpbx_leads d ON d.id = cb.lead_id
		 WHERE cb.status='PENDING'
		   AND cb.callback_at <= now() + $3::interval
		   AND (cb.agent = $1 OR (cb.recipient='ANYONE' AND ($2::bigint IS NULL OR cb.campaign_id = $2)))
		 ORDER BY cb.callback_at
		 LIMIT 100`, agent, nullableID(campaignID), within.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Callback{}
	for rows.Next() {
		var c Callback
		var when time.Time
		if err := rows.Scan(&c.ID, &c.LeadID, &c.CampaignID, &c.Agent, &c.Recipient, &when,
			&c.Note, &c.Status, &c.LeadName, &c.LeadPhone); err != nil {
			return nil, err
		}
		c.CallbackAt = when.UTC().Format(time.RFC3339)
		out = append(out, c)
	}
	return out, rows.Err()
}

// CloseCallbacks marks a lead's pending callbacks done — called when the lead is
// dispositioned, so a promise that has been kept stops resurfacing.
func (s *AgentWork) CloseCallbacks(ctx context.Context, leadID int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE tpbx_callbacks SET status='DONE' WHERE lead_id=$1 AND status='PENDING'`, leadID)
	return err
}
