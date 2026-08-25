package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LeadCalls records one row per dial attempt on a lead, and applies the
// disposition an agent gives when the call ends (docs/VICIDIAL_PARITY.md,
// phase 2).
//
// Phase 2 writes these rows from manual and preview dialing. Phase 4's
// automatic dialer writes the same rows, which is why the shape carries channel
// and timing detail that a manual dial does not fill in.
type LeadCalls struct {
	pool *pgxpool.Pool
}

// NewLeadCalls returns a LeadCalls store bound to a connection pool.
func NewLeadCalls(pool *pgxpool.Pool) *LeadCalls {
	return &LeadCalls{pool: pool}
}

// LeadCall is one attempt to reach a lead.
type LeadCall struct {
	ID         int64  `json:"id"`
	LeadID     int64  `json:"leadId"`
	CampaignID *int64 `json:"campaignId,omitempty"`
	Agent      string `json:"agent"`
	Extension  string `json:"extension"`
	Direction  string `json:"direction"`
	ChannelID  string `json:"channelId,omitempty"`
	Dialed     string `json:"dialed"`
	StartedAt  string `json:"startedAt"`
	EndedAt    string `json:"endedAt,omitempty"`
	Status     string `json:"status,omitempty"` // disposition code, once given
	Note       string `json:"note,omitempty"`

	// Filled for display.
	LeadName  string `json:"leadName,omitempty"`
	LeadPhone string `json:"leadPhone,omitempty"`
}

// Start records a new attempt and bumps the lead's call counters, so
// "called 3 times, last on…" stays true whether the call was placed by hand or
// by the dialer.
func (s *LeadCalls) Start(ctx context.Context, c LeadCall) (LeadCall, error) {
	if c.LeadID <= 0 {
		return c, errors.New("a call must reference a lead")
	}
	if c.Direction == "" {
		c.Direction = "out"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return c, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var started time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO tpbx_lead_calls (lead_id, campaign_id, agent, extension, direction, channel_id, dialed)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id, started_at`,
		c.LeadID, c.CampaignID, c.Agent, c.Extension, c.Direction, c.ChannelID, c.Dialed).
		Scan(&c.ID, &started)
	if err != nil {
		if strings.Contains(err.Error(), "violates foreign key") {
			return c, ErrNotFound
		}
		return c, err
	}
	c.StartedAt = started.UTC().Format(time.RFC3339)

	if _, err := tx.Exec(ctx, `
		UPDATE tpbx_leads
		   SET called_count = called_count + 1, last_called_at = now(), updated_at = now()
		 WHERE id = $1`, c.LeadID); err != nil {
		return c, err
	}
	return c, tx.Commit(ctx)
}

// Disposition is the outcome an agent reports for an attempt.
type DispositionInput struct {
	CallID int64  // 0 = apply to the lead's most recent open attempt
	LeadID int64  // required when CallID is 0
	Status string // disposition code
	Note   string
	Agent  string
}

// Apply stamps a disposition onto an attempt and writes the resulting status
// back to the lead. This is the whole point of the phase: what the agent says
// happened becomes the lead's state, so the next pass over the list sees it.
//
// The disposition's semantics are resolved from the campaign's vocabulary. The
// flags that need a dialer (recycle timing) or a compliance layer (DNC) are
// stored on the lead's status and acted on in P4/P6; nothing here silently
// half-implements them.
func (s *LeadCalls) Apply(ctx context.Context, in DispositionInput) (LeadCall, error) {
	var c LeadCall
	in.Status = strings.ToUpper(strings.TrimSpace(in.Status))
	if in.Status == "" || len(in.Status) > 16 {
		return c, errors.New("disposition code must be 1-16 characters")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return c, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Find the attempt to stamp: the one named, or the lead's latest.
	var (
		callID int64
		leadID int64
	)
	if in.CallID > 0 {
		err = tx.QueryRow(ctx, `SELECT id, lead_id FROM tpbx_lead_calls WHERE id=$1`, in.CallID).
			Scan(&callID, &leadID)
	} else {
		err = tx.QueryRow(ctx, `
			SELECT id, lead_id FROM tpbx_lead_calls
			 WHERE lead_id=$1 ORDER BY started_at DESC LIMIT 1`, in.LeadID).
			Scan(&callID, &leadID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// A lead can legitimately be dispositioned without an attempt on record
		// — an agent marking up a lead they reached another way. Record the
		// outcome as a completed attempt rather than losing it.
		if in.LeadID <= 0 {
			return c, ErrNotFound
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO tpbx_lead_calls (lead_id, agent, direction, started_at, ended_at, status, note)
			VALUES ($1,$2,'out',now(),now(),$3,$4) RETURNING id, lead_id`,
			in.LeadID, in.Agent, in.Status, in.Note).Scan(&callID, &leadID)
	}
	if err != nil {
		return c, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE tpbx_lead_calls
		   SET status=$2, note=$3, ended_at=COALESCE(ended_at, now())
		 WHERE id=$1`, callID, in.Status, in.Note); err != nil {
		return c, err
	}

	// last_status keeps the previous outcome visible after the new one lands,
	// which is what makes "was NA, now SALE" legible in the lead browser.
	if _, err := tx.Exec(ctx, `
		UPDATE tpbx_leads
		   SET last_status = status, status = $2, updated_at = now()
		 WHERE id = $1`, leadID, in.Status); err != nil {
		return c, err
	}

	if err := tx.Commit(ctx); err != nil {
		return c, err
	}
	return s.Get(ctx, callID)
}

// Get returns one call record.
func (s *LeadCalls) Get(ctx context.Context, id int64) (LeadCall, error) {
	var c LeadCall
	var started time.Time
	var ended *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT lc.id, lc.lead_id, lc.campaign_id, lc.agent, lc.extension, lc.direction,
		       lc.channel_id, lc.dialed, lc.started_at, lc.ended_at, lc.status, lc.note,
		       COALESCE(trim(d.first_name || ' ' || d.last_name), ''), COALESCE(d.phone_number,'')
		  FROM tpbx_lead_calls lc LEFT JOIN tpbx_leads d ON d.id = lc.lead_id
		 WHERE lc.id=$1`, id).
		Scan(&c.ID, &c.LeadID, &c.CampaignID, &c.Agent, &c.Extension, &c.Direction,
			&c.ChannelID, &c.Dialed, &started, &ended, &c.Status, &c.Note,
			&c.LeadName, &c.LeadPhone)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.StartedAt = started.UTC().Format(time.RFC3339)
	if ended != nil {
		c.EndedAt = ended.UTC().Format(time.RFC3339)
	}
	return c, nil
}

