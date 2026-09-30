import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ArrowRight, LockKey, NotePencil } from "@phosphor-icons/react";
import { Brand } from "./components/Logo";
import { AddText } from "./components/AddText";
import { ConnectorsView } from "./components/connectors/ConnectorsView";
import { FeedPage, type Filter } from "./components/feed/FeedPage";
import { AlertsView } from "./components/alerts/AlertsView";
import { AdminView } from "./components/admin/AdminView";
import { Notice } from "./components/ui";
import { APIError, login, session } from "./lib/search";
import { useAlertList, useConnectorList, useFeedStream } from "./lib/workspace";
import { useReadState } from "./lib/readState";
import { groupSources } from "./components/connectors/SourceList";
import { displayState } from "./components/connectors/HealthBadge";
import { needsCheck } from "./lib/format";

type Auth = "loading" | "login" | "ready" | "error";
type View = "feed" | "alerts" | "sources" | "admin";
export type Doc = { record: string; version: string };

// Fil, Alertes, Sources and the read-only Admin; search lives in the top bar.
const TABS: { view: View; label: string; href: string }[] = [
  { view: "feed", label: "Fil", href: "/" },
  { view: "alerts", label: "Alertes", href: "/?view=alerts" },
  { view: "sources", label: "Sources", href: "/?view=sources" },
  { view: "admin", label: "Admin", href: "/?view=admin" },
];
const TITLES: Record<View, string> = {
  feed: "Fil",
  alerts: "Alertes",
  sources: "Sources",
  admin: "Admin",
};

function urlState() {
  const p = new URLSearchParams(location.search);
  const view = p.get("view") || "";
  return {
    view: (view === "alerts" || view === "admin"
      ? view
      : ["sources", "connectors"].includes(view)
        ? "sources"
        : "feed") as View,
    alert: p.get("alert"),
    // A document's timeline on the Admin tab.
    version: p.get("version"),
    query: p.get("q") || "",
    // "Idées proches" is on unless the address turns it off.
    near: p.get("near") !== "0" && p.get("mode") !== "lexical",
    doc:
      p.get("record") && p.get("doc")
        ? { record: p.get("record")!, version: p.get("doc")! }
        : null,
  };
}

export default function App() {
  const [auth, setAuth] = useState<Auth>("loading");
  const [corpus, setCorpus] = useState("");
  const [authError, setAuthError] = useState("");
  const [password, setPassword] = useState("");
  const [loginBusy, setLoginBusy] = useState(false);
  const connect = useCallback(async () => {
    setAuthError("");
    setAuth("loading");
    try {
      const config = await session();
      setCorpus(config.corpus_id);
      setAuth("ready");
    } catch (error) {
      setAuth(
        error instanceof APIError && error.status === 401 ? "login" : "error",
      );
      setAuthError(
        error instanceof Error ? error.message : "Connexion indisponible.",
      );
    }
  }, []);
  useEffect(() => {
    void connect();
  }, [connect]);
  useEffect(() => {
    const keyboard = () => {
      document.documentElement.dataset.input = "keyboard";
    };
    const pointer = () => {
      document.documentElement.dataset.input = "pointer";
    };
    window.addEventListener("keydown", keyboard);
    window.addEventListener("pointerdown", pointer);
    return () => {
      window.removeEventListener("keydown", keyboard);
      window.removeEventListener("pointerdown", pointer);
    };
  }, []);
  const onUnauthorized = useCallback(() => setAuth("login"), []);

  if (auth === "ready")
    return <Dashboard corpus={corpus} onUnauthorized={onUnauthorized} />;
  return (
    <div className="auth-page">
      <a href="/" className="brand brand-compact">
        <Brand />
      </a>
      <main className="auth-card">
        <div className="modal-icon">
          <LockKey size={25} aria-hidden="true" />
        </div>
        <h1>Votre espace de démo.</h1>
        <p className="muted">Suivez vos sources. Soyez prévenu de ce qui compte.</p>
        {auth === "loading" ? (
          <p role="status" className="auth-status">
            Connexion à votre espace…
          </p>
        ) : auth === "error" ? (
          <Notice title="Connexion indisponible." onRetry={() => void connect()}>
            {authError}
          </Notice>
        ) : (
          <form
            onSubmit={async (event) => {
              event.preventDefault();
              if (loginBusy) return;
              setLoginBusy(true);
              setAuthError("");
              try {
                await login(password);
                setPassword("");
                await connect();
              } catch (error) {
                setAuthError(
                  error instanceof Error ? error.message : "Connexion impossible.",
                );
              } finally {
                setLoginBusy(false);
              }
            }}
          >
            <label className="field-label" htmlFor="password">
              Mot de passe
            </label>
            <input
              id="password"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              autoFocus
              required
            />
            {authError && (
              <p className="error-text" role="alert">
                {authError}
              </p>
            )}
            <button className="button primary" disabled={!password || loginBusy}>
              {loginBusy ? "Connexion…" : "Ouvrir la démo"}
              <ArrowRight size={17} aria-hidden="true" />
            </button>
          </form>
        )}
      </main>
    </div>
  );
}

