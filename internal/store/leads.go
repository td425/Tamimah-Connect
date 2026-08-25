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

// Leads is the store for the people a campaign will call: CRUD, search, and
// bulk import with duplicate detection (docs/VICIDIAL_PARITY.md, phase 1).
//
// A lead is deliberately thin on behaviour here. Everything that *decides*
// something about a lead — whether it may be dialed now, which agent gets it,
// what a disposition does to it — arrives with campaigns (P2), the hopper and
// the dialer (P4), and the compliance rules (P6). This layer only owns the
// record and the queries over it.
type Leads struct {
	pool *pgxpool.Pool
}

// NewLeads returns a Leads store bound to a connection pool.
func NewLeads(pool *pgxpool.Pool) *Leads {
	return &Leads{pool: pool}
}

// SystemStatuses are the dial statuses the console offers in its filters and
// pickers. A lead's status is NOT restricted to these — campaigns bring their
// own dispositions in P2 — but these are the ones the system itself sets.
var SystemStatuses = []string{"NEW", "CALLBK", "SALE", "NI", "NA", "BUSY", "DNC", "DROP"}

// Lead is one person to call.
type Lead struct {
	ID     int64 `json:"id"`
	ListID int64 `json:"listId"`

	Status       string `json:"status"`
	CalledCount  int    `json:"calledCount"`
	LastCalledAt string `json:"lastCalledAt,omitempty"` // RFC3339, "" = never called
	LastStatus   string `json:"lastStatus,omitempty"`

	PhoneCode   string `json:"phoneCode"`
	PhoneNumber string `json:"phoneNumber"`
	AltPhone    string `json:"altPhone"`
	AltPhoneTwo string `json:"altPhoneTwo"`

	Title      string `json:"title"`
	FirstName  string `json:"firstName"`
	LastName   string `json:"lastName"`
	Email      string `json:"email"`
	Address1   string `json:"address1"`
	Address2   string `json:"address2"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postalCode"`
	Country    string `json:"country"`
	Comments   string `json:"comments"`

	VendorLeadCode string `json:"vendorLeadCode"`
	SourceID       string `json:"sourceId"`
	Owner          string `json:"owner"`

	// GMTOffset is hours from UTC at the lead's location; nil until resolved.
	GMTOffset *float64 `json:"gmtOffset,omitempty"`

	Custom map[string]any `json:"custom,omitempty"`

	// ListName is joined in for display; never written.
	ListName string `json:"listName,omitempty"`
}

// leadColumns is the shared SELECT list, kept in one place so scanLead and
// every query stay in step.
const leadColumns = `
	d.id, d.list_id, d.status, d.called_count, d.last_called_at, d.last_status,
	d.phone_code, d.phone_number, d.alt_phone, d.alt_phone_two,
	d.title, d.first_name, d.last_name, d.email, d.address1, d.address2,
	d.city, d.state, d.postal_code, d.country, d.comments,
	d.vendor_lead_code, d.source_id, d.owner, d.gmt_offset, d.custom,
	COALESCE(l.name,'')`

func scanLead(row pgx.Row) (Lead, error) {
	var d Lead
	var called *time.Time
	var raw []byte
	err := row.Scan(&d.ID, &d.ListID, &d.Status, &d.CalledCount, &called, &d.LastStatus,
		&d.PhoneCode, &d.PhoneNumber, &d.AltPhone, &d.AltPhoneTwo,
		&d.Title, &d.FirstName, &d.LastName, &d.Email, &d.Address1, &d.Address2,
		&d.City, &d.State, &d.PostalCode, &d.Country, &d.Comments,
		&d.VendorLeadCode, &d.SourceID, &d.Owner, &d.GMTOffset, &raw, &d.ListName)
	if err != nil {
		return d, err
	}
	if called != nil {
		d.LastCalledAt = called.UTC().Format(time.RFC3339)
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &d.Custom); err != nil {
			return d, err
		}
	}
	return d, nil
}

// CheckPhoneNumber validates a number for dialability and returns it in the
// digits-only form the dialer will use. This is ViciDial's check_phone_number:
// the same rule must apply at import, at manual dial, and (from P4) at hopper
// fill, so it lives here rather than in a handler.
func CheckPhoneNumber(number string) (string, error) {
	var b strings.Builder
	for _, r := range number {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.' || r == '+':
			// Formatting the operator pasted in; drop it silently.
		default:
			return "", fmt.Errorf("phone number %q contains invalid character %q", number, r)
		}
	}
	digits := b.String()
	if len(digits) < 6 {
		return "", fmt.Errorf("phone number %q is too short to dial", number)
	}
	if len(digits) > 18 {
		return "", fmt.Errorf("phone number %q is too long (max 18 digits)", number)
	}
	return digits, nil
}

