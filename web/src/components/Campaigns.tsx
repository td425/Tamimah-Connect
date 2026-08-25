import { useCallback, useEffect, useState } from "react";
import {
  automaticDialing,
  can,
  createAgentAccount,
  createCampaign,
  deleteAgentAccount,
  deleteCampaign,
  deleteDisposition,
  deletePauseCode,
  listAgentAccounts,
  listCampaigns,
  listDispositions,
  listExtensions,
  listPauseCodes,
  nextPreviewLead,
  saveDisposition,
  savePauseCode,
  updateAgentAccount,
  updateCampaign,
  type AgentAccount,
  type Campaign,
  type Disposition,
  type Extension,
  type Lead,
  type Me,
  type PauseCode,
} from "../api";
import type { Notify } from "../types";

const BLANK: Campaign = {
  id: 0,
  code: "",
  name: "",
  description: "",
  active: true,
  dialMethod: "MANUAL",
  dialLevel: 1,
  adaptiveMax: 3,
  hopperLevel: 50,
  dialTimeout: 30,
  leadOrder: "DOWN",
  dialStatuses: ["NEW"],
  dropRateTarget: 3,
  amdEnabled: false,
  outboundCid: "",
  trunk: "",
  wrapupSeconds: 0,
  script: "",
  listCount: 0,
  leadCount: 0,
  agentCount: 0,
};

const BLANK_AGENT: AgentAccount = {
  id: 0,
  username: "",
  displayName: "",
  extension: "",
  active: true,
  campaigns: [],
};

type Tab = "campaigns" | "dispositions" | "pause" | "agents";

