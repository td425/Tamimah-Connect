import { useCallback, useEffect, useRef, useState } from "react";
import {
  APP_VERSION,
  can,
  connectEvents,
  getBranding,
  getMe,
  getSoftphoneInfo,
  logout,
  type Feature,
  type Me,
  type SoftphoneInfo,
  type WsEnvelope,
} from "./api";
import type { Toast } from "./types";
import { describeEvent, nowTime, type TickerLine } from "./events";
import Dashboard from "./components/Dashboard";
import Extensions from "./components/Extensions";
import Trunks from "./components/Trunks";
import Routing from "./components/Routing";
import IVRPage from "./components/IVR";
import Leads from "./components/Leads";
import Transports from "./components/Transports";
import Analytics from "./components/Analytics";
import Settings from "./components/Settings";
import Users from "./components/Users";
import CallHistory from "./components/CallHistory";
import Login from "./components/Login";
import TotpSetup from "./components/TotpSetup";
import logoLight from "./assets/xelo-light.png";
import logoDark from "./assets/xelo-dark.png";

// `feature`, when present, restricts a nav item to users whose role grants
// "view" on that feature. The dashboard has no feature gate — every
// authenticated user lands there.
const NAV: { key: string; label: string; ready: boolean; feature?: Feature }[] = [
  { key: "dashboard", label: "Dashboard", ready: true },
  { key: "extensions", label: "Extensions", ready: true, feature: "extensions" },
  { key: "trunks", label: "Trunks", ready: true, feature: "trunks" },
  { key: "routing", label: "Routing", ready: true, feature: "routing" },
  { key: "ivr", label: "IVR", ready: true, feature: "ivr" },
  { key: "leads", label: "Leads", ready: true, feature: "leads" },
  { key: "cdr", label: "Call History", ready: true, feature: "cdr" },
  { key: "analytics", label: "Analytics", ready: true, feature: "analytics" },
  { key: "transports", label: "Transports / TLS", ready: true, feature: "transports" },
  { key: "settings", label: "Settings", ready: true, feature: "settings" },
  { key: "users", label: "Users", ready: true, feature: "users" },
];

function currentView(): string {
  const h = (location.hash || "#dashboard").slice(1);
  return NAV.some((n) => n.key === h && n.ready) ? h : "dashboard";
}

function savedTheme(): "light" | "dark" | null {
  const s = localStorage.getItem("tpbx.theme");
  return s === "light" || s === "dark" ? s : null;
}

function initialTheme(): "light" | "dark" {
  return savedTheme() ?? (window.matchMedia?.("(prefers-color-scheme: light)").matches ? "light" : "dark");
}

export default function App() {
  const [me, setMe] = useState<Me | null>(null);
  const [authChecked, setAuthChecked] = useState(false);
  const [theme, setTheme] = useState<"light" | "dark">(initialTheme);
  // Whether the user has an explicit saved theme. Only then do we skip the
  // admin-configured default theme from branding.
  const hasSavedTheme = useRef(savedTheme() !== null);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);
  const toggleTheme = () =>
    setTheme((t) => {
      const next = t === "light" ? "dark" : "light";
      localStorage.setItem("tpbx.theme", next); // an explicit toggle is remembered
      hasSavedTheme.current = true;
      return next;
    });

  // Apply admin branding: set the tab title, and adopt the configured default
  // theme for users who have not chosen one themselves.
  useEffect(() => {
    getBranding()
      .then((b) => {
        if (b.brandName) document.title = b.brandName + " · Control Console";
        if (!hasSavedTheme.current && (b.defaultTheme === "light" || b.defaultTheme === "dark")) {
          setTheme(b.defaultTheme);
        }
      })
      .catch(() => {});
  }, []);

  useEffect(() => {
    getMe()
      .then(setMe)
      .catch(() => setMe(null))
      .finally(() => setAuthChecked(true));
  }, []);

  if (!authChecked) {
    return <div className="login-screen" />; // brief blank while checking session
  }
  if (!me) {
    return <Login onLogin={setMe} />;
  }
  if (me.totpSetupRequired) {
    // The user's role mandates two-factor and they have not enrolled yet:
    // force enrolment before the console is reachable. Re-fetch identity on
    // success so the (now-cleared) requirement takes effect.
    return (
      <div className="login-screen">
        <div className="login-card">
          <div className="login-brand">
            <img className="brand-logo lg" src={logoLight} alt="XeloVoice" />
            <div className="rev">TWO-FACTOR REQUIRED</div>
          </div>
          <TotpSetup onDone={() => getMe().then(setMe).catch(() => setMe(null))} />
          <button className="btn ghost small" onClick={() => logout().then(() => setMe(null))}>
            Sign out
          </button>
        </div>
      </div>
    );
  }
  return <Console me={me} onLogout={() => setMe(null)} theme={theme} onToggleTheme={toggleTheme} />;
}

