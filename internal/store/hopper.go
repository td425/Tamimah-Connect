package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Hopper is the queue of leads the dialer pulls from, plus the Do-Not-Call list
// it must clear them against and the counters the pacing governor reads
// (docs/VICIDIAL_PARITY.md, phase 4).
//
// The hopper exists so the pacing loop never runs a large selection query in
// its hot path: a filler goroutine tops it up in the background, and the loop
// takes the next row and goes.
type Hopper struct {
	pool *pgxpool.Pool
}

// NewHopper returns a Hopper store bound to a connection pool.
func NewHopper(pool *pgxpool.Pool) *Hopper {
	return &Hopper{pool: pool}
}

// HopperEntry is one queued lead.
type HopperEntry struct {
	ID          int64  `json:"id"`
	LeadID      int64  `json:"leadId"`
	CampaignID  int64  `json:"campaignId"`
	Priority    int    `json:"priority"`
	State       string `json:"state"`
	Source      string `json:"source"`
	PhoneNumber string `json:"phoneNumber,omitempty"`
	LeadName    string `json:"leadName,omitempty"`
}

// Fill tops a campaign's hopper up to want entries, returning how many were
// added. Selection applies, in one statement, every rule that decides whether a
// lead may be dialed at all:
//
//   - the campaign is active and the list is active and unexpired,
//   - the lead's status is one the campaign dials,
//   - the lead is not already queued,
//   - the number is on no applicable Do-Not-Call list,
//   - a lead dispositioned recently is not redialed before its disposition's
//     recycle delay has passed.
//
// Doing it as one query matters: a check split between SQL and Go is a check
// that can be skipped by a code path that forgets it, and these are the rules
// that keep the dialer lawful.
func (s *Hopper) Fill(ctx context.Context, campaignID int64, want int) (int, error) {
	if want <= 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO tpbx_hopper (lead_id, campaign_id, priority, source)
		SELECT d.id, c.id, 0, 'filler'
		  FROM tpbx_leads d
		  JOIN tpbx_lists l     ON l.id = d.list_id
		  JOIN tpbx_campaigns c ON c.id = l.campaign_id
		 WHERE c.id = $1
		   AND c.active
		   AND l.active
		   AND (l.expires_on IS NULL OR l.expires_on >= current_date)
		   AND d.status = ANY (c.dial_statuses)
		   AND NOT EXISTS (
		         SELECT 1 FROM tpbx_hopper h
		          WHERE h.lead_id = d.id AND h.state <> 'DONE')
		   AND NOT EXISTS (
		         SELECT 1 FROM tpbx_dnc n
		          WHERE n.phone_number = d.phone_number
		            AND (n.campaign_id IS NULL OR n.campaign_id = c.id))
		   AND (
		         d.last_called_at IS NULL
		         OR NOT EXISTS (
		              SELECT 1 FROM tpbx_dispositions p
			       WHERE p.code = d.status
			         AND (p.campaign_id IS NULL OR p.campaign_id = c.id)
			         AND (p.recycle_after_sec = 0
			              OR d.last_called_at + make_interval(secs => p.recycle_after_sec) > now()))
		       )
		 ORDER BY d.last_called_at NULLS FIRST, d.id
		 LIMIT $2
		ON CONFLICT DO NOTHING`, campaignID, want)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// Depth returns how many READY entries a campaign has queued.
func (s *Hopper) Depth(ctx context.Context, campaignID int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM tpbx_hopper WHERE campaign_id=$1 AND state='READY'`, campaignID).Scan(&n)
	return n, err
}

// Take claims the next lead for dialing, marking it DIALING in the same
// statement so two pacing ticks — or two servers — cannot hand the same person
// to two different agents.
func (s *Hopper) Take(ctx context.Context, campaignID int64) (HopperEntry, error) {
	var e HopperEntry
	err := s.pool.QueryRow(ctx, `
		UPDATE tpbx_hopper SET state='DIALING'
		 WHERE id = (
		     SELECT h.id FROM tpbx_hopper h
		      WHERE h.campaign_id=$1 AND h.state='READY'
		      ORDER BY h.priority DESC, h.id
		      FOR UPDATE SKIP LOCKED
		      LIMIT 1)
		RETURNING id, lead_id, campaign_id, priority, state, source`, campaignID).
		Scan(&e.ID, &e.LeadID, &e.CampaignID, &e.Priority, &e.State, &e.Source)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// Release returns a claimed entry to READY — used when the dial could not be
// placed at all, so the lead is not silently lost.
func (s *Hopper) Release(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE tpbx_hopper SET state='READY' WHERE id=$1`, id)
	return err
}

// Done marks an entry finished.
func (s *Hopper) Done(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE tpbx_hopper SET state='DONE' WHERE id=$1`, id)
	return err
}