// ForLead returns a lead's attempt history, newest first.
func (s *LeadCalls) ForLead(ctx context.Context, leadID int64, limit int) ([]LeadCall, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, lead_id, campaign_id, agent, extension, direction, channel_id, dialed,
		       started_at, ended_at, status, note
		  FROM tpbx_lead_calls WHERE lead_id=$1 ORDER BY started_at DESC LIMIT $2`, leadID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []LeadCall{}
	for rows.Next() {
		var c LeadCall
		var started time.Time
		var ended *time.Time
		if err := rows.Scan(&c.ID, &c.LeadID, &c.CampaignID, &c.Agent, &c.Extension,
			&c.Direction, &c.ChannelID, &c.Dialed, &started, &ended, &c.Status, &c.Note); err != nil {
			return nil, err
		}
		c.StartedAt = started.UTC().Format(time.RFC3339)
		if ended != nil {
			c.EndedAt = ended.UTC().Format(time.RFC3339)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NextPreviewLead hands an agent the next lead to preview from a campaign: the
// longest-untouched dialable lead in the campaign's active lists, whose status
// the campaign is willing to dial.
//
// This is a simplified stand-in for the P4 hopper — it selects one lead on
// demand rather than maintaining a queue, and it deliberately does not apply
// call-time or DNC rules, which arrive in P6. Preview dialing is agent-paced,
// so a human sees every number before it is called.
func (s *LeadCalls) NextPreviewLead(ctx context.Context, campaignID int64) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		SELECT d.id
		  FROM tpbx_leads d
		  JOIN tpbx_lists l    ON l.id = d.list_id
		  JOIN tpbx_campaigns c ON c.id = l.campaign_id
		 WHERE c.id = $1
		   AND c.active AND l.active
		   AND (l.expires_on IS NULL OR l.expires_on >= current_date)
		   AND d.status = ANY (c.dial_statuses)
		 ORDER BY d.last_called_at NULLS FIRST, d.id
		 LIMIT 1`, campaignID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}
