// API + live-event client for the TPBX console.
//
// REST calls hit the Go backend under /api; the live event stream is a single
// WebSocket at /ws that fans out normalised AMI/ARI events.

// APP_VERSION is the XeloVoice console release, shown in the top bar. Bump this
// on each meaningful release (it is deliberately independent of the underlying
// PBX engine version, which is not surfaced to operators).
export const APP_VERSION = "V1.6";

export interface Endpoint {
  technology: string;
  resource: string;
  state: string;
  channel_ids: string[] | null;
}

export interface Channel {
  id: string;
  name: string;
  state: string;
  caller: { name: string; number: string };
  connected: { name: string; number: string };
  creationtime: string;
}

export interface Status {
  time: string;
  endpoints?: Endpoint[];
  channels?: Channel[];
  endpoints_error?: string;
  channels_error?: string;
}

export interface Health {
  status: string;
  database: string;
  time: string;
}

export interface WsEnvelope {
  kind: "hello" | "ari" | "ami";
  data: any;
}

export async function getStatus(): Promise<Status> {
  const r = await fetch("/api/status");
  if (!r.ok) throw new Error(`status ${r.status}`);
  return r.json();
}

export async function getHealth(): Promise<Health> {
  const r = await fetch("/api/health");
  if (!r.ok) throw new Error(`health ${r.status}`);
  return r.json();
}

export interface AsteriskInfo {
  system?: { version?: string; entity_id?: string };
  status?: { startup_time?: string; last_reload_time?: string };
}

export async function getAsteriskInfo(): Promise<AsteriskInfo> {
  const r = await fetch("/api/asterisk/info");
  if (!r.ok) throw new Error(`info ${r.status}`);
  return r.json();
}