// Purge clears a campaign's queue, e.g. when its dialer is stopped.
func (s *Hopper) Purge(ctx context.Context, campaignID int64) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM tpbx_hopper WHERE campaign_id=$1`, campaignID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ReleaseStale returns entries stuck in DIALING back to READY. A dial that was
// claimed but never completed — the process died mid-call, Asterisk went away —
// would otherwise hold that lead out of the queue forever.
func (s *Hopper) ReleaseStale(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE tpbx_hopper SET state='READY'
		 WHERE state='DIALING' AND inserted_at < now() - make_interval(secs => $1)`,
		olderThan.Seconds())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// List returns a campaign's queued leads for the console.
func (s *Hopper) List(ctx context.Context, campaignID int64, limit int) ([]HopperEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT h.id, h.lead_id, h.campaign_id, h.priority, h.state, h.source,
		       d.phone_number, COALESCE(trim(d.first_name || ' ' || d.last_name), '')
		  FROM tpbx_hopper h JOIN tpbx_leads d ON d.id = h.lead_id
		 WHERE h.campaign_id=$1 AND h.state <> 'DONE'
		 ORDER BY h.priority DESC, h.id
		 LIMIT $2`, campaignID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HopperEntry{}
	for rows.Next() {
		var e HopperEntry
		if err := rows.Scan(&e.ID, &e.LeadID, &e.CampaignID, &e.Priority, &e.State, &e.Source,
			&e.PhoneNumber, &e.LeadName); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Do-Not-Call ----------------------------------------------------------------

// DNCEntry is one suppressed number.
type DNCEntry struct {
	ID          int64  `json:"id"`
	PhoneNumber string `json:"phoneNumber"`
	CampaignID  *int64 `json:"campaignId"` // nil = global
	Reason      string `json:"reason"`
	AddedBy     string `json:"addedBy"`
	CreatedAt   string `json:"createdAt"`
}

// AddDNC suppresses a number, globally or for one campaign. Adding a number
// that is already suppressed at that scope is not an error: the caller wanted
// it suppressed, and it is.
func (s *Hopper) AddDNC(ctx context.Context, e DNCEntry) (DNCEntry, error) {
	clean, err := CheckPhoneNumber(e.PhoneNumber)
	if err != nil {
		return e, err
	}
	e.PhoneNumber = clean
	err = s.pool.QueryRow(ctx, `
		INSERT INTO tpbx_dnc (phone_number, campaign_id, reason, added_by)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT DO NOTHING
		RETURNING id`, e.PhoneNumber, e.CampaignID, e.Reason, e.AddedBy).Scan(&e.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, nil // already suppressed at this scope
	}
	if err != nil && strings.Contains(err.Error(), "violates foreign key") {
		return e, ErrNotFound
	}
	return e, err
}

// RemoveDNC lifts a suppression by id.
func (s *Hopper) RemoveDNC(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM tpbx_dnc WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// IsDNC reports whether a number is suppressed for a campaign. The dialer calls
// this immediately before placing a call, not only at hopper-fill: a number can
// be added to the list in the seconds between the two, and the later check is
// the one that matters.
func (s *Hopper) IsDNC(ctx context.Context, number string, campaignID int64) (bool, error) {
	var found bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM tpbx_dnc
		     WHERE phone_number = $1
		       AND (campaign_id IS NULL OR campaign_id = $2))`,
		number, nullableID(campaignID)).Scan(&found)
	return found, err
}

// ListDNC returns suppressed numbers, newest first.
func (s *Hopper) ListDNC(ctx context.Context, campaignID int64, search string, limit int) ([]DNCEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, phone_number, campaign_id, reason, added_by, created_at
		  FROM tpbx_dnc
		 WHERE ($1::bigint IS NULL OR campaign_id IS NULL OR campaign_id = $1)
		   AND ($2 = '' OR phone_number LIKE '%'||$2||'%')
		 ORDER BY created_at DESC
		 LIMIT $3`, nullableID(campaignID), strings.TrimSpace(search), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DNCEntry{}
	for rows.Next() {
		var e DNCEntry
		var at time.Time
		if err := rows.Scan(&e.ID, &e.PhoneNumber, &e.CampaignID, &e.Reason, &e.AddedBy, &at); err != nil {
			return nil, err
		}
		e.CreatedAt = at.UTC().Format(time.RFC3339)
		out = append(out, e)
	}
	return out, rows.Err()
}
