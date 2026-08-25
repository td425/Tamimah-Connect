# XeloVoice — Deep Index & Developer Handoff

> **Purpose of this file.** This is the single authoritative map of the codebase.
> If you are an AI or a new developer picking this project up cold, read this
> top to bottom once. It explains what the system is, how every layer fits
> together, the non-obvious rules that will bite you, and the exact recipes for
> extending it "the same way" the existing features were built. When in doubt,
> the code is the source of truth — but this document tells you *where to look*
> and *why things are the way they are*.

XeloVoice is designed and developed by **Xelocorp** and is one of its products.
Do not resell or modify without official confirmation from Xelocorp.

---

## 0. TL;DR for the impatient

- **What it is:** a web console + backend that turns Asterisk (an open-source
  PBX) into a point-and-click phone system — extensions, trunks, call routing,
  IVR/auto-attendant, a browser WebRTC softphone, live dashboards, analytics.
- **Backend:** Go (chi router) talking to **PostgreSQL** and to Asterisk over
  **ARI** (REST + WebSocket) and **AMI** (manager). One binary: `tpbx`.
- **Frontend:** React + TypeScript + Vite, single-page app, served by the Go
  binary. A separate agent softphone SPA and a browser extension share code.
- **The key idea:** Asterisk reads *most* config live from Postgres (PJSIP
  "realtime"); the things realtime can't express (dialplan, transports) are
  **generated** by the backend into `#include` files. See §2.
- **Deploy:** `install.sh` (fresh server) / `upgrade.sh` (in place). Runs as a
  systemd service. All state in Postgres + a few generated files under
  `/var/lib/tpbx` and `/var/lib/asterisk/sounds/en/tpbx`.
- **Conventions that matter:** develop on the designated feature branch; every
  change must `gofmt` + `go build ./...` + `go vet ./...` + `npm run build`
  clean before commit; commit footer is fixed (see §12); never put the model
  identifier in artifacts; no PR unless explicitly asked.

---

## 1. Product surface (what the user sees)

Nav pages (in `web/src/App.tsx`, `NAV` array), each a component in
`web/src/components/`:

| Nav key | Component | Purpose |
|---|---|---|
| `dashboard` | `Dashboard.tsx` | Stat tiles, originate a call, "Apply Changes" (module reload), registered endpoints, **Live Activity** stream. |
| `extensions` | `Extensions.tsx` | SIP accounts CRUD, live device presence tiles, detail popup, password reset, **bulk CSV upload**. |
| `trunks` | `Trunks.tsx` | Provider/ITSP connections (register or IP mode), live reachability. |
| `routing` | `Routing.tsx` | Outbound routes (pattern → trunk **or IVR**) and inbound routes (DID → extension / IVR / voicemail / play / hangup / external-GSM / queue). |
| `ivr` | `IVR.tsx` + `IVRBuilder.tsx` | Auto-attendant menus: prompt library (upload WAV/MP3…), form editor, **drag-and-drop visual builder**, import/export JSON, sub-menus, chained actions. |
| `cdr` | `CallHistory.tsx` | Call detail records. |
| `analytics` | `Analytics.tsx` | Live call flow (originator→PBX→destination with RTP direction), agent device tiles, per-agent stats. Manager/admin only. |
| `transports` | `Transports.tsx` | PJSIP transports (UDP/TCP/TLS/WSS) + TLS; needs a service restart to re-bind. Admin only. |
| `settings` | `Settings.tsx` | WebRTC/TURN/STUN configuration for the softphone. Admin only. |
| `users` | `Users.tsx` | Console users (admin/manager/viewer roles). Admin only. |

Two extra front-ends served by the same binary:
- **Agent softphone** at `/phone` (`web/src/agent/*`, built to `dist-agent`).
- **Browser extension** (`web/src/ext/*`, built to `dist-ext`, Chrome MV3 +
  Firefox) — packaged into a downloadable zip during install.

Brand: the "XELOVOICE" wordmark PNGs live in `web/src/assets/` (light/dark,
swapped by theme). A licensing notice appears in the sidebar footer and in
generated config file headers.

---

## 2. THE core design principle — hybrid config (read this twice)

Asterisk gets its configuration from two very different places, and knowing
which is which explains 80% of the codebase.

### (a) PJSIP **realtime** (database, live, no reload for reads)
Endpoints, AORs, auths, contacts, trunk identities/registrations are stored in
Postgres tables (`ps_endpoints`, `ps_aors`, `ps_auths`, `ps_contacts`,
`ps_endpoint_id_ips`, `ps_registrations`). Asterisk reads them **on demand**
via `res_config_pgsql` (native pgsql — **not** ODBC). So creating an extension
is just SQL — no file writing.
- Caveat: **registrations and identifies** (trunks) are only read at
  load/reload time. After writing a trunk you must `res_pjsip.so` reload
  (`s.reloadPJSIP`). Endpoints/AORs are truly on-demand.

### (b) **Generated `#include` files** (what realtime can't do)
The **dialplan** (routing + IVR) and **PJSIP transports** cannot live in
realtime, so the backend *generates* them into files that Asterisk `#include`s:
- Dialplan → `TPBX_DIALPLAN_FILE` = `/var/lib/tpbx/extensions_tpbx.conf`
  (produced by `store.Routes.GenerateDialplan` + `store.IVRs.GenerateDialplan`,
  written + reloaded by `api.Server.applyDialplan`).
- Transports → `TPBX_TRANSPORTS_FILE` = `/var/lib/tpbx/pjsip_transports.conf`
  (produced by `store.Transports.GenerateConfig`, written on startup and on
  change; **bind changes need a full Asterisk restart**, not a reload).

These files live under the service's **own writable state dir** (`/var/lib/tpbx`),
never `/etc`, because systemd `ProtectSystem=full` makes `/etc` read-only for
the service. `extensions.conf` in `/etc/asterisk` `#include`s them.

**Rule of thumb when adding a feature:** ask "can Asterisk express this in a
realtime table?" If yes → write SQL in a `store` type. If no (it's dialplan or
a load-time object) → generate it into an include file and reload/restart.