function Dashboard({
  corpus,
  onUnauthorized,
}: {
  corpus: string;
  onUnauthorized: () => void;
}) {
  const [initial] = useState(urlState);
  const [view, setView] = useState<View>(initial.view);
  const [input, setInput] = useState(initial.query);
  const [near, setNear] = useState(initial.near);
  const [doc, setDoc] = useState<Doc | null>(initial.doc);
  const [alert, setAlert] = useState<string | null>(initial.alert);
  const [version, setVersion] = useState<string | null>(initial.version);
  const [filter, setFilter] = useState<Filter>({ kind: "all" });
  const [paused, setPaused] = useState(false);
  const [adding, setAdding] = useState(false);
  const [toast, setToast] = useState<{ text: string; at: number } | null>(
    null,
  );
  const [openSource, setOpenSource] = useState<string | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const query = input.trim();

  const hold = () =>
    paused ||
    (view === "feed" &&
      (!!query ||
        !!doc ||
        (scroller.current?.scrollTop || 0) > 8 ||
        window.scrollY > 8));
  const feed = useFeedStream(hold, onUnauthorized);
  const alerts = useAlertList(onUnauthorized);
  const sources = useConnectorList(onUnauthorized);
  const reading = useReadState();
  const titles = useMemo(
    () => new Map(feed.items.map((item) => [item.record_id, item.title])),
    [feed.items],
  );

  const notify = useCallback((text: string) => {
    setToast({ text, at: Date.now() });
  }, []);
  useEffect(() => {
    if (!toast) return;
    const timer = setTimeout(() => setToast(null), 3500);
    return () => clearTimeout(timer);
  }, [toast]);

  const open = useCallback((record: string, version: string) => {
    setView("feed");
    setDoc({ record, version });
  }, []);
  // Opening an article marks it read in this browser.
  const { markRead } = reading;
  useEffect(() => {
    if (doc) markRead({ record_id: doc.record, version_id: doc.version });
  }, [doc, markRead]);

  useEffect(() => {
    const p = new URLSearchParams();
    if (view !== "feed") p.set("view", view);
    if (view === "alerts" && alert) p.set("alert", alert);
    if (view === "admin" && version) p.set("version", version);
    if (view === "feed" && query) p.set("q", query);
    if (view === "feed" && query && !near) p.set("near", "0");
    if (view === "feed" && doc) {
      p.set("record", doc.record);
      p.set("doc", doc.version);
    }
    history.replaceState(
      null,
      "",
      p.size ? "?" + p.toString() : location.pathname,
    );
    document.title =
      view === "feed" && query
        ? `${query} — Quivr Veille`
        : `${TITLES[view]} — Quivr Veille`;
  }, [view, alert, version, query, near, doc]);

  // "/" or Ctrl/Cmd+K puts the cursor in the search box, from any page.
  useEffect(() => {
    const listener = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement;
      const typing =
        ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName) ||
        target.isContentEditable;
      if (adding || document.querySelector("dialog[open]")) return;
      if (
        (event.key === "/" && !typing) ||
        ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k")
      ) {
        event.preventDefault();
        searchRef.current?.focus();
        searchRef.current?.select();
      }
    };
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, [adding]);

  const alertCount = alerts.list?.available
    ? Object.keys(alerts.list.matched).length
    : 0;
  const toCheck = groupSources(sources.connectors).filter((c) =>
    needsCheck(displayState(c)),
  ).length;
  const badges: Record<View, string> = {
    feed: "",
    alerts: alertCount ? String(alertCount) : "",
    sources: toCheck ? String(toCheck) : "",
    admin: "",
  };
  const badgeLabels: Record<View, string> = {
    feed: "",
    admin: "",
    alerts: `${alertCount} article${alertCount > 1 ? "s" : ""} attrapé${alertCount > 1 ? "s" : ""} par vos alertes`,
    sources: `${toCheck} source${toCheck > 1 ? "s" : ""} à vérifier`,
  };
  const liveLabel = paused ? "En pause" : feed.live ? "En direct" : "Reconnexion…";

  return (
    <div className="shell" data-view={view}>
      <header className="bar">
        <a
          className="bar-brand"
          href="/"
          onClick={(event) => {
            event.preventDefault();
            setView("feed");
            setInput("");
            setDoc(null);
            setFilter({ kind: "all" });
          }}
        >
          <span className="bar-name">Quivr</span>
          <span className="bar-product">Veille</span>
        </a>
        <nav className="bar-tabs" aria-label="Sections">
          {TABS.map(({ view: target, label, href }) => (
            <a
              key={target}
              href={href}
              aria-current={view === target ? "page" : undefined}
              onClick={(event) => {
                event.preventDefault();
                if (target === "alerts") setAlert(null);
                setView(target);
              }}
            >
              {label}
              {badges[target] && (
                <span className="bar-badge">
                  <span aria-hidden="true">
                    {badges[target]}
                    {target === "sources" && (
                      <span className="bar-badge-long"> à vérifier</span>
                    )}
                  </span>
                  <span className="visually-hidden">
                    , {badgeLabels[target]}
                  </span>
                </span>
              )}
            </a>
          ))}
        </nav>
        <form
          role="search"
          className="bar-search"
          data-active={!!query || undefined}
          onSubmit={(event) => {
            event.preventDefault();
            setView("feed");
          }}
        >
          <svg
            width="16"
            height="16"
            viewBox="0 0 20 20"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            aria-hidden="true"
          >
            <circle cx="8.5" cy="8.5" r="6" />
            <path d="M13 13l5 5" strokeLinecap="round" />
          </svg>
          <input
            ref={searchRef}
            type="search"
            aria-label="Rechercher dans le fil"
            placeholder="Rechercher un mot, un lieu, une personne…"
            value={input}
            maxLength={500}
            onChange={(event) => {
              setInput(event.target.value);
              setView("feed");
              setDoc(null);
              setFilter((f) => (f.kind === "source" ? f : { kind: "all" }));
            }}
            onKeyDown={(event) => {
              if (event.key === "Escape" && input) {
                event.preventDefault();
                setInput("");
              }
            }}
          />
          {input && (
            <button
              type="button"
              className="bar-clear"
              onClick={() => {
                setInput("");
                searchRef.current?.focus();
              }}
            >
              Effacer
            </button>
          )}
        </form>
        <div className="bar-actions">
          <button
            type="button"
            className="bar-add"
            onClick={() => setAdding(true)}
          >
            <NotePencil size={16} aria-hidden="true" />
            <span className="bar-add-label">Ajouter du texte</span>
          </button>
          <button
            type="button"
            className="bar-live"
            data-state={paused ? "paused" : feed.live ? "live" : "off"}
            aria-pressed={!paused}
            title={paused ? "Reprendre les arrivées" : "Mettre les arrivées en pause"}
            onClick={() => {
              setPaused((p) => !p);
              notify(
                paused
                  ? "Les nouveaux articles s’affichent de nouveau dès leur arrivée."
                  : "Arrivées en pause : les nouveaux articles attendent en haut du fil.",
              );
            }}
          >
            <span className="bar-live-dot" aria-hidden="true" />
            {liveLabel}
          </button>
        </div>
      </header>
      {view === "feed" ? (
        <FeedPage
          corpus={corpus}
          query={query}
          near={near}
          onNear={setNear}
          filter={filter}
          onFilter={setFilter}
          doc={doc}
          onOpen={open}
          onClose={() => setDoc(null)}
          onQuery={setInput}
          feed={feed}
          alerts={alerts}
          connectors={sources.connectors}
          reading={reading}
          scroller={scroller}
          onAdd={() => setAdding(true)}
          onAlerts={() => setView("alerts")}
          onSources={(id) => {
            setOpenSource(id || null);
            setView("sources");
          }}
          notify={notify}
          onUnauthorized={onUnauthorized}
        />
      ) : view === "alerts" ? (
        <AlertsView
          selected={alert}
          onSelect={setAlert}
          onOpen={open}
          connectors={sources.connectors}
          feedItems={feed.items}
          onChanged={() => void alerts.reload()}
          notify={notify}
          onUnauthorized={onUnauthorized}
        />
      ) : view === "admin" ? (
        <AdminView
          titles={titles}
          selected={version}
          onSelect={setVersion}
          onOpen={(record, target) => {
            setVersion(null);
            open(record, target);
          }}
          onAdd={() => setAdding(true)}
          onSources={() => {
            setOpenSource(null);
            setView("sources");
          }}
          onUnauthorized={onUnauthorized}
        />
      ) : (
        <ConnectorsView
          corpus={corpus}
          feedItems={feed.items}
          matched={alerts.list?.matched || {}}
          initialSelected={openSource}
          onChanged={() => void sources.reload()}
          onAdd={() => setAdding(true)}
          notify={notify}
          onUnauthorized={onUnauthorized}
        />
      )}
      <div className="toast-region" role="status" aria-live="polite">
        {toast && (
          <p className="toast" key={toast.at}>
            {toast.text}
          </p>
        )}
      </div>
      {adding && (
        <AddText
          corpus={corpus}
          onClose={() => setAdding(false)}
          onAdded={() => undefined}
          onOpen={(record, version) => {
            setAdding(false);
            open(record, version);
          }}
        />
      )}
    </div>
  );
}
