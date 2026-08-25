# ViciDial feature parity — gap analysis & execution plan

> **Source of truth for this plan:** the ViciDial 2.14b0.5 API reference
> (`non_agent_api.php` — 59 functions, `agc/api.php` — 31 functions, API build
> 250105-1001). That document is a *behavioural spec*: each function tells us
> what the underlying feature must do and what data it must hold. This plan maps
> all 90 functions onto XeloVoice, says what already exists, what is missing, and
> in what order to build it.
>
> **Legal note.** ViciDial is AGPL. We implement *equivalent features* from the
> published API behaviour; we do not copy ViciDial source, schema DDL, or its
> Perl dialer daemons into this repo. Concepts (a "hopper", a "lead", a dial
> ratio) are not the copyrighted part — the code is.

---

## 0. The one-paragraph summary

XeloVoice today is an **Asterisk control console with call-center reporting**.
ViciDial is an **outbound predictive dialer + CRM + agent desktop** that happens
to also do inbound. The overlap is roughly 20%: we already own the telephony
substrate (PJSIP realtime, trunks, DIDs, IVR, transports, a WebRTC/native
softphone, CDR/CEL, queue reporting, RBAC, an API-token control plane). What we
do not have is the entire **campaign/lead/dialer half** — no leads, no lists, no
campaigns, no hopper, no dispositions-driven call flow, no dialer engine, and an
agent screen that is a phone rather than an agent desktop. That half is where
essentially all the work is.

---

## 1. What ViciDial actually is (decoded from the API surface)

The 90 functions cluster into seven domains. This is the feature list we are
committing to, expressed as domains rather than endpoints:

| Domain | What it means | API functions |
|---|---|---|
| **Leads & lists** | A CRM: leads live in *lists*, lists carry custom fields, leads carry status/callbacks/timezone/DNC state, with duplicate detection and archiving. | `add_lead`, `update_lead`, `batch_update_lead`, `lead_search`, `lead_status_search`, `lead_all_info`, `lead_field_info`, `lead_callback_info`, `lead_dearchive`, `ccc_lead_info`, `add_list`, `update_list`, `list_info`, `list_custom_fields`, `check_phone_number` |
| **Compliance** | DNC lists (campaign-scoped and global), Filter Phone Groups, per-state/timezone call-time windows. | `add_dnc_phone`, `delete_dnc_phone`, `add_fpg_phone`, `delete_fpg_phone` |
| **Campaigns & dialer** | The pacing engine: dial method (`MANUAL`/`RATIO`/`ADAPT_*`/`INBOUND_MAN`), dial level, hopper, lead order, dial statuses, caller-ID groups, presets. | `update_campaign`, `campaigns_list`, `hopper_list`, `hopper_bulk_insert`, `update_presets`, `update_alt_url`, `update_cid_group_entry`, `server_refresh` |
| **Agent desktop** | Not a softphone — a *screen*: manual/preview dial, disposition and move on, pause with a reason code, transfer/conference, park, DTMF, recording control, scripts, webforms, live field editing, notifications. | the 31 `agc/api.php` functions |
| **Inbound / in-groups** | DIDs routed to in-groups (skill-based queues), agent in-group selection, in-group real-time state. | `add_did`, `copy_did`, `update_did`, `add_group_alias`, `in_group_status`, `agent_ingroup_info`, `did_log_export` |
| **Users & devices** | Agent accounts distinct from SIP devices, user groups, remote agents, phones and phone aliases. | `add_user`, `copy_user`, `update_user`, `user_details`, `update_remote_agent`, `add_phone`, `update_phone`, `add_phone_alias`, `update_phone_alias`, `user_group_status` |
| **Reporting & recordings** | Agent time-and-motion exports, call-status and disposition breakdowns, recording lookup, live monitoring (listen/whisper/barge). | `agent_stats_export`, `call_status_stats`, `call_dispo_report`, `phone_number_log`, `recording_lookup`, `blind_monitor`, `agent_status`, `logged_in_agents`, `callid_info`, `update_log_entry`, `sounds_list`, `moh_list`, `vm_list`, `version`, `webserver` |

