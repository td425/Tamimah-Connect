import { useCallback, useEffect, useState } from "react";
import {
  createInboundRoute,
  createOutboundRoute,
  deleteInboundRoute,
  deleteOutboundRoute,
  listInboundRoutes,
  listIVRs,
  listIngroups,
  listOutboundRoutes,
  listSounds,
  listTrunks,
  updateInboundRoute,
  updateOutboundRoute,
  can,
  type InboundRoute,
  type IVR,
  type Me,
  type OutboundRoute,
  type SoundFile,
  type Trunk,
} from "../api";
import type { Notify } from "../types";

const BLANK_OUT: OutboundRoute = {
  id: 0,
  name: "",
  pattern: "_9.",
  destType: "trunk",
  trunk: "",
  ivr: "",
  strip: 1,
  prepend: "",
  callerId: "",
  position: 100,
  enabled: true,
};

const BLANK_IN: InboundRoute = {
  id: 0,
  name: "",
  did: "",
  destination: "",
  enabled: true,
};

export default function Routing({ notify, me }: { notify: Notify; me: Me }) {
  const canCreate = can(me, "routing", "create");
  const canEdit = can(me, "routing", "edit");
  const canDelete = can(me, "routing", "delete");
  const [outbound, setOutbound] = useState<OutboundRoute[]>([]);
  const [inbound, setInbound] = useState<InboundRoute[]>([]);
  const [trunks, setTrunks] = useState<Trunk[]>([]);
  const [ivrs, setIvrs] = useState<IVR[]>([]);
  const [ingroups, setIngroups] = useState<string[]>([]);
  const [sounds, setSounds] = useState<SoundFile[]>([]);
  const [editOut, setEditOut] = useState<OutboundRoute | null>(null);
  const [editIn, setEditIn] = useState<InboundRoute | null>(null);

  const refresh = useCallback(() => {
    listOutboundRoutes().then(setOutbound).catch((e) => notify({ kind: "err", text: (e as Error).message }));
    listInboundRoutes().then(setInbound).catch((e) => notify({ kind: "err", text: (e as Error).message }));
    listTrunks().then(setTrunks).catch(() => {});
    listIVRs().then(setIvrs).catch(() => {});
    // In-groups are the ACD destination an inbound route should normally use.
    listIngroups().then((p) => setIngroups(p.ingroups.map((g) => g.name))).catch(() => {});
    listSounds().then((r) => setSounds(r.sounds)).catch(() => {});
  }, [notify]);

  useEffect(refresh, [refresh]);

  const delOut = async (id: number) => {
    if (!confirm("Delete this outbound route?")) return;
    try {
      await deleteOutboundRoute(id);
      notify({ kind: "ok", text: "Outbound route deleted" });
      refresh();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };
  const delIn = async (id: number) => {
    if (!confirm("Delete this inbound route?")) return;
    try {
      await deleteInboundRoute(id);
      notify({ kind: "ok", text: "Inbound route deleted" });
      refresh();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  return (
    <>
      <div className="page-head">
        <h2>Routing</h2>
      </div>

      <section className="panel">
        <header>
          Outbound Routes
          {canCreate && (
            <button className="btn small" style={{ float: "right" }} onClick={() => setEditOut({ ...BLANK_OUT })}>
              + New
            </button>
          )}
        </header>
        {outbound.length === 0 ? (
          <div className="empty">No outbound routes. Add one to place calls through a trunk.</div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Pattern</th>
                <th>Destination</th>
                <th>Strip</th>
                <th>Prepend</th>
                <th>On</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {outbound.map((r) => (
                <tr key={r.id}>
                  <td>{r.name}</td>
                  <td>{r.pattern}</td>
                  <td>
                    {r.destType === "ivr" ? (
                      <span className="badge">IVR: {r.ivr}</span>
                    ) : (
                      `trunk: ${r.trunk}`
                    )}
                  </td>
                  <td>{r.destType === "ivr" ? "—" : r.strip}</td>
                  <td>{r.destType === "ivr" ? "—" : r.prepend || "-"}</td>
                  <td>{r.enabled ? "yes" : "no"}</td>
                  <td className="row-action">
                    {canEdit && <button className="btn small" onClick={() => setEditOut(r)}>Edit</button>}
                    {canDelete && <button className="btn danger" onClick={() => delOut(r.id)}>Delete</button>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section className="panel">
        <header>
          Inbound Routes
          {canCreate && (
            <button className="btn small" style={{ float: "right" }} onClick={() => setEditIn({ ...BLANK_IN })}>
              + New
            </button>
          )}
        </header>
        {inbound.length === 0 ? (
          <div className="empty">No inbound routes. Add one to send incoming DIDs to an extension.</div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>DID (match)</th>
                <th>Destination</th>
                <th>On</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {inbound.map((r) => (
                <tr key={r.id}>
                  <td>{r.name}</td>
                  <td>{r.did}</td>
                  <td>
                    {r.destination.includes(":") ? (
                      <span className="badge">{r.destination.replace(":", ": ")}</span>
                    ) : (
                      `ext: ${r.destination}`
                    )}
                  </td>
                  <td>{r.enabled ? "yes" : "no"}</td>
                  <td className="row-action">
                    {canEdit && <button className="btn small" onClick={() => setEditIn(r)}>Edit</button>}
                    {canDelete && <button className="btn danger" onClick={() => delIn(r.id)}>Delete</button>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      {editOut && (
        <OutForm
          initial={editOut}
          trunks={trunks}
          ivrs={ivrs}
          onClose={() => setEditOut(null)}
          onSaved={(m) => { notify({ kind: "ok", text: m }); setEditOut(null); refresh(); }}
          onError={(m) => notify({ kind: "err", text: m })}
        />
      )}
      {editIn && (
        <InForm
          initial={editIn}
          ivrs={ivrs}
          ingroups={ingroups}
          sounds={sounds}
          trunks={trunks}
          onClose={() => setEditIn(null)}
          onSaved={(m) => { notify({ kind: "ok", text: m }); setEditIn(null); refresh(); }}
          onError={(m) => notify({ kind: "err", text: m })}
        />
      )}
    </>
  );
}

function OutForm({
  initial, trunks, ivrs, onClose, onSaved, onError,
}: {
  initial: OutboundRoute;
  trunks: Trunk[];
  ivrs: IVR[];
  onClose: () => void;
  onSaved: (m: string) => void;
  onError: (m: string) => void;
}) {
  const [f, setF] = useState<OutboundRoute>(initial);
  const [busy, setBusy] = useState(false);
  const isNew = f.id === 0;
  const toIVR = f.destType === "ivr";
  const set = <K extends keyof OutboundRoute>(k: K, v: OutboundRoute[K]) =>
    setF((p) => ({ ...p, [k]: v }));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (toIVR && !f.ivr) return onError("Choose an IVR menu");
    if (!toIVR && !f.trunk) return onError("Choose a trunk");
    setBusy(true);
    try {
      if (isNew) { await createOutboundRoute(f); onSaved(`Created route ${f.name}`); }
      else { await updateOutboundRoute(f.id, f); onSaved(`Updated route ${f.name}`); }
    } catch (err) { onError((err as Error).message); }
    finally { setBusy(false); }
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <header>{isNew ? "New Outbound Route" : `Edit ${f.name}`}</header>
        <form className="form" onSubmit={submit}>
          <div className="form-row">
            <label>Name<input value={f.name} onChange={(e) => set("name", e.target.value)} /></label>
            <label>
              Send to
              <select value={f.destType} onChange={(e) => set("destType", e.target.value as OutboundRoute["destType"])}>
                <option value="trunk">Trunk (external)</option>
                <option value="ivr">IVR menu</option>
              </select>
            </label>
          </div>
          <div className="form-row">
            <label>
              Dial pattern <span className="hint-inline">(e.g. _9. = 9 then any)</span>
              <input value={f.pattern} onChange={(e) => set("pattern", e.target.value)} />
            </label>
            {toIVR ? (
              <label>
                IVR menu
                <select value={f.ivr} onChange={(e) => set("ivr", e.target.value)}>
                  <option value="">— choose —</option>
                  {ivrs.map((v) => <option key={v.id} value={v.name}>{v.name}</option>)}
                </select>
              </label>
            ) : (
              <label>
                Trunk
                <select value={f.trunk} onChange={(e) => set("trunk", e.target.value)}>
                  <option value="">— choose —</option>
                  {trunks.map((t) => <option key={t.name} value={t.name}>{t.name}</option>)}
                </select>
              </label>
            )}
          </div>
          {!toIVR && (
            <div className="form-row">
              <label>
                Strip <span className="hint-inline">(leading digits)</span>
                <input type="number" min={0} value={f.strip} onChange={(e) => set("strip", parseInt(e.target.value || "0", 10))} />
              </label>
              <label>Prepend<input value={f.prepend} onChange={(e) => set("prepend", e.target.value)} /></label>
            </div>
          )}
          {!toIVR && (
            <label>Caller ID override<input value={f.callerId} onChange={(e) => set("callerId", e.target.value)} /></label>
          )}
          <label className="check">
            <input type="checkbox" checked={f.enabled} onChange={(e) => set("enabled", e.target.checked)} />
            Enabled
          </label>
          {toIVR ? (
            <p className="hint-inline">
              Result: dialing a number matching the pattern sends the caller into
              the <b>{f.ivr || "…"}</b> auto-attendant menu. Useful for an internal
              "dial 500 for the menu" or steering certain numbers to an announcement.
            </p>
          ) : (
            <p className="hint-inline">
              Result: dialing a number matching the pattern strips {f.strip} leading
              digit(s), prepends "{f.prepend}", and dials it out via <b>{f.trunk || "…"}</b>.
              Keep outbound numbers longer than 4 digits (or prefixed) so they don't
              collide with internal extensions.
            </p>
          )}
          <div className="form-actions">
            <button type="button" className="btn ghost" onClick={onClose}>Cancel</button>
            <button type="submit" className="btn" disabled={busy}>{busy ? "Saving…" : "Save"}</button>
          </div>
        </form>
      </div>
    </div>
  );
}

// parseDest / encodeDest translate between the stored destination string
// (bare number = extension; "type:value" otherwise; "hangup") and the picker.
function parseInDest(dest: string): { type: string; value: string } {
  const d = (dest || "").trim();
  if (!d) return { type: "extension", value: "" };
  if (d === "hangup") return { type: "hangup", value: "" };
  if (d.includes(":")) {
    const [t, v] = d.split(/:(.*)/s);
    return { type: t, value: v || "" };
  }
  return { type: "extension", value: d };
}
function encodeInDest(type: string, value: string): string {
  if (type === "hangup") return "hangup";
  if (type === "extension") return value.trim();
  return `${type}:${value.trim()}`;
}
// External value is "<number>@<trunk>".
function extNum(v: string): string {
  const i = v.lastIndexOf("@");
  return i >= 0 ? v.slice(0, i) : v;
}
function extTrunk(v: string): string {
  const i = v.lastIndexOf("@");
  return i >= 0 ? v.slice(i + 1) : "";
}
// Queue value is "<agents>;<holdprompt>".
function qAgents(v: string): string {
  const i = v.indexOf(";");
  return i >= 0 ? v.slice(0, i) : v;
}
function qPrompt(v: string): string {
  const i = v.indexOf(";");
  return i >= 0 ? v.slice(i + 1) : "";
}

function InForm({
  initial, ivrs, ingroups, sounds, trunks, onClose, onSaved, onError,
}: {
  initial: InboundRoute;
  ivrs: IVR[];
  ingroups: string[];
  sounds: SoundFile[];
  trunks: Trunk[];
  onClose: () => void;
  onSaved: (m: string) => void;
  onError: (m: string) => void;
}) {
  const [f, setF] = useState<InboundRoute>(initial);
  const parsed = parseInDest(initial.destination);
  const [dType, setDType] = useState(parsed.type);
  const [dVal, setDVal] = useState(parsed.value);
  const [busy, setBusy] = useState(false);
  const isNew = f.id === 0;
  const set = <K extends keyof InboundRoute>(k: K, v: InboundRoute[K]) =>
    setF((p) => ({ ...p, [k]: v }));

  const needsVal = dType !== "hangup";

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (needsVal && !dVal.trim()) return onError("Choose / enter a destination");
    const payload = { ...f, destination: encodeInDest(dType, dVal) };
    setBusy(true);
    try {
      if (isNew) { await createInboundRoute(payload); onSaved(`Created route ${f.name}`); }
      else { await updateInboundRoute(f.id, payload); onSaved(`Updated route ${f.name}`); }
    } catch (err) { onError((err as Error).message); }
    finally { setBusy(false); }
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <header>{isNew ? "New Inbound Route" : `Edit ${f.name}`}</header>
        <form className="form" onSubmit={submit}>
          <label>Name<input value={f.name} onChange={(e) => set("name", e.target.value)} /></label>
          <div className="form-row">
            <label>
              DID / number to match <span className="hint-inline">(use _. for any)</span>
              <input value={f.did} placeholder="e.g. 18005551212 or _." onChange={(e) => set("did", e.target.value)} />
            </label>
            <label>
              Send to
              <select value={dType} onChange={(e) => setDType(e.target.value)}>
                <option value="extension">Extension</option>
                <option value="ingroup">In-group (ACD queue)</option>
                <option value="queue">Ring agents — hunt group, no reporting</option>
                <option value="external">External / GSM</option>
                <option value="ivr">IVR menu</option>
                <option value="voicemail">Voicemail</option>
                <option value="playback">Play message</option>
                <option value="hangup">Hang up</option>
              </select>
            </label>
          </div>
          {needsVal && (
            <label>
              {dType === "ivr" ? "IVR menu" : dType === "ingroup" ? "In-group" : dType === "voicemail" ? "Mailbox" : dType === "playback" ? "Prompt" : dType === "external" ? "External number via trunk" : dType === "queue" ? "Agents + hold prompt" : "Destination extension"}
              {dType === "ingroup" ? (
                <select value={dVal} onChange={(e) => setDVal(e.target.value)}>
                  <option value="">— choose in-group —</option>
                  {ingroups.map((g) => <option key={g} value={g}>{g}</option>)}
                </select>
              ) : dType === "ivr" ? (
                <select value={dVal} onChange={(e) => setDVal(e.target.value)}>
                  <option value="">— choose menu —</option>
                  {ivrs.map((v) => <option key={v.id} value={v.name}>{v.name}</option>)}
                </select>
              ) : dType === "playback" ? (
                <select value={dVal} onChange={(e) => setDVal(e.target.value)}>
                  <option value="">— choose prompt —</option>
                  {sounds.map((s) => <option key={s.name} value={s.ref}>{s.name}</option>)}
                </select>
              ) : dType === "external" ? (
                <div style={{ display: "flex", gap: 8 }}>
                  <input
                    value={extNum(dVal)}
                    placeholder="+9198… (GSM)"
                    onChange={(e) => setDVal(`${e.target.value}@${extTrunk(dVal)}`)}
                  />
                  <select value={extTrunk(dVal)} onChange={(e) => setDVal(`${extNum(dVal)}@${e.target.value}`)} style={{ flex: "0 0 150px" }}>
                    <option value="">via trunk…</option>
                    {trunks.map((t) => <option key={t.name} value={t.name}>{t.name}</option>)}
                  </select>
                </div>
              ) : dType === "queue" ? (
                <div style={{ display: "flex", gap: 8 }}>
                  <input
                    value={qAgents(dVal)}
                    placeholder="agents e.g. 1001&1002"
                    onChange={(e) => setDVal(`${e.target.value};${qPrompt(dVal)}`)}
                  />
                  <select value={qPrompt(dVal)} onChange={(e) => setDVal(`${qAgents(dVal)};${e.target.value}`)} style={{ flex: "0 0 150px" }}>
                    <option value="">hold prompt…</option>
                    {sounds.map((s) => <option key={s.name} value={s.ref}>{s.name}</option>)}
                  </select>
                </div>
              ) : (
                <input value={dVal} placeholder={dType === "voicemail" ? "1001" : "1001"} onChange={(e) => setDVal(e.target.value)} />
              )}
            </label>
          )}
          <label className="check">
            <input type="checkbox" checked={f.enabled} onChange={(e) => set("enabled", e.target.checked)} />
            Enabled
          </label>
          <p className="hint-inline">
            Incoming calls on a trunk whose dialed number matches the DID are sent
            to this destination — ring an extension, drop into an IVR menu, go to
            voicemail, play a message, or hang up.
          </p>
          <div className="form-actions">
            <button type="button" className="btn ghost" onClick={onClose}>Cancel</button>
            <button type="submit" className="btn" disabled={busy}>{busy ? "Saving…" : "Save"}</button>
          </div>
        </form>
      </div>
    </div>
  );
}
