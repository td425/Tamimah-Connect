package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Lists is the store for lead lists: the batches leads are loaded into and
// which a campaign will later dial (see docs/VICIDIAL_PARITY.md, phase 1).
//
// A list owns its leads — deleting one deletes them (ON DELETE CASCADE), which
// is why Delete refuses to run without an explicit confirmation from the caller.
type Lists struct {
	pool *pgxpool.Pool
}

// NewLists returns a Lists store bound to a connection pool.
func NewLists(pool *pgxpool.Pool) *Lists {
	return &Lists{pool: pool}
}

// CustomField describes one operator-defined field on a list's leads. Values
// live in Lead.Custom keyed by Name.
type CustomField struct {
	Name    string   `json:"name"`              // key in Lead.Custom; lowercase, no spaces
	Label   string   `json:"label"`             // what the agent screen shows
	Type    string   `json:"type"`              // text | number | date | select
	Options []string `json:"options,omitempty"` // for type=select
}

// List is a batch of leads.
type List struct {
	ID           int64         `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	CampaignID   string        `json:"campaignId"` // soft reference until P2
	Active       bool          `json:"active"`
	ExpiresOn    string        `json:"expiresOn"` // YYYY-MM-DD, "" = never
	CustomFields []CustomField `json:"customFields"`

	// Counts are computed on List/Get, not stored.
	LeadCount   int            `json:"leadCount"`
	StatusCount map[string]int `json:"statusCount,omitempty"`
}

// customFieldTypes are the field types the agent screen knows how to render.
var customFieldTypes = map[string]bool{"text": true, "number": true, "date": true, "select": true}

func (l *List) normalise() error {
	l.Name = strings.TrimSpace(l.Name)
	if len(l.Name) < 2 || len(l.Name) > 128 {
		return errors.New("list name must be 2-128 characters")
	}
	l.CampaignID = strings.TrimSpace(l.CampaignID)
	l.ExpiresOn = strings.TrimSpace(l.ExpiresOn)
	if l.ExpiresOn != "" {
		if _, err := time.Parse("2006-01-02", l.ExpiresOn); err != nil {
			return errors.New("expiry date must be YYYY-MM-DD")
		}
	}
	if l.CustomFields == nil {
		l.CustomFields = []CustomField{}
	}
	seen := map[string]bool{}
	for i := range l.CustomFields {
		f := &l.CustomFields[i]
		f.Name = strings.ToLower(strings.TrimSpace(f.Name))
		if f.Name == "" {
			return errors.New("every custom field needs a name")
		}
		for _, r := range f.Name {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
				return fmt.Errorf("custom field %q may only contain lowercase letters, digits and _", f.Name)
			}
		}
		if seen[f.Name] {
			return fmt.Errorf("duplicate custom field %q", f.Name)
		}
		seen[f.Name] = true
		if f.Label = strings.TrimSpace(f.Label); f.Label == "" {
			f.Label = f.Name
		}
		if f.Type == "" {
			f.Type = "text"
		}
		if !customFieldTypes[f.Type] {
			return fmt.Errorf("custom field %q has unknown type %q", f.Name, f.Type)
		}
		if f.Type != "select" {
			f.Options = nil
		}
	}
	return nil
}

// List returns every list with its lead count, newest first.
func (s *Lists) List(ctx context.Context) ([]List, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT l.id, l.name, l.description, l.campaign_id, l.active,
		       COALESCE(to_char(l.expires_on,'YYYY-MM-DD'),''), l.custom_fields,
		       (SELECT count(*) FROM tpbx_leads d WHERE d.list_id = l.id)
		  FROM tpbx_lists l
		 ORDER BY l.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []List{}
	for rows.Next() {
		l, err := scanList(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Get returns one list, including a breakdown of its leads by status — the
// summary the console's list panel and ViciDial's list_info both want.
func (s *Lists) Get(ctx context.Context, id int64) (List, error) {
	l, err := scanList(s.pool.QueryRow(ctx, `
		SELECT l.id, l.name, l.description, l.campaign_id, l.active,
		       COALESCE(to_char(l.expires_on,'YYYY-MM-DD'),''), l.custom_fields,
		       (SELECT count(*) FROM tpbx_leads d WHERE d.list_id = l.id)
		  FROM tpbx_lists l WHERE l.id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return l, ErrNotFound
	}
	if err != nil {
		return l, err
	}

	rows, err := s.pool.Query(ctx,
		`SELECT status, count(*) FROM tpbx_leads WHERE list_id=$1 GROUP BY status ORDER BY status`, id)
	if err != nil {
		return l, err
	}
	defer rows.Close()
	l.StatusCount = map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return l, err
		}
		l.StatusCount[st] = n
	}
	return l, rows.Err()
}

func scanList(row pgx.Row) (List, error) {
	var l List
	var raw []byte
	if err := row.Scan(&l.ID, &l.Name, &l.Description, &l.CampaignID, &l.Active,
		&l.ExpiresOn, &raw, &l.LeadCount); err != nil {
		return l, err
	}
	l.CustomFields = []CustomField{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &l.CustomFields); err != nil {
			return l, err
		}
	}
	return l, nil
}

// Create inserts a list and returns it with its assigned id.
func (s *Lists) Create(ctx context.Context, l List) (List, error) {
	if err := l.normalise(); err != nil {
		return l, err
	}
	raw, err := json.Marshal(l.CustomFields)
	if err != nil {
		return l, err
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO tpbx_lists (name, description, campaign_id, active, expires_on, custom_fields)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		l.Name, l.Description, l.CampaignID, l.Active, nullableDate(l.ExpiresOn), raw).Scan(&l.ID)
	return l, err
}

// Update rewrites a list's settings. Custom fields are replaced wholesale;
// removing a field leaves the values in tpbx_leads.custom untouched (they stop
// being displayed but are not destroyed, so a mistaken removal is recoverable).
func (s *Lists) Update(ctx context.Context, l List) error {
	if err := l.normalise(); err != nil {
		return err
	}
	raw, err := json.Marshal(l.CustomFields)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE tpbx_lists
		   SET name=$2, description=$3, campaign_id=$4, active=$5, expires_on=$6,
		       custom_fields=$7, updated_at=now()
		 WHERE id=$1`,
		l.ID, l.Name, l.Description, l.CampaignID, l.Active, nullableDate(l.ExpiresOn), raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a list and, with it, every lead in it. The caller must pass
// the lead count it last showed the operator: if the list has grown since, the
// delete is refused rather than silently destroying more than was confirmed.
func (s *Lists) Delete(ctx context.Context, id int64, expectLeads int) error {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM tpbx_leads WHERE list_id=$1`, id).Scan(&n)
	if err != nil {
		return err
	}
	if n != expectLeads {
		return fmt.Errorf("list now holds %d lead(s), not %d — reload and confirm again", n, expectLeads)
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM tpbx_lists WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ResetLeads puts every lead in the list back to a fresh dialable state: the
// status returns to NEW and the call counter clears. This is ViciDial's "reset
// leads in list", used to re-run a list from the top.
func (s *Lists) ResetLeads(ctx context.Context, id int64) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE tpbx_leads
		   SET status='NEW', called_count=0, last_called_at=NULL, last_status='', updated_at=now()
		 WHERE list_id=$1`, id)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CustomFields returns the field schema for one list, used to validate lead
// custom values on write.
func (s *Lists) CustomFields(ctx context.Context, id int64) ([]CustomField, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT custom_fields FROM tpbx_lists WHERE id=$1`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	out := []CustomField{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// nullableDate turns "" into a SQL NULL so an empty expiry means "never".
func nullableDate(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
