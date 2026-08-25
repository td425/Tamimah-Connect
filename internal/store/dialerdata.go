package store

import (
	"context"
	"time"
)

// This file holds the runtime reads the dialer engine makes on every tick:
// which agents are free, and how the campaign's recent calls have gone. They
// live in the store rather than the engine so the same numbers back the
// console's live campaign panel.

// ReadyAgent is an agent the dialer may hand a call to.
type ReadyAgent struct {
	AgentID   int64  `json:"agentId"`
	Username  string `json:"username"`
	Extension string `json:"extension"`
	Since     string `json:"since"`
}

// ReadyAgents returns the agents currently available on a campaign: signed in,
// on this campaign, not paused, not already on a call, and bound to a device.
// Longest-idle first, which is the fair way to hand out work and matches what
// agents expect.
func (s *AgentWork) ReadyAgents(ctx context.Context, campaignID int64) ([]ReadyAgent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.username, a.extension, st.since
		  FROM tpbx_agent_state st
		  JOIN tpbx_agents a ON a.id = st.agent_id
		 WHERE st.campaign_id = $1
		   AND NOT st.paused
		   AND st.current_call IS NULL
		   AND a.active
		   AND a.extension <> ''
		 ORDER BY st.since`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ReadyAgent{}
	for rows.Next() {
		var a ReadyAgent
		var since time.Time
		if err := rows.Scan(&a.AgentID, &a.Username, &a.Extension, &since); err != nil {
			return nil, err
		}
		a.Since = since.UTC().Format(time.RFC3339)
		out = append(out, a)
	}
	return out, rows.Err()
}

// CampaignCalls is the recent automatic-dialing record a campaign is paced by.
type CampaignCalls struct {
	Placed   int `json:"placed"`
	Answered int `json:"answered"`
	Dropped  int `json:"dropped"`
	Live     int `json:"live"` // placed, not yet finished
}

// RecentCalls counts a campaign's automatic calls over a window. Only
// engine-placed calls are counted: an agent's own manual dial is not something
// the pacing governor caused, and folding it in would distort both rates.
func (s *AgentWork) RecentCalls(ctx context.Context, campaignID int64, window time.Duration) (CampaignCalls, error) {
	var c CampaignCalls
	err := s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE answered_at IS NOT NULL),
		       count(*) FILTER (WHERE dropped),
		       count(*) FILTER (WHERE ended_at IS NULL)
		  FROM tpbx_lead_calls
		 WHERE campaign_id = $1
		   AND placed_by = 'auto'
		   AND started_at >= now() - make_interval(secs => $2)`,
		campaignID, window.Seconds()).Scan(&c.Placed, &c.Answered, &c.Dropped, &c.Live)
	return c, err
}

// RunningCampaign is a campaign the engine should be dialing for.
type RunningCampaign struct {
	ID          int64
	Code        string
	DialMethod  string
	DialLevel   float64
	AdaptiveMax float64
	HopperLevel int
	DialTimeout int
	DropCeiling float64
	OutboundCID string
	Trunk       string
	AMDEnabled  bool
}

// RunningCampaigns returns the campaigns whose dialer has been started. Both
// flags must hold: `active` says the operation exists, `dialer_running` says a
// supervisor turned the machine on. Stopping the dialer must not retire the
// campaign, so the two are separate.
func (s *AgentWork) RunningCampaigns(ctx context.Context) ([]RunningCampaign, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, code, dial_method, dial_level, adaptive_max, hopper_level,
		       dial_timeout, drop_rate_target, outbound_cid, trunk, amd_enabled
		  FROM tpbx_campaigns
		 WHERE active AND dialer_running
		 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RunningCampaign{}
	for rows.Next() {
		var c RunningCampaign
		if err := rows.Scan(&c.ID, &c.Code, &c.DialMethod, &c.DialLevel, &c.AdaptiveMax,
			&c.HopperLevel, &c.DialTimeout, &c.DropCeiling, &c.OutboundCID, &c.Trunk,
			&c.AMDEnabled); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetDialerRunning starts or stops a campaign's automatic dialing.
func (s *AgentWork) SetDialerRunning(ctx context.Context, campaignID int64, running bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tpbx_campaigns SET dialer_running=$2, updated_at=now() WHERE id=$1`, campaignID, running)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
