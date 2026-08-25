import { useCallback, useEffect, useRef, useState } from "react";
import { agentConfig, agentLogin, agentLogout, type AgentConfig } from "./api";
import AgentDesk from "./AgentDesk";
import { Softphone as SipPhone, type PhoneState, type CallLogEntry } from "./sip";
import { Ringer } from "./ringer";
import logoDark from "../assets/xelo-dark.png";

const DIALPAD = ["1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "0", "#"];

const STATE_LABEL: Record<PhoneState, string> = {
  offline: "Offline",
  connecting: "Connecting…",
  registered: "Ready",
  incoming: "Incoming call",
  outgoing: "Calling…",
  active: "In call",
  failed: "Connection failed",
};

export default function Softphone() {
  const [booting, setBooting] = useState(true);
  const [needLogin, setNeedLogin] = useState(false);
  const [cfg, setCfg] = useState<AgentConfig | null>(null);
  const [state, setState] = useState<PhoneState>("offline");
  const [detail, setDetail] = useState("");
  const [incoming, setIncoming] = useState<string | null>(null);
  const [error, setError] = useState("");

  const [dial, setDial] = useState("");
  const [muted, setMuted] = useState(false);
  const [speaker, setSpeaker] = useState(true);
  const [dnd, setDnd] = useState(false);
  const [recording, setRecording] = useState(false);
  const [transferring, setTransferring] = useState(false);
  const [transferTarget, setTransferTarget] = useState("");
  const [seconds, setSeconds] = useState(0);
  const [log, setLog] = useState<CallLogEntry[]>([]);

  const phoneRef = useRef<SipPhone | null>(null);
  const audioRef = useRef<HTMLAudioElement | null>(null);
  const ringerRef = useRef<Ringer>(new Ringer());
  const startedRef = useRef(false);

  // Unlock audio on the first user gesture so the ringtone can play.
  useEffect(() => {
    const unlock = () => ringerRef.current.unlock();
    window.addEventListener("pointerdown", unlock, { once: true });
    window.addEventListener("keydown", unlock, { once: true });
    return () => {
      window.removeEventListener("pointerdown", unlock);
      window.removeEventListener("keydown", unlock);
    };
  }, []);

  // Ring on incoming, ringback while calling out, silence otherwise. Also reset
  // per-call UI when a call ends.
  useEffect(() => {
    const r = ringerRef.current;
    if (state === "incoming") r.incoming();
    else if (state === "outgoing") r.ringback();
    else r.stop();

    if (state !== "active") {
      setSeconds(0);
      setRecording(false);
      setTransferring(false);
    }
    if (state === "registered" || state === "offline") setMuted(false);
  }, [state]);

  // Call timer.
  useEffect(() => {
    if (state !== "active") return;
    const t = setInterval(() => setSeconds((s) => s + 1), 1000);
    return () => clearInterval(t);
  }, [state]);

  const startPhone = useCallback((config: AgentConfig) => {
    if (startedRef.current || !audioRef.current) return;
    startedRef.current = true;
    const phone = new SipPhone(
      {
        wsUrl: config.wsUrl,
        domain: config.domain,
        extension: config.extension,
        password: config.password,
        displayName: config.displayName,
        iceServers: config.iceServers,
        iceTransportPolicy: config.iceTransportPolicy,
      },
      {
        onState: (s, d) => {
          setState(s);
          setDetail(d ?? "");
          if (s !== "incoming") setIncoming(null);
        },
        onIncoming: (from) => setIncoming(from),
        onError: (msg) => setError(msg),
        onRecording: (blob) => downloadRecording(blob, config.extension),
        onCallEnded: (entry) => setLog((prev) => [entry, ...prev].slice(0, 50)),
      },
      audioRef.current
    );
    phoneRef.current = phone;
    void phone.start();
  }, []);

  useEffect(() => {
    agentConfig()
      .then((config) => {
        setCfg(config);
        setNeedLogin(false);
      })
      .catch(() => setNeedLogin(true))
      .finally(() => setBooting(false));
  }, []);

  useEffect(() => {
    if (cfg) startPhone(cfg);
  }, [cfg, startPhone]);

  // Load the persisted call log when the agent identity is known, and save it
  // back whenever it changes (per-extension, survives reloads).
  useEffect(() => {
    if (!cfg) return;
    try {
      const raw = localStorage.getItem(`tpbx.calllog.${cfg.extension}`);
      if (raw) setLog(JSON.parse(raw) as CallLogEntry[]);
    } catch {
      /* ignore corrupt storage */
    }
  }, [cfg]);
  useEffect(() => {
    if (!cfg) return;
    try {
      localStorage.setItem(`tpbx.calllog.${cfg.extension}`, JSON.stringify(log));
    } catch {
      /* storage full/unavailable */
    }
  }, [log, cfg]);

  useEffect(() => {
    return () => {
      void phoneRef.current?.stop();
      phoneRef.current = null;
      startedRef.current = false;
    };
  }, []);

  const doLogin = async (extension: string, password: string) => {
    setError("");
    await agentLogin(extension, password);
    const config = await agentConfig();
    setCfg(config);
    setNeedLogin(false);
  };

  const doLogout = async () => {
    await phoneRef.current?.stop();
    phoneRef.current = null;
    startedRef.current = false;
    await agentLogout();
    setCfg(null);
    setNeedLogin(true);
    setState("offline");
  };

  const press = (key: string) => {
    if (state === "active") phoneRef.current?.sendDtmf(key);
    setDial((d) => d + key);
  };

  const placeCall = () => {
    if (!dial) return;
    setError("");
    void phoneRef.current?.call(dial.trim());
  };

  const toggleMute = () => {
    const next = !muted;
    setMuted(next);
    phoneRef.current?.setMuted(next);
  };

  const toggleSpeaker = () => {
    const next = !speaker;
    setSpeaker(next);
    phoneRef.current?.setSpeaker(next);
  };

  const toggleDND = () => {
    const next = !dnd;
    setDnd(next);
    phoneRef.current?.setDND(next);
  };

  const toggleRecord = () => {
    if (recording) {
      phoneRef.current?.stopRecording();
      setRecording(false);
    } else if (phoneRef.current?.startRecording()) {
      setRecording(true);
    } else {
      setError("nothing to record yet");
    }
  };

  const doTransfer = () => {
    const t = transferTarget.trim();
    if (!t) return;
    void phoneRef.current?.blindTransfer(t);
    setTransferring(false);
    setTransferTarget("");
  };

  const inCall = state === "active" || state === "outgoing";

  if (booting) {
    return <div className="phone-shell login-screen" />;
  }

  return (
    <div className="phone-shell">
      <audio ref={audioRef} autoPlay />
      {needLogin ? (
        <LoginCard onLogin={doLogin} error={error} />
      ) : (
        <div className="phone">
          <div className="phone-top">
            <div className="phone-id">
              <strong>{cfg?.displayName}</strong>
              <span>ext {cfg?.extension}</span>
            </div>
            <div className="phone-top-right">
              <span className={`status-pill ${state}`}>
                <span className="status-dot" />
                {STATE_LABEL[state]}
              </span>
              <button
                className={`dnd-toggle ${dnd ? "on" : ""}`}
                onClick={toggleDND}
                title="Do Not Disturb — auto-decline incoming calls"
              >
                DND
              </button>
            </div>
          </div>

          {recording && (
            <div className="rec-bar">
              <span className="rec-dot" /> Recording
            </div>
          )}

          {error && <div className="phone-error">{error}</div>}

          {/* The campaign side of the screen. It renders nothing when the
              agent works no campaigns, so a deployment that does not use them
              sees exactly the softphone it had before. */}
          <AgentDesk />

          <div className="phone-display">
            {transferring ? (
              <div className="transfer-box">
                <input
                  className="dial-input"
                  autoFocus
                  value={transferTarget}
                  placeholder="Transfer to…"
                  onChange={(e) => setTransferTarget(e.target.value.replace(/[^0-9*#+]/g, ""))}
                  onKeyDown={(e) => e.key === "Enter" && doTransfer()}
                />
              </div>
            ) : state === "active" ? (
              <>
                <div className="call-peer">{detail || "connected"}</div>
                <div className="call-timer">{fmtTime(seconds)}</div>
              </>
            ) : state === "outgoing" ? (
              <>
                <div className="call-peer">{detail}</div>
                <div className="call-timer">calling…</div>
              </>
            ) : (
              <input
                className="dial-input"
                value={dial}
                placeholder="Enter number"
                onChange={(e) => setDial(e.target.value.replace(/[^0-9*#+]/g, ""))}
                onKeyDown={(e) => e.key === "Enter" && placeCall()}
              />
            )}
          </div>

          {!transferring && (
            <div className="dialpad">
              {DIALPAD.map((k) => (
                <button key={k} className="key" onClick={() => press(k)}>
                  {k}
                </button>
              ))}
            </div>
          )}

          {/* Action rows */}
          {transferring ? (
            <div className="phone-actions">
              <button className="btn round" onClick={() => setTransferring(false)}>
                Cancel
              </button>
              <button className="btn round call" onClick={doTransfer} disabled={!transferTarget}>
                Transfer
              </button>
            </div>
          ) : inCall ? (
            <>
              <div className="phone-actions">
                <button
                  className={`btn round ${muted ? "muted" : ""}`}
                  onClick={toggleMute}
                  disabled={state !== "active"}
                >
                  {muted ? "Unmute" : "Mute"}
                </button>
                <button
                  className={`btn round ${speaker ? "" : "muted"}`}
                  onClick={toggleSpeaker}
                  disabled={state !== "active"}
                >
                  {speaker ? "Speaker" : "Spk off"}
                </button>
                <button
                  className="btn round"
                  onClick={() => setTransferring(true)}
                  disabled={state !== "active"}
                >
                  Transfer
                </button>
                <button
                  className={`btn round ${recording ? "recording" : ""}`}
                  onClick={toggleRecord}
                  disabled={state !== "active"}
                >
                  {recording ? "Stop Rec" : "Record"}
                </button>
              </div>
              <button className="btn round hangup wide" onClick={() => phoneRef.current?.hangup()}>
                End
              </button>
            </>
          ) : (
            <div className="phone-actions">
              <button className="btn round" onClick={() => setDial((d) => d.slice(0, -1))}>
                ⌫
              </button>
              <button
                className="btn round call"
                onClick={placeCall}
                disabled={state !== "registered" || !dial}
              >
                Call
              </button>
              <button className="btn round" onClick={() => setDial("")}>
                Clear
              </button>
            </div>
          )}

          {log.length > 0 && (
            <div className="call-log">
              <div className="call-log-head">
                <span>Recent</span>
                <button className="call-log-clear" onClick={() => setLog([])}>
                  Clear
                </button>
              </div>
              <ul>
                {log.map((e, i) => {
                  const missed = e.direction === "in" && !e.answered;
                  const kind = missed ? "missed" : e.direction;
                  return (
                    <li
                      key={`${e.at}-${i}`}
                      className={`log-entry ${kind}`}
                      title="Click to redial"
                      onClick={() => setDial(e.peer)}
                    >
                      <span className={`log-icon ${kind}`}>
                        {missed ? "✕" : e.direction === "in" ? "↙" : "↗"}
                      </span>
                      <span className="log-peer">{e.peer}</span>
                      <span className="log-meta">
                        {e.answered ? fmtTime(e.durationSec) : missed ? "missed" : "—"}
                        {" · "}
                        {fmtClock(e.at)}
                      </span>
                    </li>
                  );
                })}
              </ul>
            </div>
          )}

          <button className="phone-logout" onClick={doLogout}>
            Sign out
          </button>
        </div>
      )}

      {incoming && (
        <div className="incoming-overlay">
          <div className="incoming-card">
            <div className="incoming-label">Incoming call</div>
            <div className="incoming-from">{incoming}</div>
            <div className="incoming-actions">
              <button className="btn round hangup" onClick={() => phoneRef.current?.reject()}>
                Decline
              </button>
              <button className="btn round call" onClick={() => phoneRef.current?.answer()}>
                Answer
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function downloadRecording(blob: Blob, extension: string) {
  const ext = blob.type.includes("mp4") ? "mp4" : "webm";
  const stamp = new Date().toISOString().replace(/[:.]/g, "-");
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `call-${extension}-${stamp}.${ext}`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

function LoginCard({
  onLogin,
  error,
}: {
  onLogin: (extension: string, password: string) => Promise<void>;
  error: string;
}) {
  const [extension, setExtension] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [localErr, setLocalErr] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setLocalErr("");
    try {
      await onLogin(extension.trim(), password);
    } catch (err) {
      setLocalErr((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="login-card" onSubmit={submit}>
      <div className="login-brand">
        <img className="brand-logo lg" src={logoDark} alt="XeloVoice" />
        <div className="login-sub">Softphone</div>
      </div>
      <label>
        Extension
        <input
          value={extension}
          autoFocus
          placeholder="1001"
          onChange={(e) => setExtension(e.target.value)}
        />
      </label>
      <label>
        SIP password
        <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
      </label>
      {(localErr || error) && <div className="login-error">{localErr || error}</div>}
      <button className="btn" type="submit" disabled={busy}>
        {busy ? "Signing in…" : "Sign in"}
      </button>
    </form>
  );
}

function fmtTime(sec: number): string {
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

function fmtClock(ms: number): string {
  const d = new Date(ms);
  const today = new Date();
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  return d.toDateString() === today.toDateString()
    ? time
    : `${d.toLocaleDateString([], { month: "short", day: "numeric" })} ${time}`;
}
