package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Campaigns is the store for outbound campaigns and the two vocabularies each
// one owns: dispositions (what happened on a call) and pause codes (why an
// agent is not taking calls). See docs/VICIDIAL_PARITY.md, phase 2.
//
// The pacing fields on a campaign are persisted but not yet acted upon —
// nothing dials automatically until the P4 engine. Manual and preview dialing,
// which phase 2 does deliver, use only OutboundCID, Trunk and DialTimeout.
type Campaigns struct {
	pool *pgxpool.Pool
}

// NewCampaigns returns a Campaigns store bound to a connection pool.
func NewCampaigns(pool *pgxpool.Pool) *Campaigns {
	return &Campaigns{pool: pool}
}

// DialMethods are the pacing strategies a campaign may use. MANUAL and PREVIEW
// are agent-driven and work today; the rest need the P4 dialer.
var DialMethods = []string{
	"MANUAL", "PREVIEW", "RATIO",
	"ADAPT_AVERAGE", "ADAPT_HARD_LIMIT", "ADAPT_TAPERED", "INBOUND_MAN",
}

// AutomaticDialing reports whether a dial method needs the dialer engine. The
// console uses it to mark a campaign as "waiting for the dialer" rather than
// letting an operator believe RATIO is already calling people.
func AutomaticDialing(method string) bool {
	switch method {
	case "MANUAL", "PREVIEW", "":
		return false
	default:
		return true
	}
}

// LeadOrders are the orders the hopper filler will pull leads in (P4).
var LeadOrders = []string{"DOWN", "UP", "RANDOM", "OLDEST_CALL", "FEWEST_CALLS"}

// Campaign is one calling operation: who to call, how fast, and how the call
// presents itself.
type Campaign struct {
	ID          int64  `json:"id"`
	Code        string `json:"code"` // short dialplan-safe key, e.g. WEBOUT
	Name        string `json:"name"`
	Description string `json:"description"`
	Active      bool   `json:"active"`

	DialMethod     string   `json:"dialMethod"`
	DialLevel      float64  `json:"dialLevel"`
	AdaptiveMax    float64  `json:"adaptiveMax"`
	HopperLevel    int      `json:"hopperLevel"`
	DialTimeout    int      `json:"dialTimeout"`
	LeadOrder      string   `json:"leadOrder"`
	DialStatuses   []string `json:"dialStatuses"`
	DropRateTarget float64  `json:"dropRateTarget"`
	AMDEnabled     bool     `json:"amdEnabled"`

	OutboundCID   string `json:"outboundCid"`
	Trunk         string `json:"trunk"`
	WrapupSeconds int    `json:"wrapupSeconds"`
	Script        string `json:"script"`

	// Computed for display, never written.
	ListCount  int `json:"listCount"`
	LeadCount  int `json:"leadCount"`
	AgentCount int `json:"agentCount"`
}

func (c *Campaign) normalise() error {
	c.Code = strings.ToUpper(strings.TrimSpace(c.Code))
	if len(c.Code) < 2 || len(c.Code) > 16 {
		return errors.New("campaign code must be 2-16 characters")
	}
	for _, r := range c.Code {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return fmt.Errorf("campaign code %q may only contain letters, digits, _ and -", c.Code)
		}
	}
	if c.Name = strings.TrimSpace(c.Name); c.Name == "" {
		c.Name = c.Code
	}
	if len(c.Name) > 64 {
		return errors.New("campaign name is longer than 64 characters")
	}

	if c.DialMethod == "" {
		c.DialMethod = "MANUAL"
	}
	c.DialMethod = strings.ToUpper(c.DialMethod)
	if !contains(DialMethods, c.DialMethod) {
		return fmt.Errorf("unknown dial method %q", c.DialMethod)
	}
	if c.LeadOrder == "" {
		c.LeadOrder = "DOWN"
	}
	c.LeadOrder = strings.ToUpper(c.LeadOrder)
	if !contains(LeadOrders, c.LeadOrder) {
		return fmt.Errorf("unknown lead order %q", c.LeadOrder)
	}

	if c.DialLevel <= 0 {
		c.DialLevel = 1
	}
	if c.DialLevel > 10 {
		return errors.New("dial level above 10 lines per agent is not sane")
	}
	if c.AdaptiveMax <= 0 {
		c.AdaptiveMax = 3
	}
	if c.AdaptiveMax > 10 {
		return errors.New("adaptive maximum above 10 lines per agent is not sane")
	}
	if c.HopperLevel <= 0 {
		c.HopperLevel = 50
	}
	if c.HopperLevel > 2000 {
		return errors.New("hopper level must be 2000 or less")
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 30
	}
	if c.DialTimeout > 120 {
		return errors.New("dial timeout must be 120 seconds or less")
	}
	// The drop-rate ceiling is a compliance limit. Refuse to store a value that
	// would let the P4 pacing loop abandon calls beyond what regulators allow,
	// rather than discovering it when the dialer starts running.
	if c.DropRateTarget <= 0 {
		c.DropRateTarget = 3
	}
	if c.DropRateTarget > 10 {
		return errors.New("drop-rate ceiling above 10% is not permitted; 3% is the common regulatory limit")
	}
	if c.WrapupSeconds < 0 || c.WrapupSeconds > 600 {
		return errors.New("wrap-up must be between 0 and 600 seconds")
	}

	if len(c.DialStatuses) == 0 {
		c.DialStatuses = []string{"NEW"}
	}
	clean := make([]string, 0, len(c.DialStatuses))
	seen := map[string]bool{}
	for _, s := range c.DialStatuses {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		clean = append(clean, s)
	}
	c.DialStatuses = clean

	// Store the caller ID in the digits-only form, not as the operator typed
	// it: this value goes straight into Asterisk's CALLERID, where brackets and
	// spaces are not presentation, they are wrong.
	if c.OutboundCID != "" {
		clean, err := CheckPhoneNumber(c.OutboundCID)
		if err != nil {
			return fmt.Errorf("outbound caller ID: %w", err)
		}
		c.OutboundCID = clean
	}
	return nil
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

