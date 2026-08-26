import { useCallback, useEffect, useRef, useState } from "react";
import { soundAudioUrl, type IVR, type IVRDestType, type SoundFile, type Trunk } from "../api";

// A fully visual, drag-and-drop IVR builder. The menu is one root node with an
// output port per key (plus invalid/timeout); each port wires to a destination
// node (extension, sub-menu, voicemail, play message, hang up, repeat). Users
// drag node types from the palette, drag nodes to arrange them, and drag from a
// port onto a node (or empty space) to connect. Positions persist in ivr.layout.

const MENU_W = 250;
const MENU_HEAD = 154; // fixed header height so port Y math is deterministic
const ROW_H = 34;
const NODE_W = 186;
const NODE_H = 88;

type Kind = IVRDestType;

interface BNode {
  id: string;
  kind: Kind;
  value: string;
  label: string;
  x: number;
  y: number;
}
interface KeyRow {
  keyId: string;
  digit: string;
}
// portKey: a keyId, or the literal "invalid" / "timeout".
type Edges = Record<string, string>; // portKey -> nodeId

const PALETTE: { kind: Kind; label: string; icon: string }[] = [
  { kind: "extension", label: "Ring extension", icon: "☎" },
  { kind: "ingroup", label: "In-group (ACD queue)", icon: "⑃" },
  { kind: "queue", label: "Ring agents (hunt group)", icon: "⏳" },
  { kind: "external", label: "Call external / GSM", icon: "📱" },
  { kind: "ivr", label: "Sub-menu", icon: "▤" },
  { kind: "voicemail", label: "Voicemail", icon: "✉" },
  { kind: "playback", label: "Play message", icon: "▶" },
  { kind: "repeat", label: "Repeat menu", icon: "↻" },
  { kind: "hangup", label: "Hang up", icon: "⏻" },
];
const KIND_LABEL: Record<Kind, string> = {
  extension: "Ring extension",
  ingroup: "In-group",
  queue: "Ring agents (hunt)",
  external: "Call external / GSM",
  ivr: "Sub-menu",
  voicemail: "Voicemail",
  playback: "Play message",
  repeat: "Repeat menu",
  hangup: "Hang up",
};
const needsValue = (k: Kind) => k !== "hangup" && k !== "repeat";

// External destinations encode "<number>@<trunk>".
function parseExternal(v: string): { num: string; trunk: string } {
  const i = v.lastIndexOf("@");
  return i >= 0 ? { num: v.slice(0, i), trunk: v.slice(i + 1) } : { num: v, trunk: "" };
}
function makeExternal(num: string, trunk: string): string {
  return `${num}@${trunk}`;
}
// Queue destinations encode "<agents>;<holdprompt>".
function parseQueue(v: string): { agents: string; prompt: string } {
  const i = v.indexOf(";");
  return i >= 0 ? { agents: v.slice(0, i), prompt: v.slice(i + 1) } : { agents: v, prompt: "" };
}
function makeQueue(agents: string, prompt: string): string {
  return `${agents};${prompt}`;
}

let uid = 0;
const nid = () => `n${Date.now().toString(36)}_${uid++}`;

// --- (de)serialisation ------------------------------------------------------

interface Layout {
  menu?: { x: number; y: number };
  nodes?: Record<string, { x: number; y: number }>; // slot -> pos; slot = digit|invalid|timeout
}

function parseDest(dest: string): { kind: Kind; value: string } | null {
  const d = (dest || "").trim();
  if (!d) return null;
  if (d === "hangup") return { kind: "hangup", value: "" };
  const [t, v] = d.split(/:(.*)/s);
  return { kind: (t as Kind) || "extension", value: v || "" };
}

