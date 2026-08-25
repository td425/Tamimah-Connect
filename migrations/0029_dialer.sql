-- 0029_dialer.sql
--
-- Phase 4 of the ViciDial parity plan (docs/VICIDIAL_PARITY.md): the outbound
-- dialer engine — the hopper it pulls from, the outcome detail it records, and
-- the Do-Not-Call list it must check before any of it happens.
--
-- DNC is pulled forward from phase 6 deliberately. Everything before this point
-- dialed only when a human pressed a button; from here the machine places calls
-- on its own, and a machine that cannot check a suppression list must not be
-- allowed to dial at all. Call-time windows (the other half of phase 6) still
-- need per-lead timezone derivation and stay there; until they land, the
-- engine's own guard is that a campaign only runs while someone has it active.

-- The hopper: leads selected and queued for dialing, refilled by a per-campaign
-- goroutine. It exists so the pacing loop never runs a large query in its hot
-- path — it takes the next row and goes.
CREATE TABLE IF NOT EXISTS tpbx_hopper (
    id          BIGSERIAL PRIMARY KEY,
    lead_id     BIGINT      NOT NULL REFERENCES tpbx_leads (id)     ON DELETE CASCADE,
    campaign_id BIGINT      NOT NULL REFERENCES tpbx_campaigns (id) ON DELETE CASCADE,
    -- Higher dials first; a callback promoted into the hopper outranks fresh
    -- leads because someone was promised a time.
    priority    INTEGER     NOT NULL DEFAULT 0,
    state       VARCHAR(16) NOT NULL DEFAULT 'READY', -- READY | DIALING | DONE
    source      VARCHAR(24) NOT NULL DEFAULT 'filler',
    inserted_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A lead is in the hopper once. The partial unique index lets a lead be
-- re-queued later (after its DONE row is cleared) without a conflict.
CREATE UNIQUE INDEX IF NOT EXISTS tpbx_hopper_lead_once
    ON tpbx_hopper (lead_id) WHERE state <> 'DONE';
CREATE INDEX IF NOT EXISTS tpbx_hopper_next
    ON tpbx_hopper (campaign_id, priority DESC, id) WHERE state = 'READY';

-- Outcome detail the dialer records that a manual call never had.
ALTER TABLE tpbx_lead_calls
    ADD COLUMN IF NOT EXISTS answered_at   TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS talk_seconds  INTEGER     NOT NULL DEFAULT 0,
    -- dropped marks a call answered by a person with no agent to give it to.
    -- This is the abandoned-call count regulators care about, and what the
    -- pacing governor reads back to slow itself down.
    ADD COLUMN IF NOT EXISTS dropped       BOOLEAN     NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS amd_result    VARCHAR(16) NOT NULL DEFAULT '',
    -- 'auto' distinguishes engine-placed calls from agent-placed ones, so the
    -- drop rate is computed over automatic dialing only.
    ADD COLUMN IF NOT EXISTS placed_by     VARCHAR(8)  NOT NULL DEFAULT 'agent';

CREATE INDEX IF NOT EXISTS tpbx_lead_calls_campaign_started
    ON tpbx_lead_calls (campaign_id, started_at DESC);
CREATE INDEX IF NOT EXISTS tpbx_lead_calls_dropped
    ON tpbx_lead_calls (campaign_id, started_at DESC) WHERE dropped;

-- Do-Not-Call. A number may be suppressed globally or for one campaign only:
-- "never call me again" and "stop calling me about this" are different
-- promises, and collapsing them either over-suppresses or under-suppresses.
CREATE TABLE IF NOT EXISTS tpbx_dnc (
    id           BIGSERIAL   PRIMARY KEY,
    phone_number VARCHAR(32) NOT NULL,
    -- NULL = global.
    campaign_id  BIGINT      REFERENCES tpbx_campaigns (id) ON DELETE CASCADE,
    reason       VARCHAR(64) NOT NULL DEFAULT '',
    added_by     VARCHAR(64) NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS tpbx_dnc_global
    ON tpbx_dnc (phone_number) WHERE campaign_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS tpbx_dnc_campaign
    ON tpbx_dnc (phone_number, campaign_id) WHERE campaign_id IS NOT NULL;

-- Whether a campaign's automatic dialing is running. Separate from
-- tpbx_campaigns.active on purpose: active means "this operation exists and may
-- be worked by agents", running means "the machine is placing calls right now".
-- A supervisor stops the dialer without retiring the campaign.
ALTER TABLE tpbx_campaigns
    ADD COLUMN IF NOT EXISTS dialer_running BOOLEAN NOT NULL DEFAULT false;