const campaignColumns = `
	c.id, c.code, c.name, c.description, c.active,
	c.dial_method, c.dial_level, c.adaptive_max, c.hopper_level, c.dial_timeout,
	c.lead_order, c.dial_statuses, c.drop_rate_target, c.amd_enabled,
	c.outbound_cid, c.trunk, c.wrapup_seconds, c.script,
	(SELECT count(*) FROM tpbx_lists l WHERE l.campaign_id = c.id),
	(SELECT count(*) FROM tpbx_leads d JOIN tpbx_lists l2 ON l2.id = d.list_id WHERE l2.campaign_id = c.id),
	(SELECT count(*) FROM tpbx_agent_campaigns ac WHERE ac.campaign_id = c.id)`

func scanCampaign(row pgx.Row) (Campaign, error) {
	var c Campaign
	err := row.Scan(&c.ID, &c.Code, &c.Name, &c.Description, &c.Active,
		&c.DialMethod, &c.DialLevel, &c.AdaptiveMax, &c.HopperLevel, &c.DialTimeout,
		&c.LeadOrder, &c.DialStatuses, &c.DropRateTarget, &c.AMDEnabled,
		&c.OutboundCID, &c.Trunk, &c.WrapupSeconds, &c.Script,
		&c.ListCount, &c.LeadCount, &c.AgentCount)
	return c, err
}

