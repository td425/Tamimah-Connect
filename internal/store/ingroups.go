package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Ingroups is the store for skill-based inbound queues — ViciDial's in-groups,
// served by Asterisk's app_queue (docs/VICIDIAL_PARITY.md, phase 5).
//
// An in-group spans two tables on purpose. `queues` and `queue_members` are
// **Asterisk's own realtime schema**, read live by app_queue with no file and
// no reload — the same arrangement extensions and trunks already use. What
// Asterisk has no column for (description, the give-up destination) lives in
// tpbx_ingroups beside it.
//
// This is also the fix for the reporting gap: app_queue writes queue_log, and
// queue_log is what every call-center metric on the dashboard is computed from.
// A call through an in-group is a call the dashboard can actually see.
type Ingroups struct {
	pool *pgxpool.Pool
}

// NewIngroups returns an Ingroups store bound to a connection pool.
func NewIngroups(pool *pgxpool.Pool) *Ingroups {
	return &Ingroups{pool: pool}
}

// QueueStrategies are the ways app_queue may offer a waiting call to members.
var QueueStrategies = []string{
	"ringall", "leastrecent", "fewestcalls", "random", "rrmemory", "linear", "wrandom",
}

// Ingroup is one inbound queue.
type Ingroup struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Active      bool   `json:"active"`

	Strategy         string `json:"strategy"`
	MusicOnHold      string `json:"musicOnHold"`
	Announce         string `json:"announce"`
	RingTimeout      int    `json:"ringTimeout"` // seconds one member is rung
	WrapupTime       int    `json:"wrapupTime"`
	MaxCallers       int    `json:"maxCallers"` // 0 = unlimited
	ServiceLevel     int    `json:"serviceLevel"`
	JoinEmpty        string `json:"joinEmpty"`
	LeaveWhenEmpty   string `json:"leaveWhenEmpty"`
	AnnouncePosition string `json:"announcePosition"`
	PeriodicAnnounce string `json:"periodicAnnounce"`
	PeriodicFreq     int    `json:"periodicAnnounceFrequency"`

	DropAction string `json:"dropAction"`
	MaxWait    int    `json:"maxWait"`

	// Live counts, computed for display.
	MemberCount  int `json:"memberCount"`
	AllowedCount int `json:"allowedCount"`
}

func (g *Ingroup) normalise() error {
	g.Name = strings.ToUpper(strings.TrimSpace(g.Name))
	if len(g.Name) < 2 || len(g.Name) > 64 {
		return errors.New("in-group name must be 2-64 characters")
	}
	// The name becomes a dialplan argument to Queue(), so keep it to characters
	// that cannot terminate or confuse the line it is written into.
	for _, r := range g.Name {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return fmt.Errorf("in-group name %q may only contain letters, digits, _ and -", g.Name)
		}
	}
	if g.Strategy == "" {
		g.Strategy = "ringall"
	}
	if !contains(QueueStrategies, g.Strategy) {
		return fmt.Errorf("unknown ring strategy %q", g.Strategy)
	}
	if g.RingTimeout <= 0 {
		g.RingTimeout = 20
	}
	if g.RingTimeout > 300 {
		return errors.New("ring timeout must be 300 seconds or less")
	}
	if g.WrapupTime < 0 || g.WrapupTime > 600 {
		return errors.New("wrap-up must be between 0 and 600 seconds")
	}
	if g.MaxCallers < 0 {
		g.MaxCallers = 0
	}
	if g.ServiceLevel <= 0 {
		g.ServiceLevel = 20
	}
	if g.MaxWait < 0 {
		g.MaxWait = 0
	}
	if g.JoinEmpty == "" {
		g.JoinEmpty = "yes"
	}
	if g.LeaveWhenEmpty == "" {
		g.LeaveWhenEmpty = "no"
	}
	if g.AnnouncePosition == "" {
		g.AnnouncePosition = "no"
	}
	if g.DropAction = strings.TrimSpace(g.DropAction); g.DropAction == "" {
		g.DropAction = "hangup"
	}
	return nil
}