// normalise validates and cleans a lead in place, against its list's custom
// field schema.
func (d *Lead) normalise(fields []CustomField) error {
	if d.ListID <= 0 {
		return errors.New("a lead must belong to a list")
	}
	clean, err := CheckPhoneNumber(d.PhoneNumber)
	if err != nil {
		return err
	}
	d.PhoneNumber = clean

	// Alternate numbers are optional, so a bad one is dropped rather than
	// failing the whole import row.
	if d.AltPhone != "" {
		if alt, err := CheckPhoneNumber(d.AltPhone); err == nil {
			d.AltPhone = alt
		} else {
			d.AltPhone = ""
		}
	}
	if d.AltPhoneTwo != "" {
		if alt, err := CheckPhoneNumber(d.AltPhoneTwo); err == nil {
			d.AltPhoneTwo = alt
		} else {
			d.AltPhoneTwo = ""
		}
	}

	d.Status = strings.ToUpper(strings.TrimSpace(d.Status))
	if d.Status == "" {
		d.Status = "NEW"
	}
	if len(d.Status) > 16 {
		return fmt.Errorf("status %q is longer than 16 characters", d.Status)
	}
	if d.Email = strings.TrimSpace(d.Email); len(d.Email) > 128 {
		return errors.New("email is longer than 128 characters")
	}

	// Keep only values the list actually declares, so a stray import column
	// cannot quietly bloat every row.
	if len(d.Custom) > 0 {
		allowed := make(map[string]bool, len(fields))
		for _, f := range fields {
			allowed[f.Name] = true
		}
		for k := range d.Custom {
			if !allowed[k] {
				delete(d.Custom, k)
			}
		}
	}
	if d.Custom == nil {
		d.Custom = map[string]any{}
	}
	return nil
}

// LeadQuery is the filter behind the console's lead browser. It covers
// ViciDial's lead_search (by number), lead_status_search (by status + date)
// and the plain per-list listing in one shape.
type LeadQuery struct {
	ListID     int64  // 0 = all lists
	Status     string // "" = any
	Phone      string // partial match on any of the three numbers
	Name       string // case-insensitive partial on first/last name
	Owner      string
	Since      string // YYYY-MM-DD, on updated_at
	Until      string // YYYY-MM-DD, inclusive
	Limit      int    // capped at maxLeadPage
	Offset     int
	OrderNewer bool // true = newest first (default), false = oldest first
}

// maxLeadPage bounds a single page. Lists run to millions of rows; the console
// pages and the export path (P8) will stream instead of widening this.
const maxLeadPage = 500

// Search returns a page of leads plus the total number of matches, so the UI
// can show "1-50 of 12,904".
func (s *Leads) Search(ctx context.Context, q LeadQuery) ([]Lead, int, error) {
	where := []string{"1=1"}
	args := []any{}
	add := func(clause string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}

	if q.ListID > 0 {
		add("d.list_id = $%d", q.ListID)
	}
	if st := strings.ToUpper(strings.TrimSpace(q.Status)); st != "" {
		add("d.status = $%d", st)
	}
	if p := strings.TrimSpace(q.Phone); p != "" {
		// Match on the digits only, so "(727) 555" finds 7275551212.
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, p)
		if digits != "" {
			add("(d.phone_number LIKE '%%'||$%d||'%%' OR d.alt_phone LIKE '%%'||$%[1]d||'%%' OR d.alt_phone_two LIKE '%%'||$%[1]d||'%%')", digits)
		}
	}
	if n := strings.TrimSpace(q.Name); n != "" {
		add("(d.first_name ILIKE '%%'||$%d||'%%' OR d.last_name ILIKE '%%'||$%[1]d||'%%')", n)
	}
	if o := strings.TrimSpace(q.Owner); o != "" {
		add("d.owner = $%d", o)
	}
	if v := strings.TrimSpace(q.Since); v != "" {
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return nil, 0, errors.New("'since' must be YYYY-MM-DD")
		}
		add("d.updated_at >= $%d::date", v)
	}
	if v := strings.TrimSpace(q.Until); v != "" {
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return nil, 0, errors.New("'until' must be YYYY-MM-DD")
		}
		add("d.updated_at < ($%d::date + interval '1 day')", v)
	}

	clause := strings.Join(where, " AND ")

	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM tpbx_leads d WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := q.Limit
	if limit <= 0 || limit > maxLeadPage {
		limit = maxLeadPage
	}
	order := "DESC"
	if !q.OrderNewer {
		order = "ASC"
	}
	args = append(args, limit, max(q.Offset, 0))
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s
		  FROM tpbx_leads d LEFT JOIN tpbx_lists l ON l.id = d.list_id
		 WHERE %s
		 ORDER BY d.id %s
		 LIMIT $%d OFFSET $%d`, leadColumns, clause, order, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []Lead{}
	for rows.Next() {
		d, err := scanLead(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

// Get returns one lead with everything on it (ViciDial's lead_all_info).
func (s *Leads) Get(ctx context.Context, id int64) (Lead, error) {
	d, err := scanLead(s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT %s FROM tpbx_leads d LEFT JOIN tpbx_lists l ON l.id = d.list_id
		 WHERE d.id=$1`, leadColumns), id))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// DupScope says how widely Create looks for an existing copy of a lead before