function Console({
  me,
  onLogout,
  theme,
  onToggleTheme,
}: {
  me: Me;
  onLogout: () => void;
  theme: "light" | "dark";
  onToggleTheme: () => void;
}) {
  const nav = NAV.filter((n) => !n.feature || can(me, n.feature, "view"));
  const [view, setView] = useState<string>(currentView());
  const [wsOpen, setWsOpen] = useState(false);
  const [lines, setLines] = useState<TickerLine[]>([]);
  const [toast, setToast] = useState<Toast | null>(null);
  const [softphone, setSoftphone] = useState<SoftphoneInfo | null>(null);
  const lineId = useRef(0);

  useEffect(() => {
    const onHash = () => setView(currentView());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  useEffect(() => {
    getSoftphoneInfo().then(setSoftphone).catch(() => setSoftphone(null));
  }, []);

  useEffect(() => {
    return connectEvents((env: WsEnvelope) => {
      if (env.kind === "hello") return;
      const d = describeEvent(env);
      if (!d) return; // noise -> skip
      setLines((prev) =>
        [
          { id: lineId.current++, category: d.category, text: d.text, time: nowTime() },
          ...prev,
        ].slice(0, 100)
      );
    }, setWsOpen);
  }, []);

  const notify = useCallback((t: Toast) => {
    setToast(t);
    setTimeout(() => setToast(null), 4000);
  }, []);

  const doLogout = async () => {
    await logout();
    onLogout();
  };

  // Fall back to the dashboard if the current view is one this user cannot see
  // (e.g. they navigated by URL hash to a feature their role lacks).
  const viewNav = NAV.find((n) => n.key === view);
  const allowedView = viewNav && (!viewNav.feature || can(me, viewNav.feature, "view")) ? view : "dashboard";

  return (
    <div className="app">
      <div className="brand">
        <div>
          <img
            className="brand-logo"
            src={theme === "light" ? logoLight : logoDark}
            alt="XeloVoice"
          />
          <div className="rev">CONTROL CONSOLE</div>
        </div>
      </div>

      <div className="topbar">
        <span />
        <span className="topbar-right">
          <span className="ver">{APP_VERSION}</span>
          <span>
            <span className={`dot ${wsOpen ? "up" : "down"}`} />
            {wsOpen ? "LIVE" : "RECONNECTING"}
          </span>
          <span className="ver">{me.username}</span>
          {(softphone?.installers ?? []).map((inst) => {
            const label = inst.platform === "android" ? "Android" : "Windows";
            return inst.available ? (
              <a
                key={inst.platform}
                className="btn ghost small"
                href={inst.url}
                download
                title={`Download the ${label} softphone`}
              >
                ⬇ Softphone ({label})
              </a>
            ) : (
              <button
                key={inst.platform}
                className="btn ghost small"
                disabled
                title={`The ${label} softphone installer has not been published to this server yet.`}
              >
                {label} softphone (n/a)
              </button>
            );
          })}
          <button
            className="btn ghost small"
            onClick={onToggleTheme}
            title={theme === "light" ? "Switch to dark" : "Switch to light"}
          >
            {theme === "light" ? "Dark" : "Light"}
          </button>
          <button className="btn ghost small" onClick={doLogout}>
            Logout
          </button>
        </span>
      </div>

      <nav className="nav">
        {nav.map((n) => (
          <a
            key={n.key}
            href={n.ready ? `#${n.key}` : undefined}
            className={`${n.key === view ? "active" : ""} ${n.ready ? "" : "soon"}`}
          >
            {n.label}
            {!n.ready && <span className="soon-tag">SOON</span>}
          </a>
        ))}
        <div className="nav-notice">
          Designed &amp; developed by <strong>Xelocorp</strong>. XeloVoice is a
          product of Xelocorp. Do not resell or modify this software without
          official confirmation from Xelocorp.
        </div>
      </nav>

      <main className="main">
        {allowedView === "extensions" ? (
          <Extensions notify={notify} me={me} />
        ) : allowedView === "trunks" ? (
          <Trunks notify={notify} me={me} />
        ) : allowedView === "routing" ? (
          <Routing notify={notify} me={me} />
        ) : allowedView === "ivr" ? (
          <IVRPage notify={notify} me={me} />
        ) : allowedView === "leads" ? (
          <Leads notify={notify} me={me} />
        ) : allowedView === "transports" ? (
          <Transports notify={notify} me={me} />
        ) : allowedView === "analytics" ? (
          <Analytics notify={notify} />
        ) : allowedView === "settings" ? (
          <Settings notify={notify} me={me} />
        ) : allowedView === "users" ? (
          <Users notify={notify} me={me} />
        ) : allowedView === "cdr" ? (
          <CallHistory notify={notify} me={me} />
        ) : (
          <Dashboard wsOpen={wsOpen} lines={lines} notify={notify} />
        )}
      </main>

      {toast && <div className={`toast ${toast.kind}`}>{toast.text}</div>}
    </div>
  );
}