---

## 2. Gap analysis against XeloVoice today

### 2.1 Already ours (reuse, don't rebuild)

| ViciDial concept | XeloVoice equivalent |
|---|---|
| SIP phones (`add_phone`, `update_phone`) | `store/extensions.go` — full CRUD over PJSIP realtime, bulk CSV upload, live presence |
| Carrier/trunk config | `store/trunks.go` (register + IP mode) |
| DIDs / inbound routing | `tpbx_inbound_routes` + generated `[tpbx-inbound]` |
| IVR / call menus | `store/ivr.go` + the drag-and-drop `IVRBuilder.tsx` — richer than ViciDial's call-menu editor |
| Audio store (`sounds_list`, `moh_list`) | `api/sounds.go` prompt library with ffmpeg transcode |
| Users, user levels, permissions | `tpbx_users` + `tpbx_roles` RBAC matrix + TOTP — cleaner than ViciDial's numeric `user_level` |
| Agent softphone | `/phone` SPA, Electron desktop, native Android (pjsua2) |
| Call log (`phone_number_log`) | CDR/CEL in Postgres, `store/cdr.go` |
| Queue reporting | `queue_log` (0023) + `store/dashboard.go` — Service Level, Offered/Handled/Abandoned, AHT |
| Dispositions (partial) | `tpbx_softphone_events` nature/resolution/hangup_cause wrap-up (0022) |
| API access | `/api/v1` + hashed API tokens (0025) |

### 2.2 Missing entirely

- **Leads, lists, custom fields, callbacks, lead archive** — no table, no concept.
- **Campaigns** — no notion of a campaign at all. This is the organising object
  the entire ViciDial model hangs off; almost every other feature references it.
- **The hopper and the dialer engine** — no outbound automation of any kind.
  Today every outbound call is a human pressing dial.
- **Dispositions as a call-flow primitive** — we tag calls *after the fact* for
  analytics; ViciDial's status drives recycling, callbacks, hopper eligibility
  and reporting.
- **DNC / filter phone groups / call-time windows** — no compliance layer.
- **Agent desktop semantics** — pause codes, preview dial, script panel,
  webforms, alt-phone dialing, transfer-conference frame, park, monitoring.
- **In-groups as skills** — our "queue" is a target list, not a skilled ACD.
- **Recording lookup/management** — Asterisk can record; nothing indexes it.
- **Remote agents**, **CID groups**, **campaign presets**, **alt dispo URLs**.

### 2.3 One existing inconsistency this plan must fix

`store/ivr.go` compiles the `queue` action into a `While`/`Dial()` retry loop —
**not** Asterisk `app_queue`. But `docs/CALL_CENTER.md` and `store/dashboard.go`
compute every call-center metric from the `queue_log` table, which **only
`app_queue` writes**. So a queue built in the GUI today produces *no* dashboard
numbers; the docs quietly tell the operator to hand-write `Queue()` in the
dialplan instead. Real ACD/in-group work (Phase 5) must move the generated
dialplan onto `app_queue` with realtime queue members, which then makes the
existing dashboard light up from GUI-managed queues.

---

## 3. Target architecture

### 3.1 Data model — new `tpbx_*` tables

Mirroring ViciDial's *concepts*, not its MySQL DDL. Postgres types, our naming,
one migration per phase.