// jsonPost/jsonDelete throw an Error carrying the backend error message so the
// UI can surface exactly why a control action failed.
async function request(method: string, url: string, body?: unknown): Promise<any> {
  const r = await fetch(url, {
    method,
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data?.error || `${method} ${url} failed (${r.status})`);
  return data;
}

export interface OriginateInput {
  endpoint: string;
  extension: string;
  context: string;
  callerId?: string;
}

export function originate(input: OriginateInput): Promise<any> {
  return request("POST", "/api/originate", input);
}

export function hangup(channelId: string): Promise<any> {
  return request("DELETE", `/api/channels/${encodeURIComponent(channelId)}`);
}

export function reloadModule(module: string): Promise<any> {
  return request("POST", "/api/reload", { module });
}

// --- Extensions (Phase 3) ---------------------------------------------------

export interface Extension {
  id: string;
  password?: string;
  context: string;
  transport: string;
  codecs: string;
  callerId: string;
  maxContacts: number;
  webrtc: boolean;
  dtmfMode: string;
}

export async function listExtensions(): Promise<Extension[]> {
  const r = await fetch("/api/extensions");
  if (!r.ok) throw new Error(`extensions ${r.status}`);
  const data = await r.json();
  return data.extensions ?? [];
}

export async function getExtension(id: string): Promise<Extension> {
  return request("GET", `/api/extensions/${encodeURIComponent(id)}`);
}

export function createExtension(e: Partial<Extension>): Promise<any> {
  return request("POST", "/api/extensions", e);
}

export function updateExtension(id: string, e: Partial<Extension>): Promise<any> {
  return request("PUT", `/api/extensions/${encodeURIComponent(id)}`, e);
}

export function deleteExtension(id: string): Promise<any> {
  return request("DELETE", `/api/extensions/${encodeURIComponent(id)}`);
}

// Live registration state of an extension (presence dot + device illustration).
export interface ExtStatus {
  online: boolean;
  ip?: string;
  port?: number;
  userAgent?: string;
  device: "mobile" | "web" | "desk" | "none";
  lastSeen?: string; // ISO time it was last seen registered (offline only)
}

export async function getExtensionStatus(): Promise<Record<string, ExtStatus>> {
  const r = await fetch("/api/extensions/status");
  if (!r.ok) throw new Error(`ext status ${r.status}`);
  return (await r.json()).status ?? {};
}

// resetExtensionPassword sets (or, with no password, generates) a new SIP
// secret and returns the value actually applied.
export async function resetExtensionPassword(
  id: string,
  password?: string
): Promise<{ password: string }> {
  return request("POST", `/api/extensions/${encodeURIComponent(id)}/password`, {
    password: password ?? "",
  });
}

export interface BulkResult {
  created: number;
  results: { id: string; ok: boolean; error?: string }[];
}

export function bulkCreateExtensions(extensions: Partial<Extension>[]): Promise<BulkResult> {
  return request("POST", "/api/extensions/bulk", { extensions });
}

// --- Trunks (Phase 4) -------------------------------------------------------

export interface Trunk {
  name: string;
  mode: "register" | "ip";
  host: string;
  port: number;
  username: string;
  password?: string;
  fromUser: string;
  fromDomain: string;
  context: string;
  transport: string;
  codecs: string;
  // state is live reachability from Asterisk (online/offline/unknown),
  // populated on list responses only; not part of the stored trunk.
  state?: string;
}

export async function listTrunks(): Promise<Trunk[]> {
  const r = await fetch("/api/trunks");
  if (!r.ok) throw new Error(`trunks ${r.status}`);
  const data = await r.json();
  return data.trunks ?? [];
}

export async function getTrunk(id: string): Promise<Trunk> {
  return request("GET", `/api/trunks/${encodeURIComponent(id)}`);
}

export function createTrunk(t: Partial<Trunk>): Promise<any> {
  return request("POST", "/api/trunks", t);
}

export function updateTrunk(id: string, t: Partial<Trunk>): Promise<any> {
  return request("PUT", `/api/trunks/${encodeURIComponent(id)}`, t);
}

export function deleteTrunk(id: string): Promise<any> {
  return request("DELETE", `/api/trunks/${encodeURIComponent(id)}`);
}

// --- Routing (Phase 5) ------------------------------------------------------

export interface OutboundRoute {
  id: number;
  name: string;
  pattern: string;
  destType: "trunk" | "ivr";
  trunk: string;
  ivr: string;
  strip: number;
  prepend: string;
  callerId: string;
  position: number;
  enabled: boolean;
}

export interface InboundRoute {
  id: number;
  name: string;
  did: string;
  destination: string;
  enabled: boolean;
}

export async function listOutboundRoutes(): Promise<OutboundRoute[]> {
  const r = await fetch("/api/routes/outbound");
  if (!r.ok) throw new Error(`outbound ${r.status}`);
  return (await r.json()).routes ?? [];
}
export function createOutboundRoute(r: Partial<OutboundRoute>): Promise<any> {
  return request("POST", "/api/routes/outbound", r);
}
export function updateOutboundRoute(id: number, r: Partial<OutboundRoute>): Promise<any> {
  return request("PUT", `/api/routes/outbound/${id}`, r);
}
export function deleteOutboundRoute(id: number): Promise<any> {
  return request("DELETE", `/api/routes/outbound/${id}`);
}

export async function listInboundRoutes(): Promise<InboundRoute[]> {
  const r = await fetch("/api/routes/inbound");
  if (!r.ok) throw new Error(`inbound ${r.status}`);
  return (await r.json()).routes ?? [];
}
export function createInboundRoute(r: Partial<InboundRoute>): Promise<any> {
  return request("POST", "/api/routes/inbound", r);
}
export function updateInboundRoute(id: number, r: Partial<InboundRoute>): Promise<any> {
  return request("PUT", `/api/routes/inbound/${id}`, r);
}
export function deleteInboundRoute(id: number): Promise<any> {
  return request("DELETE", `/api/routes/inbound/${id}`);
}

// --- Lead lists & leads (ViciDial parity, phase 1) ---------------------------

export interface CustomField {
  name: string;
  label: string;
  type: "text" | "number" | "date" | "select";
  options?: string[];
}

export interface LeadList {
  id: number;
  name: string;
  description: string;
  campaignId: number | null; // the campaign that dials this list; null = unassigned
  campaignCode?: string;     // joined for display
  active: boolean;
  expiresOn: string; // YYYY-MM-DD, "" = never
  customFields: CustomField[];
  leadCount: number;
  statusCount?: Record<string, number>;
}

export interface Lead {
  id: number;
  listId: number;
  status: string;
  calledCount: number;
  lastCalledAt?: string;
  lastStatus?: string;
  phoneCode: string;
  phoneNumber: string;
  altPhone: string;
  altPhoneTwo: string;
  title: string;
  firstName: string;
  lastName: string;
  email: string;
  address1: string;
  address2: string;
  city: string;
  state: string;
  postalCode: string;
  country: string;
  comments: string;
  vendorLeadCode: string;
  sourceId: string;
  owner: string;
  gmtOffset?: number;
  custom?: Record<string, unknown>;
  listName?: string;
}

// DupScope decides how widely an import looks for an existing copy of a lead:
// within the list, across the campaign's lists, anywhere, or not at all.
export type DupScope = "none" | "list" | "campaign" | "system";

export interface LeadQuery {
  listId?: number;
  status?: string;
  phone?: string;
  name?: string;
  owner?: string;
  since?: string;
  until?: string;
  limit?: number;
  offset?: number;
  order?: "newest" | "oldest";
}

export interface LeadPage {
  leads: Lead[];
  total: number;
  offset: number;
}

export interface LeadImportRow {
  row: number;
  id?: number;
  phone: string;
  ok: boolean;
  error?: string;
}

export interface LeadImportResult {
  created: number;
  duplicates: number;
  failed: number;
  results: LeadImportRow[];
}

export async function listLeadLists(): Promise<LeadList[]> {
  const r = await fetch("/api/lists");
  if (!r.ok) throw new Error(`lists ${r.status}`);
  const data = await r.json();
  return data.lists ?? [];
}

export async function getLeadList(id: number): Promise<LeadList> {
  const r = await fetch(`/api/lists/${id}`);
  if (!r.ok) throw new Error(`list ${r.status}`);
  return r.json();
}

export function createLeadList(l: Partial<LeadList>): Promise<LeadList> {
  return request("POST", "/api/lists", l);
}

export function updateLeadList(id: number, l: Partial<LeadList>): Promise<any> {
  return request("PUT", `/api/lists/${id}`, l);
}

// leadCount is echoed back as a confirmation: the server refuses the delete if
// the list has grown since it was displayed, so a delete never destroys more
// leads than the operator was shown.
export function deleteLeadList(id: number, leadCount: number): Promise<any> {
  return request("DELETE", `/api/lists/${id}?leads=${leadCount}`);
}

export function resetLeadList(id: number): Promise<{ leads: number }> {
  return request("PUT", `/api/lists/${id}/reset`);
}

export async function searchLeads(q: LeadQuery): Promise<LeadPage> {
  const p = new URLSearchParams();
  if (q.listId) p.set("listId", String(q.listId));
  if (q.status) p.set("status", q.status);
  if (q.phone) p.set("phone", q.phone);
  if (q.name) p.set("name", q.name);
  if (q.owner) p.set("owner", q.owner);
  if (q.since) p.set("since", q.since);
  if (q.until) p.set("until", q.until);
  if (q.limit) p.set("limit", String(q.limit));
  if (q.offset) p.set("offset", String(q.offset));
  if (q.order) p.set("order", q.order);
  const r = await fetch(`/api/leads?${p.toString()}`);
  if (!r.ok) {
    const data = await r.json().catch(() => ({}));
    throw new Error(data?.error || `leads ${r.status}`);
  }
  return r.json();
}

export async function getLeadStatuses(): Promise<string[]> {
  const r = await fetch("/api/leads/statuses");
  if (!r.ok) throw new Error(`statuses ${r.status}`);
  const data = await r.json();
  return data.statuses ?? [];
}

export async function getLead(id: number): Promise<Lead> {
  const r = await fetch(`/api/leads/${id}`);
  if (!r.ok) throw new Error(`lead ${r.status}`);
  return r.json();
}

export function createLead(
  lead: Partial<Lead>,
  dupScope: DupScope = "list"
): Promise<Lead> {
  return request("POST", "/api/leads", { ...lead, dupScope });
}

export function updateLead(id: number, lead: Partial<Lead>): Promise<any> {
  return request("PUT", `/api/leads/${id}`, lead);
}

export function deleteLead(id: number): Promise<any> {
  return request("DELETE", `/api/leads/${id}`);
}

export function setLeadStatus(ids: number[], status: string): Promise<{ updated: number }> {
  return request("PUT", "/api/leads/status", { ids, status });
}

export function importLeads(
  listId: number,
  leads: Partial<Lead>[],
  dupScope: DupScope,
  dupDays = 0
): Promise<LeadImportResult> {
  return request("POST", "/api/leads/bulk", { listId, leads, dupScope, dupDays });
}

// --- Campaigns, dispositions, pause codes, agents (parity phase 2) -----------

export interface Campaign {
  id: number;
  code: string;
  name: string;
  description: string;
  active: boolean;

  dialMethod: string;
  dialLevel: number;
  adaptiveMax: number;
  hopperLevel: number;
  dialTimeout: number;
  leadOrder: string;
  dialStatuses: string[];
  dropRateTarget: number;
  amdEnabled: boolean;

  outboundCid: string;
  trunk: string;
  wrapupSeconds: number;
  script: string;

  listCount: number;
  leadCount: number;
  agentCount: number;
}

// automaticDialing reports whether a dial method needs the phase-4 engine. The
// UI uses it to say "waiting for the dialer" rather than letting an operator
// believe RATIO is already placing calls.
export function automaticDialing(method: string): boolean {
  return method !== "MANUAL" && method !== "PREVIEW" && method !== "";
}

export interface Disposition {
  id: number;
  campaignId: number | null; // null = system-wide
  code: string;
  name: string;
  selectable: boolean;
  humanAnswered: boolean;
  isSale: boolean;
  notInterested: boolean;
  dnc: boolean;
  callback: boolean;
  recycleAfterSec: number;
  position: number;
}

export interface PauseCode {
  id: number;
  campaignId: number | null;
  code: string;
  name: string;
  billable: boolean;
  position: number;
}

export interface AgentAccount {
  id: number;
  username: string;
  displayName: string;
  extension: string;
  active: boolean;
  campaigns: number[];
  campaignCodes?: string[];
}

export interface LeadCall {
  id: number;
  leadId: number;
  campaignId?: number;
  agent: string;
  extension: string;
  direction: string;
  channelId?: string;
  dialed: string;
  startedAt: string;
  endedAt?: string;
  status?: string;
  note?: string;
}

export interface CampaignsPayload {
  campaigns: Campaign[];
  dialMethods: string[];
  leadOrders: string[];
}

export async function listCampaigns(): Promise<CampaignsPayload> {
  const r = await fetch("/api/campaigns");
  if (!r.ok) throw new Error(`campaigns ${r.status}`);
  return r.json();
}

export function createCampaign(c: Partial<Campaign>): Promise<Campaign> {
  return request("POST", "/api/campaigns", c);
}

export function updateCampaign(id: number, c: Partial<Campaign>): Promise<any> {
  return request("PUT", `/api/campaigns/${id}`, c);
}

export function deleteCampaign(id: number): Promise<any> {
  return request("DELETE", `/api/campaigns/${id}`);
}

export async function listDispositions(campaignId = 0): Promise<Disposition[]> {
  const r = await fetch(`/api/dispositions?campaign=${campaignId}`);
  if (!r.ok) throw new Error(`dispositions ${r.status}`);
  return (await r.json()).dispositions ?? [];
}

export function saveDisposition(d: Partial<Disposition>): Promise<Disposition> {
  return request(d.id ? "PUT" : "POST", "/api/dispositions", d);
}

export function deleteDisposition(id: number): Promise<any> {
  return request("DELETE", `/api/dispositions/${id}`);
}

export async function listPauseCodes(campaignId = 0): Promise<PauseCode[]> {
  const r = await fetch(`/api/pause-codes?campaign=${campaignId}`);
  if (!r.ok) throw new Error(`pause codes ${r.status}`);
  return (await r.json()).pauseCodes ?? [];
}

export function savePauseCode(p: Partial<PauseCode>): Promise<PauseCode> {
  return request(p.id ? "PUT" : "POST", "/api/pause-codes", p);
}

export function deletePauseCode(id: number): Promise<any> {
  return request("DELETE", `/api/pause-codes/${id}`);
}

export async function listAgentAccounts(): Promise<AgentAccount[]> {
  const r = await fetch("/api/agents");
  if (!r.ok) throw new Error(`agents ${r.status}`);
  return (await r.json()).agents ?? [];
}

export function createAgentAccount(a: Partial<AgentAccount>): Promise<AgentAccount> {
  return request("POST", "/api/agents", a);
}

export function updateAgentAccount(id: number, a: Partial<AgentAccount>): Promise<any> {
  return request("PUT", `/api/agents/${id}`, a);
}

export function deleteAgentAccount(id: number): Promise<any> {
  return request("DELETE", `/api/agents/${id}`);
}

// dialLead rings the agent's own extension first; when they answer, Asterisk
// dials the lead through the configured outbound routes.
export function dialLead(
  leadId: number,
  input: { extension: string; campaignId?: number; agent?: string }
): Promise<{ dialed: string; call?: LeadCall; logError?: string }> {
  return request("PUT", `/api/leads/${leadId}/dial`, input);
}

export function dispositionLead(
  leadId: number,
  input: { status: string; note?: string; callId?: number; campaignId?: number }
): Promise<LeadCall> {
  return request("PUT", `/api/leads/${leadId}/disposition`, input);
}

export async function listLeadCalls(leadId: number): Promise<LeadCall[]> {
  const r = await fetch(`/api/leads/${leadId}/calls`);
  if (!r.ok) throw new Error(`calls ${r.status}`);
  return (await r.json()).calls ?? [];
}

// nextPreviewLead hands back the next lead the campaign would dial, so the
// agent can look at it before deciding to place the call.
export async function nextPreviewLead(campaignId: number): Promise<{ lead: Lead | null; note?: string }> {
  const r = await fetch(`/api/campaigns/${campaignId}/next-lead`);
  if (!r.ok) throw new Error(`next lead ${r.status}`);
  return r.json();
}

// --- Transports / TLS -------------------------------------------------------

export interface Transport {
  name: string;
  protocol: "udp" | "tcp" | "tls" | "wss";
  bindAddr: string;
  bindPort: number;
  tlsCertFile: string;
  tlsPrivKeyFile: string;
  tlsCaListFile: string;
  tlsMethod: string;
  externalMediaAddress: string;
  externalSignalingAddress: string;
  localNet: string;
  enabled: boolean;
  position: number;
}

export async function listTransports(): Promise<Transport[]> {
  const r = await fetch("/api/transports");
  if (!r.ok) throw new Error(`transports ${r.status}`);
  return (await r.json()).transports ?? [];
}
export async function getTransport(name: string): Promise<Transport> {
  return request("GET", `/api/transports/${encodeURIComponent(name)}`);
}
export function createTransport(t: Partial<Transport>): Promise<any> {
  return request("POST", "/api/transports", t);
}
export function updateTransport(name: string, t: Partial<Transport>): Promise<any> {
  return request("PUT", `/api/transports/${encodeURIComponent(name)}`, t);
}
export function deleteTransport(name: string): Promise<any> {
  return request("DELETE", `/api/transports/${encodeURIComponent(name)}`);
}
export function restartAsterisk(): Promise<any> {
  return request("POST", "/api/asterisk/restart");
}

// --- Global PJSIP / TLS settings (Misc PJSip + TLS/SSL/SRTP panels) ----------

export interface PJSIPSettings {
  allowTransportsReload: boolean;
  enableDebug: boolean;
  keepAliveInterval: number;
  contactCallerId: boolean;
  taskprocessorOverloadTrigger: "global" | "pjsip_only" | "none";
  endpointIdentifierOrder: string; // csv, e.g. "ip,username,anonymous"
  certName: string;
  tlsMethod: string;
  verifyClient: boolean;
  verifyServer: boolean;
}

export async function getPJSIPSettings(): Promise<PJSIPSettings> {
  const r = await fetch("/api/pjsip/settings");
  if (!r.ok) throw new Error(`pjsip settings ${r.status}`);
  return (await r.json()).settings;
}
export function savePJSIPSettings(s: PJSIPSettings): Promise<any> {
  return request("PUT", "/api/pjsip/settings", s);
}

// --- RTP stats (per channel) ------------------------------------------------

export interface RTPStat {
  rx: number; // packets received from the peer (peer is sending audio)
  tx: number; // packets sent to the peer (peer is receiving audio)
  known?: boolean; // false when Asterisk reported no RTP data at all
  raw?: string; // underlying QoS string, for diagnostics
}

export async function getRTP(): Promise<Record<string, RTPStat>> {
  const r = await fetch("/api/rtp");
  if (!r.ok) throw new Error(`rtp ${r.status}`);
  return (await r.json()).rtp ?? {};
}

// --- IVR / auto-attendant ---------------------------------------------------

export type IVRDestType =
  | "extension"
  | "ivr"
  | "voicemail"
  | "playback"
  | "external"
  | "queue"
  | "repeat"
  | "hangup";

export interface IVROption {
  digit: string;
  destType: IVRDestType;
  destValue: string;
  label: string;
}
export interface IVR {
  id: number;
  name: string;
  greeting: string;
  timeoutSec: number;
  maxRetries: number;
  invalidDest: string;
  timeoutDest: string;
  layout?: string; // opaque JSON for the visual builder canvas
  options: IVROption[];
}

export async function listIVRs(): Promise<IVR[]> {
  const r = await fetch("/api/ivrs");
  if (!r.ok) throw new Error(`ivrs ${r.status}`);
  return (await r.json()).ivrs ?? [];
}
export function getIVR(id: number): Promise<IVR> {
  return request("GET", `/api/ivrs/${id}`);
}
export function createIVR(v: Partial<IVR>): Promise<any> {
  return request("POST", "/api/ivrs", v);
}
export function updateIVR(id: number, v: Partial<IVR>): Promise<any> {
  return request("PUT", `/api/ivrs/${id}`, v);
}
export function deleteIVR(id: number): Promise<any> {
  return request("DELETE", `/api/ivrs/${id}`);
}

// --- IVR prompt library (uploaded .wav files) -------------------------------

export interface SoundFile {
  name: string; // bare name, no extension
  ref: string; // dialplan reference, e.g. "tpbx/welcome"
  file: string; // on-disk filename
  size: number;
  modified: string;
}

export interface SoundsResponse {
  sounds: SoundFile[];
  prefix: string;
  configured: boolean;
}

export async function listSounds(): Promise<SoundsResponse> {
  const r = await fetch("/api/sounds");
  if (!r.ok) throw new Error(`sounds ${r.status}`);
  return r.json();
}

export async function uploadSound(
  file: File,
  name?: string
): Promise<{ name: string; ref: string; note?: string }> {
  const fd = new FormData();
  fd.append("file", file);
  if (name) fd.append("name", name);
  const r = await fetch("/api/sounds", { method: "POST", body: fd });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data.error || `upload ${r.status}`);
  return data;
}