---

## 3. Repository map (every file, what it does)

```
.
├── cmd/tpbx/main.go        Entry point. Subcommands: serve|migrate|create-admin|version.
│                           Wires config → db → stores → api.Server; runs ARI+AMI event
│                           loops; regenerates transports + dialplan on startup.
├── embed.go                //go:embed migrations/*.sql  (migrations travel in the binary)
├── go.mod / go.sum         Go 1.25. Deps: chi, pgx v5, coder/websocket.
├── Makefile                Dev helpers (build/run/db).
├── NOTICE                  Xelocorp ownership/licensing notice.
├── README.md               User-facing install/dev quickstart.
│
├── internal/
│   ├── config/config.go    Env-var config loader (all TPBX_* vars). Defaults for single-VM.
│   ├── db/db.go            pgxpool connection open/close.
│   ├── migrate/migrate.go  Forward-only SQL migration runner (schema_migrations table).
│   ├── ari/ari.go          ARI client: REST calls (channels, endpoints, originate,
│   │                       hangup, reload, RTP counters, info) + Stasis event WebSocket.
│   ├── ami/ami.go          AMI client: login, action Exec (e.g. "core restart now"),
│   │                       event stream.
│   ├── ws/hub.go           In-process pub/sub hub broadcasting events to browser WS clients.
│   ├── store/              DB-backed domain logic (one file per resource):
│   │   ├── extensions.go   SIP accounts (ps_auths+ps_aors+ps_endpoints as one "extension");
│   │   │                   List/Get/Create/Update/Delete, live Status (presence from
│   │   │                   ps_contacts + tpbx_ext_presence), SetPassword, device classify.
│   │   ├── trunks.go       Trunk objects (register/ip mode), splitHostPort helper.
│   │   ├── routes.go       Outbound/Inbound routes + GenerateDialplan (the [tpbx-outbound]
│   │   │                   and [tpbx-inbound] contexts). Dest encodings live here.
│   │   ├── ivr.go          IVR menus + options; GenerateDialplan ([tpbx-ivr-<name>]);
│   │   │                   ALL action types (extension/ivr/voicemail/playback/repeat/
│   │   │                   external/queue/hangup); sound path resolution (absolute).
│   │   ├── transports.go   PJSIP transports + GenerateConfig (the transports include).
│   │   ├── users.go        Console users + sessions (opaque tokens, bcrypt).
│   │   ├── agents.go       Agent softphone sessions (extension + SIP secret auth).
│   │   ├── settings.go     WebRTC settings (host/wss/stun/turn) persisted in DB.
│   │   ├── analytics.go    Per-agent stats from CDR + CEL.
│   │   └── cdr.go          Call detail records query.
│   └── api/                HTTP surface (chi). One file per area; api.go is the spine:
│       ├── api.go          Server struct, Router() (all routes + middleware), extension
│       │                   handlers, randomSecret, SPA/static serving.
│       ├── auth.go         login/logout/me/change-password + requireAuth/requireAdmin/
│       │                   requireManager middleware + audit.
│       ├── agent.go        /api/agent/* (agent login/config), CORS, iceServers (TURN).
│       ├── routes.go       outbound/inbound handlers + applyDialplan + ApplyDialplan.
│       ├── ivr.go          IVR handlers (create/update/delete call applyDialplan).
│       ├── sounds.go       Prompt library: list/upload(+transcode)/audio/delete.
│       ├── trunks.go handlers live in api.go (see reloadPJSIP), transports.go, cdr.go,
│       ├── analytics.go, settings.go   … the rest of the handlers.
│
├── migrations/*.sql        0001..0016 (see §5). Applied in order, once each.
│
├── asterisk/               Templates rendered into /etc/asterisk by install.sh:
│   ├── extensions.conf     Static contexts (from-internal, from-trunk) that #include the
│   │                       generated dialplan; wires realtime.
│   ├── pjsip_transports.conf  Seed transports for first boot.
│   ├── sorcery.conf        Maps PJSIP object types to the realtime pgsql backend.
│   ├── extconfig.conf      extconfig: which realtime families use pgsql.
│   ├── res_pgsql.conf      pgsql connection (host/db/user/pass placeholders).
│   ├── ari.conf manager.conf  ARI + AMI users/permissions (localhost only).
│   ├── cdr_pgsql.conf cel_pgsql.conf  CDR/CEL to Postgres.
│   ├── modules.conf        Preload/noload — CRUCIALLY `noload = chan_sip.so` (see §14).
│   └── logger.conf
│
├── install.sh              Fresh-server installer (root). See §11.
├── upgrade.sh              In-place upgrade (pull → build → migrate → restart).
├── scripts/
│   ├── lib.sh              Shared shell functions/vars (build_app, run_migrations,
│   │                       provision_sounds, ensure_ffmpeg, ensure_env_kv, SOUNDS_DIR…).
│   └── diagnose.sh         On-box diagnostics.
│
├── docs/
│   ├── ARCHITECTURE.md     Original design rationale (shorter).
│   ├── TROUBLESHOOTING.md  Symptom → fix table.
│   └── DEEP_INDEX.md       ← you are here.
│
└── web/                    Frontend (Vite). Three build targets:
    ├── package.json        scripts: dev, build (dashboard), build:agent, build:ext.
    ├── vite.config.ts / vite.agent.config.ts / vite.ext.config.ts
    ├── index.html agent.html popup.html options.html background.html offscreen.html
    ├── manifest.chrome.json manifest.firefox.json   (extension manifests)
    └── src/
        ├── main.tsx        Dashboard SPA entry.
        ├── App.tsx         Auth gate, nav, top bar, theme toggle, live-event wiring.
        ├── api.ts          ALL dashboard API calls + shared TS types. Central contract.
        ├── events.ts       describeEvent(): raw ARI/AMI → friendly Live Activity lines.
        ├── theme.css        Entire visual system (CSS variables, light/dark, all panels).
        ├── types.ts        Notify/Toast UI types.
        ├── assets/         XeloVoice logos (light/dark PNG).
        ├── components/     One file per nav page (+ CallFlow.tsx, IVRBuilder.tsx).
        ├── agent/          Softphone SPA (Sip.js wrapper sip.ts, ringer.ts, api.ts).
        └── ext/            Browser extension (MV3 SW/offscreen engine, popup, options).
```