// Build the initial graph from the stored IVR + layout.
function fromIVR(ivr: IVR): {
  menu: { x: number; y: number };
  keys: KeyRow[];
  nodes: BNode[];
  edges: Edges;
} {
  let layout: Layout = {};
  try {
    layout = ivr.layout ? JSON.parse(ivr.layout) : {};
  } catch {
    layout = {};
  }
  const menu = layout.menu || { x: 40, y: 60 };
  const keys: KeyRow[] = [];
  const nodes: BNode[] = [];
  const edges: Edges = {};
  const lnodes = layout.nodes || {};
  const col2 = menu.x + MENU_W + 120;

  // Options that share a digit form that key's chain, in array order. The first
  // links from the key; each subsequent one links from the previous node's out.
  const byDigit: Record<string, typeof ivr.options> = {};
  const digitOrder: string[] = [];
  (ivr.options || []).forEach((o) => {
    if (!byDigit[o.digit]) {
      byDigit[o.digit] = [];
      digitOrder.push(o.digit);
    }
    byDigit[o.digit].push(o);
  });
  digitOrder.forEach((digit, di) => {
    const keyId = `k${di}_${digit}`;
    keys.push({ keyId, digit });
    let prevId = "";
    byDigit[digit].forEach((o, step) => {
      const pos = lnodes[`${digit}#${step}`] ||
        lnodes[digit] || { x: col2 + step * (NODE_W + 40), y: menu.y + di * (NODE_H + 40) };
      const id = nid();
      nodes.push({ id, kind: o.destType, value: o.destValue, label: o.label, x: pos.x, y: pos.y });
      if (step === 0) edges[keyId] = id;
      else edges[`out:${prevId}`] = id;
      prevId = id;
    });
  });

  const inv = parseDest(ivr.invalidDest);
  if (inv) {
    const pos = lnodes["invalid"] || { x: col2, y: menu.y + (keys.length + 1) * (NODE_H + 40) };
    const id = nid();
    nodes.push({ id, kind: inv.kind, value: inv.value, label: "on invalid", x: pos.x, y: pos.y });
    edges["invalid"] = id;
  }
  const tmo = parseDest(ivr.timeoutDest);
  if (tmo) {
    const pos = lnodes["timeout"] || { x: col2, y: menu.y + (keys.length + 2) * (NODE_H + 40) };
    const id = nid();
    nodes.push({ id, kind: tmo.kind, value: tmo.value, label: "on timeout", x: pos.x, y: pos.y });
    edges["timeout"] = id;
  }
  return { menu, keys, nodes, edges };
}

function encodeDest(n: BNode | undefined): string {
  if (!n) return "";
  if (n.kind === "hangup") return "hangup";
  return `${n.kind}:${n.value}`;
}

// --- component --------------------------------------------------------------

