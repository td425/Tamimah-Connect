// Agent softphone API client. All requests are same-origin and rely on the
// tpbx_agent session cookie set by the backend at login.

export interface AgentConfig {
  extension: string;
  displayName: string;
  password: string;
  domain: string;
  wsUrl: string;
  iceServers: RTCIceServer[];
  iceTransportPolicy?: RTCIceTransportPolicy;
}

export interface AgentIdentity {
  extension: string;
  displayName: string;
}

async function post(path: string, body?: unknown): Promise<Response> {
  return fetch(path, {
    method: "POST",
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
}

export async function agentLogin(extension: string, password: string): Promise<AgentIdentity> {
  const r = await post("/api/agent/login", { extension, password });
  if (!r.ok) {
    const msg = await r.json().catch(() => ({ error: `login failed (${r.status})` }));
    throw new Error(msg.error ?? "login failed");
  }
  return r.json();
}

export async function agentLogout(): Promise<void> {
  await post("/api/agent/logout");
}

export async function agentConfig(): Promise<AgentConfig> {
  const r = await fetch("/api/agent/config");
  if (!r.ok) throw new Error(`config ${r.status}`);
  return r.json();
}

// --- Agent desktop (parity phase 3) ------------------------------------------
//
// These turn the softphone from a phone into an agent screen. Every call is
// authenticated by the same agent session; the backend resolves who the agent
// is from that session, never from anything sent here.

export interface DeskCampaign {
  id: number;
  code: string;
  name: string;
  script: string;
  wrapupSeconds: number;
}

export interface DeskDisposition {
  id: number;
  code: string;
  name: string;
  callback: boolean;
  selectable: boolean;
}

export interface DeskPauseCode {
  id: number;
  code: string;
  name: string;
  billable: boolean;
}

export interface DeskLead {
  id: number;
  listId: number;
  status: string;
  calledCount: number;
  phoneCode: string;
  phoneNumber: string;
  altPhone: string;
  altPhoneTwo: string;
  title: string;
  firstName: string;
  lastName: string;
  email: string;
  address1: string;
  city: string;
  state: string;
  postalCode: string;
  comments: string;
  custom?: Record<string, unknown>;
  listName?: string;
}

export interface DeskState {
  agentId: number;
  campaignId: number | null;
  paused: boolean;
  pauseCode: string;
  currentLead: number | null;
  currentCall: number | null;
  since: string;
  campaignCode?: string;
  campaignName?: string;
  script?: string;
  wrapupSeconds?: number;
}

export interface DeskSession {
  agent: { id: number; username: string; displayName: string; extension: string };
  state: DeskState;
  campaigns: DeskCampaign[];
  dispositions: DeskDisposition[];
  pauseCodes: DeskPauseCode[];
  lead: DeskLead | null;
}

export interface DeskCallback {
  id: number;
  leadId: number;
  agent: string;
  recipient: string;
  callbackAt: string;
  note: string;
  leadName?: string;
  leadPhone?: string;
}

async function deskJSON(path: string, body?: unknown, method = "POST"): Promise<any> {
  const r = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data?.error || `${path} failed (${r.status})`);
  return data;
}

export function deskSession(): Promise<DeskSession> {
  return deskJSON("/api/agent/session", undefined, "GET");
}

export function deskSetCampaign(campaignId: number): Promise<any> {
  return deskJSON("/api/agent/campaign", { campaignId });
}

export function deskPause(paused: boolean, code = ""): Promise<DeskState> {
  return deskJSON("/api/agent/pause", { paused, code });
}

export function deskNextLead(): Promise<{ lead: DeskLead | null; note?: string }> {
  return deskJSON("/api/agent/next-lead", undefined, "GET");
}

export function deskDial(altPhone = ""): Promise<{ dialed: string; logError?: string }> {
  return deskJSON("/api/agent/dial", { altPhone });
}

export function deskDisposition(input: {
  status: string;
  note?: string;
  callbackAt?: string;
  callbackTo?: string;
}): Promise<{ callbackError?: string }> {
  return deskJSON("/api/agent/disposition", input);
}

export function deskUpdateLead(fields: Record<string, string>): Promise<DeskLead> {
  return deskJSON("/api/agent/lead", { fields });
}

export async function deskCallbacks(): Promise<DeskCallback[]> {
  const d = await deskJSON("/api/agent/callbacks", undefined, "GET");
  return d.callbacks ?? [];
}

export function deskTakeLead(leadId: number): Promise<{ lead: DeskLead }> {
  return deskJSON("/api/agent/take-lead", { leadId });
}