---

## 4. Backend request lifecycle

1. `main.run()` loads `config`, opens `db`, builds the `ari.Client`, starts two
   goroutines: `runARIEvents` (Stasis WS → hub) and `runAMIEvents` (AMI → hub),
   both reconnecting forever.
2. It constructs `api.Server` (a big struct holding every `store` + ARI + hub +
   file paths + WebRTC params + a `RestartAsterisk` closure). It calls
   `store.SetSoundLocation(...)` and best-effort regenerates transports +
   dialplan so file-based config reflects the DB on boot.
3. `srv.Router()` builds the chi tree:
   - Public: `/api/health`, `/api/login`, `/api/logout`.
   - `/api/agent/*` (its own CORS + token auth for the softphone/extension).
   - Everything else behind `requireAuth` + `audit`; some behind
     `requireManager` (analytics) or `requireAdmin` (users, transports,
     settings, restart).
   - Non-API paths: `/phone` + `/phone/*` (agent SPA), `/downloads/*`
     (extension zip; 404s if missing), `/` (dashboard SPA fallback).
4. Handlers are thin: decode JSON → call a `store` method → for dialplan-backed
   resources call `applyDialplan` (regenerate file + reload) → return JSON.
5. Events flow the other way: Asterisk → ARI/AMI goroutine → `hub.Broadcast` →
   browser WS (`/ws`) → `App.tsx` `connectEvents` → `events.ts describeEvent` →
   Live Activity.

**Server struct fields** (in `api.go`) you'll reference constantly: `DB`, `ARI`,
`Hub`, `Ext`, `Trunks`, `Routes`, `IVRs`, `Transports`, `Users`, `Agents`,
`Settings`, `Analytics`, `CDR`, `DialplanFile`, `TransportsFile`, `WebDir`,
`AgentWebDir`, `SoundsDir`, `SoundsPrefix`, `Domain`, `WSSPort`, `TURNSecret`,
`TURNTTL`, `RestartAsterisk`.

---

## 5. Data model / migrations

Migrations are plain SQL in `migrations/NNNN_name.sql`, embedded in the binary
(`embed.go`), applied in filename order exactly once (tracked in
`schema_migrations`). `tpbx migrate` (run by install/upgrade) is idempotent.

| # | File | Adds |
|---|---|---|
| 0001 | pjsip_realtime | PJSIP realtime tables: `ps_endpoints`, `ps_aors`, `ps_auths`, `ps_contacts`, `ps_endpoint_id_ips`, `ps_registrations`. |
| 0002 | cdr_cel | CDR + CEL tables Asterisk writes call records to. |
| 0003 | gui | `tpbx_users` (console accounts) + `tpbx_audit_log`. |
| 0004 | ps_contacts_full | Adds the remaining `ps_contacts` columns Asterisk INSERTs on REGISTER (else contacts fail). |
| 0005 | routes | `tpbx_outbound_routes`, `tpbx_inbound_routes`. |
| 0006 | sessions | `tpbx_sessions` (console auth tokens). |
| 0007 | transports | `tpbx_transports`. |
| 0008 | agent_sessions | `tpbx_agent_sessions`. |
| 0009–0011 | webrtc_* | WebRTC/TURN/STUN settings columns. |
| 0012 | rtp_timeout | `ps_endpoints.rtp_timeout` + `rtp_timeout_hold` columns (drop dead calls). |
| 0013 | ivr | `tpbx_ivrs`, `tpbx_ivr_options`. |
| 0014 | ext_presence | `tpbx_ext_presence` (last-seen memory for offline devices). |
| 0015 | outbound_ivr | `tpbx_outbound_routes.dest_type` + `ivr` (route to a menu). |
| 0016 | ivr_layout | `tpbx_ivrs.layout` (visual builder canvas positions, opaque JSON). |
| 0017 | roles | `tpbx_roles` (per-feature `permissions` JSONB, `require_totp`, `built_in`); seeds admin/manager/operator/viewer. See §17. |
| 0018 | totp | `tpbx_users.totp_secret` + `totp_enabled` (two-factor). See §18. |
| 0019 | pjsip_settings | `tpbx_pjsip_settings` (singleton row: global res_pjsip options + TLS defaults). See §19. |
| 0020 | system_settings | `tpbx_system_settings` (singleton: public domain, brand name, default theme, timezone). |
| 0021 | softphone_events | `tpbx_softphone_events` (client-side telemetry: DND, registration, per-call outcome). |
| 0022 | call_dispositions | Wrap-up columns on `tpbx_softphone_events` (nature, resolution, hangup cause, note). |
| 0023 | queue_log | `queue_log` — Asterisk app_queue's own log, written via realtime. Source of the call-center dashboard. |
| 0024 | sla_seconds | `tpbx_system_settings.sla_seconds` (service-level threshold). |
| 0025 | api_tokens | `tpbx_api_tokens` (hashed bearer tokens for `/api/v1`). |
| 0026 | leads_lists | `tpbx_lists` + `tpbx_leads`, and the `leads` RBAC feature. See §20. |
| 0027 | campaigns | `tpbx_campaigns`, `tpbx_dispositions`, `tpbx_pause_codes`, `tpbx_agents` (+ campaign assignments), `tpbx_lead_calls`; promotes `tpbx_lists.campaign_id` to a real reference; `campaigns` RBAC feature. See §21. |
| 0028 | agent_desktop | `tpbx_agent_state` (live working state), `tpbx_agent_log` (append-only shift log), `tpbx_callbacks`. See §22. |

Two families of tables:
- **`ps_*`** — Asterisk's PJSIP realtime schema. XeloVoice writes rows; Asterisk
  reads them. Column names are dictated by Asterisk/Alembic; don't rename.
