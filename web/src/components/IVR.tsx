import { useCallback, useEffect, useRef, useState } from "react";
import {
  createIVR,
  deleteIVR,
  deleteSound,
  getIVR,
  listIVRs,
  listSounds,
  listTrunks,
  soundAudioUrl,
  updateIVR,
  uploadSound,
  can,
  type IVR,
  type IVRDestType,
  type IVROption,
  type Me,
  type SoundFile,
  type Trunk,
} from "../api";
import type { Notify } from "../types";
import { IVRBuilder } from "./IVRBuilder";

const DEST_TYPES: { value: IVRDestType; label: string }[] = [
  { value: "extension", label: "Ring extension" },
  { value: "ingroup", label: "Send to in-group (ACD queue)" },
  { value: "queue", label: "Ring agents — hunt group, no ACD reporting" },
  { value: "external", label: "Call external / GSM" },
  { value: "ivr", label: "Go to sub-menu" },
  { value: "voicemail", label: "Voicemail" },
  { value: "playback", label: "Play message" },
  { value: "repeat", label: "Repeat menu" },
  { value: "hangup", label: "Hang up" },
];

const DEST_LABEL: Record<IVRDestType, string> = {
  extension: "Ring ext.",
  ingroup: "In-group",
  queue: "Ring agents",
  external: "Call GSM",
  ivr: "Sub-menu",
  voicemail: "Voicemail",
  playback: "Play msg",
  repeat: "Repeat",
  hangup: "Hang up",
};

function needsTarget(t: IVRDestType): boolean {
  return t !== "repeat" && t !== "hangup";
}

// External destinations encode "<number>@<trunk>".
export function parseExternal(v: string): { num: string; trunk: string } {
  const i = v.lastIndexOf("@");
  return i >= 0 ? { num: v.slice(0, i), trunk: v.slice(i + 1) } : { num: v, trunk: "" };
}
export function makeExternal(num: string, trunk: string): string {
  return `${num}@${trunk}`;
}

// Queue ("hold if busy") destinations encode "<agents>;<holdprompt>".
export function parseQueue(v: string): { agents: string; prompt: string } {
  const i = v.indexOf(";");
  return i >= 0 ? { agents: v.slice(0, i), prompt: v.slice(i + 1) } : { agents: v, prompt: "" };
}
export function makeQueue(agents: string, prompt: string): string {
  return `${agents};${prompt}`;
}

const BLANK: IVR = {
  id: 0,
  name: "",
  greeting: "",
  timeoutSec: 5,
  maxRetries: 3,
  invalidDest: "",
  timeoutDest: "",
  options: [{ digit: "1", destType: "extension", destValue: "", label: "" }],
};

