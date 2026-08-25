-- 0027_campaigns.sql
--
-- Phase 2 of the ViciDial parity plan (docs/VICIDIAL_PARITY.md): campaigns —
-- the object the rest of the dialer hangs off — plus the two vocabularies a
-- campaign owns (dispositions and pause codes), agents as first-class
-- identities, and a per-attempt call log.
--
-- What this migration does NOT do is dial anything. The pacing columns
-- (dial_method, dial_level, adaptive_max, hopper_level, drop_rate_target...)
-- are stored so a campaign is a complete object, and are read by nobody until
-- the dialer engine lands in P4. Manual and preview dialing, which P2 does
-- deliver, need none of them.

CREATE TABLE IF NOT EXISTS tpbx_campaigns (
    id            BIGSERIAL PRIMARY KEY,
    -- code is the short dialplan-safe key (WEBOUT, COLLECT1); name is prose.
    code          VARCHAR(16)  NOT NULL UNIQUE,
    name          VARCHAR(64)  NOT NULL DEFAULT '',
    description   TEXT         NOT NULL DEFAULT '',
    active        BOOLEAN      NOT NULL DEFAULT true,

    -- Pacing. Consumed by the P4 dialer; inert until then.
    -- MANUAL/PREVIEW are agent-driven; RATIO and the ADAPT_* methods are the
    -- automatic ones. PREVIEW is ours — ViciDial models it as a campaign option
    -- on MANUAL, but as a distinct method it states the intent plainly.
    dial_method      VARCHAR(24)  NOT NULL DEFAULT 'MANUAL',
    dial_level       NUMERIC(4,2) NOT NULL DEFAULT 1.00,
    adaptive_max     NUMERIC(4,2) NOT NULL DEFAULT 3.00,
    hopper_level     INTEGER      NOT NULL DEFAULT 50,
    dial_timeout     INTEGER      NOT NULL DEFAULT 30,
    lead_order       VARCHAR(32)  NOT NULL DEFAULT 'DOWN',
    -- Which lead statuses this campaign is willing to dial.
    dial_statuses    TEXT[]       NOT NULL DEFAULT ARRAY['NEW']::text[],
    -- Regulatory ceiling on abandoned calls, in percent. The P4 pacing loop
    -- must hold below this; it is a hard limit, not a target to drift past.
    drop_rate_target NUMERIC(4,2) NOT NULL DEFAULT 3.00,
    amd_enabled      BOOLEAN      NOT NULL DEFAULT false,

    -- Call presentation and agent experience.
    outbound_cid   VARCHAR(32) NOT NULL DEFAULT '', -- caller ID shown to the customer
    trunk          VARCHAR(64) NOT NULL DEFAULT '', -- '' = let the outbound routes decide
    wrapup_seconds INTEGER     NOT NULL DEFAULT 0,
    script         TEXT        NOT NULL DEFAULT '', -- what the agent reads (P3 renders it)

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Dispositions: what an agent may say happened, and what that means. A row with
-- campaign_id NULL is system-wide and available to every campaign.
CREATE TABLE IF NOT EXISTS tpbx_dispositions (
    id          BIGSERIAL PRIMARY KEY,
    campaign_id BIGINT      REFERENCES tpbx_campaigns (id) ON DELETE CASCADE,
    code        VARCHAR(16) NOT NULL,
    name        VARCHAR(64) NOT NULL DEFAULT '',
    selectable  BOOLEAN     NOT NULL DEFAULT true,  -- offered on the agent screen
    -- Semantics the dialer and the reports read off the disposition rather than
    -- hard-coding status strings.
    human_answered    BOOLEAN NOT NULL DEFAULT true,
    is_sale           BOOLEAN NOT NULL DEFAULT false,
    not_interested    BOOLEAN NOT NULL DEFAULT false,
    dnc               BOOLEAN NOT NULL DEFAULT false, -- P6 adds the number to DNC
    callback          BOOLEAN NOT NULL DEFAULT false, -- P3 schedules one
    -- Seconds before the lead may be dialed again; 0 = never redial.
    recycle_after_sec INTEGER NOT NULL DEFAULT 0,
    position          INTEGER NOT NULL DEFAULT 0
);

-- A code is unique within its campaign, and separately unique among the
-- system-wide rows (NULLs do not collide in a plain UNIQUE constraint).
CREATE UNIQUE INDEX IF NOT EXISTS tpbx_dispositions_campaign_code
    ON tpbx_dispositions (campaign_id, code) WHERE campaign_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS tpbx_dispositions_system_code
    ON tpbx_dispositions (code) WHERE campaign_id IS NULL;

-- The system-wide dispositions every deployment starts with. They mirror the
-- statuses store.SystemStatuses already sets, so existing leads line up.
INSERT INTO tpbx_dispositions
    (campaign_id, code, name, human_answered, is_sale, not_interested, dnc, callback, recycle_after_sec, position)
VALUES
    (NULL, 'SALE',   'Sale made',        true,  true,  false, false, false, 0,     1),
    (NULL, 'CALLBK', 'Call back later',  true,  false, false, false, true,  0,     2),
    (NULL, 'NI',     'Not interested',   true,  false, true,  false, false, 0,     3),
    (NULL, 'NA',     'No answer',        false, false, false, false, false, 3600,  4),
    (NULL, 'BUSY',   'Busy',             false, false, false, false, false, 900,   5),
    (NULL, 'DNC',    'Do not call',      true,  false, false, true,  false, 0,     6),
    (NULL, 'DROP',   'Dropped call',     false, false, false, false, false, 1800,  7)
ON CONFLICT DO NOTHING;

-- Pause codes: why an agent is not taking calls. Billable separates paid
-- not-ready time (training, meeting) from unpaid (lunch, break) in the reports.
CREATE TABLE IF NOT EXISTS tpbx_pause_codes (
    id          BIGSERIAL PRIMARY KEY,
    campaign_id BIGINT      REFERENCES tpbx_campaigns (id) ON DELETE CASCADE,
    code        VARCHAR(16) NOT NULL,
    name        VARCHAR(64) NOT NULL DEFAULT '',
    billable    BOOLEAN     NOT NULL DEFAULT true,
    position    INTEGER     NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS tpbx_pause_codes_campaign_code
    ON tpbx_pause_codes (campaign_id, code) WHERE campaign_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS tpbx_pause_codes_system_code
    ON tpbx_pause_codes (code) WHERE campaign_id IS NULL;

INSERT INTO tpbx_pause_codes (campaign_id, code, name, billable, position) VALUES
    (NULL, 'BREAK',   'Break',           false, 1),
    (NULL, 'LUNCH',   'Lunch',           false, 2),
    (NULL, 'MEETING', 'Meeting',         true,  3),
    (NULL, 'TRAIN',   'Training',        true,  4),
    (NULL, 'ADMIN',   'Admin work',      true,  5),
    (NULL, 'TECH',    'Technical issue', true,  6)
ON CONFLICT DO NOTHING;

-- Agents as first-class identities.
--
-- Until now an agent WAS a SIP extension: store/agents.go authenticates the
-- softphone against ps_auths, so the device and the person were the same thing.
-- That cannot carry campaign membership, pause time or per-agent statistics —
-- one person may use different devices, and a device may be shared across a
-- shift. So the person becomes a row here and the extension stays what it
-- always was: the device they are currently reachable on.
--
-- Softphone authentication is deliberately NOT changed by this migration; the
-- web, desktop and Android clients keep logging in with extension + SIP secret.
-- P3 moves that onto this table, resolving the agent by extension, which is why
-- extension is indexed.
CREATE TABLE IF NOT EXISTS tpbx_agents (
    id           BIGSERIAL PRIMARY KEY,
    username     VARCHAR(64)  NOT NULL UNIQUE,
    display_name VARCHAR(128) NOT NULL DEFAULT '',
    extension    VARCHAR(64)  NOT NULL DEFAULT '', -- current device binding
    active       BOOLEAN      NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS tpbx_agents_extension_idx
    ON tpbx_agents (extension) WHERE extension <> '';

-- Which campaigns an agent may work. P3's agent screen offers exactly these.
CREATE TABLE IF NOT EXISTS tpbx_agent_campaigns (
    agent_id    BIGINT NOT NULL REFERENCES tpbx_agents (id)    ON DELETE CASCADE,
    campaign_id BIGINT NOT NULL REFERENCES tpbx_campaigns (id) ON DELETE CASCADE,
    PRIMARY KEY (agent_id, campaign_id)
);

-- One row per dial attempt on a lead. P2 writes these from manual/preview
-- dialing and stamps the disposition onto them; P4's automatic dialer writes
-- the same rows, which is why the shape carries channel and timing detail no
-- manual dial needs.
CREATE TABLE IF NOT EXISTS tpbx_lead_calls (
    id          BIGSERIAL PRIMARY KEY,
    lead_id     BIGINT      NOT NULL REFERENCES tpbx_leads (id)     ON DELETE CASCADE,
    campaign_id BIGINT      REFERENCES tpbx_campaigns (id) ON DELETE SET NULL,
    agent       VARCHAR(64) NOT NULL DEFAULT '', -- tpbx_agents.username, or a console user
    extension   VARCHAR(64) NOT NULL DEFAULT '', -- device the attempt went to
    direction   VARCHAR(8)  NOT NULL DEFAULT 'out',
    channel_id  VARCHAR(128) NOT NULL DEFAULT '', -- ARI channel, for correlation
    dialed      VARCHAR(32) NOT NULL DEFAULT '', -- the number actually dialed
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at    TIMESTAMPTZ,
    status      VARCHAR(16) NOT NULL DEFAULT '', -- disposition code, once given
    note        TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS tpbx_lead_calls_lead_idx    ON tpbx_lead_calls (lead_id, started_at DESC);
CREATE INDEX IF NOT EXISTS tpbx_lead_calls_agent_idx   ON tpbx_lead_calls (agent, started_at DESC);
CREATE INDEX IF NOT EXISTS tpbx_lead_calls_started_idx ON tpbx_lead_calls (started_at DESC);

-- Lists point at a real campaign now.
--
-- 0026 shipped tpbx_lists.campaign_id as free text precisely because campaigns
-- did not exist. Rather than strand those values, promote them: any code an
-- operator typed becomes a real campaign, the lists are repointed at it by id,
-- and the text column goes. Two sources of truth for "which campaign" is the
-- exact bug this plan already documents in the queue reporting; not creating a
-- second instance of it.
INSERT INTO tpbx_campaigns (code, name, description)
SELECT DISTINCT upper(trim(campaign_id)), upper(trim(campaign_id)),
       'Created automatically from a list''s campaign label when campaigns were introduced.'
  FROM tpbx_lists
 WHERE trim(campaign_id) <> ''
ON CONFLICT (code) DO NOTHING;

ALTER TABLE tpbx_lists ADD COLUMN IF NOT EXISTS campaign BIGINT
    REFERENCES tpbx_campaigns (id) ON DELETE SET NULL;

UPDATE tpbx_lists l
   SET campaign = c.id
  FROM tpbx_campaigns c
 WHERE upper(trim(l.campaign_id)) = c.code;

ALTER TABLE tpbx_lists DROP COLUMN IF EXISTS campaign_id;
ALTER TABLE tpbx_lists RENAME COLUMN campaign TO campaign_id;

CREATE INDEX IF NOT EXISTS tpbx_lists_campaign_id_idx ON tpbx_lists (campaign_id);

-- Campaign administration joins the RBAC matrix. It covers campaigns,
-- their dispositions and pause codes, and agent accounts — one operational
-- concern, so one feature key rather than four.
UPDATE tpbx_roles
   SET permissions = permissions || '{"campaigns":{"view":true,"create":true,"edit":true,"delete":true}}'::jsonb
 WHERE name IN ('admin', 'manager');

UPDATE tpbx_roles
   SET permissions = permissions || '{"campaigns":{"view":true,"create":false,"edit":false,"delete":false}}'::jsonb
 WHERE name IN ('operator', 'viewer');