- **`tpbx_*`** — XeloVoice's own tables (routes, ivrs, users, sessions,
  settings, presence). Free to evolve via new migrations.

---

## 6. The IVR system (the most feature-dense area — study this to extend it)

### 6.1 Data shape
`store.IVR` = { id, name, greeting, timeoutSec, maxRetries, invalidDest,
timeoutDest, layout, options[] }. `store.IVROption` = { digit, destType,
destValue, label }. Stored in `tpbx_ivrs` + `tpbx_ivr_options` (one row per
option, ordered by `position`).

### 6.2 Action (destType) types and how they compile
Defined in `store/ivr.go` (`ivrActionLines`, `ivrDestLines`, `destType`):

| destType | `destValue` encoding | Dialplan produced |
|---|---|---|
| `extension` | `1001` | `Dial(PJSIP/1001,30)` then Hangup |
| `external` | `NUMBER@TRUNK` (`splitExternal`) | `Dial(PJSIP/NUMBER@TRUNK,60)` then Hangup |
| `queue` | `AGENTS;HOLDPROMPT` (`splitQueue`, agents `&`-joined) | While/EndWhile loop: ring all agents, on no-answer play hold prompt, retry (queueMaxTries×queueRing). |
| `ivr` | `menuName` | `Goto(tpbx-ivr-menuName,s,1)` (sub-menu). |
| `voicemail` | `mailbox` | `VoiceMail(mailbox@default,u)` then Hangup |
| `playback` | sound ref (`tpbx/name`) | `Playback(<absolute path>)` — intermediate, continues. |
| `repeat` | — | `Goto(tpbx-ivr-<self>,s,menu)` (replay this menu). |
| `hangup` | — | `Hangup()` |

### 6.3 Chained actions (multiple steps per key)
Options that **share a digit** form that key's chain, in array/`position`
order. `GenerateDialplan` groups by digit and emits them in sequence:
`playback` is *intermediate* (plays then continues); any other action is
*terminal* (ends the chain). So "press 1 → play message → ring 1001" is two
options with digit `1`: `{playback, tpbx/msg}` then `{extension, 1001}`.

### 6.4 Sounds — the language-path gotcha (see §14) and transcoding
- Uploaded prompts live under `SoundsDir` = `/var/lib/asterisk/sounds/en/tpbx`,
  referenced as `tpbx/<name>` (`SoundsPrefix`).
- `store.resolveSound` rewrites a managed ref to an **absolute path**
  (`/var/lib/asterisk/sounds/en/tpbx/<name>`) in the dialplan so
  `Background()`/`Playback()` bypass per-language lookup (channels with no
  language set otherwise play silence). Set via `store.SetSoundLocation` in main.
- `api/sounds.go` transcodes uploads to **8 kHz/16-bit mono PCM WAV** via
  ffmpeg (fallback sox), and chmods 0644 so the `asterisk` user can read them.
  Any audio (mp3/m4a/odd WAV) is accepted — the converter validates.

### 6.5 Visual builder (`IVRBuilder.tsx`)
A dependency-free node-graph editor. The **menu** is a root node with an output
port per key + invalid/timeout; each port wires to a **destination block**;
blocks have their own output port so they **chain** (`out:<nodeId>` edges).
Positions persist in `tpbx_ivrs.layout` (JSON keyed by `digit#step`). Sub-menu
blocks can create a child IVR inline and "↗" to open it. On save the builder
walks each key's chain into ordered options (see `fromIVR` / `save`).

### 6.6 To add a NEW IVR action type (full recipe)
1. **Backend** (`store/ivr.go`): add a `case` in `ivrActionLines` **and**
   `ivrDestLines` (fallbacks/inbound), and add the string to `destType()`'s
   whitelist. Add any `split*`/encode helper if the value packs multiple fields.
2. **Frontend types** (`web/src/api.ts`): add it to the `IVRDestType` union.
3. **IVR.tsx**: add to `DEST_TYPES`, `DEST_LABEL`, `needsTarget`, the FlowMap
   `destText`, `TargetInput`, and the fallback `DestPicker`. Add `parse*/make*`
   helpers if multi-field.
4. **IVRBuilder.tsx**: add to `PALETTE`, `KIND_LABEL`, `needsValue`, and a
   `NodeField` branch (the inline editor for that block).
5. **Routing.tsx** (optional): add it to the inbound "Send to" selector if it
   makes sense for a DID.
6. Build all three (`npm run build`, `build:agent` if agent touched), `gofmt`,
   `go build`, `go vet`. The `external` and `queue` types are complete
   worked examples — copy their structure.

---

## 7. Routing (`store/routes.go`, `Routing.tsx`)

- **Outbound**: pattern match in `[tpbx-outbound]` (included by `from-internal`).
  `dest_type='trunk'` → strip/prepend then `Dial(PJSIP/num@trunk)`;
  `dest_type='ivr'` → `Goto(tpbx-ivr-<name>,s,1)`.
- **Inbound**: DID match in `[tpbx-inbound]` (included by `from-trunk`).
  `destination` is a bare number (extension) OR a `type:value` string handled by
  `ivrDestLines` — supports `ivr:`, `voicemail:`, `playback:`, `external:`,
  `queue:`, and the bare keyword `hangup`.
- Front-end encodes these in `Routing.tsx` (`parseInDest`/`encodeInDest`,
  `ext*`/`q*` helpers). Same `NUMBER@TRUNK` and `AGENTS;HOLDPROMPT` encodings as
  IVR.

---

## 8. Frontend architecture

- **`api.ts` is the contract.** Every backend endpoint has a typed function and
  interface here. When you add/modify an API, update this file first; components
  import from it. It also re-exports shared types (Extension, Trunk, IVR, etc.).
- **`theme.css`** is the entire design system: CSS variables on `:root` (dark
  base) and `:root[data-theme="light"]`. Colors are a single green hue (`--g`)
  over green-black surfaces + one amber accent. To restyle, change the variables.
  The theme is applied by `App.tsx` writing `document.documentElement.dataset.theme`.
