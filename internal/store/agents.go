package store

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AgentSessionTTL is how long a softphone login stays valid. Longer than the
// admin console's TTL because an agent keeps the phone open through a shift.
const AgentSessionTTL = 16 * time.Hour

// Agents authenticates softphone users against the PJSIP realtime credentials
// (ps_auths) and manages their sessions. There is no separate agent account
// store: an agent IS a SIP extension, so the SIP secret is the login.
type Agents struct {
	pool *pgxpool.Pool
}

// NewAgents returns an Agents store bound to a connection pool.
func NewAgents(pool *pgxpool.Pool) *Agents {
	return &Agents{pool: pool}
}

// Agent is a softphone identity: the extension and its display name.
type Agent struct {
	Extension   string `json:"extension"`
	DisplayName string `json:"displayName"`
	Password    string `json:"-"` // SIP secret; never serialised in list contexts
}

// Authenticate verifies an extension + SIP secret against ps_auths. The stored
// password is plaintext (PJSIP userpass auth), so this is a constant-time
// string compare. On success it returns the agent with its callerid-derived
// display name.
func (s *Agents) Authenticate(ctx context.Context, extension, password string) (Agent, error) {
	var a Agent
	var stored, callerid string
	err := s.pool.QueryRow(ctx, `
		SELECT au.id, COALESCE(au.password,''), COALESCE(e.callerid,'')
		  FROM ps_auths au
		  LEFT JOIN ps_endpoints e ON e.id = au.id
		 WHERE au.id = $1`, extension).Scan(&a.Extension, &stored, &callerid)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrAuth
	}
	if err != nil {
		return a, err
	}
	if stored == "" || subtle.ConstantTimeCompare([]byte(stored), []byte(password)) != 1 {
		return a, ErrAuth
	}
	a.Password = stored
	a.DisplayName = callerIDName(callerid)
	if a.DisplayName == "" {
		a.DisplayName = extension
	}
	return a, nil
}

// Get returns an agent (with SIP secret) by extension, for session refresh.
func (s *Agents) Get(ctx context.Context, extension string) (Agent, error) {
	var a Agent
	var callerid string
	err := s.pool.QueryRow(ctx, `
		SELECT au.id, COALESCE(au.password,''), COALESCE(e.callerid,'')
		  FROM ps_auths au
		  LEFT JOIN ps_endpoints e ON e.id = au.id
		 WHERE au.id = $1`, extension).Scan(&a.Extension, &a.Password, &callerid)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	a.DisplayName = callerIDName(callerid)
	if a.DisplayName == "" {
		a.DisplayName = extension
	}
	return a, nil
}

// CreateSession issues an opaque session token for an authenticated agent.
func (s *Agents) CreateSession(ctx context.Context, extension string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tpbx_agent_sessions (token, extension, expires_at)
		VALUES ($1, $2, now() + make_interval(secs => $3))`,
		token, extension, AgentSessionTTL.Seconds())
	if err != nil {
		return "", err
	}
	return token, nil
}

// LookupSession resolves a token to the agent's extension, or ok=false.
func (s *Agents) LookupSession(ctx context.Context, token string) (string, bool) {
	if token == "" {
		return "", false
	}
	var ext string
	err := s.pool.QueryRow(ctx, `
		SELECT extension FROM tpbx_agent_sessions
		 WHERE token=$1 AND expires_at > now()`, token).Scan(&ext)
	if err != nil {
		return "", false
	}
	return ext, true
}

// DeleteSession revokes an agent session (logout).
func (s *Agents) DeleteSession(ctx context.Context, token string) {
	_, _ = s.pool.Exec(ctx, `DELETE FROM tpbx_agent_sessions WHERE token=$1`, token)
}

// callerIDName extracts the display-name part from a PJSIP callerid value.
//
// Both spellings occur and both must work: the quoted `"Alice" <1001>` that
// Asterisk documentation uses, and the bare `Alice <1001>` that the console's
// own Extensions form suggests. Handling only the quoted form is why a
// softphone used to greet its agent by extension number instead of by name.
func callerIDName(cid string) string {
	if cid == "" {
		return ""
	}
	if i := indexByte(cid, '"'); i >= 0 {
		if j := indexByte(cid[i+1:], '"'); j >= 0 {
			return cid[i+1 : i+1+j]
		}
	}
	// Unquoted: everything before the angle-bracketed number is the name.
	if i := indexByte(cid, '<'); i > 0 {
		return strings.TrimSpace(cid[:i])
	}
	// A bare value with no number at all is the name itself. When it is just
	// the extension repeated, callers fall back to the extension regardless.
	return strings.TrimSpace(cid)
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
