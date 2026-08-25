-- 0026_leads_lists.sql
--
-- Phase 1 of the ViciDial parity plan (docs/VICIDIAL_PARITY.md): the CRM half
-- of a dialer — leads organised into lists.
--
-- A *list* is a batch of leads loaded for one purpose ("March web enquiries").
-- A *lead* is a person to call: a phone number plus contact detail, a status,
-- and a call history counter. Everything later — campaigns, the hopper, the
-- dialer, dispositions, callbacks, DNC — hangs off these two tables, so the
-- shape here is deliberately close to the vocabulary the rest of the plan uses
-- (vendor_lead_code, source_id, called_count, phone_code) even where our own
-- naming would differ. That keeps the mapping obvious when P2/P4 land.
--
-- Two forward references are intentional and are plain columns, not foreign
-- keys, because the tables they point at do not exist yet:
--   * tpbx_lists.campaign_id  -> tpbx_campaigns (P2)
--   * tpbx_leads.status       -> tpbx_dispositions (P2)
-- P2 adds the constraints once those tables exist. Until then a list with no
-- campaign is simply a parked list, which is also a legitimate steady state
-- (imported data waiting to be assigned).

CREATE TABLE IF NOT EXISTS tpbx_lists (
    id            BIGSERIAL PRIMARY KEY,
    name          VARCHAR(128) NOT NULL,
    description   TEXT         NOT NULL DEFAULT '',
    -- Soft reference to the campaign that will dial this list (P2).
    campaign_id   VARCHAR(64)  NOT NULL DEFAULT '',
    active        BOOLEAN      NOT NULL DEFAULT true,
    -- Leads stop being dialable after this date; NULL = never expires.
    expires_on    DATE,
    -- Per-list custom field schema: a JSON array of
    --   {"name":"policy_no","label":"Policy #","type":"text","options":[...]}
    -- Values for these live in tpbx_leads.custom, keyed by name.
    custom_fields JSONB        NOT NULL DEFAULT '[]'::jsonb,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS tpbx_lists_campaign_idx ON tpbx_lists (campaign_id);

CREATE TABLE IF NOT EXISTS tpbx_leads (
    id               BIGSERIAL PRIMARY KEY,
    list_id          BIGINT       NOT NULL REFERENCES tpbx_lists (id) ON DELETE CASCADE,

    -- Dial state. status is free text (1-16 chars) because dispositions are
    -- per-campaign data from P2 onwards; the console offers the common system
    -- statuses (NEW/CALLBK/SALE/DNC/...) but never restricts to them.
    status           VARCHAR(16)  NOT NULL DEFAULT 'NEW',
    called_count     INTEGER      NOT NULL DEFAULT 0,
    last_called_at   TIMESTAMPTZ,
    last_status      VARCHAR(16)  NOT NULL DEFAULT '',

    -- Number to dial. phone_code is the dial/country prefix kept separate from
    -- the subscriber number, exactly as the dialer will need it.
    phone_code       VARCHAR(8)   NOT NULL DEFAULT '',
    phone_number     VARCHAR(32)  NOT NULL,
    alt_phone        VARCHAR(32)  NOT NULL DEFAULT '',
    alt_phone_two    VARCHAR(32)  NOT NULL DEFAULT '',

    -- Contact detail.
    title            VARCHAR(16)  NOT NULL DEFAULT '',
    first_name       VARCHAR(64)  NOT NULL DEFAULT '',
    last_name        VARCHAR(64)  NOT NULL DEFAULT '',
    email            VARCHAR(128) NOT NULL DEFAULT '',
    address1         VARCHAR(128) NOT NULL DEFAULT '',
    address2         VARCHAR(128) NOT NULL DEFAULT '',
    city             VARCHAR(64)  NOT NULL DEFAULT '',
    state            VARCHAR(32)  NOT NULL DEFAULT '',
    postal_code      VARCHAR(16)  NOT NULL DEFAULT '',
    country          VARCHAR(32)  NOT NULL DEFAULT '',
    comments         TEXT         NOT NULL DEFAULT '',

    -- Provenance and ownership.
    vendor_lead_code VARCHAR(64)  NOT NULL DEFAULT '', -- the customer's own key
    source_id        VARCHAR(64)  NOT NULL DEFAULT '', -- where the lead came from
    owner            VARCHAR(64)  NOT NULL DEFAULT '', -- assigned agent/user

    -- Local-time offset in hours from UTC, used by the P6 call-time rules.
    -- NULL means "not resolved yet"; P6 derives it from state/postal/prefix.
    gmt_offset       NUMERIC(4,2),

    -- Values for the list's custom_fields, keyed by field name.
    custom           JSONB        NOT NULL DEFAULT '{}'::jsonb,

    created_at       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Search paths the console and (later) the hopper filler actually use.
CREATE INDEX IF NOT EXISTS tpbx_leads_list_idx    ON tpbx_leads (list_id);
CREATE INDEX IF NOT EXISTS tpbx_leads_phone_idx   ON tpbx_leads (phone_number);
CREATE INDEX IF NOT EXISTS tpbx_leads_status_idx  ON tpbx_leads (status);
CREATE INDEX IF NOT EXISTS tpbx_leads_updated_idx ON tpbx_leads (updated_at DESC);
CREATE INDEX IF NOT EXISTS tpbx_leads_vendor_idx  ON tpbx_leads (vendor_lead_code)
    WHERE vendor_lead_code <> '';
-- Name search is case-insensitive in the UI, so index the folded form.
CREATE INDEX IF NOT EXISTS tpbx_leads_lastname_idx ON tpbx_leads (lower(last_name));

-- The "leads" console feature joins the RBAC matrix (see DEEP_INDEX §17).
-- Admin is granted everything in code, but its stored matrix is kept complete;
-- manager/operator get full access, viewer read-only. Roles created by an
-- operator since 0017 get nothing here — an admin grants it explicitly, which
-- is the safe default for a feature holding customer data.
UPDATE tpbx_roles
   SET permissions = permissions || '{"leads":{"view":true,"create":true,"edit":true,"delete":true}}'::jsonb
 WHERE name IN ('admin', 'manager', 'operator');

UPDATE tpbx_roles
   SET permissions = permissions || '{"leads":{"view":true,"create":false,"edit":false,"delete":false}}'::jsonb
 WHERE name = 'viewer';