export default function IVRPage({ notify, me }: { notify: Notify; me: Me }) {
  const canCreate = can(me, "ivr", "create");
  const canEdit = can(me, "ivr", "edit");
  const canDelete = can(me, "ivr", "delete");
  const [rows, setRows] = useState<IVR[]>([]);
  const [sounds, setSounds] = useState<SoundFile[]>([]);
  const [trunks, setTrunks] = useState<Trunk[]>([]);
  const [soundsOK, setSoundsOK] = useState(true);
  const [loading, setLoading] = useState(true);
  const [editing, setEditing] = useState<IVR | null>(null);
  const [isNew, setIsNew] = useState(false);
  const [building, setBuilding] = useState<IVR | null>(null);
  const [buildNew, setBuildNew] = useState(false);
  const importRef = useRef<HTMLInputElement>(null);

  const refresh = useCallback(() => {
    setLoading(true);
    listIVRs()
      .then(setRows)
      .catch((e) => notify({ kind: "err", text: (e as Error).message }))
      .finally(() => setLoading(false));
  }, [notify]);

  const refreshSounds = useCallback(() => {
    listSounds()
      .then((r) => {
        setSounds(r.sounds);
        setSoundsOK(r.configured);
      })
      .catch(() => setSoundsOK(false));
  }, []);

  useEffect(refresh, [refresh]);
  useEffect(refreshSounds, [refreshSounds]);
  useEffect(() => {
    listTrunks().then(setTrunks).catch(() => {});
  }, []);

  const openNew = () => {
    setIsNew(true);
    setEditing({ ...BLANK, options: [{ digit: "1", destType: "extension", destValue: "", label: "" }] });
  };
  const openEdit = async (id: number) => {
    try {
      setIsNew(false);
      setEditing(await getIVR(id));
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };
  const openBuildNew = () => {
    setBuildNew(true);
    setBuilding({ ...BLANK, options: [{ digit: "1", destType: "extension", destValue: "", label: "" }] });
  };
  const openBuild = async (id: number) => {
    try {
      setBuildNew(false);
      setBuilding(await getIVR(id));
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  // Create a blank submenu on the fly (used by the "＋ new submenu" action in
  // the Sub-menu block), then refresh so it appears in pickers.
  const createSubmenu = async (nm: string) => {
    const name = nm.trim();
    if (!name) return;
    try {
      await createIVR({
        name,
        greeting: "",
        timeoutSec: 5,
        maxRetries: 3,
        invalidDest: "",
        timeoutDest: "",
        options: [],
      });
      notify({ kind: "ok", text: `Created submenu ${name}` });
      refresh();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
      throw e;
    }
  };

  // Open a menu (by name) in the builder — for editing a submenu from a parent.
  const openIVRByName = async (nm: string) => {
    try {
      const all = await listIVRs();
      const found = all.find((r) => r.name === nm);
      if (!found) {
        notify({ kind: "err", text: `Menu "${nm}" not found — save it first.` });
        return;
      }
      setBuildNew(false);
      setBuilding(await getIVR(found.id));
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };
  const duplicate = async (v: IVR) => {
    try {
      const full = await getIVR(v.id);
      setIsNew(true);
      setEditing({ ...full, id: 0, name: `${full.name}-copy` });
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };
  const onDelete = async (v: IVR) => {
    if (!confirm(`Delete IVR "${v.name}"?`)) return;
    try {
      await deleteIVR(v.id);
      notify({ kind: "ok", text: `Deleted ${v.name}` });
      refresh();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  // Export one IVR as a portable JSON "script".
  const exportIVR = async (v: IVR) => {
    try {
      const full = await getIVR(v.id);
      const clean = { ...full, id: undefined };
      const blob = new Blob([JSON.stringify(clean, null, 2)], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `ivr-${full.name}.json`;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  // Import a JSON script: a single object opens the editor prefilled; an array
  // creates every menu it contains.
  const importFile = async (file: File) => {
    let parsed: unknown;
    try {
      parsed = JSON.parse(await file.text());
    } catch {
      notify({ kind: "err", text: "Not valid JSON." });
      return;
    }
    const arr = Array.isArray(parsed) ? parsed : [parsed];
    const items = arr.filter((x) => x && typeof x === "object") as Partial<IVR>[];
    if (items.length === 0) {
      notify({ kind: "err", text: "No IVR definitions found in file." });
      return;
    }
    if (items.length === 1) {
      const v = normalizeImported(items[0]);
      setIsNew(true);
      setEditing(v);
      notify({ kind: "ok", text: "Loaded — review and click Create." });
      return;
    }
    let ok = 0;
    const errs: string[] = [];
    for (const it of items) {
      try {
        await createIVR(normalizeImported(it));
        ok++;
      } catch (e) {
        errs.push(`${it.name || "?"}: ${(e as Error).message}`);
      }
    }
    notify({
      kind: errs.length ? "err" : "ok",
      text: `Imported ${ok}/${items.length}${errs.length ? ` — ${errs[0]}` : ""}`,
    });
    refresh();
  };

  return (
    <>
      <div className="page-head">
        <h2>IVR / Auto-Attendant</h2>
        <div className="row-action">
          <input
            ref={importRef}
            type="file"
            accept=".json,application/json"
            style={{ display: "none" }}
            onChange={(e) => e.target.files?.[0] && importFile(e.target.files[0])}
          />
          {canCreate && (
            <button className="btn ghost" onClick={() => importRef.current?.click()}>
              Import script
            </button>
          )}
          {canCreate && (
            <button className="btn ghost" onClick={openNew}>
              + New (form)
            </button>
          )}
          {canCreate && (
            <button className="btn" onClick={openBuildNew}>
              ✎ Visual Builder
            </button>
          )}
        </div>
      </div>

      <PromptLibrary
        sounds={sounds}
        configured={soundsOK}
        notify={notify}
        onChange={refreshSounds}
        canCreate={canCreate}
        canDelete={canDelete}
      />

      <section className="panel">
        <header>Menus</header>
        {loading ? (
          <div className="empty">Loading…</div>
        ) : rows.length === 0 ? (
          <div className="empty">
            No IVR menus yet. Create one, then point an inbound route at it
            (destination <code>ivr:&lt;name&gt;</code>).
          </div>
        ) : (
          <div className="ivr-cards">
            {rows.map((v) => (
              <div className="ivr-card" key={v.id}>
                <div className="ivr-card-head">
                  <div>
                    <div className="ivr-card-name">{v.name}</div>
                    <div className="ivr-card-sub">
                      {v.greeting ? `🔊 ${v.greeting}` : "no greeting"} · {v.timeoutSec}s × {v.maxRetries}
                    </div>
                  </div>
                  <div className="row-action">
                    {canEdit && (
                      <button className="btn small" onClick={() => openBuild(v.id)}>
                        Build
                      </button>
                    )}
                    {canEdit && (
                      <button className="btn ghost small" onClick={() => openEdit(v.id)}>
                        Edit
                      </button>
                    )}
                    {canCreate && (
                      <button className="btn ghost small" onClick={() => duplicate(v)}>
                        Duplicate
                      </button>
                    )}
                    <button className="btn ghost small" onClick={() => exportIVR(v)}>
                      Export
                    </button>
                    {canDelete && (
                      <button className="btn danger" onClick={() => onDelete(v)}>
                        Delete
                      </button>
                    )}
                  </div>
                </div>
                <FlowMap ivr={v} />
              </div>
            ))}
          </div>
        )}
        <p className="hint-inline" style={{ padding: "0 16px 16px" }}>
          Route a DID to a menu on the <strong>Routing</strong> page by setting
          the inbound destination to <code>ivr:&lt;name&gt;</code>. Greetings and
          "Play message" actions use prompts from the library above (or any
          system sound path).
        </p>
      </section>

      {editing && (
        <IVRForm
          initial={editing}
          isNew={isNew}
          ivrNames={rows.map((r) => r.name)}
          sounds={sounds}
          trunks={trunks}
          onClose={() => setEditing(null)}
          onSaved={(msg) => {
            notify({ kind: "ok", text: msg });
            setEditing(null);
            refresh();
          }}
          onError={(msg) => notify({ kind: "err", text: msg })}
        />
      )}

      {building && (
        <IVRBuilder
          key={building.id || (buildNew ? "new" : "edit")}
          initial={building}
          isNew={buildNew}
          ivrNames={rows.map((r) => r.name)}
          sounds={sounds}
          trunks={trunks}
          onCreateSubmenu={createSubmenu}
          onOpenIVR={openIVRByName}
          onCancel={() => setBuilding(null)}
          onSave={async (v) => {
            if (!v.name.trim()) {
              notify({ kind: "err", text: "Menu name is required" });
              throw new Error("name required");
            }
            try {
              if (buildNew) await createIVR(v);
              else await updateIVR(v.id, v);
              notify({ kind: "ok", text: `Saved IVR ${v.name}` });
              setBuilding(null);
              refresh();
            } catch (e) {
              notify({ kind: "err", text: (e as Error).message });
              throw e;
            }
          }}
        />
      )}
    </>
  );
}

// normalizeImported coerces a loosely-shaped JSON object into a valid IVR.
function normalizeImported(o: Partial<IVR>): IVR {
  return {
    id: 0,
    name: (o.name || "").trim(),
    greeting: o.greeting || "",
    timeoutSec: o.timeoutSec || 5,
    maxRetries: o.maxRetries || 3,
    invalidDest: o.invalidDest || "",
    timeoutDest: o.timeoutDest || "",
    options: Array.isArray(o.options)
      ? o.options.map((x) => ({
          digit: String(x.digit ?? ""),
          destType: (x.destType as IVRDestType) || "extension",
          destValue: x.destValue || "",
          label: x.label || "",
        }))
      : [],
  };
}

function fmtSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

// PromptLibrary manages the uploaded .wav prompts used for greetings/messages.
function PromptLibrary({
  sounds,
  configured,
  notify,
  onChange,
  canCreate,
  canDelete,
}: {
  sounds: SoundFile[];
  configured: boolean;
  notify: Notify;
  onChange: () => void;
  canCreate: boolean;
  canDelete: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const [name, setName] = useState("");
  const fileRef = useRef<HTMLInputElement>(null);
  const [pending, setPending] = useState<File | null>(null);

  // The prompt list (one audio player per row) grows tall fast, so it is
  // collapsed by default and the preference is remembered across reloads.
  const [collapsed, setCollapsed] = useState<boolean>(() => {
    const v = localStorage.getItem("tpbx.promptlib.collapsed");
    return v === null ? true : v === "1";
  });
  useEffect(() => {
    localStorage.setItem("tpbx.promptlib.collapsed", collapsed ? "1" : "0");
  }, [collapsed]);
  const totalSize = sounds.reduce((sum, s) => sum + (s.size || 0), 0);

  const doUpload = async () => {
    if (!pending) {
      fileRef.current?.click();
      return;
    }
    setBusy(true);
    try {
      const r = await uploadSound(pending, name || undefined);
      notify({ kind: "ok", text: `Uploaded prompt "${r.name}"${r.note ? " — " + r.note : ""}` });
      setPending(null);
      setName("");
      onChange();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };

  const remove = async (s: SoundFile) => {
    if (!confirm(`Delete prompt "${s.name}"? Any IVR using it will fall silent.`)) return;
    try {
      await deleteSound(s.name);
      notify({ kind: "ok", text: `Deleted ${s.name}` });
      onChange();
    } catch (e) {
      notify({ kind: "err", text: (e as Error).message });
    }
  };

  return (
    <section className="panel">
      <header style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12 }}>
        <span>Prompt Library</span>
        {configured && sounds.length > 0 && (
          <button
            type="button"
            className="btn ghost small"
            aria-expanded={!collapsed}
            onClick={() => setCollapsed((c) => !c)}
            title={collapsed ? "Show all prompts" : "Minimize the prompt list"}
          >
            {collapsed ? `▸ Show all (${sounds.length})` : "▾ Minimize"}
          </button>
        )}
      </header>
      {!configured ? (
        <div className="empty">
          Sound uploads are not configured on this server (set{" "}
          <code>TPBX_SOUNDS_DIR</code>). You can still reference existing system
          sound paths in greetings.
        </div>
      ) : (
        <>
          <div className="sound-upload">
            <input
              ref={fileRef}
              type="file"
              accept="audio/*,.wav,.mp3,.m4a,.ogg,.flac,.aac"
              style={{ display: "none" }}
              onChange={(e) => {
                const f = e.target.files?.[0] || null;
                setPending(f);
                if (f && !name) setName(f.name.replace(/\.[^.]+$/, "").replace(/[^A-Za-z0-9_-]/g, "-"));
              }}
            />
            <button type="button" className="btn ghost small" onClick={() => fileRef.current?.click()}>
              {pending ? `📄 ${pending.name}` : "Choose audio…"}
            </button>
            <input
              placeholder="prompt name (e.g. welcome)"
              value={name}
              onChange={(e) => setName(e.target.value.replace(/[^A-Za-z0-9_-]/g, ""))}
              style={{ maxWidth: 220 }}
            />
            {canCreate && (
              <button type="button" className="btn small" disabled={busy || !pending} onClick={doUpload}>
                {busy ? "Uploading…" : "Upload"}
              </button>
            )}
          </div>
          {sounds.length === 0 ? (
            <div className="empty">
              No prompts yet. Upload any audio (WAV, MP3, M4A…) — it is auto-converted to a supported audio format.
            </div>
          ) : collapsed ? (
            <div
              className="empty"
              style={{ cursor: "pointer" }}
              onClick={() => setCollapsed(false)}
              title="Show all prompts"
            >
              {sounds.length} prompt{sounds.length === 1 ? "" : "s"} · {fmtSize(totalSize)} —
              list minimized. Click “Show all” to expand.
            </div>
          ) : (
            <table>
              <thead>
                <tr>
                  <th>Prompt</th>
                  <th>Reference</th>
                  <th>Size</th>
                  <th>Preview</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {sounds.map((s) => (
                  <tr key={s.name}>
                    <td>
                      <strong>{s.name}</strong>
                    </td>
                    <td>
                      <code>{s.ref}</code>
                    </td>
                    <td>{fmtSize(s.size)}</td>
                    <td>
                      <audio controls preload="none" src={soundAudioUrl(s.name)} className="sound-player" />
                    </td>
                    <td className="row-action">
                      {canDelete && (
                        <button className="btn danger" onClick={() => remove(s)}>
                          Delete
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <p className="hint-inline" style={{ padding: "0 16px 16px" }}>
            Prompts are stored under the system sound library and referenced as{" "}
            <code>{sounds[0]?.ref?.split("/")[0] || "tpbx"}/&lt;name&gt;</code>. For
            best compatibility convert to 8kHz / 16-bit mono PCM WAV before upload.
          </p>
        </>
      )}
    </section>
  );
}

// groupByDigit collapses options that share a key into an ordered chain of
// steps (so "play then ring" shows as one key with two steps).
function groupByDigit(options: IVROption[]): { digit: string; steps: IVROption[] }[] {
  const order: string[] = [];
  const map: Record<string, IVROption[]> = {};
  for (const o of options) {
    if (!o.digit) continue;
    if (!map[o.digit]) {
      map[o.digit] = [];
      order.push(o.digit);
    }
    map[o.digit].push(o);
  }
  return order.map((digit) => ({ digit, steps: map[digit] }));
}

// FlowMap draws a compact, read-only diagram of a menu: greeting -> keys ->
// destinations, plus the invalid/timeout fallbacks.
function FlowMap({ ivr }: { ivr: IVR }) {
  const destText = (t: IVRDestType, v: string) => {
    if (t === "external") {
      const { num, trunk } = parseExternal(v);
      return `Call ${num || "?"}${trunk ? ` via ${trunk}` : ""}`;
    }
    if (t === "queue") {
      const { agents } = parseQueue(v);
      return `Ring agents ${agents || "?"} (hold if busy)`;
    }
    return needsTarget(t) ? `${DEST_LABEL[t]} ${v || "?"}` : DEST_LABEL[t];
  };
  return (
    <div className="flowmap">
      <div className="fm-greeting">
        <span className="fm-ico">🔊</span>
        {ivr.greeting || "no greeting"}
      </div>
      <div className="fm-keys">
        {ivr.options.filter((o) => o.digit).length === 0 && <span className="fm-empty">no keys</span>}
        {groupByDigit(ivr.options).map((grp, i) => (
          <div className="fm-key" key={i}>
            <span className="fm-digit">{grp.digit}</span>
            <span className="fm-arrow">→</span>
            <span className="fm-dest">
              {grp.steps.map((o, j) => (
                <span key={j}>
                  {j > 0 && <span className="fm-arrow"> → </span>}
                  {destText(o.destType, o.destValue)}
                </span>
              ))}
              {grp.steps[0]?.label ? <span className="fm-tag">{grp.steps[0].label}</span> : null}
            </span>
          </div>
        ))}
      </div>
      <div className="fm-fallbacks">
        <span>invalid: {fallbackText(ivr.invalidDest)}</span>
        <span>timeout: {fallbackText(ivr.timeoutDest)}</span>
      </div>
    </div>
  );
}

function fallbackText(dest: string): string {
  if (!dest) return "replay / hang up";
  if (dest === "hangup") return "hang up";
  const [t, v] = dest.split(/:(.*)/s);
  return `${t} ${v || ""}`.trim();
}

function IVRForm({
  initial,
  isNew,
  ivrNames,
  sounds,
  trunks,
  onClose,
  onSaved,
  onError,
}: {
  initial: IVR;
  isNew: boolean;
  ivrNames: string[];
  sounds: SoundFile[];
  trunks: Trunk[];
  onClose: () => void;
  onSaved: (msg: string) => void;
  onError: (msg: string) => void;
}) {
  const [f, setF] = useState<IVR>(initial);
  const [busy, setBusy] = useState(false);
  const set = <K extends keyof IVR>(k: K, v: IVR[K]) => setF((p) => ({ ...p, [k]: v }));

  const setOpt = (i: number, patch: Partial<IVROption>) =>
    setF((p) => ({ ...p, options: p.options.map((o, j) => (j === i ? { ...o, ...patch } : o)) }));
  const addOpt = () =>
    setF((p) => ({
      ...p,
      options: [...p.options, { digit: "", destType: "extension", destValue: "", label: "" }],
    }));
  const rmOpt = (i: number) => setF((p) => ({ ...p, options: p.options.filter((_, j) => j !== i) }));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      if (isNew) {
        await createIVR(f);
        onSaved(`Created IVR ${f.name}`);
      } else {
        await updateIVR(f.id, f);
        onSaved(`Updated IVR ${f.name}`);
      }
    } catch (err) {
      onError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: 720 }}>
        <header>{isNew ? "New IVR" : `Edit ${f.name}`}</header>
        <form className="form" onSubmit={submit}>
          <div className="form-row">
            <label>
              Name <span className="hint-inline">(context id)</span>
              <input value={f.name} disabled={!isNew} placeholder="main" onChange={(e) => set("name", e.target.value)} />
            </label>
            <GreetingPicker
              value={f.greeting}
              sounds={sounds}
              onChange={(v) => set("greeting", v)}
            />
          </div>

          <div className="form-row">
            <label>
              Wait for input (seconds)
              <input
                type="number"
                value={f.timeoutSec}
                onChange={(e) => set("timeoutSec", parseInt(e.target.value || "5", 10))}
              />
            </label>
            <label>
              Retries before giving up
              <input
                type="number"
                value={f.maxRetries}
                onChange={(e) => set("maxRetries", parseInt(e.target.value || "3", 10))}
              />
            </label>
          </div>

          <div className="ivr-opts">
            <div className="ivr-opts-head">
              <span>Key</span>
              <span>Action</span>
              <span>Target</span>
              <span>Label</span>
              <span></span>
            </div>
            {f.options.map((o, i) => (
              <div className="ivr-opt-row" key={i}>
                <input
                  className="k"
                  value={o.digit}
                  maxLength={1}
                  placeholder="1"
                  onChange={(e) => setOpt(i, { digit: e.target.value.replace(/[^0-9*#]/g, "") })}
                />
                <select
                  value={o.destType}
                  onChange={(e) => setOpt(i, { destType: e.target.value as IVRDestType })}
                >
                  {DEST_TYPES.map((t) => (
                    <option key={t.value} value={t.value}>
                      {t.label}
                    </option>
                  ))}
                </select>
                <TargetInput
                  opt={o}
                  ivrNames={ivrNames}
                  sounds={sounds}
                  trunks={trunks}
                  onChange={(v) => setOpt(i, { destValue: v })}
                />
                <input value={o.label} placeholder="Sales" onChange={(e) => setOpt(i, { label: e.target.value })} />
                <button type="button" className="btn small danger" onClick={() => rmOpt(i)}>
                  ✕
                </button>
              </div>
            ))}
            <button type="button" className="btn ghost small" onClick={addOpt}>
              + Add key
            </button>
            <p className="hint-inline">
              Tip: add two rows with the <strong>same key</strong> to chain steps
              in order — e.g. "1 → Play message" then "1 → Ring extension". The
              visual Builder shows this as connected blocks.
            </p>
          </div>

          <div className="form-row">
            <DestPicker label="On invalid key" value={f.invalidDest} onChange={(v) => set("invalidDest", v)} ivrNames={ivrNames} sounds={sounds} trunks={trunks} />
            <DestPicker label="On timeout" value={f.timeoutDest} onChange={(v) => set("timeoutDest", v)} ivrNames={ivrNames} sounds={sounds} trunks={trunks} />
          </div>

          <div className="ivr-preview">
            <div className="ivr-preview-head">Flow preview</div>
            <FlowMap ivr={f} />
          </div>

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

// GreetingPicker lets the operator pick an uploaded prompt (with inline preview)
// or type any system sound path.
function GreetingPicker({
  value,
  sounds,
  onChange,
}: {
  value: string;
  sounds: SoundFile[];
  onChange: (v: string) => void;
}) {
  const match = sounds.find((s) => s.ref === value);
  const custom = !!value && !match;
  const [mode, setMode] = useState<"lib" | "custom">(custom ? "custom" : "lib");

  return (
    <label>
      Greeting sound
      <div style={{ display: "flex", gap: 8, alignItems: "center" }}>
        {mode === "lib" ? (
          <select
            value={match ? match.ref : ""}
            onChange={(e) => {
              if (e.target.value === "__custom__") {
                setMode("custom");
                onChange("");
              } else onChange(e.target.value);
            }}
            style={{ flex: 1 }}
          >
            <option value="">— none —</option>
            {sounds.map((s) => (
              <option key={s.name} value={s.ref}>
                {s.name}
              </option>
            ))}
            <option value="__custom__">Custom path…</option>
          </select>
        ) : (
          <input
            value={value}
            placeholder="custom/welcome"
            onChange={(e) => onChange(e.target.value)}
            style={{ flex: 1 }}
          />
        )}
        {match && <audio controls preload="none" src={soundAudioUrl(match.name)} className="sound-player sm" />}
        {mode === "custom" && (
          <button type="button" className="btn ghost small" onClick={() => setMode("lib")}>
            Library
          </button>
        )}
      </div>
    </label>
  );
}

// TargetInput adapts to the selected action type.
function TargetInput({
  opt,
  ivrNames,
  sounds,
  trunks,
  onChange,
}: {
  opt: IVROption;
  ivrNames: string[];
  sounds: SoundFile[];
  trunks: Trunk[];
  onChange: (v: string) => void;
}) {
  if (!needsTarget(opt.destType)) {
    return <input disabled placeholder="—" value="" />;
  }
  if (opt.destType === "external") {
    const { num, trunk } = parseExternal(opt.destValue);
    return (
      <div style={{ display: "flex", gap: 6 }}>
        <input
          value={num}
          placeholder="+9198… (GSM)"
          onChange={(e) => onChange(makeExternal(e.target.value, trunk))}
        />
        <select value={trunk} onChange={(e) => onChange(makeExternal(num, e.target.value))}>
          <option value="">via trunk…</option>
          {trunks.map((t) => (
            <option key={t.name} value={t.name}>
              {t.name}
            </option>
          ))}
        </select>
      </div>
    );
  }
  if (opt.destType === "queue") {
    const { agents, prompt } = parseQueue(opt.destValue);
    return (
      <div style={{ display: "flex", gap: 6 }}>
        <input
          value={agents}
          placeholder="agents e.g. 1001&1002"
          onChange={(e) => onChange(makeQueue(e.target.value, prompt))}
        />
        <select value={prompt} onChange={(e) => onChange(makeQueue(agents, e.target.value))}>
          <option value="">hold prompt…</option>
          {sounds.map((s) => (
            <option key={s.name} value={s.ref}>
              {s.name}
            </option>
          ))}
        </select>
      </div>
    );
  }
  if (opt.destType === "ivr") {
    return (
      <input
        list="ivr-names"
        value={opt.destValue}
        placeholder="menu name"
        onChange={(e) => onChange(e.target.value)}
      />
    );
  }
  if (opt.destType === "playback") {
    return (
      <select value={opt.destValue} onChange={(e) => onChange(e.target.value)}>
        <option value="">— choose prompt —</option>
        {sounds.map((s) => (
          <option key={s.name} value={s.ref}>
            {s.name}
          </option>
        ))}
      </select>
    );
  }
  return (
    <>
      <input
        value={opt.destValue}
        placeholder={opt.destType === "voicemail" ? "mailbox e.g. 1001" : "1001"}
        onChange={(e) => onChange(e.target.value)}
      />
      <datalist id="ivr-names">
        {ivrNames.map((n) => (
          <option key={n} value={n} />
        ))}
      </datalist>
    </>
  );
}

// DestPicker edits a "type:value" destination string used by fallbacks.
function DestPicker({
  label,
  value,
  onChange,
  ivrNames,
  sounds,
  trunks,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  ivrNames: string[];
  sounds: SoundFile[];
  trunks: Trunk[];
}) {
  const [type, val] = value.includes(":") ? value.split(/:(.*)/s) : [value || "", ""];
  const set = (t: string, v: string) => {
    if (t === "" || t === "hangup") onChange(t === "hangup" ? "hangup" : "");
    else onChange(`${t}:${v}`);
  };
  const ext = type === "external" ? parseExternal(val) : { num: "", trunk: "" };
  const q = type === "queue" ? parseQueue(val) : { agents: "", prompt: "" };
  return (
    <label>
      {label}
      <div style={{ display: "flex", gap: 8 }}>
        <select value={type} onChange={(e) => set(e.target.value, val)} style={{ flex: "0 0 140px" }}>
          <option value="">Replay / hang up</option>
          <option value="extension">Extension</option>
          <option value="queue">Ring agents (hold)</option>
          <option value="external">External / GSM</option>
          <option value="ivr">Sub-menu</option>
          <option value="voicemail">Voicemail</option>
          <option value="playback">Play message</option>
          <option value="hangup">Hang up</option>
        </select>
        {type === "queue" ? (
          <div style={{ display: "flex", gap: 6, flex: 1 }}>
            <input
              value={q.agents}
              placeholder="agents e.g. 1001&1002"
              onChange={(e) => set(type, makeQueue(e.target.value, q.prompt))}
            />
            <select value={q.prompt} onChange={(e) => set(type, makeQueue(q.agents, e.target.value))}>
              <option value="">hold prompt…</option>
              {sounds.map((s) => (
                <option key={s.name} value={s.ref}>
                  {s.name}
                </option>
              ))}
            </select>
          </div>
        ) : type === "playback" ? (
          <select value={val} onChange={(e) => set(type, e.target.value)} style={{ flex: 1 }}>
            <option value="">— prompt —</option>
            {sounds.map((s) => (
              <option key={s.name} value={s.ref}>
                {s.name}
              </option>
            ))}
          </select>
        ) : type === "external" ? (
          <div style={{ display: "flex", gap: 6, flex: 1 }}>
            <input
              value={ext.num}
              placeholder="+9198… (GSM)"
              onChange={(e) => set(type, makeExternal(e.target.value, ext.trunk))}
            />
            <select value={ext.trunk} onChange={(e) => set(type, makeExternal(ext.num, e.target.value))}>
              <option value="">via trunk…</option>
              {trunks.map((t) => (
                <option key={t.name} value={t.name}>
                  {t.name}
                </option>
              ))}
            </select>
          </div>
        ) : type === "extension" || type === "ivr" || type === "voicemail" ? (
          <input
            list={type === "ivr" ? "ivr-names-fb" : undefined}
            value={val}
            placeholder={type === "ivr" ? "menu name" : type === "voicemail" ? "mailbox" : "1001"}
            onChange={(e) => set(type, e.target.value)}
          />
        ) : null}
        <datalist id="ivr-names-fb">
          {ivrNames.map((n) => (
            <option key={n} value={n} />
          ))}
        </datalist>
      </div>
    </label>
  );
}