export function deleteSound(name: string): Promise<any> {
  return request("DELETE", `/api/sounds/${encodeURIComponent(name)}`);
}

export function soundAudioUrl(name: string): string {
  return `/api/sounds/${encodeURIComponent(name)}/audio`;
}

// --- WebRTC / TURN settings (admin) -----------------------------------------

export interface WebRTCSettings {
  publicHost: string;
  wssPort: string;
  wssUrl: string;
  stunEnabled: boolean;
  stunUrls: string;
  turnEnabled: boolean;
  turnMode: "builtin" | "static" | "none";
  turnHost: string;
  turnUrls: string;
  turnStaticUser: string;
  turnStaticPassword: string;
  turnTls: boolean;
  iceTransportPolicy: "all" | "relay";
}

export interface WebRTCSettingsResponse {
  settings: WebRTCSettings;
  builtinReady: boolean;
}

export async function getWebRTCSettings(): Promise<WebRTCSettingsResponse> {
  const r = await fetch("/api/settings/webrtc");
  if (!r.ok) throw new Error(`settings ${r.status}`);
  return r.json();
}
export function saveWebRTCSettings(s: WebRTCSettings): Promise<any> {
  return request("PUT", "/api/settings/webrtc", s);
}

// --- System / Branding settings (admin) -------------------------------------

