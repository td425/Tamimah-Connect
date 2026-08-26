import { useCallback, useEffect, useState } from "react";
import {
  can,
  createIngroup,
  deleteIngroup,
  getIngroup,
  listAgentAccounts,
  listIngroups,
  setIngroupAgents,
  updateIngroup,
  type AgentAccount,
  type Ingroup,
  type IngroupAgent,
  type Me,
} from "../api";
import type { Notify } from "../types";

const BLANK: Ingroup = {
  name: "",
  description: "",
  active: true,
  strategy: "ringall",
  musicOnHold: "",
  announce: "",
  ringTimeout: 20,
  wrapupTime: 0,
  maxCallers: 0,
  serviceLevel: 20,
  joinEmpty: "yes",
  leaveWhenEmpty: "no",
  announcePosition: "no",
  periodicAnnounce: "",
  periodicAnnounceFrequency: 0,
  dropAction: "hangup",
  maxWait: 300,
  memberCount: 0,
  allowedCount: 0,
};

export default function Ingroups({ notify, me }: { notify: Notify; me: Me }) {
  const canCreate = can(me, "routing", "create");
  const canEdit = can(me, "routing", "edit");
  const canDelete = can(me, "routing", "delete");

  const [rows, setRows] = useState<Ingroup[]>([]);
  const [strategies, setStrategies] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [editing, setEditing] = useState<Ingroup | null>(null);
  const [isNew, setIsNew] = useState(false);
  const [staffing, setStaffing] = useState<string | null>(null);

  const refresh = useCallback(() => {
    setLoading(true);
    listIngroups()
      .then((p) => {
        setRows(p.ingroups);
        setStrategies(p.strategies);
      })
      .catch((e) => notify({ kind: "err", text: (e as Error).message }))
      .finally(() => setLoading(false));
  }, [notify]);

  useEffect(refresh, [refresh]);

  const onDelete = async (g: Ingroup) => {
    if (!confirm(`Delete in-group ${g.name}? Any route pointing at it will stop working.`)) return;
    try {
      await deleteIngroup(g.name);
      notify({ kind: "ok", text: `Deleted ${g.name}` });
      refresh();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  const openEdit = async (name: string) => {
    try {
      const res = await getIngroup(name);
      setIsNew(false);
      setEditing(res.ingroup);
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  return (
    <>
      <div className="page-head">
        <h2>In-Groups</h2>
        {canCreate && (
          <button
            className="btn"
            onClick={() => {
              setIsNew(true);
              setEditing({ ...BLANK });
            }}
          >
            + New In-Group
          </button>
        )}
      </div>

      <section className="panel">
        <header>ACD Queues</header>
        <p className="hint-inline" style={{ padding: "0 1rem" }}>
          An in-group is a real Asterisk queue: callers wait in it, agents join it for a shift, and
          every event is written to <code>queue_log</code> — which is what the Overview dashboard's
          Service Level, Offered, Handled and Abandoned numbers are computed from. Point an inbound
          route or an IVR key at one to get a call centre you can actually measure. (The older
          "Ring agents" action is a hunt group: it works, but reports nothing.)
        </p>

        {loading ? (
          <div className="empty">Loading…</div>
        ) : rows.length === 0 ? (
          <div className="empty">No in-groups yet.</div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Strategy</th>
                <th>Agents on now</th>
                <th>Permitted</th>
                <th>Ring</th>
                <th>SLA</th>
                <th>Status</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {rows.map((g) => (
                <tr key={g.name}>
                  <td className="mono-sm">
                    {g.name}
                    {g.description && <div className="dash-sub">{g.description}</div>}
                  </td>
                  <td>
                    <span className="badge">{g.strategy}</span>
                  </td>
                  <td>{g.memberCount}</td>
                  <td>{g.allowedCount}</td>
                  <td>{g.ringTimeout}s</td>
                  <td>{g.serviceLevel}s</td>
                  <td>
                    <span className={`badge ${g.active ? "" : "offline"}`}>
                      {g.active ? "active" : "inactive"}
                    </span>
                  </td>
                  <td className="row-action">
                    {canEdit && (
                      <>
                        <button className="btn small" onClick={() => setStaffing(g.name)}>
                          Agents
                        </button>
                        <button className="btn small" onClick={() => openEdit(g.name)}>
                          Edit
                        </button>
                      </>
                    )}
                    {canDelete && (
                      <button className="btn danger" onClick={() => onDelete(g)}>
                        Delete
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      {editing && (
        <IngroupForm
          initial={editing}
          isNew={isNew}
          strategies={strategies}
          onClose={() => setEditing(null)}
          onSaved={(msg) => {
            notify({ kind: "ok", text: msg });
            setEditing(null);
            refresh();
          }}
          onError={(msg) => notify({ kind: "err", text: msg })}
        />
      )}

      {staffing && (
        <StaffingModal
          ingroup={staffing}
          onClose={() => setStaffing(null)}
          onSaved={(msg) => {
            notify({ kind: "ok", text: msg });
            setStaffing(null);
            refresh();
          }}
          onError={(msg) => notify({ kind: "err", text: msg })}
        />
      )}
    </>
  );
}

function IngroupForm({
  initial,
  isNew,
  strategies,
  onClose,
  onSaved,
  onError,
}: {
  initial: Ingroup;
  isNew: boolean;
  strategies: string[];
  onClose: () => void;
  onSaved: (msg: string) => void;
  onError: (msg: string) => void;
}) {
  const [f, setF] = useState<Ingroup>(initial);
  const [busy, setBusy] = useState(false);
  const set = <K extends keyof Ingroup>(k: K, v: Ingroup[K]) => setF((p) => ({ ...p, [k]: v }));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      if (isNew) {
        await createIngroup(f);
        onSaved(`Created in-group ${f.name.toUpperCase()}`);
      } else {
        await updateIngroup(f.name, f);
        onSaved(`Updated ${f.name}`);
      }
    } catch (err) {
      onError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal wide" onClick={(e) => e.stopPropagation()}>
        <header>{isNew ? "New In-Group" : `Edit ${f.name}`}</header>
        <form className="form" onSubmit={submit}>
          <div className="form-row">
            <label>
              Name {!isNew && <span className="hint-inline">(fixed)</span>}
              <input
                value={f.name}
                disabled={!isNew}
                placeholder="SALES"
                onChange={(e) => set("name", e.target.value.toUpperCase())}
              />
            </label>
            <label>
              Description
              <input value={f.description} onChange={(e) => set("description", e.target.value)} />
            </label>
            <label>
              Active
              <select value={f.active ? "yes" : "no"} onChange={(e) => set("active", e.target.value === "yes")}>
                <option value="yes">Yes</option>
                <option value="no">No</option>
              </select>
            </label>
          </div>

          <div className="form-row">
            <label>
              Ring strategy
              <select value={f.strategy} onChange={(e) => set("strategy", e.target.value)}>
                {(strategies.length ? strategies : [f.strategy]).map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Ring each agent for
              <input
                type="number"
                value={f.ringTimeout}
                onChange={(e) => set("ringTimeout", parseInt(e.target.value || "20", 10))}
              />
            </label>
            <label>
              Wrap-up after a call
              <input
                type="number"
                value={f.wrapupTime}
                onChange={(e) => set("wrapupTime", parseInt(e.target.value || "0", 10))}
              />
            </label>
            <label>
              Service level (s)
              <input
                type="number"
                value={f.serviceLevel}
                onChange={(e) => set("serviceLevel", parseInt(e.target.value || "20", 10))}
              />
            </label>
          </div>

          <div className="form-row">
            <label>
              Music on hold
              <input
                value={f.musicOnHold}
                placeholder="(default)"
                onChange={(e) => set("musicOnHold", e.target.value)}
              />
            </label>
            <label>
              Max callers waiting <span className="hint-inline">(0 = unlimited)</span>
              <input
                type="number"
                value={f.maxCallers}
                onChange={(e) => set("maxCallers", parseInt(e.target.value || "0", 10))}
              />
            </label>
            <label>
              Announce position
              <select value={f.announcePosition} onChange={(e) => set("announcePosition", e.target.value)}>
                <option value="no">No</option>
                <option value="yes">Yes</option>
              </select>
            </label>
          </div>

          <div className="form-row">
            <label>
              Give up after <span className="hint-inline">(0 = wait forever)</span>
              <input
                type="number"
                value={f.maxWait}
                onChange={(e) => set("maxWait", parseInt(e.target.value || "0", 10))}
              />
            </label>
            <label>
              Then send the caller to
              <input
                value={f.dropAction}
                placeholder="hangup · 2000 · voicemail:2000 · ivr:main"
                onChange={(e) => set("dropAction", e.target.value)}
              />
            </label>
          </div>

          <div className="form-row">
            <label>
              Callers may join when no agent is on
              <select value={f.joinEmpty} onChange={(e) => set("joinEmpty", e.target.value)}>
                <option value="yes">Yes</option>
                <option value="no">No — send them straight to the give-up destination</option>
              </select>
            </label>
            <label>
              Eject waiting callers if the last agent leaves
              <select value={f.leaveWhenEmpty} onChange={(e) => set("leaveWhenEmpty", e.target.value)}>
                <option value="no">No</option>
                <option value="yes">Yes</option>
              </select>
            </label>
          </div>

          <p className="hint-inline">
            The queue itself is read live from the database by Asterisk, so these settings apply
            without a reload. Only the give-up destination is compiled into the dialplan.
          </p>

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

// StaffingModal edits who MAY take an in-group's calls. Which of those they are
// signed in to right now is the agent's own choice, made on their screen.
function StaffingModal({
  ingroup,
  onClose,
  onSaved,
  onError,
}: {
  ingroup: string;
  onClose: () => void;
  onSaved: (msg: string) => void;
  onError: (msg: string) => void;
}) {
  const [agents, setAgents] = useState<AgentAccount[]>([]);
  const [allowed, setAllowed] = useState<Map<number, IngroupAgent>>(new Map());
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    listAgentAccounts().then(setAgents).catch(() => setAgents([]));
    getIngroup(ingroup)
      .then((res) => setAllowed(new Map(res.agents.map((a) => [a.agentId, a]))))
      .catch((e) => onError((e as Error).message));
  }, [ingroup, onError]);

  const toggle = (a: AgentAccount) =>
    setAllowed((prev) => {
      const next = new Map(prev);
      if (next.has(a.id)) next.delete(a.id);
      else
        next.set(a.id, {
          agentId: a.id,
          username: a.username,
          displayName: a.displayName,
          extension: a.extension,
          penalty: 0,
          loggedIn: false,
          paused: false,
        });
      return next;
    });

  const setPenalty = (id: number, penalty: number) =>
    setAllowed((prev) => {
      const next = new Map(prev);
      const cur = next.get(id);
      if (cur) next.set(id, { ...cur, penalty });
      return next;
    });

  const save = async () => {
    setBusy(true);
    try {
      await setIngroupAgents(ingroup, [...allowed.values()]);
      onSaved(`Updated who takes ${ingroup} calls`);
    } catch (e) {
      onError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal wide" onClick={(e) => e.stopPropagation()}>
        <header>Agents for {ingroup}</header>
        <div className="form">
          <p className="hint-inline">
            Ticking an agent lets them take this in-group's calls. They choose which of their
            permitted in-groups they are actually on from their own screen — this is permission,
            not attendance. Lower penalty is offered calls first, which is how skill tiers are
            expressed.
          </p>

          {agents.length === 0 ? (
            <p className="hint-inline">No agents yet — create them on the Campaigns page.</p>
          ) : (
            <table>
              <thead>
                <tr>
                  <th style={{ width: 28 }}></th>
                  <th>Agent</th>
                  <th>Extension</th>
                  <th>Penalty</th>
                  <th>On now</th>
                </tr>
              </thead>
              <tbody>
                {agents.map((a) => {
                  const sel = allowed.get(a.id);
                  return (
                    <tr key={a.id}>
                      <td>
                        <input type="checkbox" checked={!!sel} onChange={() => toggle(a)} />
                      </td>
                      <td>
                        {a.displayName}
                        <div className="dash-sub">{a.username}</div>
                      </td>
                      <td>{a.extension || <span className="hint-inline">unbound</span>}</td>
                      <td>
                        <input
                          type="number"
                          style={{ width: 70 }}
                          disabled={!sel}
                          value={sel?.penalty ?? 0}
                          onChange={(e) => setPenalty(a.id, parseInt(e.target.value || "0", 10))}
                        />
                      </td>
                      <td>
                        {sel?.loggedIn ? (
                          <span className={`badge ${sel.paused ? "offline" : ""}`}>
                            {sel.paused ? "paused" : "taking calls"}
                          </span>
                        ) : (
                          "—"
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}

          <div className="form-actions">
            <button type="button" className="btn ghost" onClick={onClose}>
              Cancel
            </button>
            <button type="button" className="btn" disabled={busy} onClick={save}>
              {busy ? "Saving…" : "Save"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
