package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AgentAccounts is the store for agents as people, and for which campaigns each
// may work (docs/VICIDIAL_PARITY.md, phase 2).
//
// Note the split from the neighbouring Agents store, which is about *sessions*:
// it authenticates a softphone against a SIP extension's secret, because until
// phase 2 an agent simply WAS an extension. That could not carry campaign
// membership, pause time or per-agent statistics — one person may work from
// different devices, and one device may be shared across a shift. So the person
// lives here and the extension stays the device they are currently on.
//
// Softphone authentication is untouched by this: the web, desktop and Android
// clients still log in with extension + SIP secret. Phase 3 moves that onto this
// table via ByExtension, which is why the binding is indexed.
type AgentAccounts struct {
	pool *pgxpool.Pool
}

// NewAgentAccounts returns an AgentAccounts store bound to a connection pool.
func NewAgentAccounts(pool *pgxpool.Pool) *AgentAccounts {
	return &AgentAccounts{pool: pool}
}

// AgentAccount is one agent: a person, the device they are reachable on, and
// the campaigns they may work.
type AgentAccount struct {
	ID          int64   `json:"id"`
	Username    string  `json:"username"`
	DisplayName string  `json:"displayName"`
	Extension   string  `json:"extension"` // "" = not bound to a device yet
	Active      bool    `json:"active"`
	Campaigns   []int64 `json:"campaigns"` // campaign ids this agent may work

	// CampaignCodes is filled for display so a list view need not join.
	CampaignCodes []string `json:"campaignCodes,omitempty"`
}

func (a *AgentAccount) normalise() error {
	a.Username = strings.ToLower(strings.TrimSpace(a.Username))
	if len(a.Username) < 2 || len(a.Username) > 64 {
		return errors.New("agent username must be 2-64 characters")
	}
	for _, r := range a.Username {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return fmt.Errorf("agent username %q may only contain lowercase letters, digits, _ - and .", a.Username)
		}
	}
	if a.DisplayName = strings.TrimSpace(a.DisplayName); a.DisplayName == "" {
		a.DisplayName = a.Username
	}
	a.Extension = strings.TrimSpace(a.Extension)
	if a.Campaigns == nil {
		a.Campaigns = []int64{}
	}
	return nil
}

// List returns every agent with the campaigns they are assigned to.
func (s *AgentAccounts) List(ctx context.Context) ([]AgentAccount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.username, a.display_name, a.extension, a.active,
		       COALESCE(array_agg(ac.campaign_id) FILTER (WHERE ac.campaign_id IS NOT NULL), '{}') AS campaigns,
		       COALESCE(array_agg(c.code)         FILTER (WHERE c.code IS NOT NULL),         '{}') AS codes
		  FROM tpbx_agents a
		  LEFT JOIN tpbx_agent_campaigns ac ON ac.agent_id = a.id
		  LEFT JOIN tpbx_campaigns c        ON c.id = ac.campaign_id
		 GROUP BY a.id
		 ORDER BY a.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentAccount{}
	for rows.Next() {
		var a AgentAccount
		if err := rows.Scan(&a.ID, &a.Username, &a.DisplayName, &a.Extension, &a.Active,
			&a.Campaigns, &a.CampaignCodes); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Get returns one agent by id.
func (s *AgentAccounts) Get(ctx context.Context, id int64) (AgentAccount, error) {
	return s.scanOne(ctx, `WHERE a.id = $1`, id)
}

// ByExtension resolves the agent currently bound to a SIP extension. Phase 3's
// softphone login uses this to turn a device login into an agent identity.
func (s *AgentAccounts) ByExtension(ctx context.Context, ext string) (AgentAccount, error) {
	return s.scanOne(ctx, `WHERE a.extension = $1 AND a.active`, strings.TrimSpace(ext))
}

func (s *AgentAccounts) scanOne(ctx context.Context, where string, arg any) (AgentAccount, error) {
	var a AgentAccount
	err := s.pool.QueryRow(ctx, `
		SELECT a.id, a.username, a.display_name, a.extension, a.active,
		       COALESCE(array_agg(ac.campaign_id) FILTER (WHERE ac.campaign_id IS NOT NULL), '{}'),
		       COALESCE(array_agg(c.code)         FILTER (WHERE c.code IS NOT NULL),         '{}')
		  FROM tpbx_agents a
		  LEFT JOIN tpbx_agent_campaigns ac ON ac.agent_id = a.id
		  LEFT JOIN tpbx_campaigns c        ON c.id = ac.campaign_id
		 `+where+`
		 GROUP BY a.id`, arg).
		Scan(&a.ID, &a.Username, &a.DisplayName, &a.Extension, &a.Active, &a.Campaigns, &a.CampaignCodes)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// Create inserts an agent and its campaign assignments.
func (s *AgentAccounts) Create(ctx context.Context, a AgentAccount) (AgentAccount, error) {
	if err := a.normalise(); err != nil {
		return a, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return a, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = tx.QueryRow(ctx, `
		INSERT INTO tpbx_agents (username, display_name, extension, active)
		VALUES ($1,$2,$3,$4) RETURNING id`,
		a.Username, a.DisplayName, a.Extension, a.Active).Scan(&a.ID)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return a, ErrConflict
		}
		return a, err
	}
	if err := setAgentCampaigns(ctx, tx, a.ID, a.Campaigns); err != nil {
		return a, err
	}
	return a, tx.Commit(ctx)
}

// Update rewrites an agent and replaces its campaign assignments.
func (s *AgentAccounts) Update(ctx context.Context, a AgentAccount) error {
	if err := a.normalise(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE tpbx_agents SET username=$2, display_name=$3, extension=$4, active=$5, updated_at=now()
		 WHERE id=$1`, a.ID, a.Username, a.DisplayName, a.Extension, a.Active)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return ErrConflict
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := setAgentCampaigns(ctx, tx, a.ID, a.Campaigns); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Delete removes an agent. Their call records survive: tpbx_lead_calls stores
// the username as text precisely so history outlives the account.
func (s *AgentAccounts) Delete(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM tpbx_agents WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func setAgentCampaigns(ctx context.Context, tx pgx.Tx, agentID int64, campaigns []int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM tpbx_agent_campaigns WHERE agent_id=$1`, agentID); err != nil {
		return err
	}
	for _, c := range campaigns {
		if c <= 0 {
			continue
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO tpbx_agent_campaigns (agent_id, campaign_id) VALUES ($1,$2)
			ON CONFLICT DO NOTHING`, agentID, c)
		if err != nil {
			if strings.Contains(err.Error(), "violates foreign key") {
				return fmt.Errorf("campaign %d does not exist", c)
			}
			return err
		}
	}
	return nil
}