export interface SystemSettings {
  publicDomain: string; // FQDN/IP agents reach; "" = derive from request host
  brandName: string; // shown in the console title / browser tab
  defaultTheme: "light" | "dark"; // default for users with no saved theme
  timezone: string; // IANA name (informational)
  slaSeconds: number; // call-center service-level threshold (seconds)
}

export interface SystemSettingsResponse {
  settings: SystemSettings;
  envDomain: string; // install-time TPBX_DOMAIN, shown as the fallback
}

export async function getSystemSettings(): Promise<SystemSettingsResponse> {
  const r = await fetch("/api/settings/system");
  if (!r.ok) throw new Error(`settings ${r.status}`);
  return r.json();
}
export function saveSystemSettings(s: SystemSettings): Promise<any> {
  return request("PUT", "/api/settings/system", s);
}

// --- API tokens (machine-to-machine /api/v1 authentication) ------------------

export interface ApiToken {
  id: number;
  name: string;
  prefix: string; // first chars, for display
  createdBy: string;
  createdAt: string;
  lastUsedAt: string | null;
  revoked: boolean;
}

// The absolute base URL clients should call, e.g. "https://pbx.example.com/api/v1".
export function apiV1Base(): string {
  return window.location.origin + "/api/v1";
}
export function apiDocsUrl(): string {
  return window.location.origin + "/api/v1/docs";
}