```
tpbx_campaigns        id, name, active, dial_method, dial_level, adaptive_max,
                      hopper_level, dial_timeout, lead_order, lead_order_secondary,
                      dial_statuses[], outbound_cid, cid_group_id, trunk,
                      amd_enabled, drop_rate_target, drop_action, safe_harbour_sound,
                      wrapup_seconds, script_id, lead_filter_id, dispo_url, webform_urls
tpbx_lists            id, campaign_id, name, description, active, reset_time,
                      expiry_date, custom_field_schema (JSONB)
tpbx_leads            id, list_id, status, phone_number, phone_code, alt_phone1/2,
                      first/last_name, address..., state, timezone_offset,
                      vendor_lead_code, source_id, called_count, last_call_time,
                      last_call_status, entry_date, modify_date, owner, custom (JSONB)
tpbx_leads_archive    same shape; lead_dearchive moves rows back
tpbx_hopper           lead_id, campaign_id, priority, state, source, inserted_at
tpbx_dispositions     campaign_id, status, name, selectable, human_answered,
                      is_sale, not_interested, dnc, callback, recycle_after_sec
tpbx_callbacks        lead_id, campaign_id, user, callback_at, recipient(ANYONE|USERONLY), note
tpbx_dnc              phone_number, scope (GLOBAL|campaign_id), added_by, at
tpbx_filter_groups    group_id, name;  tpbx_filter_group_numbers  group_id, phone_number
tpbx_call_times       name, per-day windows, per-state overrides, timezone rules
tpbx_ingroups         id, name, skills, moh, announce, drop_action, queue_priority
tpbx_agent_ingroups   user, ingroup_id                       (agent's selected skills)
tpbx_agent_log        user, campaign, login_at, logout_at, pause_sec, wait_sec,
                      talk_sec, dispo_sec, pause_code           (agent_stats_export)
tpbx_dial_log         lead_id, campaign_id, call_id, uniqueid, dialed_at, answered_at,
                      status, length_sec, agent, drop_flag, amd_result
tpbx_recordings       call_id, uniqueid, lead_id, agent, started_at, duration, path
tpbx_scripts          id, name, body (HTML with --A--field--B-- substitutions)
tpbx_pause_codes      campaign_id, code, name, billable
tpbx_cid_groups       id, name, mode; tpbx_cid_group_entries  group_id, cid, state
```

### 3.2 The dialer engine — the hard part

ViciDial dials with Perl daemons driving AGI and MeetMe conferences. We have
**ARI**, which is a strictly better tool for this. The design:

- **Agent session = a holding bridge.** When an agent goes available, the
  backend originates one call to their extension and parks that channel in a
  per-agent ARI **holding bridge** (this is ViciDial's `call_agent` /
  session-conference model). Customer channels are *moved into* the bridge on
  connect, so the agent hears the customer instantly with no ring — which is
  what makes predictive dialing feel instant.
- **Hopper filler** — one goroutine per active campaign: selects leads by
  `lead_order`, applies list/filter/DNC/call-time/timezone rules, and tops the
  hopper up to `hopper_level`. Cheap, purely SQL, runs on a ticker.
- **Pacing loop** — computes lines to open per tick:
  - `MANUAL` — none; agent-initiated only.
  - `RATIO` — `available_agents × dial_level`.
  - `ADAPT_*` — same, with `dial_level` adjusted from a moving average of
    answer rate and measured drop rate, clamped by `adaptive_max` and by the
    configured **drop-rate ceiling**.
- **Originate → classify → connect.** Each line is an ARI originate into our
  Stasis app. On answer: optional AMD; human → pick the longest-idle available
  agent, move the channel into their bridge, write `tpbx_dial_log` + fire a
  screen-pop over the existing `ws` hub. No agent free → **drop**: play the
  safe-harbour message or route to an in-group, and count it against drop rate.
  No answer/busy/machine → classify and write the status back to the lead.
- **Recycling.** Disposition rules decide whether the lead returns to the hopper
  (after `recycle_after_sec`), schedules a callback, or is retired.

All of this lives in a new `internal/dialer` package and reuses `internal/ari`.
It is the one component that must be **restart-safe and idempotent** — a crash
must not leave customer channels alive with no agent.

### 3.3 Compliance is not optional