export default function Campaigns({ notify, me }: { notify: Notify; me: Me }) {
  const canCreate = can(me, "campaigns", "create");
  const canEdit = can(me, "campaigns", "edit");
  const canDelete = can(me, "campaigns", "delete");

  const [tab, setTab] = useState<Tab>("campaigns");
  const [campaigns, setCampaigns] = useState<Campaign[]>([]);
  const [dialMethods, setDialMethods] = useState<string[]>([]);
  const [leadOrders, setLeadOrders] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [editing, setEditing] = useState<Campaign | null>(null);

  const refresh = useCallback(() => {
    setLoading(true);
    listCampaigns()
      .then((p) => {
        setCampaigns(p.campaigns);
        setDialMethods(p.dialMethods);
        setLeadOrders(p.leadOrders);
      })
      .catch((e) => notify({ kind: "err", text: (e as Error).message }))
      .finally(() => setLoading(false));
  }, [notify]);

  useEffect(refresh, [refresh]);

  const onDelete = async (c: Campaign) => {
    if (
      !confirm(
        `Delete campaign ${c.code}?\n\nIts ${c.listCount} list(s) stay and become unassigned — leads are never deleted with a campaign.`
      )
    )
      return;
    try {
      await deleteCampaign(c.id);
      notify({ kind: "ok", text: `Deleted campaign ${c.code}` });
      refresh();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  return (
    <>
      <div className="page-head">
        <h2>Campaigns</h2>
        {canCreate && tab === "campaigns" && (
          <button className="btn" onClick={() => setEditing({ ...BLANK })}>
            + New Campaign
          </button>
        )}
      </div>

      <div className="tabbar">
        {(
          [
            ["campaigns", "Campaigns"],
            ["dispositions", "Dispositions"],
            ["pause", "Pause codes"],
            ["agents", "Agents"],
          ] as [Tab, string][]
        ).map(([key, label]) => (
          <button key={key} className={`tab ${tab === key ? "active" : ""}`} onClick={() => setTab(key)}>
            {label}
          </button>
        ))}
      </div>

      {tab === "campaigns" && (
        <section className="panel">
          <header>Outbound Campaigns</header>
          {loading ? (
            <div className="empty">Loading…</div>
          ) : campaigns.length === 0 ? (
            <div className="empty">
              No campaigns yet. A campaign decides who gets called, from which lists, with what
              caller ID — create one, then point a lead list at it.
            </div>
          ) : (
            <table>
              <thead>
                <tr>
                  <th>Code</th>
                  <th>Name</th>
                  <th>Dialing</th>
                  <th>Lists</th>
                  <th>Leads</th>
                  <th>Agents</th>
                  <th>Caller ID</th>
                  <th>Status</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {campaigns.map((c) => (
                  <tr key={c.id}>
                    <td className="mono-sm">{c.code}</td>
                    <td>
                      {c.name}
                      {c.description && <div className="dash-sub">{c.description}</div>}
                    </td>
                    <td>
                      <span className="badge">{c.dialMethod}</span>
                      {automaticDialing(c.dialMethod) && (
                        <div className="dash-sub" title="Automatic pacing needs the dialer engine, which is not built yet">
                          waiting for dialer
                        </div>
                      )}
                    </td>
                    <td>{c.listCount}</td>
                    <td>{c.leadCount.toLocaleString()}</td>
                    <td>{c.agentCount}</td>
                    <td className="mono-sm">{c.outboundCid || "—"}</td>
                    <td>
                      <span className={`badge ${c.active ? "" : "offline"}`}>
                        {c.active ? "active" : "paused"}
                      </span>
                    </td>
                    <td className="row-action">
                      <button className="btn small" onClick={() => previewNext(c, notify)}>
                        Next lead
                      </button>
                      {canEdit && (
                        <button className="btn small" onClick={() => setEditing(c)}>
                          Edit
                        </button>
                      )}
                      {canDelete && (
                        <button className="btn danger" onClick={() => onDelete(c)}>
                          Delete
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <p className="hint-inline" style={{ padding: "0 1rem 1rem" }}>
            Manual and preview campaigns work today — dial a lead from the Leads page and record
            what happened. The automatic methods (RATIO, ADAPT_*) are stored and validated here but
            place no calls until the dialer engine ships.
          </p>
        </section>
      )}

      {tab === "dispositions" && (
        <VocabPanel
          kind="disposition"
          campaigns={campaigns}
          canEdit={canEdit}
          canDelete={canDelete}
          notify={notify}
        />
      )}

      {tab === "pause" && (
        <VocabPanel
          kind="pause"
          campaigns={campaigns}
          canEdit={canEdit}
          canDelete={canDelete}
          notify={notify}
        />
      )}

      {tab === "agents" && (
        <AgentsPanel
          campaigns={campaigns}
          canCreate={canCreate}
          canEdit={canEdit}
          canDelete={canDelete}
          notify={notify}
        />
      )}

      {editing && (
        <CampaignForm
          initial={editing}
          dialMethods={dialMethods}
          leadOrders={leadOrders}
          onClose={() => setEditing(null)}
          onSaved={(msg) => {
            notify({ kind: "ok", text: msg });
            setEditing(null);
            refresh();
          }}
          onError={(msg) => notify({ kind: "err", text: msg })}
        />
      )}
    </>
  );
}

// previewNext shows the lead this campaign would dial next — the preview-dial
// question ("who is up?") answered without placing a call.
async function previewNext(c: Campaign, notify: Notify) {
  try {
    const res = await nextPreviewLead(c.id);
    if (!res.lead) {
      notify({ kind: "err", text: res.note || "No dialable leads in this campaign." });
      return;
    }
    const l: Lead = res.lead;
    const who = [l.firstName, l.lastName].filter(Boolean).join(" ");
    notify({
      kind: "ok",
      text: `Next in ${c.code}: ${l.phoneCode}${l.phoneNumber}${who ? ` (${who})` : ""} — ${l.status}`,
    });
  } catch (e) {
    notify({ kind: "err", text: (e as Error).message });
  }
}

// --- Campaign editor ---------------------------------------------------------

function CampaignForm({
  initial,
  dialMethods,
  leadOrders,
  onClose,
  onSaved,
  onError,
}: {
  initial: Campaign;
  dialMethods: string[];
  leadOrders: string[];
  onClose: () => void;
  onSaved: (msg: string) => void;
  onError: (msg: string) => void;
}) {
  const [f, setF] = useState<Campaign>(initial);
  const [busy, setBusy] = useState(false);
  const isNew = initial.id === 0;
  const set = <K extends keyof Campaign>(k: K, v: Campaign[K]) => setF((prev) => ({ ...prev, [k]: v }));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      if (isNew) {
        await createCampaign(f);
        onSaved(`Created campaign ${f.code.toUpperCase()}`);
      } else {
        await updateCampaign(f.id, f);
        onSaved(`Updated campaign ${f.code}`);
      }
    } catch (err) {
      onError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const auto = automaticDialing(f.dialMethod);

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal wide" onClick={(e) => e.stopPropagation()}>
        <header>{isNew ? "New Campaign" : `Edit Campaign ${f.code}`}</header>
        <form className="form" onSubmit={submit}>
          <div className="form-row">
            <label>
              Code {!isNew && <span className="hint-inline">(fixed)</span>}
              <input
                value={f.code}
                disabled={!isNew}
                placeholder="WEBOUT"
                onChange={(e) => set("code", e.target.value.toUpperCase())}
              />
            </label>
            <label>
              Name
              <input value={f.name} placeholder="Web enquiry follow-up" onChange={(e) => set("name", e.target.value)} />
            </label>
            <label>
              Active
              <select value={f.active ? "yes" : "no"} onChange={(e) => set("active", e.target.value === "yes")}>
                <option value="yes">Yes</option>
                <option value="no">No — paused</option>
              </select>
            </label>
          </div>

          <label>
            Description
            <input value={f.description} onChange={(e) => set("description", e.target.value)} />
          </label>

          <div className="form-row">
            <label>
              Dial method
              <select value={f.dialMethod} onChange={(e) => set("dialMethod", e.target.value)}>
                {(dialMethods.length ? dialMethods : [f.dialMethod]).map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Outbound caller ID
              <input
                value={f.outboundCid}
                placeholder="(defaults to the route's)"
                onChange={(e) => set("outboundCid", e.target.value)}
              />
            </label>
            <label>
              Wrap-up seconds
              <input
                type="number"
                value={f.wrapupSeconds}
                onChange={(e) => set("wrapupSeconds", parseInt(e.target.value || "0", 10))}
              />
            </label>
          </div>

          {auto && (
            <p className="hint-inline">
              <strong>{f.dialMethod}</strong> is an automatic method. These settings are saved and
              validated now, but no calls are placed until the dialer engine ships — until then this
              campaign behaves as manual/preview.
            </p>
          )}

          <div className="ivr-opts">
            <div className="ivr-opts-head">
              <span>Pacing {auto ? "" : "(used once automatic dialing is enabled)"}</span>
            </div>
            <div className="form-row">
              <label>
                Lines per agent
                <input
                  type="number"
                  step="0.1"
                  value={f.dialLevel}
                  onChange={(e) => set("dialLevel", parseFloat(e.target.value || "1"))}
                />
              </label>
              <label>
                Adaptive maximum
                <input
                  type="number"
                  step="0.1"
                  value={f.adaptiveMax}
                  onChange={(e) => set("adaptiveMax", parseFloat(e.target.value || "3"))}
                />
              </label>
              <label>
                Drop-rate ceiling %
                <input
                  type="number"
                  step="0.1"
                  value={f.dropRateTarget}
                  onChange={(e) => set("dropRateTarget", parseFloat(e.target.value || "3"))}
                />
              </label>
              <label>
                Dial timeout (s)
                <input
                  type="number"
                  value={f.dialTimeout}
                  onChange={(e) => set("dialTimeout", parseInt(e.target.value || "30", 10))}
                />
              </label>
            </div>
            <p className="hint-inline">
              The drop-rate ceiling is a compliance limit on abandoned calls, not a target — the
              dialer must pace below it. Values above 10% are refused.
            </p>
          </div>

          <div className="form-row">
            <label>
              Lead order
              <select value={f.leadOrder} onChange={(e) => set("leadOrder", e.target.value)}>
                {(leadOrders.length ? leadOrders : [f.leadOrder]).map((o) => (
                  <option key={o} value={o}>
                    {o}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Hopper level
              <input
                type="number"
                value={f.hopperLevel}
                onChange={(e) => set("hopperLevel", parseInt(e.target.value || "50", 10))}
              />
            </label>
            <label>
              Dial these lead statuses <span className="hint-inline">(comma separated)</span>
              <input
                value={f.dialStatuses.join(", ")}
                placeholder="NEW, NA, BUSY"
                onChange={(e) =>
                  set(
                    "dialStatuses",
                    e.target.value
                      .split(",")
                      .map((s) => s.trim().toUpperCase())
                      .filter(Boolean)
                  )
                }
              />
            </label>
          </div>

          <label>
            Agent script <span className="hint-inline">(shown on the agent screen from phase 3)</span>
            <textarea rows={4} value={f.script} onChange={(e) => set("script", e.target.value)} />
          </label>

          <div className="form-actions">
            <button type="button" className="btn ghost" onClick={onClose}>
              Cancel
            </button>
            <button type="submit" className="btn" disabled={busy}>
              {busy ? "Saving…" : isNew ? "Create" : "Save"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// --- Dispositions & pause codes ----------------------------------------------

// VocabPanel edits the two per-campaign vocabularies. They differ only in their
// flags, so one component with a `kind` beats two near-identical ones.
function VocabPanel({
  kind,
  campaigns,
  canEdit,
  canDelete,
  notify,
}: {
  kind: "disposition" | "pause";
  campaigns: Campaign[];
  canEdit: boolean;
  canDelete: boolean;
  notify: Notify;
}) {
  const [campaignId, setCampaignId] = useState(0);
  const [dispos, setDispos] = useState<Disposition[]>([]);
  const [pauses, setPauses] = useState<PauseCode[]>([]);
  const [loading, setLoading] = useState(true);
  const [draft, setDraft] = useState<Partial<Disposition & PauseCode> | null>(null);

  const refresh = useCallback(() => {
    setLoading(true);
    const p = kind === "disposition" ? listDispositions(campaignId) : listPauseCodes(campaignId);
    p.then((rows: any) => (kind === "disposition" ? setDispos(rows) : setPauses(rows)))
      .catch((e) => notify({ kind: "err", text: (e as Error).message }))
      .finally(() => setLoading(false));
  }, [kind, campaignId, notify]);

  useEffect(refresh, [refresh]);

  const onSave = async () => {
    if (!draft?.code) {
      notify({ kind: "err", text: "A code is required." });
      return;
    }
    try {
      const payload = { ...draft, campaignId: campaignId || null };
      if (kind === "disposition") await saveDisposition(payload as Disposition);
      else await savePauseCode(payload as PauseCode);
      notify({ kind: "ok", text: `Saved ${draft.code}` });
      setDraft(null);
      refresh();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  const onDelete = async (id: number, code: string) => {
    if (!confirm(`Delete ${code}?`)) return;
    try {
      if (kind === "disposition") await deleteDisposition(id);
      else await deletePauseCode(id);
      notify({ kind: "ok", text: `Deleted ${code}` });
      refresh();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  const rows: (Disposition | PauseCode)[] = kind === "disposition" ? dispos : pauses;

  return (
    <section className="panel">
      <header>{kind === "disposition" ? "Dispositions" : "Pause codes"}</header>

      <div className="form" style={{ paddingBottom: 0 }}>
        <div className="form-row">
          <label>
            Campaign
            <select value={campaignId} onChange={(e) => setCampaignId(parseInt(e.target.value, 10))}>
              <option value={0}>System-wide only</option>
              {campaigns.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.code} — {c.name}
                </option>
              ))}
            </select>
          </label>
          {canEdit && (
            <label>
              &nbsp;
              <button
                type="button"
                className="btn"
                onClick={() =>
                  setDraft(
                    kind === "disposition"
                      ? { code: "", name: "", selectable: true, humanAnswered: true, recycleAfterSec: 0 }
                      : { code: "", name: "", billable: true }
                  )
                }
              >
                + Add {kind === "disposition" ? "disposition" : "pause code"}
              </button>
            </label>
          )}
        </div>
        <p className="hint-inline">
          {campaignId === 0
            ? "System-wide entries apply to every campaign. Pick a campaign to add ones only it uses."
            : "This campaign's own entries, plus the system-wide ones it inherits."}
        </p>
      </div>

      {loading ? (
        <div className="empty">Loading…</div>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Code</th>
              <th>Name</th>
              <th>Scope</th>
              {kind === "disposition" ? (
                <>
                  <th>Meaning</th>
                  <th>Redial after</th>
                </>
              ) : (
                <th>Billable</th>
              )}
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => {
              const d = row as Disposition;
              const p = row as PauseCode;
              const system = row.campaignId === null;
              return (
                <tr key={row.id}>
                  <td className="mono-sm">{row.code}</td>
                  <td>{row.name}</td>
                  <td>
                    <span className={`badge ${system ? "" : "offline"}`}>
                      {system ? "system" : "campaign"}
                    </span>
                  </td>
                  {kind === "disposition" ? (
                    <>
                      <td>
                        {[
                          d.isSale && "sale",
                          d.callback && "callback",
                          d.dnc && "DNC",
                          d.notInterested && "not interested",
                          !d.humanAnswered && "no human",
                          !d.selectable && "not offered",
                        ]
                          .filter(Boolean)
                          .join(" · ") || "—"}
                      </td>
                      <td>{d.recycleAfterSec > 0 ? `${d.recycleAfterSec}s` : "never"}</td>
                    </>
                  ) : (
                    <td>{p.billable ? "yes" : "no"}</td>
                  )}
                  <td className="row-action">
                    {canEdit && (
                      <button className="btn small" onClick={() => setDraft(row as any)}>
                        Edit
                      </button>
                    )}
                    {canDelete && !system && (
                      <button className="btn danger" onClick={() => onDelete(row.id, row.code)}>
                        Delete
                      </button>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}

      {draft && (
        <div className="modal-backdrop" onClick={() => setDraft(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <header>{draft.id ? `Edit ${draft.code}` : "New entry"}</header>
            <div className="form">
              <div className="form-row">
                <label>
                  Code
                  <input
                    value={draft.code ?? ""}
                    placeholder={kind === "disposition" ? "SALE" : "BREAK"}
                    onChange={(e) => setDraft({ ...draft, code: e.target.value.toUpperCase() })}
                  />
                </label>
                <label>
                  Name
                  <input
                    value={draft.name ?? ""}
                    onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                  />
                </label>
              </div>

              {kind === "disposition" ? (
                <>
                  <div className="form-row">
                    {(
                      [
                        ["selectable", "Offer to agents"],
                        ["humanAnswered", "A human answered"],
                        ["isSale", "Counts as a sale"],
                        ["notInterested", "Not interested"],
                        ["dnc", "Add to Do-Not-Call"],
                        ["callback", "Schedules a callback"],
                      ] as [keyof Disposition, string][]
                    ).map(([key, label]) => (
                      <label key={String(key)}>
                        {label}
                        <select
                          value={(draft as any)[key] ? "yes" : "no"}
                          onChange={(e) => setDraft({ ...draft, [key]: e.target.value === "yes" })}
                        >
                          <option value="yes">Yes</option>
                          <option value="no">No</option>
                        </select>
                      </label>
                    ))}
                  </div>
                  <label>
                    Redial after (seconds, 0 = never)
                    <input
                      type="number"
                      value={draft.recycleAfterSec ?? 0}
                      onChange={(e) =>
                        setDraft({ ...draft, recycleAfterSec: parseInt(e.target.value || "0", 10) })
                      }
                    />
                  </label>
                  <p className="hint-inline">
                    Do-Not-Call and redial timing are recorded here and enforced by later phases —
                    the compliance layer and the dialer respectively.
                  </p>
                </>
              ) : (
                <label>
                  Billable <span className="hint-inline">(paid not-ready time)</span>
                  <select
                    value={draft.billable ? "yes" : "no"}
                    onChange={(e) => setDraft({ ...draft, billable: e.target.value === "yes" })}
                  >
                    <option value="yes">Yes</option>
                    <option value="no">No</option>
                  </select>
                </label>
              )}

              <div className="form-actions">
                <button type="button" className="btn ghost" onClick={() => setDraft(null)}>
                  Cancel
                </button>
                <button type="button" className="btn" onClick={onSave}>
                  Save
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
    </section>
  );
}

// --- Agents ------------------------------------------------------------------

function AgentsPanel({
  campaigns,
  canCreate,
  canEdit,
  canDelete,
  notify,
}: {
  campaigns: Campaign[];
  canCreate: boolean;
  canEdit: boolean;
  canDelete: boolean;
  notify: Notify;
}) {
  const [agents, setAgents] = useState<AgentAccount[]>([]);
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [loading, setLoading] = useState(true);
  const [editing, setEditing] = useState<AgentAccount | null>(null);

  const refresh = useCallback(() => {
    setLoading(true);
    listAgentAccounts()
      .then(setAgents)
      .catch((e) => notify({ kind: "err", text: (e as Error).message }))
      .finally(() => setLoading(false));
  }, [notify]);

  useEffect(refresh, [refresh]);

  useEffect(() => {
    listExtensions()
      .then(setExtensions)
      .catch(() => setExtensions([]));
  }, []);

  const onDelete = async (a: AgentAccount) => {
    if (!confirm(`Delete agent ${a.username}? Their call history is kept.`)) return;
    try {
      await deleteAgentAccount(a.id);
      notify({ kind: "ok", text: `Deleted ${a.username}` });
      refresh();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  const save = async (a: AgentAccount) => {
    try {
      if (a.id === 0) {
        await createAgentAccount(a);
        notify({ kind: "ok", text: `Created agent ${a.username}` });
      } else {
        await updateAgentAccount(a.id, a);
        notify({ kind: "ok", text: `Updated agent ${a.username}` });
      }
      setEditing(null);
      refresh();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  return (
    <section className="panel">
      <header>Agents</header>
      <p className="hint-inline" style={{ padding: "0 1rem" }}>
        An agent is a person; the extension is the device they are currently reachable on. Softphone
        sign-in still uses the extension and its SIP secret — phase 3 moves it onto these accounts.
      </p>

      {canCreate && (
        <div className="form" style={{ paddingBottom: 0 }}>
          <button type="button" className="btn" onClick={() => setEditing({ ...BLANK_AGENT })}>
            + New Agent
          </button>
        </div>
      )}

      {loading ? (
        <div className="empty">Loading…</div>
      ) : agents.length === 0 ? (
        <div className="empty">No agents yet.</div>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Username</th>
              <th>Name</th>
              <th>Extension</th>
              <th>Campaigns</th>
              <th>Status</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {agents.map((a) => (
              <tr key={a.id}>
                <td className="mono-sm">{a.username}</td>
                <td>{a.displayName}</td>
                <td>{a.extension || <span className="hint-inline">unbound</span>}</td>
                <td>{(a.campaignCodes ?? []).join(", ") || "—"}</td>
                <td>
                  <span className={`badge ${a.active ? "" : "offline"}`}>
                    {a.active ? "active" : "disabled"}
                  </span>
                </td>
                <td className="row-action">
                  {canEdit && (
                    <button className="btn small" onClick={() => setEditing(a)}>
                      Edit
                    </button>
                  )}
                  {canDelete && (
                    <button className="btn danger" onClick={() => onDelete(a)}>
                      Delete
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {editing && (
        <AgentForm
          initial={editing}
          campaigns={campaigns}
          extensions={extensions}
          onClose={() => setEditing(null)}
          onSave={save}
        />
      )}
    </section>
  );
}

function AgentForm({
  initial,
  campaigns,
  extensions,
  onClose,
  onSave,
}: {
  initial: AgentAccount;
  campaigns: Campaign[];
  extensions: Extension[];
  onClose: () => void;
  onSave: (a: AgentAccount) => void;
}) {
  const [f, setF] = useState<AgentAccount>(initial);
  const isNew = initial.id === 0;
  const set = <K extends keyof AgentAccount>(k: K, v: AgentAccount[K]) =>
    setF((prev) => ({ ...prev, [k]: v }));

  const toggleCampaign = (id: number) =>
    setF((prev) => ({
      ...prev,
      campaigns: prev.campaigns.includes(id)
        ? prev.campaigns.filter((c) => c !== id)
        : [...prev.campaigns, id],
    }));

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <header>{isNew ? "New Agent" : `Edit ${f.username}`}</header>
        <div className="form">
          <div className="form-row">
            <label>
              Username
              <input value={f.username} placeholder="a.hassan" onChange={(e) => set("username", e.target.value)} />
            </label>
            <label>
              Display name
              <input value={f.displayName} onChange={(e) => set("displayName", e.target.value)} />
            </label>
          </div>

          <div className="form-row">
            <label>
              Extension <span className="hint-inline">(the device they use)</span>
              <select value={f.extension} onChange={(e) => set("extension", e.target.value)}>
                <option value="">Unbound</option>
                {extensions.map((x) => (
                  <option key={x.id} value={x.id}>
                    {x.id} {x.callerId ? `— ${x.callerId}` : ""}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Active
              <select value={f.active ? "yes" : "no"} onChange={(e) => set("active", e.target.value === "yes")}>
                <option value="yes">Yes</option>
                <option value="no">No — cannot work</option>
              </select>
            </label>
          </div>

          <div className="ivr-opts">
            <div className="ivr-opts-head">
              <span>Campaigns this agent may work</span>
            </div>
            {campaigns.length === 0 ? (
              <p className="hint-inline">No campaigns exist yet.</p>
            ) : (
              campaigns.map((c) => (
                <label key={c.id} className="ivr-opt-row" style={{ gridTemplateColumns: "auto 1fr" }}>
                  <input
                    type="checkbox"
                    checked={f.campaigns.includes(c.id)}
                    onChange={() => toggleCampaign(c.id)}
                  />
                  <span>
                    {c.code} — {c.name}
                  </span>
                </label>
              ))
            )}
          </div>

          <div className="form-actions">
            <button type="button" className="btn ghost" onClick={onClose}>
              Cancel
            </button>
            <button type="button" className="btn" onClick={() => onSave(f)}>
              {isNew ? "Create" : "Save"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
