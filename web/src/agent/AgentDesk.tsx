import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  deskCallbacks,
  deskDial,
  deskIngroups,
  deskSetIngroups,
  deskDisposition,
  deskNextLead,
  deskPause,
  deskSession,
  deskSetCampaign,
  deskTakeLead,
  deskUpdateLead,
  type DeskCallback,
  type DeskIngroup,
  type DeskLead,
  type DeskSession,
} from "./api";

// AgentDesk is the campaign side of the softphone: the panel that turns a phone
// into an agent screen. It sits alongside the dialer rather than replacing it —
// an agent who is not working a campaign still has their phone, and an agent
// who is gets a script, the lead in front of them, and a disposition to record.
//
// It renders nothing at all when the agent is assigned to no campaigns, so a
// deployment that does not use campaigns sees exactly the softphone it had
// before phase 3.

// fieldsOnScreen are the lead fields the agent may correct mid-call. The
// backend enforces the same allowlist; this is just what gets drawn.
const EDITABLE: [keyof DeskLead & string, string][] = [
  ["firstName", "First name"],
  ["lastName", "Last name"],
  ["email", "Email"],
  ["address1", "Address"],
  ["city", "City"],
  ["state", "State"],
  ["postalCode", "Postcode"],
];

export default function AgentDesk() {
  const [session, setSession] = useState<DeskSession | null>(null);
  // The desk reports its own successes and failures inline. Routing them
  // through the phone's error bar would drop the successes and make a
  // "saved as SALE" look like a call failure.
  const [notice, setNotice] = useState<{ kind: "ok" | "err"; text: string } | null>(null);
  const [lead, setLead] = useState<DeskLead | null>(null);
  const [callbacks, setCallbacks] = useState<DeskCallback[]>([]);
  const [ingroups, setIngroups] = useState<DeskIngroup[]>([]);
  const [busy, setBusy] = useState(false);
  const [collapsed, setCollapsed] = useState(false);

  const notify = useCallback((kind: "ok" | "err", text: string) => {
    setNotice({ kind, text });
    window.setTimeout(() => setNotice(null), 5000);
  }, []);

  // Disposition form.
  const [status, setStatus] = useState("");
  const [note, setNote] = useState("");
  const [callbackAt, setCallbackAt] = useState("");
  const [callbackTo, setCallbackTo] = useState("ANYONE");

  // Wrap-up countdown after a disposition, so an agent gets the campaign's
  // configured breather before the next lead.
  const [wrapLeft, setWrapLeft] = useState(0);
  const wrapTimer = useRef<number | null>(null);

  const refresh = useCallback(async () => {
    try {
      const s = await deskSession();
      setSession(s);
      setLead(s.lead);
    } catch {
      setSession(null); // desk features unavailable; the phone still works
    }
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const refreshCallbacks = useCallback(() => {
    deskCallbacks().then(setCallbacks).catch(() => setCallbacks([]));
  }, []);

  useEffect(() => {
    refreshCallbacks();
    const t = setInterval(refreshCallbacks, 60000);
    return () => clearInterval(t);
  }, [refreshCallbacks]);

  // Inbound in-groups. An agent with none permitted simply never sees the
  // control, so an outbound-only deployment is unaffected.
  useEffect(() => {
    deskIngroups().then(setIngroups).catch(() => setIngroups([]));
  }, []);

  useEffect(() => {
    if (wrapLeft <= 0) return;
    wrapTimer.current = window.setTimeout(() => setWrapLeft((n) => n - 1), 1000);
    return () => {
      if (wrapTimer.current) window.clearTimeout(wrapTimer.current);
    };
  }, [wrapLeft]);

  // How long the agent has been in the current state, ticking locally so the
  // screen does not need to poll to show "paused for 4:12".
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  const inState = useMemo(() => {
    if (!session?.state.since) return 0;
    return Math.max(0, Math.floor((now - new Date(session.state.since).getTime()) / 1000));
  }, [session, now]);

  if (!session || session.campaigns.length === 0) return null;

  const st = session.state;
  const dispositions = session.dispositions.filter((d) => d.selectable);
  const selected = dispositions.find((d) => d.code === status);

  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      notify("err", (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const onCampaign = (id: number) =>
    run(async () => {
      await deskSetCampaign(id);
      setLead(null);
      await refresh();
    });

  const onPause = (paused: boolean, code = "") =>
    run(async () => {
      await deskPause(paused, code);
      await refresh();
    });

  const onNext = () =>
    run(async () => {
      const res = await deskNextLead();
      if (!res.lead) {
        notify("err", res.note || "No leads available.");
        return;
      }
      setLead(res.lead);
      setStatus("");
      setNote("");
      setCallbackAt("");
    });

  const onDial = (alt = "") =>
    run(async () => {
      const res = await deskDial(alt);
      notify("ok", `Ringing your phone — it will dial ${res.dialed} when you answer.`);
      if (res.logError) notify("err", `Call placed but not logged: ${res.logError}`);
    });

  const onSave = () =>
    run(async () => {
      if (!status) {
        notify("err", "Choose what happened on the call.");
        return;
      }
      const res = await deskDisposition({
        status,
        note,
        callbackAt: callbackAt ? new Date(callbackAt).toISOString() : undefined,
        callbackTo,
      });
      if (res.callbackError) notify("err", res.callbackError);
      else notify("ok", `Saved as ${status}`);
      setLead(null);
      setStatus("");
      setNote("");
      setCallbackAt("");
      setWrapLeft(session.state.wrapupSeconds ?? 0);
      refreshCallbacks();
      await refresh();
    });

  const onField = (key: string, value: string) =>
    run(async () => {
      const updated = await deskUpdateLead({ [key]: value });
      setLead(updated);
    });

  const onTakeCallback = (cb: DeskCallback) =>
    run(async () => {
      const res = await deskTakeLead(cb.leadId);
      setLead(res.lead);
      setStatus("");
      setNote("");
    });

  return (
    <div className="desk">
      {notice && <div className={`desk-notice ${notice.kind}`}>{notice.text}</div>}

      <div className="desk-bar">
        <span className="desk-who">
          {session.agent.displayName}
          <span className="desk-sub">{session.agent.extension}</span>
        </span>

        <select
          className="desk-campaign"
          value={st.campaignId ?? 0}
          disabled={busy}
          onChange={(e) => onCampaign(parseInt(e.target.value, 10))}
        >
          <option value={0}>No campaign</option>
          {session.campaigns.map((c) => (
            <option key={c.id} value={c.id}>
              {c.code} — {c.name}
            </option>
          ))}
        </select>

        {st.paused ? (
          <>
            <span className="desk-state paused">
              PAUSED{st.pauseCode ? ` · ${st.pauseCode}` : ""} · {fmt(inState)}
            </span>
            <button className="desk-btn go" disabled={busy} onClick={() => onPause(false)}>
              Go ready
            </button>
          </>
        ) : (
          <>
            <span className="desk-state ready">READY · {fmt(inState)}</span>
            <select
              className="desk-pause"
              value=""
              disabled={busy}
              onChange={(e) => e.target.value && onPause(true, e.target.value)}
            >
              <option value="">Pause…</option>
              {session.pauseCodes.map((p) => (
                <option key={p.id} value={p.code}>
                  {p.name}
                </option>
              ))}
            </select>
          </>
        )}

        {wrapLeft > 0 && <span className="desk-state wrap">WRAP {fmt(wrapLeft)}</span>}

        <button className="desk-btn" onClick={() => setCollapsed((c) => !c)}>
          {collapsed ? "Show" : "Hide"}
        </button>
      </div>

      {!collapsed && (
        <div className="desk-body">
          {ingroups.length > 0 && (
            <div className="desk-ingroups">
              <div className="desk-h">Taking calls for</div>
              <div className="desk-dispo-grid">
                {ingroups.map((g) => (
                  <button
                    key={g.ingroup}
                    className={`desk-btn ${g.selected ? "on" : ""}`}
                    disabled={busy}
                    title={g.description || g.ingroup}
                    onClick={() =>
                      run(async () => {
                        const next = ingroups
                          .filter((x) => (x.ingroup === g.ingroup ? !x.selected : x.selected))
                          .map((x) => x.ingroup);
                        setIngroups(await deskSetIngroups(next));
                        notify("ok", g.selected ? `Left ${g.ingroup}` : `Now taking ${g.ingroup} calls`);
                      })
                    }
                  >
                    {g.ingroup}
                  </button>
                ))}
              </div>
            </div>
          )}

          {callbacks.length > 0 && (
            <div className="desk-callbacks">
              <div className="desk-h">Callbacks due ({callbacks.length})</div>
              {callbacks.slice(0, 5).map((cb) => (
                <button key={cb.id} className="desk-cb" disabled={busy} onClick={() => onTakeCallback(cb)}>
                  <strong>{cb.leadName || cb.leadPhone}</strong>
                  <span className="desk-sub">
                    {new Date(cb.callbackAt).toLocaleString()}
                    {cb.recipient === "USERONLY" ? " · yours" : ""}
                  </span>
                </button>
              ))}
            </div>
          )}

          {!lead ? (
            <div className="desk-empty">
              {st.campaignId ? (
                <>
                  <p>No lead on screen.</p>
                  <button className="desk-btn go" disabled={busy || wrapLeft > 0} onClick={onNext}>
                    {wrapLeft > 0 ? `Wrap-up ${fmt(wrapLeft)}` : "Next lead"}
                  </button>
                </>
              ) : (
                <p>Choose a campaign to start working leads.</p>
              )}
            </div>
          ) : (
            <>
              <div className="desk-lead">
                <div className="desk-h">
                  {[lead.title, lead.firstName, lead.lastName].filter(Boolean).join(" ") || "Lead"}
                  <span className="desk-sub">
                    {lead.listName} · {lead.status} · called {lead.calledCount}×
                  </span>
                </div>

                <div className="desk-numbers">
                  <button className="desk-btn go" disabled={busy} onClick={() => onDial()}>
                    ☎ {lead.phoneCode}
                    {lead.phoneNumber}
                  </button>
                  {lead.altPhone && (
                    <button className="desk-btn" disabled={busy} onClick={() => onDial("alt")}>
                      Alt {lead.altPhone}
                    </button>
                  )}
                  {lead.altPhoneTwo && (
                    <button className="desk-btn" disabled={busy} onClick={() => onDial("alt2")}>
                      Alt2 {lead.altPhoneTwo}
                    </button>
                  )}
                </div>

                <div className="desk-fields">
                  {EDITABLE.map(([key, label]) => (
                    <label key={key}>
                      {label}
                      <input
                        defaultValue={String(lead[key] ?? "")}
                        onBlur={(e) => {
                          if (e.target.value !== String(lead[key] ?? "")) onField(key, e.target.value);
                        }}
                      />
                    </label>
                  ))}
                </div>
              </div>

              {st.script && (
                <div className="desk-script">
                  <div className="desk-h">Script</div>
                  <p>{fillScript(st.script, lead)}</p>
                </div>
              )}

              <div className="desk-dispo">
                <div className="desk-h">What happened?</div>
                <div className="desk-dispo-grid">
                  {dispositions.map((d) => (
                    <button
                      key={d.id}
                      className={`desk-btn ${status === d.code ? "on" : ""}`}
                      disabled={busy}
                      onClick={() => setStatus(d.code)}
                      title={d.name}
                    >
                      {d.code}
                    </button>
                  ))}
                </div>

                <input
                  className="desk-note"
                  placeholder="Note (optional)"
                  value={note}
                  onChange={(e) => setNote(e.target.value)}
                />

                {selected?.callback && (
                  <div className="desk-callback-form">
                    <label>
                      Call back at
                      <input
                        type="datetime-local"
                        value={callbackAt}
                        onChange={(e) => setCallbackAt(e.target.value)}
                      />
                    </label>
                    <label>
                      Who takes it
                      <select value={callbackTo} onChange={(e) => setCallbackTo(e.target.value)}>
                        <option value="ANYONE">Anyone on the campaign</option>
                        <option value="USERONLY">Me — I promised</option>
                      </select>
                    </label>
                  </div>
                )}

                <button className="desk-btn go wide" disabled={busy || !status} onClick={onSave}>
                  Save &amp; next
                </button>
              </div>
            </>
          )}
        </div>
      )}
    </div>
  );
}

// fillScript substitutes the lead's own values into the campaign script, so an
// agent reads "Good morning Ahmed" rather than a placeholder. Unknown tokens
// are left visible rather than blanked — a script with a typo should look
// wrong, not silently read as a gap.
function fillScript(script: string, lead: DeskLead): string {
  return script.replace(/\{(\w+)\}/g, (whole, key: string) => {
    const v = (lead as unknown as Record<string, unknown>)[key];
    return v === undefined || v === null || v === "" ? whole : String(v);
  });
}

function fmt(sec: number): string {
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}