export function IVRBuilder({
  initial,
  isNew,
  ivrNames,
  sounds,
  trunks,
  onCreateSubmenu,
  onOpenIVR,
  onCancel,
  onSave,
}: {
  initial: IVR;
  isNew: boolean;
  ivrNames: string[];
  sounds: SoundFile[];
  trunks: Trunk[];
  onCreateSubmenu: (name: string) => Promise<void>;
  onOpenIVR: (name: string) => void;
  onCancel: () => void;
  onSave: (ivr: IVR) => Promise<void>;
}) {
  const g = fromIVR(initial);
  const [name, setName] = useState(initial.name);
  const [greeting, setGreeting] = useState(initial.greeting);
  const [timeoutSec, setTimeoutSec] = useState(initial.timeoutSec || 5);
  const [maxRetries, setMaxRetries] = useState(initial.maxRetries || 3);
  const [menu, setMenu] = useState(g.menu);
  const [keys, setKeys] = useState<KeyRow[]>(g.keys);
  const [nodes, setNodes] = useState<BNode[]>(g.nodes);
  const [edges, setEdges] = useState<Edges>(g.edges);
  const [pan, setPan] = useState({ x: 0, y: 0 });
  const [busy, setBusy] = useState(false);
  const [connect, setConnect] = useState<{ port: string; x: number; y: number } | null>(null);

  const worldRef = useRef<HTMLDivElement>(null);
  const gesture = useRef<
    | { type: "pan"; sx: number; sy: number; px: number; py: number }
    | { type: "node"; id: string; ox: number; oy: number }
    | { type: "menu"; ox: number; oy: number }
    | { type: "connect"; port: string }
    | null
  >(null);

  const nodeById = (id: string) => nodes.find((n) => n.id === id);

  // world coords from a pointer event (pan-independent).
  const toWorld = useCallback((e: PointerEvent | React.PointerEvent) => {
    const r = worldRef.current!.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  }, []);

  // Port positions (world coords), computed from the deterministic layout.
  const portList = [...keys.map((k) => k.keyId), "invalid", "timeout"];
  const portY = (portKey: string) => {
    const i = portList.indexOf(portKey);
    return menu.y + MENU_HEAD + i * ROW_H + ROW_H / 2;
  };
  const portPos = (portKey: string) => {
    // A node's own output port ("out:<id>") sits on that node's right edge; all
    // other ports live on the menu's right edge.
    if (portKey.startsWith("out:")) {
      const n = nodeById(portKey.slice(4));
      if (n) return { x: n.x + NODE_W, y: n.y + NODE_H / 2 };
    }
    return { x: menu.x + MENU_W, y: portY(portKey) };
  };
  const inPos = (n: BNode) => ({ x: n.x, y: n.y + NODE_H / 2 });

  // --- gestures -------------------------------------------------------------
  useEffect(() => {
    const move = (e: PointerEvent) => {
      const gg = gesture.current;
      if (!gg) return;
      if (gg.type === "pan") {
        setPan({ x: gg.px + (e.clientX - gg.sx), y: gg.py + (e.clientY - gg.sy) });
      } else if (gg.type === "node") {
        const w = toWorld(e);
        setNodes((ns) => ns.map((n) => (n.id === gg.id ? { ...n, x: w.x - gg.ox, y: w.y - gg.oy } : n)));
      } else if (gg.type === "menu") {
        const w = toWorld(e);
        setMenu({ x: w.x - gg.ox, y: w.y - gg.oy });
      } else if (gg.type === "connect") {
        const w = toWorld(e);
        setConnect({ port: gg.port, x: w.x, y: w.y });
      }
    };
    const up = (e: PointerEvent) => {
      const gg = gesture.current;
      gesture.current = null;
      if (gg?.type === "connect") {
        const w = toWorld(e);
        const hit = nodes.find(
          (n) => w.x >= n.x && w.x <= n.x + NODE_W && w.y >= n.y && w.y <= n.y + NODE_H
        );
        if (hit) {
          // Ignore a node connecting to itself.
          if (gg.port !== `out:${hit.id}`) linkPortToNode(gg.port, hit.id);
        } else {
          // Drop on empty canvas -> create a default node here and connect it.
          const id = nid();
          const newNode: BNode = {
            kind: "extension",
            value: "",
            label: "",
            id,
            x: w.x,
            y: w.y - NODE_H / 2,
          };
          setNodes((ns) => [...ns, newNode]);
          linkPortToNode(gg.port, id);
        }
        setConnect(null);
      }
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
    return () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodes, keys, toWorld]);

  // Assign a port -> node, ensuring each node has a single incoming edge.
  const linkPortToNode = (port: string, nodeId: string) => {
    setEdges((es) => {
      const next: Edges = {};
      for (const [p, n] of Object.entries(es)) {
        if (n === nodeId) continue; // drop other ports pointing here
        next[p] = n;
      }
      next[port] = nodeId;
      return next;
    });
  };

  const startPan = (e: React.PointerEvent) => {
    gesture.current = { type: "pan", sx: e.clientX, sy: e.clientY, px: pan.x, py: pan.y };
  };
  const startNodeDrag = (e: React.PointerEvent, n: BNode) => {
    e.stopPropagation();
    const w = toWorld(e);
    gesture.current = { type: "node", id: n.id, ox: w.x - n.x, oy: w.y - n.y };
  };
  const startMenuDrag = (e: React.PointerEvent) => {
    e.stopPropagation();
    const w = toWorld(e);
    gesture.current = { type: "menu", ox: w.x - menu.x, oy: w.y - menu.y };
  };
  const startConnect = (e: React.PointerEvent, port: string) => {
    e.stopPropagation();
    const p = portPos(port);
    gesture.current = { type: "connect", port };
    setConnect({ port, x: p.x, y: p.y });
  };

  // --- palette drop ---------------------------------------------------------
  const onDrop = (e: React.DragEvent) => {
    e.preventDefault();
    const kind = e.dataTransfer.getData("kind") as Kind;
    if (!kind) return;
    const w = toWorld(e as unknown as React.PointerEvent);
    setNodes((ns) => [...ns, { id: nid(), kind, value: "", label: "", x: w.x - NODE_W / 2, y: w.y - NODE_H / 2 }]);
  };

  // --- key + node ops -------------------------------------------------------
  const addKey = () => setKeys((ks) => [...ks, { keyId: nid(), digit: nextDigit(ks) }]);
  const setDigit = (keyId: string, digit: string) =>
    setKeys((ks) => ks.map((k) => (k.keyId === keyId ? { ...k, digit } : k)));
  const rmKey = (keyId: string) => {
    setKeys((ks) => ks.filter((k) => k.keyId !== keyId));
    setEdges((es) => {
      const n = { ...es };
      delete n[keyId];
      return n;
    });
  };
  const patchNode = (id: string, p: Partial<BNode>) =>
    setNodes((ns) => ns.map((n) => (n.id === id ? { ...n, ...p } : n)));
  const rmNode = (id: string) => {
    setNodes((ns) => ns.filter((n) => n.id !== id));
    setEdges((es) => {
      const n: Edges = {};
      // Drop edges pointing at this node AND this node's own outgoing edge.
      for (const [p, t] of Object.entries(es)) {
        if (t === id || p === `out:${id}`) continue;
        n[p] = t;
      }
      return n;
    });
  };
  const disconnect = (port: string) =>
    setEdges((es) => {
      const n = { ...es };
      delete n[port];
      return n;
    });

  // --- save -----------------------------------------------------------------
  const save = async () => {
    // The parent validates the name and surfaces the error / notification.
    const layout: Layout = { menu, nodes: {} };
    const options: { digit: string; destType: Kind; destValue: string; label: string }[] = [];
    // Walk each key's chain (key -> node -> node.out -> ...) into an ordered
    // list of actions for that digit, so "play message then ring extension"
    // round-trips as two options sharing the key.
    for (const k of keys) {
      if (!k.digit || !edges[k.keyId]) continue;
      let cur: string | undefined = edges[k.keyId];
      const seen = new Set<string>();
      let step = 0;
      while (cur && !seen.has(cur)) {
        seen.add(cur);
        const n = nodeById(cur);
        if (!n) break;
        options.push({ digit: k.digit, destType: n.kind, destValue: n.value, label: step === 0 ? n.label : "" });
        layout.nodes![`${k.digit}#${step}`] = { x: n.x, y: n.y };
        cur = edges[`out:${cur}`];
        step++;
      }
    }
    // Fallbacks remain single-action.
    const invNode = edges["invalid"] ? nodeById(edges["invalid"]) : undefined;
    const tmoNode = edges["timeout"] ? nodeById(edges["timeout"]) : undefined;
    if (invNode) layout.nodes!["invalid"] = { x: invNode.x, y: invNode.y };
    if (tmoNode) layout.nodes!["timeout"] = { x: tmoNode.x, y: tmoNode.y };

    setBusy(true);
    try {
      await onSave({
        ...initial,
        name: name.trim(),
        greeting,
        timeoutSec,
        maxRetries,
        invalidDest: encodeDest(invNode),
        timeoutDest: encodeDest(tmoNode),
        layout: JSON.stringify(layout),
        options,
      });
    } finally {
      setBusy(false);
    }
  };

  // openSub persists the current menu, then asks the parent to open the named
  // submenu in the builder (so nested menus can be edited in place).
  const openSub = async (nm: string) => {
    try {
      await save();
    } catch {
      return; // save failed (e.g. bad name) -> stay here
    }
    onOpenIVR(nm);
  };

  // --- render ---------------------------------------------------------------
  const edgeEls = Object.entries(edges).map(([port, nodeId]) => {
    const n = nodeById(nodeId);
    if (!n) return null;
    const a = portPos(port);
    const b = inPos(n);
    return <path key={port} className="ib-edge" d={bezier(a.x, a.y, b.x, b.y)} />;
  });

  return (
    <div className="ib-overlay">
      <div className="ib-topbar">
        <div className="ib-title">
          Visual IVR Builder{" "}
          <span className="ib-sub">— drag from a key ● to a block; chain blocks via the ● on a block's right (e.g. Play message → Ring extension)</span>
        </div>
        <div className="row-action">
          <button className="btn ghost" onClick={() => setPan({ x: 0, y: 0 })}>
            Reset view
          </button>
          <button className="btn ghost" onClick={onCancel}>
            Cancel
          </button>
          <button className="btn" disabled={busy} onClick={save}>
            {busy ? "Saving…" : isNew ? "Create menu" : "Save menu"}
          </button>
        </div>
      </div>

      <div className="ib-body">
        <aside className="ib-palette">
          <div className="ib-palette-head">Blocks</div>
          {PALETTE.map((p) => (
            <div
              key={p.kind}
              className="ib-pal-item"
              draggable
              onDragStart={(e) => e.dataTransfer.setData("kind", p.kind)}
            >
              <span className="ib-pal-ico">{p.icon}</span>
              {p.label}
            </div>
          ))}
          <div className="ib-palette-help">
            Drag a block onto the canvas, then drag from a key dot to it (or to
            empty space to spawn one). To run steps in sequence, drag from a
            block's right-side dot to the next block — e.g. Play message →
            Ring extension. Only "Play message" continues; other actions end the
            chain. Drag headers to arrange, drag the background to pan.
          </div>
        </aside>

        <div
          className="ib-canvas"
          onPointerDown={startPan}
          onDragOver={(e) => e.preventDefault()}
          onDrop={onDrop}
        >
          <div className="ib-world" ref={worldRef} style={{ transform: `translate(${pan.x}px, ${pan.y}px)` }}>
            <svg className="ib-edges" width={6000} height={4000}>
              {edgeEls}
              {connect && (
                <path
                  className="ib-edge dragging"
                  d={bezier(portPos(connect.port).x, portPos(connect.port).y, connect.x, connect.y)}
                />
              )}
            </svg>

            {/* Menu (root) node */}
            <div className="ib-menu" style={{ left: menu.x, top: menu.y, width: MENU_W }}>
              <div className="ib-drag" onPointerDown={startMenuDrag}>
                <span className="ib-menu-ico">▤</span> Menu
              </div>
              <div className="ib-menu-fields" style={{ height: MENU_HEAD - 30 }}>
                <input
                  className="ib-name"
                  value={name}
                  disabled={!isNew}
                  placeholder="menu name"
                  onChange={(e) => setName(e.target.value)}
                  onPointerDown={(e) => e.stopPropagation()}
                />
                <div className="ib-greeting">
                  <select
                    value={sounds.some((s) => s.ref === greeting) ? greeting : ""}
                    onChange={(e) => setGreeting(e.target.value)}
                    onPointerDown={(e) => e.stopPropagation()}
                  >
                    <option value="">— greeting —</option>
                    {sounds.map((s) => (
                      <option key={s.name} value={s.ref}>
                        {s.name}
                      </option>
                    ))}
                    {greeting && !sounds.some((s) => s.ref === greeting) && (
                      <option value={greeting}>{greeting}</option>
                    )}
                  </select>
                  {sounds.find((s) => s.ref === greeting) && (
                    <audio
                      controls
                      preload="none"
                      className="sound-player sm"
                      src={soundAudioUrl(sounds.find((s) => s.ref === greeting)!.name)}
                      onPointerDown={(e) => e.stopPropagation()}
                    />
                  )}
                </div>
                <div className="ib-nums">
                  <label onPointerDown={(e) => e.stopPropagation()}>
                    wait
                    <input
                      type="number"
                      value={timeoutSec}
                      onChange={(e) => setTimeoutSec(parseInt(e.target.value || "5", 10))}
                    />
                    s
                  </label>
                  <label onPointerDown={(e) => e.stopPropagation()}>
                    retries
                    <input
                      type="number"
                      value={maxRetries}
                      onChange={(e) => setMaxRetries(parseInt(e.target.value || "3", 10))}
                    />
                  </label>
                </div>
              </div>

              {/* key ports */}
              {keys.map((k) => (
                <div className="ib-prow" key={k.keyId} style={{ height: ROW_H }}>
                  <input
                    className="ib-digit"
                    value={k.digit}
                    maxLength={1}
                    placeholder="#"
                    onPointerDown={(e) => e.stopPropagation()}
                    onChange={(e) => setDigit(k.keyId, e.target.value.replace(/[^0-9*#]/g, ""))}
                  />
                  <span className="ib-prow-label">key</span>
                  <button
                    className="ib-rowdel"
                    title="remove key"
                    onPointerDown={(e) => e.stopPropagation()}
                    onClick={() => rmKey(k.keyId)}
                  >
                    ✕
                  </button>
                  <span
                    className={`ib-port out ${edges[k.keyId] ? "on" : ""}`}
                    onPointerDown={(e) => startConnect(e, k.keyId)}
                    onDoubleClick={() => disconnect(k.keyId)}
                    title="drag to a block to connect"
                  />
                </div>
              ))}
              <div className="ib-prow special" style={{ height: ROW_H }}>
                <span className="ib-prow-label wide">on invalid key</span>
                <span
                  className={`ib-port out ${edges["invalid"] ? "on" : ""}`}
                  onPointerDown={(e) => startConnect(e, "invalid")}
                  onDoubleClick={() => disconnect("invalid")}
                />
              </div>
              <div className="ib-prow special" style={{ height: ROW_H }}>
                <span className="ib-prow-label wide">on timeout</span>
                <span
                  className={`ib-port out ${edges["timeout"] ? "on" : ""}`}
                  onPointerDown={(e) => startConnect(e, "timeout")}
                  onDoubleClick={() => disconnect("timeout")}
                />
              </div>
              <button className="ib-addkey" onPointerDown={(e) => e.stopPropagation()} onClick={addKey}>
                + add key
              </button>
            </div>

            {/* destination nodes */}
            {nodes.map((n) => (
              <div
                key={n.id}
                className={`ib-node k-${n.kind}`}
                style={{ left: n.x, top: n.y, width: NODE_W, height: NODE_H }}
              >
                <span className="ib-port in" />
                {/* output port: chain this block to a following action */}
                <span
                  className={`ib-port out ${edges[`out:${n.id}`] ? "on" : ""}`}
                  onPointerDown={(e) => startConnect(e, `out:${n.id}`)}
                  onDoubleClick={() => disconnect(`out:${n.id}`)}
                  title="drag to the next action (e.g. after Play message, ring an extension)"
                />
                <div className="ib-drag sm" onPointerDown={(e) => startNodeDrag(e, n)}>
                  {KIND_LABEL[n.kind]}
                  <button
                    className="ib-rowdel"
                    onPointerDown={(e) => e.stopPropagation()}
                    onClick={() => rmNode(n.id)}
                    title="delete block"
                  >
                    ✕
                  </button>
                </div>
                <div className="ib-node-body" onPointerDown={(e) => e.stopPropagation()}>
                  <NodeField
                    n={n}
                    sounds={sounds}
                    trunks={trunks}
                    ivrNames={ivrNames}
                    onCreateSubmenu={onCreateSubmenu}
                    onOpenIVR={openSub}
                    onChange={(p) => patchNode(n.id, p)}
                  />
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>
      <datalist id="ib-ivr-names">
        {ivrNames.map((nm) => (
          <option key={nm} value={nm} />
        ))}
      </datalist>
    </div>
  );
}

function NodeField({
  n,
  sounds,
  trunks,
  ivrNames,
  onCreateSubmenu,
  onOpenIVR,
  onChange,
}: {
  n: BNode;
  sounds: SoundFile[];
  trunks: Trunk[];
  ivrNames: string[];
  onCreateSubmenu: (name: string) => Promise<void>;
  onOpenIVR: (name: string) => void;
  onChange: (p: Partial<BNode>) => void;
}) {
  if (!needsValue(n.kind)) {
    return <div className="ib-node-note">{n.kind === "repeat" ? "replays this menu" : "ends the call"}</div>;
  }
  if (n.kind === "ivr") {
    const known = ivrNames.includes(n.value);
    return (
      <div style={{ display: "flex", gap: 4 }}>
        <select
          value={known ? n.value : n.value ? "__cur__" : ""}
          style={{ flex: 1 }}
          onChange={(e) => {
            const v = e.target.value;
            if (v === "__new__") {
              const nm = window.prompt("New submenu name (letters, digits, _ - .):");
              if (nm && nm.trim()) onCreateSubmenu(nm.trim()).then(() => onChange({ value: nm.trim() })).catch(() => {});
            } else if (v !== "__cur__") {
              onChange({ value: v });
            }
          }}
        >
          <option value="">— submenu —</option>
          {ivrNames.map((nm) => (
            <option key={nm} value={nm}>
              {nm}
            </option>
          ))}
          {n.value && !known && <option value="__cur__">{n.value}</option>}
          <option value="__new__">＋ new submenu…</option>
        </select>
        {n.value && (
          <button
            type="button"
            className="btn ghost small"
            title="Open this submenu in the builder (saves current first)"
            onClick={() => onOpenIVR(n.value)}
          >
            ↗
          </button>
        )}
      </div>
    );
  }
  if (n.kind === "external") {
    const { num, trunk } = parseExternal(n.value);
    return (
      <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        <input
          value={num}
          placeholder="+9198… (GSM)"
          onChange={(e) => onChange({ value: makeExternal(e.target.value, trunk) })}
        />
        <select value={trunk} onChange={(e) => onChange({ value: makeExternal(num, e.target.value) })}>
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
  if (n.kind === "playback") {
    return (
      <select value={n.value} onChange={(e) => onChange({ value: e.target.value })}>
        <option value="">— prompt —</option>
        {sounds.map((s) => (
          <option key={s.name} value={s.ref}>
            {s.name}
          </option>
        ))}
      </select>
    );
  }
  if (n.kind === "queue") {
    const { agents, prompt } = parseQueue(n.value);
    return (
      <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
        <input
          value={agents}
          placeholder="agents e.g. 1001&1002"
          onChange={(e) => onChange({ value: makeQueue(e.target.value, prompt) })}
        />
        <select value={prompt} onChange={(e) => onChange({ value: makeQueue(agents, e.target.value) })}>
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
  return (
    <input
      value={n.value}
      placeholder={n.kind === "voicemail" ? "mailbox" : "1001"}
      onChange={(e) => onChange({ value: e.target.value })}
    />
  );
}

function bezier(x1: number, y1: number, x2: number, y2: number): string {
  const c = Math.max(40, Math.abs(x2 - x1) / 2);
  return `M ${x1} ${y1} C ${x1 + c} ${y1}, ${x2 - c} ${y2}, ${x2} ${y2}`;
}

function nextDigit(ks: KeyRow[]): string {
  const used = new Set(ks.map((k) => k.digit));
  for (const d of "1234567890*#") if (!used.has(d)) return d;
  return "";
}