// inserting it. This is ViciDial's duplicate-check option set, reduced to the
// three that actually differ in behaviour.
type DupScope string

const (
	DupNone     DupScope = "none"     // insert regardless
	DupList     DupScope = "list"     // same phone already in this list
	DupCampaign DupScope = "campaign" // same phone in any list of this campaign
	DupSystem   DupScope = "system"   // same phone anywhere
)

// ErrDuplicate is returned by Create when the duplicate check rejects a lead.
var ErrDuplicate = errors.New("lead already exists")

// findDuplicate returns the id of an existing lead matching phone within scope,
// or 0. days > 0 narrows the check to leads touched in that window, which is how
// a recycled list avoids colliding with its own history.
func (s *Leads) findDuplicate(ctx context.Context, phone string, listID int64, scope DupScope, days int) (int64, error) {
	var (
		query string
		args  []any
	)
	switch scope {
	case DupList:
		query = `SELECT d.id FROM tpbx_leads d WHERE d.phone_number=$1 AND d.list_id=$2`
		args = []any{phone, listID}
	case DupCampaign:
		query = `
			SELECT d.id FROM tpbx_leads d
			  JOIN tpbx_lists l ON l.id = d.list_id
			 WHERE d.phone_number=$1
			   AND l.campaign_id IS NOT NULL
			   AND l.campaign_id = (SELECT campaign_id FROM tpbx_lists WHERE id=$2)`
		args = []any{phone, listID}
	case DupSystem:
		query = `SELECT d.id FROM tpbx_leads d WHERE d.phone_number=$1`
		args = []any{phone}
	default:
		return 0, nil
	}
	if days > 0 {
		query += fmt.Sprintf(" AND d.created_at >= now() - ($%d || ' days')::interval", len(args)+1)
		args = append(args, days)
	}
	query += " LIMIT 1"

	var id int64
	err := s.pool.QueryRow(ctx, query, args...).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// Create inserts a lead, honouring the duplicate scope. On a duplicate it
// returns ErrDuplicate wrapped with the id of the lead already holding that
// number, so an import report can point at it.
func (s *Leads) Create(ctx context.Context, d Lead, scope DupScope, dupDays int) (Lead, error) {
	fields, err := s.listFields(ctx, d.ListID)
	if err != nil {
		return d, err
	}
	if err := d.normalise(fields); err != nil {
		return d, err
	}
	if scope != "" && scope != DupNone {
		dup, err := s.findDuplicate(ctx, d.PhoneNumber, d.ListID, scope, dupDays)
		if err != nil {
			return d, err
		}
		if dup > 0 {
			return d, fmt.Errorf("%w (lead %d)", ErrDuplicate, dup)
		}
	}
	raw, err := json.Marshal(d.Custom)
	if err != nil {
		return d, err
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO tpbx_leads (
			list_id, status, phone_code, phone_number, alt_phone, alt_phone_two,
			title, first_name, last_name, email, address1, address2,
			city, state, postal_code, country, comments,
			vendor_lead_code, source_id, owner, gmt_offset, custom)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		RETURNING id`,
		d.ListID, d.Status, d.PhoneCode, d.PhoneNumber, d.AltPhone, d.AltPhoneTwo,
		d.Title, d.FirstName, d.LastName, d.Email, d.Address1, d.Address2,
		d.City, d.State, d.PostalCode, d.Country, d.Comments,
		d.VendorLeadCode, d.SourceID, d.Owner, d.GMTOffset, raw).Scan(&d.ID)
	if err != nil && strings.Contains(err.Error(), "violates foreign key") {
		return d, fmt.Errorf("list %d does not exist", d.ListID)
	}
	return d, err
}

// Update rewrites a lead's editable fields. Dial state (called_count,
// last_called_at) is owned by the dialer and is not settable here; status is,
// because an operator legitimately re-qualifies a lead by hand.
func (s *Leads) Update(ctx context.Context, d Lead) error {
	fields, err := s.listFields(ctx, d.ListID)
	if err != nil {
		return err
	}
	if err := d.normalise(fields); err != nil {
		return err
	}
	raw, err := json.Marshal(d.Custom)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE tpbx_leads SET
			list_id=$2, status=$3, phone_code=$4, phone_number=$5, alt_phone=$6, alt_phone_two=$7,
			title=$8, first_name=$9, last_name=$10, email=$11, address1=$12, address2=$13,
			city=$14, state=$15, postal_code=$16, country=$17, comments=$18,
			vendor_lead_code=$19, source_id=$20, owner=$21, gmt_offset=$22, custom=$23,
			updated_at=now()
		 WHERE id=$1`,
		d.ID, d.ListID, d.Status, d.PhoneCode, d.PhoneNumber, d.AltPhone, d.AltPhoneTwo,
		d.Title, d.FirstName, d.LastName, d.Email, d.Address1, d.Address2,
		d.City, d.State, d.PostalCode, d.Country, d.Comments,
		d.VendorLeadCode, d.SourceID, d.Owner, d.GMTOffset, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes one lead.
func (s *Leads) Delete(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM tpbx_leads WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetStatus updates just the status of many leads at once — the bulk
// re-qualification the console's lead browser offers (ViciDial's
// batch_update_lead, limited to the field that matters before campaigns exist).
func (s *Leads) SetStatus(ctx context.Context, ids []int64, status string) (int64, error) {
	status = strings.ToUpper(strings.TrimSpace(status))
	if status == "" || len(status) > 16 {
		return 0, errors.New("status must be 1-16 characters")
	}
	if len(ids) == 0 {
		return 0, errors.New("no leads selected")
	}
	if len(ids) > 1000 {
		return 0, errors.New("no more than 1000 leads at a time")
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE tpbx_leads SET status=$2, updated_at=now() WHERE id = ANY($1)`, ids, status)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// listFields loads a list's custom field schema, mapping a missing list to a
// clear error rather than a foreign-key violation.
func (s *Leads) listFields(ctx context.Context, listID int64) ([]CustomField, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT custom_fields FROM tpbx_lists WHERE id=$1`, listID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("list %d does not exist", listID)
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

// agentEditableFields are the lead fields an agent may correct from the agent
// screen. Deliberately narrow: contact detail an agent hears on the call, never
// the lead's list, dial state, or provenance — those are decided by the system
// and by whoever loaded the data, not mid-call.
var agentEditableFields = map[string]string{
	"title":       "title",
	"firstName":   "first_name",
	"lastName":    "last_name",
	"email":       "email",
	"address1":    "address1",
	"address2":    "address2",
	"city":        "city",
	"state":       "state",
	"postalCode":  "postal_code",
	"country":     "country",
	"comments":    "comments",
	"altPhone":    "alt_phone",
	"altPhoneTwo": "alt_phone_two",
}

// UpdateFields applies a partial edit to a lead — ViciDial's update_fields, the
// agent correcting a name or noting an address while the customer is on the
// line. Unknown keys are ignored rather than rejected, so a client that knows
// about a field this server does not cannot fail the whole save.
//
// Custom values are addressed as "custom.<name>" and are validated against the
// list's schema, exactly as on a full update.
func (s *Leads) UpdateFields(ctx context.Context, leadID int64, fields map[string]string) error {
	if leadID <= 0 {
		return ErrNotFound
	}
	var listID int64
	if err := s.pool.QueryRow(ctx, `SELECT list_id FROM tpbx_leads WHERE id=$1`, leadID).Scan(&listID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}

	sets := []string{}
	args := []any{leadID}
	custom := map[string]any{}

	for key, val := range fields {
		if name, ok := strings.CutPrefix(key, "custom."); ok {
			custom[name] = val
			continue
		}
		col, ok := agentEditableFields[key]
		if !ok {
			continue
		}
		if col == "alt_phone" || col == "alt_phone_two" {
			if strings.TrimSpace(val) != "" {
				clean, err := CheckPhoneNumber(val)
				if err != nil {
					return err
				}
				val = clean
			}
		}
		args = append(args, val)
		sets = append(sets, fmt.Sprintf("%s=$%d", col, len(args)))
	}

	if len(custom) > 0 {
		allowed, err := s.listFields(ctx, listID)
		if err != nil {
			return err
		}
		known := make(map[string]bool, len(allowed))
		for _, f := range allowed {
			known[f.Name] = true
		}
		keep := map[string]any{}
		for k, v := range custom {
			if known[k] {
				keep[k] = v
			}
		}
		if len(keep) > 0 {
			raw, err := json.Marshal(keep)
			if err != nil {
				return err
			}
			args = append(args, raw)
			// Merge rather than replace: an agent editing one field must not
			// wipe the values they were not shown.
			sets = append(sets, fmt.Sprintf("custom = custom || $%d::jsonb", len(args)))
		}
	}

	if len(sets) == 0 {
		return nil // nothing recognised; not an error
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE tpbx_leads SET `+strings.Join(sets, ", ")+`, updated_at=now() WHERE id=$1`, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