- **Components** are self-contained; they receive a `notify` callback for toasts.
  Modals use `.modal-backdrop`/`.modal`. Tables/panels/badges are all themed.
- **Live events**: `App.tsx` `connectEvents` → `events.ts describeEvent` maps raw
  ARI/AMI to `{category: call|device|system, text}`, dropping noise. Rendered in
  Dashboard "Live Activity".
- **Agent app** (`web/src/agent/`): SIP.js over secure WebSocket. `sip.ts` wraps
  SIP.js; `ringer.ts` synthesizes the ring; `api.ts` hits `/api/agent/*`.
- **Extension** (`web/src/ext/`): MV3. Chrome uses a service worker + an
  offscreen document to run the SIP engine (offscreen has no `chrome.storage`,
  so state is seeded via messaging — see `host.ts`/`engine.ts`). Firefox uses a
  persistent background page. Token (`Authorization: Bearer`) auth + CORS.

---

## 9. Auth & roles

- Console: username/password → bcrypt check → opaque session token in
  `tpbx_sessions` (cookie). Roles: `admin` > `manager` > `viewer`. Middleware
  `requireAuth`/`requireManager`/`requireAdmin` in `api/auth.go`.
- Initial admin: `tpbx create-admin` (install.sh runs it from
  `TPBX_ADMIN_USER`/`TPBX_ADMIN_PASSWORD`), idempotent.
- Agent softphone: separate auth — SIP extension + secret → token in
  `tpbx_agent_sessions` (`api/agent.go`). CORS-enabled for the extension.
- ARI + AMI are bound to `127.0.0.1` only. TURN static secret never reaches the
  browser — the backend mints short-lived HMAC credentials (`iceServers`).

---

## 10. Config (environment variables — `internal/config/config.go`)

All are `TPBX_*` with single-VM defaults; the installer writes them to
`/etc/tpbx/tpbx.env` (systemd `EnvironmentFile`):

`TPBX_HTTP_ADDR` (`:8080`), `TPBX_DATABASE_URL`, `TPBX_ASTERISK_CONF_DIR`
(`/etc/asterisk`), `TPBX_DIALPLAN_FILE`, `TPBX_TRANSPORTS_FILE`, `TPBX_DOMAIN`,
`TPBX_SOUNDS_DIR` (`/var/lib/asterisk/sounds/en/tpbx`), `TPBX_SOUNDS_PREFIX`
(`tpbx`), `TPBX_SIP_WSS_PORT` (`8089`), `TPBX_TURN_SECRET`,
`TPBX_ARI_URL/USER/PASS/APP`, `TPBX_AMI_ADDR/USER/PASS`, plus install-only
`TPBX_ADMIN_USER/PASSWORD`, `TPBX_WEB_DIR`, `TPBX_AGENT_WEB_DIR`.
`upgrade.sh` back-fills newly-added keys via `ensure_env_kv`.

---

## 11. Deployment & lifecycle

- **`install.sh`** (root, fresh Debian/Ubuntu): installs packages (postgres,
  asterisk, coturn, certbot, **ffmpeg**, fail2ban…), provisions Postgres + the
  app DB role, generates secrets → `/etc/tpbx/tpbx.env`, renders
  `asterisk/*.conf` templates into `/etc/asterisk` (substituting secrets),
  writes `modules.conf` with `noload = chan_sip.so`, provisions Let's Encrypt
  (if `TPBX_DOMAIN`) + coturn, creates the `tpbx` system user (member of
  `asterisk` group), **provisions the sounds dir** (`provision_sounds`, owned
  `tpbx:asterisk`, setgid 2775), seeds the dialplan, **`build_app`** (builds
  dashboard + agent + extension + packages the ext zip), runs migrations,
  creates the admin, installs the systemd unit, restarts the service then
  Asterisk, writes `/root/tpbx-credentials.txt`.
- **`upgrade.sh`** (in place): pull → back-fill env keys → `ensure_ffmpeg` →
  `provision_sounds` → `build_app` → `run_migrations` → restart. Safe to re-run.
- Shared logic lives in **`scripts/lib.sh`** (so install/upgrade define "build"
  and "migrate" once). Both source it.
- systemd unit: runs `tpbx serve` as user `tpbx`, `ProtectSystem=full`
  (hence generated files under `/var`, not `/etc`), `ProtectHome=true`.
- **nginx** (operator-provided, documented in README/TROUBLESHOOTING) terminates
  TLS and proxies: `/asterisk-ws` → `https://host:8089/ws` (WebRTC signalling,
  `proxy_ssl_verify off`, Upgrade headers), `/ws` → Go (dashboard events), `/` →
  Go `:8080`.

---

## 12. Conventions & workflow (follow these exactly)

- **Branch:** develop on the designated feature branch
  (`claude/asterisk-gui-stack-plan-lc1l65` for this line of work); create it from
  the default branch if missing; never push elsewhere without permission. If the
  branch's PR was already merged, restart from the latest default branch.
- **Verify before every commit:**
  `gofmt -l internal/ cmd/` (must be empty) → `go build ./...` → `go vet ./...`
  → in `web/`: `npm run build` (and `npm run build:agent` if the agent app
  changed, `npm run build:ext` for the extension). All must be clean.