Predictive dialing is regulated. The engine must enforce, in code and not just
in the UI: the drop/abandon-rate ceiling per campaign, per-state and per-timezone
call-time windows, DNC checks at hopper-fill *and* at dial time (a number can be
DNC'd between the two), and an audit trail of every dial decision. These are
Phase-4 acceptance criteria, not follow-ups.

---

## 4. Phase plan

Each phase is independently shippable and follows the repo's existing recipe
(§13.1 of `DEEP_INDEX.md`): migration → `store` type → handlers in `Router()`
under a `requirePerm` group → typed client in `web/src/api.ts` → a component in
`NAV` → verify with `gofmt`/`go build`/`go vet`/`npm run build`.

| Phase | Deliverable | Size | Unlocks |
|---|---|---|---|
| **P1 — Leads & Lists** ✅ | `tpbx_lists`, `tpbx_leads`, custom fields, header-driven CSV import, dedup scopes, lead search/detail UI, `leads` RBAC feature. **Shipped** — migration 0026, `store/lists.go`, `store/leads.go`, `api/leads.go`, `components/Leads.tsx`; see `DEEP_INDEX.md` §20. | L | everything |
| **P2 — Campaigns & manual dial** ✅ | `tpbx_campaigns`, `tpbx_dispositions`, `tpbx_pause_codes`, `tpbx_agents`, `tpbx_lead_calls`; campaign CRUD UI, agent accounts with campaign assignment, manual + preview dial from a lead, disposition writing back to the lead. **Shipped** — migration 0027; see `DEEP_INDEX.md` §21. | L | a usable outbound desk |
| **P3 — Agent desktop v2** | Rework `/phone` into an agent screen: script panel, lead fields with live edit, disposition bar, pause-with-code, callbacks, alt-phone dialing, transfer/conference, park, DTMF, wrap-up timer. Extend the desktop/Android clients to match. | XL | ViciDial's agent surface |
| **P4 — Dialer engine** | `internal/dialer`: hopper filler, pacing (`RATIO` → `ADAPT_*`), ARI holding-bridge connect, AMD, drop handling + safe harbour, `tpbx_dial_log`, live campaign monitor UI. Compliance rules enforced. | XL | the actual product |
| **P5 — In-groups & real ACD** | Move the generated `queue` action onto `app_queue` with realtime queues + members (fixes §2.3), skills-based in-groups, DID→in-group routing, agent in-group selection, blended (`INBOUND_MAN`) agents. | L | inbound parity + working dashboard |
| **P6 — Compliance & data hygiene** | DNC (global + per-campaign), filter phone groups, call-time/timezone windows, lead archive/dearchive, list reset, duplicate-check options. | M | legal to run |
| **P7 — Recordings & monitoring** | Recording index + playback UI, on-demand start/stop, listen/whisper/barge (`blind_monitor` equivalent via ARI snoop), QA scoring hooks. | M | supervision |
| **P8 — Reporting parity** | `agent_stats_export`, `call_status_stats`, `call_dispo_report`, real-time agent/in-group/user-group status boards, export in csv/tab/json/pipe. Extends `store/dashboard.go`. | M | management |
| **P9 — API surface** *(optional)* | Expose all of the above on `/api/v1` (native REST, our style). Optionally a thin `non_agent_api.php`-shaped compatibility shim for migrating customers with existing ViciDial integrations. | M | integration/migration |

**Ordering rationale.** P1→P2→P4 is the critical path; P3 can proceed in
parallel with P2 once the campaign/disposition schema lands. P5 is independent
and is the cheapest real win (it also repairs an existing bug). P9 is last
because the internal model must settle before we freeze an external contract.

---

## 5. Function inventory — all 90 mapped

**NON-AGENT API (59)**

| Function | Domain | Phase | XeloVoice equivalent |
|---|---|---|---|
| `version`, `webserver` | meta | P9 | `/api/v1/ping` exists |
| `sounds_list`, `moh_list`, `vm_list` | audio | P9 | `api/sounds.go` — expose on v1 |
| `blind_monitor` | monitoring | P7 | new — ARI `snoop` (listen/whisper/barge) |
| `add_lead`, `update_lead`, `batch_update_lead` | leads | P1 | new |
| `lead_search`, `lead_status_search`, `lead_all_info`, `lead_field_info` | leads | P1 | new |
| `lead_callback_info` | callbacks | P2 | new |
| `lead_dearchive` | leads | P6 | new |
| `ccc_lead_info` | leads | — | cross-cluster; out of scope (single-cluster product) |
| `add_list`, `update_list`, `list_info`, `list_custom_fields` | lists | P1 | new |
| `check_phone_number` | validation | P1 | new — shared validator |
| `add_dnc_phone`, `delete_dnc_phone` | DNC | P6 | new |
| `add_fpg_phone`, `delete_fpg_phone` | filters | P6 | new |
| `update_campaign`, `campaigns_list` | campaigns | P2 | new |
| `hopper_list`, `hopper_bulk_insert` | dialer | P4 | new |
| `update_presets` | campaigns | P2 | new |
| `update_alt_url` | campaigns | P2 | new (dispo URL webhooks) |
| `update_cid_group_entry` | caller ID | P4 | new |
| `server_refresh` | cluster | — | n/a — our config is DB-live + generated includes |
| `add_did`, `copy_did`, `update_did` | inbound | P5 | **partial** — `tpbx_inbound_routes`; add copy + in-group target |
| `add_group_alias` | inbound | P5 | new |
| `in_group_status`, `agent_ingroup_info` | inbound | P5 | new |
| `did_log_export` | reporting | P8 | derivable from CDR today |
| `add_user`, `copy_user`, `update_user`, `user_details` | users | P2 | **partial** — `tpbx_users`; add copy + campaign/in-group assignment |
| `update_remote_agent` | users | P8 | new |
| `add_phone`, `update_phone` | devices | ✅ | `store/extensions.go` |
| `add_phone_alias`, `update_phone_alias` | devices | P8 | new |
| `user_group_status` | reporting | P8 | new |
| `agent_status`, `logged_in_agents` | reporting | P3 | **partial** — live presence exists; add campaign/pause/lead context |
| `callid_info`, `update_log_entry` | reporting | P8 | new |
| `agent_stats_export` | reporting | P8 | **partial** — `store/analytics.go` |
| `call_status_stats`, `call_dispo_report` | reporting | P8 | **partial** — dashboard rollups |
| `phone_number_log` | reporting | ✅ | CDR query |
| `recording_lookup` | recordings | P7 | new |
| `agent_campaigns` | campaigns | P2 | new |

**AGENT API (31)** — all land in **P3** unless noted; all become WebSocket
commands over the existing hub plus REST fallbacks under `/api/agent/*`:

`external_dial`, `external_hangup`, `external_status`, `external_pause`,
`pause_code`, `logout`, `preview_dial_action`, `external_add_lead`,
`change_ingroups` (P5), `update_fields`, `refresh_panel`, `set_timer_action`,
`st_get_agent_active_lead`, `st_login_log`, `ra_call_control` (P8),
`send_dtmf`, `park_call`, `transfer_conference`, `recording` (P7),
`stereo_recording` (P7), `webphone_url`, `call_agent` (P4 — the holding-bridge
session), `audio_playback`, `switch_lead`, `vm_message`, `calls_in_queue_count`
(P5), `force_fronter_leave_3way`, `force_fronter_audio_stop`,
`send_notification`, `version`, `webserver`.

---

## 6. Decisions — settled and outstanding

**Settled in P2.**

- **Agent identity** — resolved as recommended: `tpbx_agents` is now a
  first-class person, with the SIP extension as its device binding. Softphone
  authentication still runs on extension + SIP secret; P3 moves it across.
- **Multi-tenancy** — taken as *single-tenant*. Nothing in the product carries a
  tenant key across 27 migrations, so adding one to only the campaign tables
  would be incoherent. If multi-tenancy is ever wanted it is a cross-cutting
  migration over every `tpbx_*` table, not a per-phase choice.

**Still outstanding:**

1. **API compatibility.** Native `/api/v1` only, or also a ViciDial-shaped shim
   (`function=`, pipe-delimited `SUCCESS:`/`ERROR:` strings) so existing customer
   integrations port unchanged? Cheap if decided early, expensive later.
2. **Scale target.** Agents and calls-per-second per box drives whether the
   dialer is in-process goroutines (fine to ~100 agents) or a separate service.
   This one binds at **P4**, not P9: it decides the dialer's deployment shape.

---

*Companion documents: `docs/DEEP_INDEX.md` (the codebase map),
`docs/CALL_CENTER.md` (current ACD reporting), `docs/API.md` (`/api/v1`).*