export async function listApiTokens(): Promise<ApiToken[]> {
  const r = await fetch("/api/settings/tokens");
  if (!r.ok) throw new Error(`tokens ${r.status}`);
  return (await r.json()).tokens ?? [];
}
// createApiToken returns the plaintext token exactly once (in `token`).
export function createApiToken(name: string): Promise<{ token: string; meta: ApiToken }> {
  return request("POST", "/api/settings/tokens", { name });
}
export function revokeApiToken(id: number): Promise<any> {
  return request("POST", `/api/settings/tokens/${id}/revoke`);
}
export function deleteApiToken(id: number): Promise<any> {
  return request("DELETE", `/api/settings/tokens/${id}`);
}

// InfraInfo is the read-only, masked infrastructure config shown on the System
// tab. Secrets (DB password, ARI/AMI passwords) are masked/omitted server-side.
export interface InfraInfo {
  httpAddr: string;
  databaseUrl: string;
  ariUrl: string;
  ariUser: string;
  amiAddr: string;
  amiUser: string;
  asteriskConf: string;
  dialplanFile: string;
  transportsFile: string;
  pjsipFile: string;
  soundsDir: string;
  wssPort: string;
}

export async function getInfra(): Promise<InfraInfo> {
  const r = await fetch("/api/settings/infra");
  if (!r.ok) throw new Error(`infra ${r.status}`);
  return r.json();
}