const ingroupColumns = `
	q.name, COALESCE(g.description,''), COALESCE(g.active,true),
	COALESCE(q.strategy,'ringall'), COALESCE(q.musiconhold,''), COALESCE(q.announce,''),
	COALESCE(q.timeout,20), COALESCE(q.wrapuptime,0), COALESCE(q.maxlen,0),
	COALESCE(q.servicelevel,20), COALESCE(q.joinempty,'yes'), COALESCE(q.leavewhenempty,'no'),
	COALESCE(q.announce_position,'no'), COALESCE(q.periodic_announce,''),
	COALESCE(q.periodic_announce_frequency,0),
	COALESCE(g.drop_action,'hangup'), COALESCE(g.max_wait,300),
	(SELECT count(*) FROM queue_members m WHERE m.queue_name = q.name),
	(SELECT count(*) FROM tpbx_ingroup_agents a WHERE a.ingroup = q.name)`

func scanIngroup(row pgx.Row) (Ingroup, error) {
	var g Ingroup
	err := row.Scan(&g.Name, &g.Description, &g.Active,
		&g.Strategy, &g.MusicOnHold, &g.Announce,
		&g.RingTimeout, &g.WrapupTime, &g.MaxCallers,
		&g.ServiceLevel, &g.JoinEmpty, &g.LeaveWhenEmpty,
		&g.AnnouncePosition, &g.PeriodicAnnounce, &g.PeriodicFreq,
		&g.DropAction, &g.MaxWait, &g.MemberCount, &g.AllowedCount)
	return g, err
}

// List returns every in-group.
func (s *Ingroups) List(ctx context.Context) ([]Ingroup, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+ingroupColumns+`
		  FROM queues q LEFT JOIN tpbx_ingroups g ON g.name = q.name
		 ORDER BY q.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ingroup{}
	for rows.Next() {
		g, err := scanIngroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Names returns the in-group names, for the dialplan generator and the pickers.
func (s *Ingroups) Names(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT name FROM queues ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Get returns one in-group.
func (s *Ingroups) Get(ctx context.Context, name string) (Ingroup, error) {
	g, err := scanIngroup(s.pool.QueryRow(ctx, `
		SELECT `+ingroupColumns+`
		  FROM queues q LEFT JOIN tpbx_ingroups g ON g.name = q.name
		 WHERE q.name = $1`, strings.ToUpper(strings.TrimSpace(name))))
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

// Save creates or updates an in-group, writing both halves in one transaction
// so Asterisk can never see a queue the console has no record of.
func (s *Ingroups) Save(ctx context.Context, g Ingroup, isNew bool) error {
	if err := g.normalise(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if isNew {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM queues WHERE name=$1)`, g.Name).
			Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrConflict
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO queues (name, strategy, musiconhold, announce, timeout, wrapuptime,
		    maxlen, servicelevel, joinempty, leavewhenempty, announce_position,
		    periodic_announce, periodic_announce_frequency, context)
		VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,$7,$8,$9,$10,$11,NULLIF($12,''),NULLIF($13,0),'from-internal')
		ON CONFLICT (name) DO UPDATE SET
		    strategy=EXCLUDED.strategy, musiconhold=EXCLUDED.musiconhold,
		    announce=EXCLUDED.announce, timeout=EXCLUDED.timeout,
		    wrapuptime=EXCLUDED.wrapuptime, maxlen=EXCLUDED.maxlen,
		    servicelevel=EXCLUDED.servicelevel, joinempty=EXCLUDED.joinempty,
		    leavewhenempty=EXCLUDED.leavewhenempty,
		    announce_position=EXCLUDED.announce_position,
		    periodic_announce=EXCLUDED.periodic_announce,
		    periodic_announce_frequency=EXCLUDED.periodic_announce_frequency`,
		g.Name, g.Strategy, g.MusicOnHold, g.Announce, g.RingTimeout, g.WrapupTime,
		g.MaxCallers, g.ServiceLevel, g.JoinEmpty, g.LeaveWhenEmpty, g.AnnouncePosition,
		g.PeriodicAnnounce, g.PeriodicFreq); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO tpbx_ingroups (name, description, active, drop_action, max_wait)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (name) DO UPDATE SET
		    description=EXCLUDED.description, active=EXCLUDED.active,
		    drop_action=EXCLUDED.drop_action, max_wait=EXCLUDED.max_wait,
		    updated_at=now()`,
		g.Name, g.Description, g.Active, g.DropAction, g.MaxWait); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Delete removes an in-group and, with it, its membership and permissions.