// List returns every campaign, ordered by code.
func (s *Campaigns) List(ctx context.Context) ([]Campaign, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+campaignColumns+` FROM tpbx_campaigns c ORDER BY c.code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get returns one campaign by id.
func (s *Campaigns) Get(ctx context.Context, id int64) (Campaign, error) {
	c, err := scanCampaign(s.pool.QueryRow(ctx,
		`SELECT `+campaignColumns+` FROM tpbx_campaigns c WHERE c.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// GetByCode returns one campaign by its short code.
func (s *Campaigns) GetByCode(ctx context.Context, code string) (Campaign, error) {
	c, err := scanCampaign(s.pool.QueryRow(ctx,
		`SELECT `+campaignColumns+` FROM tpbx_campaigns c WHERE c.code=$1`,
		strings.ToUpper(strings.TrimSpace(code))))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// Create inserts a campaign. It does not seed dispositions: the system-wide set
// from migration 0027 already applies to every campaign, and a campaign only
// needs its own rows when it wants to differ.
func (s *Campaigns) Create(ctx context.Context, c Campaign) (Campaign, error) {
	if err := c.normalise(); err != nil {
		return c, err
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO tpbx_campaigns (
			code, name, description, active,
			dial_method, dial_level, adaptive_max, hopper_level, dial_timeout,
			lead_order, dial_statuses, drop_rate_target, amd_enabled,
			outbound_cid, trunk, wrapup_seconds, script)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		RETURNING id`,
		c.Code, c.Name, c.Description, c.Active,
		c.DialMethod, c.DialLevel, c.AdaptiveMax, c.HopperLevel, c.DialTimeout,
		c.LeadOrder, c.DialStatuses, c.DropRateTarget, c.AMDEnabled,
		c.OutboundCID, c.Trunk, c.WrapupSeconds, c.Script).Scan(&c.ID)
	if err != nil && strings.Contains(err.Error(), "duplicate key") {
		return c, ErrConflict
	}
	return c, err
}

// Update rewrites a campaign's settings. The code is immutable: lists, agents
// and call records all point at this campaign, and renaming its key would
// silently re-target them.
func (s *Campaigns) Update(ctx context.Context, c Campaign) error {
	existing, err := s.Get(ctx, c.ID)
	if err != nil {
		return err
	}
	c.Code = existing.Code
	if err := c.normalise(); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE tpbx_campaigns SET
			name=$2, description=$3, active=$4,
			dial_method=$5, dial_level=$6, adaptive_max=$7, hopper_level=$8, dial_timeout=$9,
			lead_order=$10, dial_statuses=$11, drop_rate_target=$12, amd_enabled=$13,
			outbound_cid=$14, trunk=$15, wrapup_seconds=$16, script=$17, updated_at=now()
		 WHERE id=$1`,
		c.ID, c.Name, c.Description, c.Active,
		c.DialMethod, c.DialLevel, c.AdaptiveMax, c.HopperLevel, c.DialTimeout,
		c.LeadOrder, c.DialStatuses, c.DropRateTarget, c.AMDEnabled,
		c.OutboundCID, c.Trunk, c.WrapupSeconds, c.Script)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a campaign. Its lists survive and fall back to unassigned
// (ON DELETE SET NULL) — leads are customer data and must never disappear
// because an operation was retired.
func (s *Campaigns) Delete(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM tpbx_campaigns WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Dispositions ---------------------------------------------------------------

// Disposition is an outcome an agent may record, and what that outcome means to
// the dialer and the reports.
type Disposition struct {
	ID         int64  `json:"id"`
	CampaignID *int64 `json:"campaignId"` // nil = system-wide
	Code       string `json:"code"`
	Name       string `json:"name"`
	Selectable bool   `json:"selectable"`

	HumanAnswered   bool `json:"humanAnswered"`
	IsSale          bool `json:"isSale"`
	NotInterested   bool `json:"notInterested"`
	DNC             bool `json:"dnc"`
	Callback        bool `json:"callback"`
	RecycleAfterSec int  `json:"recycleAfterSec"`
	Position        int  `json:"position"`
}

func (d *Disposition) normalise() error {
	d.Code = strings.ToUpper(strings.TrimSpace(d.Code))
	if len(d.Code) < 1 || len(d.Code) > 16 {
		return errors.New("disposition code must be 1-16 characters")
	}
	if d.Name = strings.TrimSpace(d.Name); d.Name == "" {
		d.Name = d.Code
	}
	if d.RecycleAfterSec < 0 {
		return errors.New("recycle delay cannot be negative")
	}
	return nil
}

// ListDispositions returns the dispositions available to a campaign: its own
// rows plus the system-wide ones. Passing 0 returns only the system-wide set.
func (s *Campaigns) ListDispositions(ctx context.Context, campaignID int64) ([]Disposition, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, campaign_id, code, name, selectable, human_answered, is_sale,
		       not_interested, dnc, callback, recycle_after_sec, position
		  FROM tpbx_dispositions
		 WHERE campaign_id IS NULL OR campaign_id = $1
		 ORDER BY campaign_id NULLS FIRST, position, code`, nullableID(campaignID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Disposition{}
	for rows.Next() {
		var d Disposition
		if err := rows.Scan(&d.ID, &d.CampaignID, &d.Code, &d.Name, &d.Selectable,
			&d.HumanAnswered, &d.IsSale, &d.NotInterested, &d.DNC, &d.Callback,
			&d.RecycleAfterSec, &d.Position); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDisposition resolves one disposition code for a campaign, preferring the
// campaign's own row over the system-wide one of the same code.
func (s *Campaigns) GetDisposition(ctx context.Context, campaignID int64, code string) (Disposition, error) {
	var d Disposition
	err := s.pool.QueryRow(ctx, `
		SELECT id, campaign_id, code, name, selectable, human_answered, is_sale,
		       not_interested, dnc, callback, recycle_after_sec, position
		  FROM tpbx_dispositions
		 WHERE code = $2 AND (campaign_id IS NULL OR campaign_id = $1)
		 ORDER BY campaign_id NULLS LAST
		 LIMIT 1`, nullableID(campaignID), strings.ToUpper(strings.TrimSpace(code))).
		Scan(&d.ID, &d.CampaignID, &d.Code, &d.Name, &d.Selectable,
			&d.HumanAnswered, &d.IsSale, &d.NotInterested, &d.DNC, &d.Callback,
			&d.RecycleAfterSec, &d.Position)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// SaveDisposition inserts or updates one disposition.
func (s *Campaigns) SaveDisposition(ctx context.Context, d Disposition) (Disposition, error) {
	if err := d.normalise(); err != nil {
		return d, err
	}
	if d.ID > 0 {
		tag, err := s.pool.Exec(ctx, `
			UPDATE tpbx_dispositions SET
				code=$2, name=$3, selectable=$4, human_answered=$5, is_sale=$6,
				not_interested=$7, dnc=$8, callback=$9, recycle_after_sec=$10, position=$11
			 WHERE id=$1`,
			d.ID, d.Code, d.Name, d.Selectable, d.HumanAnswered, d.IsSale,
			d.NotInterested, d.DNC, d.Callback, d.RecycleAfterSec, d.Position)
		if err != nil {
			return d, dispositionErr(err)
		}
		if tag.RowsAffected() == 0 {
			return d, ErrNotFound
		}
		return d, nil
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO tpbx_dispositions (campaign_id, code, name, selectable, human_answered,
			is_sale, not_interested, dnc, callback, recycle_after_sec, position)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
		d.CampaignID, d.Code, d.Name, d.Selectable, d.HumanAnswered, d.IsSale,
		d.NotInterested, d.DNC, d.Callback, d.RecycleAfterSec, d.Position).Scan(&d.ID)
	if err != nil {
		return d, dispositionErr(err)
	}
	return d, nil
}

// DeleteDisposition removes one. System-wide rows are protected: they are the
// vocabulary every campaign falls back to, and the statuses already written on
// existing leads refer to them.
func (s *Campaigns) DeleteDisposition(ctx context.Context, id int64) error {
	var campaign *int64
	err := s.pool.QueryRow(ctx, `SELECT campaign_id FROM tpbx_dispositions WHERE id=$1`, id).Scan(&campaign)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if campaign == nil {
		return errors.New("system-wide dispositions cannot be deleted; add a campaign-specific one to override it")
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM tpbx_dispositions WHERE id=$1`, id)
	return err
}

func dispositionErr(err error) error {
	if strings.Contains(err.Error(), "duplicate key") {
		return fmt.Errorf("%w: that disposition code is already defined here", ErrConflict)
	}
	return err
}

// Pause codes ----------------------------------------------------------------

// PauseCode is a reason an agent is not taking calls.
type PauseCode struct {
	ID         int64  `json:"id"`
	CampaignID *int64 `json:"campaignId"` // nil = system-wide
	Code       string `json:"code"`
	Name       string `json:"name"`
	Billable   bool   `json:"billable"`
	Position   int    `json:"position"`
}

// ListPauseCodes returns a campaign's pause codes plus the system-wide ones.
func (s *Campaigns) ListPauseCodes(ctx context.Context, campaignID int64) ([]PauseCode, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, campaign_id, code, name, billable, position
		  FROM tpbx_pause_codes
		 WHERE campaign_id IS NULL OR campaign_id = $1
		 ORDER BY campaign_id NULLS FIRST, position, code`, nullableID(campaignID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PauseCode{}
	for rows.Next() {
		var p PauseCode
		if err := rows.Scan(&p.ID, &p.CampaignID, &p.Code, &p.Name, &p.Billable, &p.Position); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SavePauseCode inserts or updates one pause code.
func (s *Campaigns) SavePauseCode(ctx context.Context, p PauseCode) (PauseCode, error) {
	p.Code = strings.ToUpper(strings.TrimSpace(p.Code))
	if len(p.Code) < 1 || len(p.Code) > 16 {
		return p, errors.New("pause code must be 1-16 characters")
	}
	if p.Name = strings.TrimSpace(p.Name); p.Name == "" {
		p.Name = p.Code
	}
	if p.ID > 0 {
		tag, err := s.pool.Exec(ctx,
			`UPDATE tpbx_pause_codes SET code=$2, name=$3, billable=$4, position=$5 WHERE id=$1`,
			p.ID, p.Code, p.Name, p.Billable, p.Position)
		if err != nil {
			return p, dispositionErr(err)
		}
		if tag.RowsAffected() == 0 {
			return p, ErrNotFound
		}
		return p, nil
	}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO tpbx_pause_codes (campaign_id, code, name, billable, position)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		p.CampaignID, p.Code, p.Name, p.Billable, p.Position).Scan(&p.ID)
	if err != nil {
		return p, dispositionErr(err)
	}
	return p, nil
}

// DeletePauseCode removes one; system-wide rows are protected as with
// dispositions.
func (s *Campaigns) DeletePauseCode(ctx context.Context, id int64) error {
	var campaign *int64
	err := s.pool.QueryRow(ctx, `SELECT campaign_id FROM tpbx_pause_codes WHERE id=$1`, id).Scan(&campaign)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if campaign == nil {
		return errors.New("system-wide pause codes cannot be deleted")
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM tpbx_pause_codes WHERE id=$1`, id)
	return err
}

// nullableID turns 0 into a SQL NULL, so "no campaign" and "campaign 0" cannot
// be confused in the WHERE clauses above.
func nullableID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}