- **Commit message footer** (every commit ends with):
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_...
  ```
- **Never** put the raw model identifier (e.g. `claude-opus-4-8`) in commits,
  code, PRs, or any pushed artifact — chat only.
- **No pull request** unless the user explicitly asks. When asked, mirror any
  `.github` PR template.
- **GitHub** operations go through the GitHub MCP tools (no `gh` CLI).
- Keep the Xelocorp ownership notice intact in the sidebar footer, generated
  config headers (dialplan/transports/env), credentials report, systemd unit,
  and `NOTICE`.
- Match surrounding code style; keep comments at the density of the file you're
  editing (this codebase comments the *why*, not the *what*).

---

## 13. Extension recipes (how to "develop the same way")

### 13.1 Add a whole new CRUD resource (end-to-end)
1. **Migration**: `migrations/00NN_thing.sql` creating `tpbx_things`.
2. **Store**: `internal/store/thing.go` — a `Things` type with
   `List/Get/Create/Update/Delete` (copy `trunks.go`).
3. **Server wiring**: add a `Things` field to `api.Server`; construct it in
   `cmd/tpbx/main.go`.
4. **Handlers**: add `handleListThings` etc. (copy an existing area) and mount
   routes in `Router()` under the right auth group. If it affects the dialplan,
   call `applyDialplan` after writes; if it affects PJSIP load-time config, call
   `reloadPJSIP` or set `RestartAsterisk`.
5. **API client**: add typed functions + interface in `web/src/api.ts`.
6. **UI**: new `web/src/components/Thing.tsx`; register in `App.tsx` `NAV` +
   the render switch (respect `roles` for admin/manager-only).
7. Verify + commit per §12.

### 13.2 Add a migration
Just drop `migrations/00NN_name.sql`. It's embedded automatically and applied on
next `tpbx migrate` (install/upgrade run it). Never edit an already-applied
migration — add a new one.

### 13.3 Add a nav page / rebrand / new event line
- Nav page: `NAV` array + render switch in `App.tsx`.
- Rebrand: logos in `web/src/assets/`, header in `App.tsx`, `theme.css`
  variables, HTML `<title>`s, extension manifests.
- Friendlier live event: add a `case` in `events.ts` `describeAMI`/`describeARI`
  (return `null` to hide noise).

---

## 14. Hard-won lessons / gotchas (the traps that cost real debugging)

1. **`chan_sip` hijacks WebRTC.** `chan_sip` grabs the `sip` WS subprotocol and
   rejects PJSIP WebRTC registrations ("Wrong password"). Fix: `noload =
   chan_sip.so` in `modules.conf`. This is set by the installer — keep it.
2. **`transport=transport-wss` breaks calls.** A ws/wss transport is created
   *dynamically* by `res_pjsip_transport_websocket`; it is NOT a named selectable
   transport. WebRTC endpoints must leave `transport` **NULL** (see
   `extensions.go` `withDefaults`). Pinning it → "Unable to retrieve PJSIP
   transport 'transport-wss'".
3. **IVR prompt plays in browser but is silent on calls.** Two causes, both
   fixed: (a) format — Asterisk `format_wav` only plays 8 kHz/16-bit mono PCM, so
   uploads are transcoded (ffmpeg/sox); (b) **language path** — `Background(tpbx/x)`
   resolves under `sounds/<language>/…` and our endpoints set no language, so
   Asterisk looked in the wrong dir → we reference prompts by **absolute path**
   (`resolveSound`). Also chmod 0644 so `asterisk` can read them.
4. **Native pgsql, not ODBC.** Realtime uses `res_config_pgsql`. Don't
   reintroduce ODBC modules; CDR/CEL use `cdr_pgsql`/`cel_pgsql`.
5. **Generated files go under `/var/lib/tpbx`,** never `/etc` — `ProtectSystem=full`
   makes `/etc` read-only for the service.
6. **Trunk changes need a PJSIP reload** (registrations/identifies are load-time);
   endpoint/AOR changes do not. **Transport bind changes need a full restart.**
7. **ARI RTP counters** come from `CHANNEL(rtpqos,audio,...)`; if empty the UI
   shows a neutral "media flowing" animation rather than a false "no audio".
8. **ps_contacts is ephemeral** — Asterisk deletes a contact on unregister, so
   "last seen" for offline devices comes from `tpbx_ext_presence`, upserted while
   a device is observed online.
9. **The agent extension offscreen doc has no `chrome.storage`** — seed engine
   state via messaging (`host.ts`), and request mic from the visible popup
   (background can't prompt).

---

## 15. Verification checklist (run before declaring done)

```
# backend
gofmt -l internal/ cmd/        # expect empty
go build ./...                 # expect clean
go vet ./...                   # expect clean

# frontend (in web/)
npm run build                  # dashboard
npm run build:agent            # if agent app touched
npm run build:ext              # if extension touched