// Branding is the public (no-auth) brand name + default theme, fetched before
// login so the tab title and initial theme can be applied without a session.
export interface Branding {
  brandName: string;
  defaultTheme: "light" | "dark";
}

export async function getBranding(): Promise<Branding> {
  const r = await fetch("/api/branding");
  if (!r.ok) throw new Error(`branding ${r.status}`);
  return r.json();
}

// SoftphoneInstaller reports whether a platform's softphone installer is present
// on the server, so the console can show a working download vs. a disabled state.
export interface SoftphoneInstaller {
  platform: "windows" | "android";
  available: boolean;
  url: string;
  name: string;
  sizeBytes?: number;
}
export interface SoftphoneInfo {
  installers: SoftphoneInstaller[];
}

export async function getSoftphoneInfo(): Promise<SoftphoneInfo> {
  const r = await fetch("/api/softphone");
  if (!r.ok) throw new Error(`softphone ${r.status}`);
  return r.json();
}

// --- Analytics (manager/admin) ----------------------------------------------

export interface AgentStat {
  extension: string;
  displayName: string;
  calls: number;
  answered: number;
  inbound: number;
  outbound: number;
  missed: number;
  talkTotal: number;
  talkAvg: number;
  longest: number;
  transfers: number;
  hangupByAgent: number;
  hangupByOther: number;
}

export interface AgentAnalytics {
  from: string;
  to: string;
  agents: AgentStat[];
}

export async function getAgentAnalytics(days: number): Promise<AgentAnalytics> {
  const r = await fetch(`/api/analytics/agents?days=${days}`);
  if (!r.ok) throw new Error(`analytics ${r.status}`);
  return r.json();
}

// --- Softphone analytics (DND, answered/rejected/missed, call log) -----------

export interface SoftphoneAgentStat {
  extension: string;
  displayName: string;
  answered: number;
  rejected: number;
  missed: number;
  failed: number;
  inbound: number;
  outbound: number;
  talkTotal: number; // seconds
  talkAvg: number; // seconds
  longest: number; // seconds
  dndActivations: number;
  dndSeconds: number;
}

export interface SoftphoneCall {
  extension: string;
  displayName: string;
  direction: "in" | "out";
  peer: string;
  outcome: "answered" | "rejected" | "missed" | "failed";
  durationSec: number;
  transport: string;
  at: string;
}

export interface SoftphoneAnalytics {
  from: string;
  to: string;
  agents: SoftphoneAgentStat[];
  recent: SoftphoneCall[];
}

export async function getSoftphoneAnalytics(days: number): Promise<SoftphoneAnalytics> {
  const r = await fetch(`/api/analytics/softphone?days=${days}`);
  if (!r.ok) throw new Error(`softphone analytics ${r.status}`);
  return r.json();
}

// --- Analytics dashboard (Overview / Extensions / Reports) -------------------