func (s *Ingroups) Delete(ctx context.Context, name string) error {
	name = strings.ToUpper(strings.TrimSpace(name))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM queue_members WHERE queue_name=$1`, name); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM queues WHERE name=$1`, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	// tpbx_ingroups (and its agent permissions) cascade from queues.
	return tx.Commit(ctx)
}

// Membership -----------------------------------------------------------------

// IngroupAgent is one agent's relationship to an in-group.
type IngroupAgent struct {
	AgentID     int64  `json:"agentId"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Extension   string `json:"extension"`
	Penalty     int    `json:"penalty"`
	// LoggedIn is whether they are a live queue member right now, as opposed to
	// merely permitted to be.
	LoggedIn bool `json:"loggedIn"`
	Paused   bool `json:"paused"`
}

// Agents returns who may take an in-group's calls and who currently is.
func (s *Ingroups) Agents(ctx context.Context, ingroup string) ([]IngroupAgent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.username, a.display_name, a.extension, ia.penalty,
		       (m.uniqueid IS NOT NULL), COALESCE(m.paused,0) <> 0
		  FROM tpbx_ingroup_agents ia
		  JOIN tpbx_agents a ON a.id = ia.agent_id
		  LEFT JOIN queue_members m
		         ON m.queue_name = ia.ingroup AND m.interface = 'PJSIP/' || a.extension
		 WHERE ia.ingroup = $1
		 ORDER BY ia.penalty, a.username`, strings.ToUpper(strings.TrimSpace(ingroup)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IngroupAgent{}
	for rows.Next() {
		var a IngroupAgent
		if err := rows.Scan(&a.AgentID, &a.Username, &a.DisplayName, &a.Extension,
			&a.Penalty, &a.LoggedIn, &a.Paused); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAgents replaces who may take an in-group's calls. Agents removed from the
// permission list are also removed from live membership: permission withdrawn
// mid-shift has to stop the calls, not just the paperwork.
func (s *Ingroups) SetAgents(ctx context.Context, ingroup string, agents []IngroupAgent) error {
	ingroup = strings.ToUpper(strings.TrimSpace(ingroup))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM tpbx_ingroup_agents WHERE ingroup=$1`, ingroup); err != nil {
		return err
	}
	for _, a := range agents {
		if a.AgentID <= 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO tpbx_ingroup_agents (ingroup, agent_id, penalty)
			VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, ingroup, a.AgentID, a.Penalty); err != nil {
			if strings.Contains(err.Error(), "violates foreign key") {
				return fmt.Errorf("agent %d does not exist", a.AgentID)
			}
			return err
		}
	}
	// Drop live members who are no longer permitted.
	if _, err := tx.Exec(ctx, `
		DELETE FROM queue_members m
		 WHERE m.queue_name = $1
		   AND NOT EXISTS (
		       SELECT 1 FROM tpbx_ingroup_agents ia
		         JOIN tpbx_agents a ON a.id = ia.agent_id
		        WHERE ia.ingroup = m.queue_name
		          AND 'PJSIP/' || a.extension = m.interface)`, ingroup); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AllowedFor returns the in-groups an agent may take calls for, and whether
// they are currently signed in to each.
func (s *Ingroups) AllowedFor(ctx context.Context, agentID int64) ([]IngroupMembership, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ia.ingroup, COALESCE(g.description,''), ia.penalty,
		       (m.uniqueid IS NOT NULL), COALESCE(m.paused,0) <> 0
		  FROM tpbx_ingroup_agents ia
		  JOIN tpbx_agents a       ON a.id = ia.agent_id
		  LEFT JOIN tpbx_ingroups g ON g.name = ia.ingroup
		  LEFT JOIN queue_members m
		         ON m.queue_name = ia.ingroup AND m.interface = 'PJSIP/' || a.extension
		 WHERE ia.agent_id = $1
		 ORDER BY ia.ingroup`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IngroupMembership{}
	for rows.Next() {
		var m IngroupMembership
		if err := rows.Scan(&m.Ingroup, &m.Description, &m.Penalty, &m.Selected, &m.Paused); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// IngroupMembership is one row of an agent's in-group picker.
type IngroupMembership struct {
	Ingroup     string `json:"ingroup"`
	Description string `json:"description"`
	Penalty     int    `json:"penalty"`
	Selected    bool   `json:"selected"`
	Paused      bool   `json:"paused"`
}

// SelectIngroups sets exactly which in-groups an agent is signed in to for this
// shift — ViciDial's change_ingroups. Anything they are not permitted is
// refused rather than quietly ignored, so an agent who thinks they are taking
// sales calls actually is.
//
// The rows written here are Asterisk's own realtime membership: app_queue reads
// them, so a change takes effect on the next call with no reload. Asterisk
// caches realtime members briefly, which is why the API also nudges it over AMI.
func (s *Ingroups) SelectIngroups(ctx context.Context, agentID int64, wanted []string) error {
	var extension string
	err := s.pool.QueryRow(ctx, `SELECT extension FROM tpbx_agents WHERE id=$1`, agentID).Scan(&extension)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(extension) == "" {
		return errors.New("this agent is not bound to an extension, so cannot join a queue")
	}
	iface := "PJSIP/" + extension

	allowed := map[string]int{}
	rows, err := s.pool.Query(ctx,
		`SELECT ingroup, penalty FROM tpbx_ingroup_agents WHERE agent_id=$1`, agentID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		var penalty int
		if err := rows.Scan(&name, &penalty); err != nil {
			rows.Close()
			return err
		}
		allowed[name] = penalty
	}
	rows.Close()

	for _, w := range wanted {
		if _, ok := allowed[strings.ToUpper(strings.TrimSpace(w))]; !ok {
			return fmt.Errorf("you are not permitted to take %s calls", w)
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM queue_members WHERE interface=$1`, iface); err != nil {
		return err
	}
	for _, w := range wanted {
		name := strings.ToUpper(strings.TrimSpace(w))
		if _, err := tx.Exec(ctx, `
			INSERT INTO queue_members (queue_name, interface, membername, state_interface, penalty)
			VALUES ($1,$2,$3,$2,$4)
			ON CONFLICT (queue_name, interface) DO UPDATE SET penalty=EXCLUDED.penalty`,
			name, iface, extension, allowed[name]); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SignOut removes an agent from every queue — called when they log out, so a
// queue never rings a phone nobody is sitting at.
func (s *Ingroups) SignOut(ctx context.Context, extension string) error {
	if strings.TrimSpace(extension) == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM queue_members WHERE interface=$1`, "PJSIP/"+extension)
	return err
}

// SetPaused pauses or resumes an agent in every queue they are a member of, so
// the desk's own pause state and their ACD availability cannot disagree.
func (s *Ingroups) SetPaused(ctx context.Context, extension string, paused bool) error {
	if strings.TrimSpace(extension) == "" {
		return nil
	}
	v := 0
	if paused {
		v = 1
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE queue_members SET paused=$2 WHERE interface=$1`, "PJSIP/"+extension, v)
	return err
}