# on a live box (optional but ideal)
sudo asterisk -rx "dialplan show tpbx-ivr-<name>"
sudo asterisk -rx "pjsip show endpoints"
```

---

## 16. Glossary

- **ARI** — Asterisk REST Interface (REST + Stasis event WebSocket). Used for
  live control (originate, hangup, reload, channel/endpoint state, RTP).
- **AMI** — Asterisk Manager Interface (TCP). Used for the event stream and
  `core restart`.
- **PJSIP realtime** — Asterisk reading SIP objects live from the DB (sorcery +
  `res_config_pgsql`).
- **Dialplan** — Asterisk's call-flow language; XeloVoice generates the routing +
  IVR contexts into an include file.
- **Sorcery** — Asterisk's object abstraction layer; `sorcery.conf` maps object
  types to the pgsql backend.
- **Endpoint / AOR / Auth / Contact** — the PJSIP objects that make up one SIP
  account.
- **Trunk** — a connection to an upstream SIP provider (register or IP mode).
- **IVR** — auto-attendant menu ("press 1 for sales…").
- **CDR / CEL** — Call Detail Records / Channel Event Logging (analytics source).
- **TURN/STUN/coturn** — NAT traversal for WebRTC media; coturn is the server,
  the backend mints short-lived credentials.

---

---

## 17. RBAC — custom roles & per-feature permissions (`store/roles.go`)

Roles are data, not code. `tpbx_roles.permissions` is a JSON map of
`feature → {view,create,edit,delete}` over the nine features in
`store.Features` (extensions, trunks, routing, ivr, cdr, analytics, transports,
settings, users). The `admin` role is `built_in` and always granted everything
in code — it cannot be edited or deleted.

- **Backend enforcement:** `Server.requirePerm(feature)` (in `api/auth.go`)
  wraps each feature's route group; the action is derived from the HTTP method
  (GET=view, POST=create, PUT/PATCH=edit, DELETE=delete). `Roles.Can()` does the
  check (admin bypasses). `/roles` CRUD is **admin-only** (`requireAdmin`).
  Dashboard control routes (status/originate/hangup/reload) stay open to any
  authenticated user.
- **Identity:** `/me` and login return `{username, role, permissions,
  totpEnabled, totpSetupRequired}` via `Server.mePayload`.
- **Frontend:** `api.ts` `can(me, feature, action)` gates nav (App.tsx) and
  every create/edit/delete button in the feature pages. The admin **Users &
  Roles** page (`Users.tsx`) edits users (role/display-name/disabled) and, for
  admins, has a Roles editor with the feature×action permission matrix and a
  per-role "require 2FA" toggle.
- **To add a feature to the matrix:** append it to `store.Features`, add a
  `requirePerm("x")` group in `Router()`, add it to the `Feature` union in
  `api.ts`, and give existing roles the permission in a new migration.

## 18. TOTP two-factor (`store/totp.go`, `api/totp.go`)

Dependency-free RFC 6238 (HMAC-SHA1, 6 digits, 30 s, ±1 step drift). Secret is
base32 in `tpbx_users.totp_secret`; `totp_enabled` flips true only after the
user proves a code.

- **Login is two-step:** password first; if the account has 2FA the server
  replies `{totpRequired:true}` (no session) and the next request must carry a
  valid `totpCode`.
- **Per-role mandate:** if the user's role has `require_totp` and they have not
  enrolled, `requireAuth` fences them to the enrolment endpoints
  (`totpSetupAllowed`) and `/me` reports `totpSetupRequired`; the frontend forces
  the `TotpSetup` screen (QR via the `qrcode` dep + manual key) before the
  console loads. A mandated role cannot self-disable 2FA.
- **Endpoints:** self-service `/totp/enroll|activate|disable`; admin recovery
  `/users/{u}/totp/reset` (clears a lost device). Users page has a self-service
  Two-factor panel, a 2FA column, and an admin Reset-2FA action.

## 19. Global PJSIP / TLS settings (`store/pjsip_settings.go`)

A **third generated include** joins the dialplan and transports:
`TPBX_PJSIP_FILE` = `/var/lib/tpbx/pjsip_globals.conf`, `#include`d by
`pjsip.conf` (added by install.sh; back-filled by upgrade.sh). It holds the
res_pjsip `[global]` options (debug, keep_alive_interval,
endpoint_identifier_order, taskprocessor_overload_trigger). Settings live in the
singleton row `tpbx_pjsip_settings`.

- **TLS defaults** (method, verify_client, verify_server) are **not** in the
  globals file — they feed `Transports.GenerateConfig(ctx, TLSDefaults)`, which
  applies them to TLS transports that don't override them. So saving PJSIP
  settings regenerates **both** includes then reloads PJSIP
  (`api/transports.go` `applyPJSIPGlobals` → `applyTransports`).
- **API:** GET/PUT `/pjsip/settings`, gated under the **transports** feature.
- **UI:** the `PJSIPPanel` on the Transports/TLS page (`Transports.tsx`) —
  "Misc PJSip Settings" + "TLS/SSL/SRTP Settings" with Yes/No + segmented
  toggles, mirroring the reference UI.
- **Startup:** `main.regeneratePJSIPGlobals` + `regenerateTransports` (now takes
  the TLS defaults) run on boot, best-effort.

## 20. Leads & lists (`store/lists.go`, `store/leads.go`, `api/leads.go`)

Phase 1 of the ViciDial parity plan (`docs/VICIDIAL_PARITY.md`): the CRM half of
a dialer. A **list** is a batch of leads loaded for one purpose; a **lead** is a
person to call. Nothing dials them yet — campaigns (P2) and the dialer engine
(P4) are what put these rows to work.

- **Schema (0026).** `tpbx_lists` (name, campaign_id, active, expiry, a JSONB
  `custom_fields` schema) and `tpbx_leads` (dial state, three phone numbers,
  contact detail, provenance, `gmt_offset`, a JSONB `custom` bag), with
  `ON DELETE CASCADE` from list to lead.
- **Two deliberate forward references**, plain columns rather than foreign keys
  because their targets do not exist yet: `tpbx_lists.campaign_id` →
  `tpbx_campaigns` (P2) and `tpbx_leads.status` → `tpbx_dispositions` (P2).
- **Phone numbers are normalised on the way in** by `store.CheckPhoneNumber`:
  formatting is stripped to digits, 6-18 digits accepted. The same function must
  be used by manual dial and (from P4) hopper fill, so it lives in the store,
  not a handler.
- **Duplicate scopes** on create/import: `none` | `list` | `campaign` | `system`,
  with an optional day window. A duplicate returns `store.ErrDuplicate` → 409.
- **Custom fields are schema'd per list.** Values not declared by the list are
  dropped on write, so a stray import column can't bloat every row. Removing a
  field from the schema hides it but does not destroy stored values.
- **Deleting a list is guarded**: the client must send the lead count it showed
  the operator (`?leads=N`) and the store refuses if the list has since grown —
  a cascade delete can never destroy more than was confirmed.
- **Import** is header-driven, not positional (`Leads.tsx` `parseLeadCSV`):
  the header names the columns in any order, aliases are generous
  (`phone`/`mobile`/`msisdn`…), columns matching the list's custom fields are
  carried into them, and unmatched columns are reported rather than dropped
  silently. Rows are attempted independently, as with the extensions bulk upload.
- **UI:** `web/src/components/Leads.tsx` — a lists table over a filtered,
  paged lead browser with multi-select bulk status changes.

## 21. Campaigns, dispositions, agents (`store/campaigns.go`, `store/agent_accounts.go`, `store/leadcalls.go`, `api/campaigns.go`)

Phase 2 of the parity plan: the object the dialer hangs off, the vocabularies it
owns, agents as people, and agent-paced dialing.