export interface VolumePoint {
  label: string;
  inbound: number;
  outbound: number;
}
export interface OverviewStats {
  totalCalls: number;
  ahtSeconds: number;
  resolutionRate: number; // 0..1
  resolutionN: number;
  onlineDevices: number;
  volume: VolumePoint[];
}
export interface LiveExtension {
  extension: string;
  displayName: string;
  status: "in_call" | "wrap" | "online";
}
export interface CallCenter {
  callsOffered: number;
  callsHandled: number;
  abandoned: number;
  allocationFailed: number;
  droppedInIvr: number;
  pendingAbandoned: number;
  serviceLevelPct: number;
  answeredPct: number;
  ahtSeconds: number;
  slaSeconds: number;
  inQueue: number;
  talking: number;
}
export interface PresentStatus {
  inIvr: number;
  inQueue: number;
  transferring: number;
  talking: number;
}
export interface AgentTally {
  total: number;
  online: number;
  onCall: number;
}
export interface OverviewResponse {
  from: string;
  to: string;
  overview: OverviewStats;
  callcenter: CallCenter;
  queues: string[];
  present: PresentStatus;
  agents: AgentTally;
  live: LiveExtension[];
}
export async function getOverview(days: number, queue = ""): Promise<OverviewResponse> {
  const qs = new URLSearchParams({ days: String(days) });
  if (queue) qs.set("queue", queue);
  const r = await fetch(`/api/analytics/overview?${qs.toString()}`);
  if (!r.ok) throw new Error(`overview ${r.status}`);
  return r.json();
}

// Web wrap-up: tag recent calls (any softphone) with a disposition.
export interface TaggableCall {
  id: string;
  extension: string;
  displayName: string;
  direction: "in" | "out";
  peer: string;
  disposition: string;
  durationSec: number;
  at: string;
  tagged: boolean;
}
export async function getWrapupCalls(days: number, ext = ""): Promise<TaggableCall[]> {
  const qs = new URLSearchParams({ days: String(days) });
  if (ext) qs.set("ext", ext);
  const r = await fetch(`/api/wrapup/calls?${qs.toString()}`);
  if (!r.ok) throw new Error(`wrapup ${r.status}`);
  return (await r.json()).calls ?? [];
}
export interface WrapupTag {
  extension: string;
  direction: string;
  peer: string;
  outcome: string;
  durationSec: number;
  at: string;
  nature: string;
  resolution: string;
  hangupCause: string;
  note: string;
}
export async function tagWrapupCall(body: WrapupTag): Promise<void> {
  const r = await fetch(`/api/wrapup/tag`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!r.ok) throw new Error(`tag ${r.status}`);
}

export interface DashSlice {
  label: string;
  count: number;
  pct: number;
}
export interface TimelineItem {
  at: string;
  kind: "call" | "dnd" | "system";
  title: string;
  detail: string;
}
export interface ExtensionDetail {
  extension: string;
  displayName: string;
  avgCallSeconds: number;
  callsToday: number;
  hangupRate: number;
  nature: DashSlice[];
  hangupCauses: DashSlice[];
  timeline: TimelineItem[];
}
export async function getExtensionDetail(ext: string, days: number): Promise<ExtensionDetail> {
  const r = await fetch(`/api/analytics/extension/${encodeURIComponent(ext)}?days=${days}`);
  if (!r.ok) throw new Error(`extension analytics ${r.status}`);
  return r.json();
}

export interface RankRow {
  extension: string;
  displayName: string;
  ahtSeconds: number;
  resolutionRate: number;
  trend: "up" | "down" | "flat";
}
export interface ReportsStats {
  peakVolume: number;
  commonHangupReason: string;
  topExtension: string;
  topExtensionName: string;
  topExtensionRate: number;
  thisWeek: number[];
  lastWeek: number[];
  insights: string[];
  ranking: RankRow[];
}
export async function getReports(days: number): Promise<ReportsStats> {
  const r = await fetch(`/api/analytics/reports?days=${days}`);
  if (!r.ok) throw new Error(`reports ${r.status}`);
  return r.json();
}

// --- Auth (Phase 8) ---------------------------------------------------------

// Feature keys the permission matrix is expressed over (mirrors the backend
// store.Features list and the nav).
export type Feature =
  | "extensions"
  | "trunks"
  | "routing"
  | "ivr"
  | "leads"
  | "campaigns"
  | "cdr"
  | "analytics"
  | "transports"
  | "settings"
  | "users";
export type Action = "view" | "create" | "edit" | "delete";

export interface Perm {
  view: boolean;
  create: boolean;
  edit: boolean;
  delete: boolean;
}
export type Permissions = Partial<Record<Feature, Perm>>;

export interface Me {
  username: string;
  role: string;
  displayName?: string;
  permissions: Permissions;
  totpEnabled: boolean;
  totpSetupRequired: boolean;
}

// A login attempt either completes (returns Me) or asks for a second factor.
export type LoginResult = Me | { totpRequired: true };
export function isTotpRequired(r: LoginResult): r is { totpRequired: true } {
  return (r as { totpRequired?: boolean }).totpRequired === true;
}

// can reports whether the current user may perform an action on a feature.
export function can(me: Me | null, feature: Feature, action: Action): boolean {
  if (!me) return false;
  if (me.role === "admin") return true;
  return me.permissions?.[feature]?.[action] === true;
}

