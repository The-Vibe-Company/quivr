import {
  type ComponentType,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { ArrowRight, LockKey } from "@phosphor-icons/react";
import {
  AdminIcon,
  AlertsIcon,
  FeedIcon,
  MoonIcon,
  PlusIcon,
  SearchIcon,
  SourcesIcon,
  SunIcon,
} from "./components/RailIcons";
import { Brand } from "./components/Logo";
import { AddText } from "./components/AddText";
import { ConnectorsView } from "./components/connectors/ConnectorsView";
import { ALL, FeedPage, type Filter } from "./components/feed/FeedPage";
import { AlertsView } from "./components/alerts/AlertsView";
import { AdminView } from "./components/admin/AdminView";
import { Notice } from "./components/ui";
import { APIError, login, searchProfiles, session } from "./lib/search";
import { offersDeep } from "./lib/deep";
import { useAlertList, useConnectorList, useFeedStream } from "./lib/workspace";
import { catches } from "./lib/alerts";
import { useReadState } from "./lib/readState";
import { rememberSourceNames } from "./lib/sourceNames";
import { groupSources } from "./components/connectors/SourceList";
import { displayState } from "./components/connectors/HealthBadge";
import { needsCheck } from "./lib/format";
import { currentTheme, onSystemTheme, saveTheme, type Theme } from "./lib/theme";
import { ChartTip } from "./components/ChartTip";

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
const TAB_ICONS: Record<View, ComponentType<{ size?: number }>> = {
  feed: FeedIcon,
  alerts: AlertsIcon,
  sources: SourcesIcon,
  admin: AdminIcon,
};
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
  // The top bar's places a page fills (see InBar), held once mounted.
  const [barMeta, setBarMeta] = useState<HTMLElement | null>(null);
  const [barActions, setBarActions] = useState<HTMLElement | null>(null);
  const bar = useMemo(() => ({ meta: barMeta, actions: barActions }), [barMeta, barActions]);
  const [input, setInput] = useState(initial.query);
  const [near, setNear] = useState(initial.near);
  // "Recherche approfondie": offered when the engine serves it, off at every
  // visit, and never written to the address or to storage.
  const [deepOffered, setDeepOffered] = useState(false);
  const [deep, setDeep] = useState(false);
  const [doc, setDoc] = useState<Doc | null>(initial.doc);
  const [alert, setAlert] = useState<string | null>(initial.alert);
  const [version, setVersion] = useState<string | null>(initial.version);
  const [filter, setFilter] = useState<Filter>(ALL);
  const [paused, setPaused] = useState(false);
  const [adding, setAdding] = useState(false);
  const [toast, setToast] = useState<{ text: string; at: number } | null>(
    null,
  );
  const [openSource, setOpenSource] = useState<string | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const query = input.trim();

  // New articles go straight into the timeline, marked unread, unless
  // arrivals are paused from the rail.
  const hold = () => paused;
  const feed = useFeedStream(hold, onUnauthorized);
  const alerts = useAlertList(onUnauthorized);
  const sources = useConnectorList(onUnauthorized);
  // Before any child renders: every label of a source reads these names.
  useMemo(() => rememberSourceNames(sources.connectors), [sources.connectors]);
  const reading = useReadState();
  const titles = useMemo(
    () => new Map(feed.items.map((item) => [item.record_id, item.title])),
    [feed.items],
  );

  useEffect(() => {
    const controller = new AbortController();
    searchProfiles(controller.signal)
      .then((list) => setDeepOffered(offersDeep(list.items || [])))
      .catch(() => setDeepOffered(false));
    return () => controller.abort();
  }, []);

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

  // Articles an alert caught that this browser has not read yet, dated by
  // the facade's index (every catch, not only the feed's latest articles).
  const caught = useMemo(() => catches(alerts.list, feed.items).byRecord, [alerts.list, feed.items]);
  let alertCount = 0;
  for (const item of caught.values()) if (reading.isUnread(item)) alertCount += 1;
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
    alerts: `${alertCount} article${alertCount > 1 ? "s" : ""} non lu${alertCount > 1 ? "s" : ""} attrapé${alertCount > 1 ? "s" : ""} par vos alertes`,
    sources: `${toCheck} source${toCheck > 1 ? "s" : ""} à vérifier`,
  };
  const liveLabel = paused ? "En pause" : feed.live ? "En direct" : "Reconnexion…";
  const [theme, setTheme] = useState<Theme>(currentTheme);
  useEffect(() => onSystemTheme(setTheme), []);

  return (
    <div className="shell" data-view={view}>
      <nav className="rail" aria-label="Sections">
        <a
          className="rail-brand"
          href="/"
          data-tip="Quivr Veille"
          onClick={(event) => {
            event.preventDefault();
            setView("feed");
            setInput("");
            setDoc(null);
            setFilter(ALL);
          }}
        >
          <span className="rail-logo" aria-hidden="true" />
          <span className="visually-hidden">Quivr Veille, accueil</span>
        </a>
        <span className="rail-sep" aria-hidden="true" />
        {TABS.map(({ view: target, label, href }) => {
          const Icon = TAB_ICONS[target];
          const tab = (
            <a
              key={target}
              href={href}
              className="rail-tab"
              data-section={target}
              data-tip={label}
              aria-current={view === target ? "page" : undefined}
              onClick={(event) => {
                event.preventDefault();
                if (target === "alerts") setAlert(null);
                if (target !== view) setDoc(null);
                setView(target);
              }}
            >
              <Icon />
              <span className="visually-hidden">{label}</span>
              {badges[target] && (
                <span className="rail-badge" data-kind={target}>
                  <span aria-hidden="true">
                    {target === "alerts" ? badges[target] : ""}
                  </span>
                  <span className="visually-hidden">
                    , {badgeLabels[target]}
                  </span>
                </span>
              )}
            </a>
          );
          if (target !== "admin") return tab;
          // Admin sits at the bottom, under the two tools: adding a text
          // (mostly for demos) and pausing arrivals.
          return (
            <div key={target} className="rail-foot">
              <button
                type="button"
                className="rail-tool"
                data-tip={theme === "dark" ? "Passer en mode clair" : "Passer en mode sombre"}
                onClick={() => {
                  const next = theme === "dark" ? "light" : "dark";
                  saveTheme(next);
                  setTheme(next);
                }}
              >
                {theme === "dark" ? <SunIcon /> : <MoonIcon />}
                <span className="visually-hidden">
                  {theme === "dark" ? "Passer en mode clair" : "Passer en mode sombre"}
                </span>
              </button>
              <button
                type="button"
                className="rail-tool"
                data-tip="Ajouter du texte"
                onClick={() => setAdding(true)}
              >
                <PlusIcon />
                <span className="visually-hidden">Ajouter du texte</span>
              </button>
              <button
                type="button"
                className="rail-tool bar-live"
                data-state={paused ? "paused" : feed.live ? "live" : "off"}
                aria-pressed={!paused}
                data-tip={paused ? "Reprendre les arrivées" : "Mettre les arrivées en pause"}
                onClick={() => {
                  if (paused) feed.showPending();
                  setPaused((p) => !p);
                  notify(
                    paused
                      ? "Les nouveaux articles s’affichent de nouveau dès leur arrivée."
                      : "Arrivées en pause : les nouveaux articles s’afficheront à la reprise.",
                  );
                }}
              >
                <span className="bar-live-dot" aria-hidden="true" />
                <span className="visually-hidden">{liveLabel}</span>
              </button>
              <span className="rail-sep" aria-hidden="true" />
              {tab}
            </div>
          );
        })}
      </nav>
      <header className="bar">
        <span className="bar-view">
          <span className="bar-view-icon" aria-hidden="true">
            {(() => {
              const ViewIcon = TAB_ICONS[view];
              return <ViewIcon size={16} />;
            })()}
          </span>
          {TITLES[view]}
          {/* A page's own figures beside its title: its count, what needs a look. */}
          <span className="bar-meta" ref={setBarMeta} />
        </span>
        <form
          role="search"
          className="bar-search"
          data-active={!!query || undefined}
          onSubmit={(event) => {
            event.preventDefault();
            setView("feed");
          }}
        >
          <span className="bar-search-icon" aria-hidden="true">
            <SearchIcon size={16} />
          </span>
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
              // A search keeps the chosen sources, which it searches within.
              setFilter((f) =>
                f.read === "all" && !f.alerts.length ? f : { ...ALL, sources: f.sources },
              );
            }}
            onKeyDown={(event) => {
              if (event.key === "Escape" && input) {
                event.preventDefault();
                setInput("");
              }
            }}
          />
          {input ? (
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
          ) : (
            <kbd className="bar-kbd" aria-hidden="true">
              /
            </kbd>
          )}
        </form>
        {/* A page's main action, at the top right. */}
        <div className="bar-actions" ref={setBarActions} />
      </header>
      {view === "feed" ? (
        <FeedPage
          corpus={corpus}
          query={query}
          near={near}
          onNear={setNear}
          deepOffered={deepOffered}
          deep={deep}
          onDeep={setDeep}
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
          onAlerts={() => {
            setDoc(null);
            setView("alerts");
          }}
          onSources={(id) => {
            setDoc(null);
            setOpenSource(id || null);
            setView("sources");
          }}
          notify={notify}
          onUnauthorized={onUnauthorized}
        />
      ) : view === "alerts" ? (
        <AlertsView
          corpus={corpus}
          bar={bar}
          selected={alert}
          onSelect={setAlert}
          doc={doc}
          // Articles open in the reader over the page; the open one closes it.
          onOpen={(record, version) => setDoc(doc?.record === record ? null : { record, version })}
          onCloseDoc={() => setDoc(null)}
          onSimilar={(text) => {
            setInput(text);
            setNear(true);
            setFilter(ALL);
            setDoc(null);
            setView("feed");
          }}
          connectors={sources.connectors}
          feedItems={feed.items}
          isUnread={reading.isUnread}
          onMarkAllRead={reading.markAllRead}
          onChanged={() => void alerts.reload()}
          notify={notify}
          onUnauthorized={onUnauthorized}
        />
      ) : view === "admin" ? (
        <AdminView
          titles={titles}
          connectors={sources.connectors}
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
          bar={bar}
          feedItems={feed.items}
          initialSelected={openSource}
          onChanged={() => void sources.reload()}
          onAdd={() => setAdding(true)}
          notify={notify}
          onUnauthorized={onUnauthorized}
        />
      )}
      <ChartTip />
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
