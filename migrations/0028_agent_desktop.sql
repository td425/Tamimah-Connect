-- 0028_agent_desktop.sql
--
-- Phase 3 of the ViciDial parity plan (docs/VICIDIAL_PARITY.md): the agent
-- desktop. Three tables turn the softphone from a phone into an agent screen.
--
-- What is deliberately NOT here: any change to how a softphone authenticates.
-- The web, desktop and Android clients still sign in with a SIP extension and
-- its secret. Phase 3 resolves that login to a tpbx_agents row (creating one on
-- first sight, see api/agent.go), so an agent gains an identity without any
-- client needing to change.

-- Live working state, one row per agent. This is deliberately in the database
-- rather than in memory: an agent's shift outlives a browser tab, a softphone
-- reconnects after a network blip and must find itself where it left off, and a
-- supervisor board (P8) needs to read the same state the agent sees.
CREATE TABLE IF NOT EXISTS tpbx_agent_state (
    agent_id     BIGINT PRIMARY KEY REFERENCES tpbx_agents (id) ON DELETE CASCADE,
    campaign_id  BIGINT REFERENCES tpbx_campaigns (id) ON DELETE SET NULL,
    -- Agents start paused: nobody should be handed a call by simply opening the
    -- app. They go ready deliberately.
    paused       BOOLEAN     NOT NULL DEFAULT true,
    pause_code   VARCHAR(16) NOT NULL DEFAULT '',
    -- The lead currently on the agent's screen (previewed or being called).
    current_lead BIGINT      REFERENCES tpbx_leads (id) ON DELETE SET NULL,
    current_call BIGINT      REFERENCES tpbx_lead_calls (id) ON DELETE SET NULL,
    -- When the present state began, so the screen can show "paused for 4:12".
    since        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Time and motion: what an agent did and when. Pairing consecutive rows gives
-- login/pause/talk durations, which is what ViciDial's agent_stats_export
-- reports on and what P8 will build its agent board from.
--
-- This is an append-only log. It is never updated in place, so a shift can be
-- reconstructed exactly even if the live state row was lost.
CREATE TABLE IF NOT EXISTS tpbx_agent_log (
    id          BIGSERIAL PRIMARY KEY,
    agent       VARCHAR(64) NOT NULL,          -- username, kept as text so history outlives the account
    extension   VARCHAR(64) NOT NULL DEFAULT '',
    campaign_id BIGINT      REFERENCES tpbx_campaigns (id) ON DELETE SET NULL,
    event       VARCHAR(16) NOT NULL,          -- login | logout | pause | resume | campaign
    pause_code  VARCHAR(16) NOT NULL DEFAULT '',
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS tpbx_agent_log_agent_at ON tpbx_agent_log (agent, at DESC);
CREATE INDEX IF NOT EXISTS tpbx_agent_log_at       ON tpbx_agent_log (at DESC);

-- Scheduled callbacks. A disposition flagged `callback` (CALLBK by default)
-- means nothing without somewhere to record when, and for whom.
--
-- recipient distinguishes ViciDial's two kinds: ANYONE puts the callback back
-- into the campaign for whoever is free, USERONLY reserves it for the agent who
-- promised it — the difference between "we'll call you back" and "I'll call you
-- back", which matters to the customer.
CREATE TABLE IF NOT EXISTS tpbx_callbacks (
    id          BIGSERIAL   PRIMARY KEY,
    lead_id     BIGINT      NOT NULL REFERENCES tpbx_leads (id) ON DELETE CASCADE,
    campaign_id BIGINT      REFERENCES tpbx_campaigns (id) ON DELETE SET NULL,
    agent       VARCHAR(64) NOT NULL DEFAULT '', -- who promised it
    recipient   VARCHAR(16) NOT NULL DEFAULT 'ANYONE', -- ANYONE | USERONLY
    callback_at TIMESTAMPTZ NOT NULL,
    note        TEXT        NOT NULL DEFAULT '',
    status      VARCHAR(16) NOT NULL DEFAULT 'PENDING', -- PENDING | DONE | CANCELLED
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS tpbx_callbacks_due
    ON tpbx_callbacks (callback_at) WHERE status = 'PENDING';
CREATE INDEX IF NOT EXISTS tpbx_callbacks_agent
    ON tpbx_callbacks (agent, callback_at) WHERE status = 'PENDING';
CREATE INDEX IF NOT EXISTS tpbx_callbacks_lead ON tpbx_callbacks (lead_id);