// getMe returns the current user, or null if not authenticated (401).
export async function getMe(): Promise<Me | null> {
  const r = await fetch("/api/me");
  if (r.status === 401) return null;
  if (!r.ok) throw new Error(`me ${r.status}`);
  return r.json();
}

export function login(username: string, password: string, totpCode?: string): Promise<LoginResult> {
  return request("POST", "/api/login", { username, password, totpCode });
}

// --- Two-factor (TOTP) ------------------------------------------------------

export interface TotpEnrollResponse {
  secret: string;
  otpauthUri: string;
}
export function enrollTotp(): Promise<TotpEnrollResponse> {
  return request("POST", "/api/totp/enroll");
}
export function activateTotp(code: string): Promise<any> {
  return request("POST", "/api/totp/activate", { code });
}
export function disableTotp(code: string): Promise<any> {
  return request("POST", "/api/totp/disable", { code });
}
export function resetUserTotp(username: string): Promise<any> {
  return request("POST", `/api/users/${encodeURIComponent(username)}/totp/reset`);
}

export async function logout(): Promise<void> {
  await fetch("/api/logout", { method: "POST" });
}

export function changePassword(password: string): Promise<any> {
  return request("POST", "/api/change-password", { password });
}

export interface GuiUser {
  username: string;
  role: string;
  displayName: string;
  disabled: boolean;
  totpEnabled: boolean;
  lastLoginAt?: string;
}

export async function listUsers(): Promise<GuiUser[]> {
  const r = await fetch("/api/users");
  if (!r.ok) throw new Error(`users ${r.status}`);
  return (await r.json()).users ?? [];
}
export function createUser(u: { username: string; password: string; role: string; displayName?: string }): Promise<any> {
  return request("POST", "/api/users", u);
}
export function updateUser(
  username: string,
  u: { role: string; displayName?: string; disabled?: boolean }
): Promise<any> {
  return request("PUT", `/api/users/${encodeURIComponent(username)}`, u);
}
export function deleteUser(username: string): Promise<any> {
  return request("DELETE", `/api/users/${encodeURIComponent(username)}`);
}
export function resetUserPassword(username: string, password: string): Promise<any> {
  return request("POST", `/api/users/${encodeURIComponent(username)}/password`, { password });
}

// --- Roles (RBAC) -----------------------------------------------------------

export interface Role {
  name: string;
  displayName: string;
  permissions: Permissions;
  requireTotp: boolean;
  builtIn: boolean;
}

export interface RolesResponse {
  roles: Role[];
  features: Feature[];
  actions: Action[];
}

export async function listRoles(): Promise<RolesResponse> {
  const r = await fetch("/api/roles");
  if (!r.ok) throw new Error(`roles ${r.status}`);
  return r.json();
}
export function createRole(role: {
  name: string;
  displayName: string;
  permissions: Permissions;
  requireTotp: boolean;
}): Promise<any> {
  return request("POST", "/api/roles", role);
}
export function updateRole(
  name: string,
  role: { displayName: string; permissions: Permissions; requireTotp: boolean }
): Promise<any> {
  return request("PUT", `/api/roles/${encodeURIComponent(name)}`, role);
}
export function deleteRole(name: string): Promise<any> {
  return request("DELETE", `/api/roles/${encodeURIComponent(name)}`);
}

// --- Call History / CDR -----------------------------------------------------

export interface CDRRecord {
  id: number;
  callDate: string;
  clid: string;
  src: string;
  dst: string;
  duration: number;
  billsec: number;
  disposition: string;
}

export interface CDRPage {
  records: CDRRecord[];
  total: number;
}

export async function listCDR(params: {
  q?: string;
  disposition?: string;
  limit: number;
  offset: number;
}): Promise<CDRPage> {
  const qs = new URLSearchParams();
  if (params.q) qs.set("q", params.q);
  if (params.disposition) qs.set("disposition", params.disposition);
  qs.set("limit", String(params.limit));
  qs.set("offset", String(params.offset));
  const r = await fetch(`/api/cdr?${qs.toString()}`);
  if (!r.ok) throw new Error(`cdr ${r.status}`);
  return r.json();
}

// connectEvents opens the live WebSocket and invokes onMessage for each frame.
// It reconnects automatically with a small backoff. Returns a close function.
export function connectEvents(
  onMessage: (env: WsEnvelope) => void,
  onOpenChange: (open: boolean) => void
): () => void {
  let closed = false;
  let ws: WebSocket | null = null;
  let backoff = 1000;

  const open = () => {
    if (closed) return;
    const proto = location.protocol === "https:" ? "wss" : "ws";
    ws = new WebSocket(`${proto}://${location.host}/ws`);
    ws.onopen = () => {
      backoff = 1000;
      onOpenChange(true);
    };
    ws.onclose = () => {
      onOpenChange(false);
      if (!closed) {
        setTimeout(open, backoff);
        backoff = Math.min(backoff * 2, 15000);
      }
    };
    ws.onmessage = (ev) => {
      try {
        onMessage(JSON.parse(ev.data));
      } catch {
        /* ignore malformed frame */
      }
    };
  };

  open();
  return () => {
    closed = true;
    ws?.close();
  };
}