- **A campaign** (`tpbx_campaigns`) carries a short dialplan-safe `code` (the
  key everything references — **immutable** after creation, since lists, agents
  and call records point at it) and a `name`. Its pacing fields (dial method,
  level, adaptive max, hopper level, drop-rate ceiling) are stored and validated
  now but **read by nobody until the P4 dialer**; `AutomaticDialing()` tells the
  console to label those campaigns "waiting for dialer" rather than let an
  operator think RATIO is placing calls.
- **The drop-rate ceiling is enforced as a limit, not a target**: values above
  10% are refused at the store, because it is a regulatory cap on abandoned
  calls that the P4 pacing loop must stay under.
- **`outbound_cid` is normalised to digits** by `CheckPhoneNumber` before
  storage — it goes straight into Asterisk's `CALLERID`, where brackets and
  spaces are wrong rather than decorative.
- **Dispositions and pause codes** (`tpbx_dispositions`, `tpbx_pause_codes`) are
  campaign-scoped with a **system-wide fallback** (`campaign_id IS NULL`),
  enforced by two partial unique indexes because NULLs do not collide in a plain
  UNIQUE. Migration 0027 seeds the system set to match `store.SystemStatuses`,
  so leads written in P1 line up. System rows cannot be deleted — a campaign
  overrides one by defining its own row with the same code.
  The semantic flags (`dnc`, `callback`, `recycle_after_sec`) are recorded here
  and *acted on* by P6 and P4; nothing half-implements them in P2.
- **Agents are now people** (`tpbx_agents`), not extensions — the split
  recommended in the parity plan. The extension is the device binding, indexed
  so `ByExtension` can resolve it. **Softphone auth is deliberately unchanged**:
  the web, desktop and Android clients still log in with extension + SIP secret
  (`store/agents.go`, which is about *sessions*); P3 moves that onto this table.
- **Dialing is agent-paced.** `PUT /leads/{id}/dial` originates to the agent's
  own extension with `context=from-internal, extension=<lead number>`, so the
  call goes out over the **outbound routes that already exist** — no
  dialer-specific dialplan. The number is re-validated at dial time, because a
  lead can be edited after import.
- **`tpbx_lead_calls` is one row per attempt.** `Start` bumps the lead's
  counters; `Apply` stamps the disposition and writes the status back to the
  lead, keeping the previous one in `last_status`. Dispositioning a lead with no
  attempt on record (an agent marking up a call made another way) records a
  completed attempt rather than losing the outcome. P4's dialer writes the same
  rows, which is why the shape carries channel/timing detail manual dialing
  leaves empty.
- **`NextPreviewLead`** is a deliberate stand-in for the P4 hopper: it selects
  one dialable lead on demand rather than maintaining a queue, and applies no
  call-time or DNC rules (those are P6). Preview dialing is agent-paced, so a
  human sees every number first.

## 22. The agent desktop (`store/agentwork.go`, `api/agentdesk.go`, `web/src/agent/AgentDesk.tsx`)

Phase 3: the softphone stops being only a phone. `AgentDesk` renders *alongside*
the dialer, and returns `null` when the agent works no campaigns — a deployment
that does not use campaigns sees exactly the softphone it had before.

- **Softphone authentication did not change.** Web, desktop and Android still
  sign in with a SIP extension and its secret. What changed is that the login
  now resolves to a `tpbx_agents` row, minting one on first sight
  (`AgentAccounts.EnsureForExtension`), so an agent gains a persistent identity
  with no client change. Provisioning is best-effort: a phone that can register
  must never be blocked because the desk features could not be set up.
- **`ByExtension` does not filter on `active`** — deliberately. Resolution
  answers "who is this device?", and a disabled agent still has an answer.
  Filtering there made a disabled account look absent, so provisioning tried to
  recreate it and the agent was told their *extension already existed* rather
  than that their account was disabled. Policy lives in `Server.agentAccount`,
  which returns a 403 saying so.
- **State is in the database** (`tpbx_agent_state`), not in memory: a shift
  outlives a browser tab, a reconnecting softphone must land back where it was,
  and the supervisor board (P8) reads the same row the agent sees. Agents start
  **paused** — opening the app must never be enough to be handed a call.
- **`tpbx_agent_log` is append-only.** Pairing consecutive rows gives
  login/pause/talk durations, which is what P8's agent reporting is built from.
  `handleAgentLogout` resolves the agent **from the token**, not the request
  context: logout sits outside `requireAgent` (signing out must work with a
  session the server no longer likes), so the context carries nothing there.
- **Callbacks** (`tpbx_callbacks`) give the CALLBK disposition somewhere to
  land. `recipient` separates ViciDial's two kinds: `ANYONE` returns the
  callback to the campaign, `USERONLY` reserves it for the agent who promised
  it — the difference between "we'll call you back" and "I'll call you back".
  A time in the past is refused; a callback disposition with no time reports
  that rather than dropping the promise; dispositioning a lead closes its
  pending callbacks so a kept promise stops resurfacing.
- **In-call edits are allowlisted.** `Leads.UpdateFields` accepts contact detail
  only — never the list, dial state or provenance — and merges custom values
  (`custom || $n`) rather than replacing, so editing one field cannot wipe the
  ones the agent was not shown. Unknown keys are ignored rather than rejected,
  so a client that knows a field this server does not cannot fail the save.
- **Alt-phone dialing** is why a lead carries three numbers: `altPhone: "alt"`
  or `"alt2"` dials those instead of the primary, each re-validated at dial time.
- **Scripts** substitute `{firstName}`-style tokens from the lead. Unknown
  tokens are left visible: a script with a typo should look wrong, not silently
  read as a gap.

A note on `callerIDName` (`store/agents.go`): it now understands the bare
`Alice <1001>` form as well as the quoted one. The console's own Extensions
form suggests the bare spelling, so softphones had been greeting agents by
extension number instead of by name — and phase 3 would have persisted that
into the agent record.

## Console version & header

The top bar shows `APP_VERSION` (in `api.ts`, currently `V1.6`) instead of the
raw PBX engine build; bump it per release. The center header text was removed
and the login screen uses the light-theme wordmark.

---

*End of deep index. If something here disagrees with the code, the code wins —
then fix this file.*
