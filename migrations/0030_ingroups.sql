-- 0030_ingroups.sql
--
-- Phase 5 of the ViciDial parity plan (docs/VICIDIAL_PARITY.md): in-groups —
-- real ACD queues served by Asterisk's app_queue, and the fix for the reporting
-- gap this plan documented in §2.3.
--
-- The gap: the console's `queue` action compiles to a While/Dial retry loop, not
-- app_queue. Every call-center metric on the Overview dashboard is computed from
-- the `queue_log` table, and **only app_queue writes it** — so a queue built in
-- the GUI produced no numbers at all, and CALL_CENTER.md quietly told operators
-- to hand-write `Queue()` in the dialplan instead. An in-group is a real queue,
-- so a call through one is a call the dashboard can see.
--
-- Following the same principle as extensions and trunks (DEEP_INDEX §2): if
-- Asterisk can read it from a realtime table, the realtime table IS the storage.
-- So `queues` and `queue_members` below are Asterisk's own schema, written by
-- the console and read live by app_queue — no generated file, no reload.

-- Asterisk realtime `queues`. res_config_pgsql selects the row and applies each
-- column as a queue option, so a subset of app_queue's options is fine: what is
-- not here simply keeps its queues.conf default.
CREATE TABLE IF NOT EXISTS queues (
    name                VARCHAR(128) PRIMARY KEY,
    musiconhold         VARCHAR(128),
    announce            VARCHAR(128),
    context             VARCHAR(128),
    timeout             INTEGER,               -- seconds to ring one member
    -- How a waiting call is offered to members. ringall is the safe default;
    -- leastrecent and fewestcalls spread work, rrmemory is round-robin.
    strategy            VARCHAR(32)  DEFAULT 'ringall',
    ringinuse           VARCHAR(8)   DEFAULT 'no',
    wrapuptime          INTEGER      DEFAULT 0,
    maxlen              INTEGER      DEFAULT 0, -- 0 = unlimited callers waiting
    -- servicelevel is the seconds threshold app_queue reports SLA against; it
    -- is kept in step with the console's own SLA setting.
    servicelevel        INTEGER      DEFAULT 20,
    retry               INTEGER      DEFAULT 5,
    weight              INTEGER      DEFAULT 0,
    joinempty           VARCHAR(128) DEFAULT 'yes',
    leavewhenempty      VARCHAR(128) DEFAULT 'no',
    autofill            VARCHAR(8)   DEFAULT 'yes',
    autopause           VARCHAR(8)   DEFAULT 'no',
    monitor_type        VARCHAR(16),
    periodic_announce   VARCHAR(255),
    periodic_announce_frequency INTEGER,
    announce_frequency  INTEGER,
    announce_holdtime   VARCHAR(8),
    announce_position   VARCHAR(8)   DEFAULT 'no',
    reportholdtime      VARCHAR(8)   DEFAULT 'no',
    memberdelay         INTEGER,
    timeoutrestart      VARCHAR(8),
    setinterfacevar     VARCHAR(8)   DEFAULT 'yes',
    setqueuevar         VARCHAR(8)   DEFAULT 'yes',
    setqueueentryvar    VARCHAR(8)   DEFAULT 'yes'
);

-- Asterisk realtime `queue_members`: who is currently in each queue.
--
-- This holds LIVE membership, not permission. An agent's membership appears
-- when they select the in-group and goes when they sign out — that is what
-- makes "I am taking sales calls this hour" mean something. Permission lives in
-- tpbx_ingroup_agents below.
CREATE TABLE IF NOT EXISTS queue_members (
    uniqueid        BIGSERIAL PRIMARY KEY,
    queue_name      VARCHAR(128) NOT NULL,
    interface       VARCHAR(255) NOT NULL,  -- e.g. PJSIP/1001
    membername      VARCHAR(128),
    state_interface VARCHAR(255),
    penalty         INTEGER NOT NULL DEFAULT 0,
    paused          INTEGER NOT NULL DEFAULT 0,
    ringinuse       VARCHAR(8)
);

CREATE UNIQUE INDEX IF NOT EXISTS queue_members_queue_interface
    ON queue_members (queue_name, interface);
CREATE INDEX IF NOT EXISTS queue_members_queue ON queue_members (queue_name);

-- The console's own view of an in-group: the settings app_queue has no column
-- for, plus the description an operator needs to tell two queues apart.
CREATE TABLE IF NOT EXISTS tpbx_ingroups (
    name        VARCHAR(128) PRIMARY KEY REFERENCES queues (name) ON DELETE CASCADE,
    description TEXT        NOT NULL DEFAULT '',
    active      BOOLEAN     NOT NULL DEFAULT true,
    -- Where a caller goes when the queue gives up (timeout, or nobody logged
    -- in): a bare extension, or an ivr:/voicemail:/hangup destination in the
    -- same encoding the inbound routes use.
    drop_action VARCHAR(128) NOT NULL DEFAULT 'hangup',
    -- How long a caller waits before drop_action takes over. 0 = wait forever.
    max_wait    INTEGER     NOT NULL DEFAULT 300,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Which agents are ALLOWED to take an in-group's calls, and at what penalty
-- (lower penalties are offered calls first, which is how skill tiers are
-- expressed). Selecting one for the current shift adds the live queue_members
-- row; this table only says they may.
CREATE TABLE IF NOT EXISTS tpbx_ingroup_agents (
    ingroup  VARCHAR(128) NOT NULL REFERENCES tpbx_ingroups (name) ON DELETE CASCADE,
    agent_id BIGINT       NOT NULL REFERENCES tpbx_agents (id)     ON DELETE CASCADE,
    penalty  INTEGER      NOT NULL DEFAULT 0,
    PRIMARY KEY (ingroup, agent_id)
);

CREATE INDEX IF NOT EXISTS tpbx_ingroup_agents_agent ON tpbx_ingroup_agents (agent_id);
