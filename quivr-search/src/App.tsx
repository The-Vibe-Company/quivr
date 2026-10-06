import {
  Component,
  type ComponentType,
  type ReactNode,
  lazy,
  Suspense,
  useCallback,
  useDeferredValue,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { ArrowRight, LockKey } from "@phosphor-icons/react";
import {
  AdminIcon,
  AlertsIcon,
  ExplorerIcon,
  FeedIcon,
  MoonIcon,
  PlusIcon,
  SearchIcon,
  SourcesIcon,
  SunIcon,
} from "./components/RailIcons";
import { Brand } from "./components/Logo";
import { ALL, FeedPage, type Filter } from "./components/feed/FeedPage";
import { LoadingState, Notice } from "./components/ui";
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
import { fetchCorpora, scopeOf, type Corpus } from "./lib/corpora";

// The Fil ships with the page; the other tabs and the text form load on first
// use, and are fetched while the browser is idle so a click does not wait.
const loaders = {
  alerts: () => import("./components/alerts/AlertsView"),
  sources: () => import("./components/connectors/ConnectorsView"),
  admin: () => import("./components/admin/AdminView"),
  explorer: () => import("./components/explorer/ExplorerView"),
  addText: () => import("./components/AddText"),
};
const AlertsView = lazy(() => loaders.alerts().then((m) => ({ default: m.AlertsView })));
const ConnectorsView = lazy(() =>
  loaders.sources().then((m) => ({ default: m.ConnectorsView })),
);
const AdminView = lazy(() => loaders.admin().then((m) => ({ default: m.AdminView })));
const ExplorerView = lazy(() =>
  loaders.explorer().then((m) => ({ default: m.ExplorerView })),
);
const AddText = lazy(() => loaders.addText().then((m) => ({ default: m.AddText })));
function prefetchTabs() {
  const load = () => Object.values(loaders).forEach((load) => void load().catch(() => {}));
  if ("requestIdleCallback" in window) {
    const id = requestIdleCallback(load, { timeout: 4000 });
    return () => cancelIdleCallback(id);
  }
  const id = setTimeout(load, 1500);
  return () => clearTimeout(id);
}

type Auth = "loading" | "login" | "ready" | "error";
type View = "feed" | "explorer" | "alerts" | "sources" | "admin";
export type Doc = { record: string; version: string };

// Fil, Explorer, Alertes, Sources and the read-only Admin; search lives in
// the top bar.
const TABS: { view: View; label: string; href: string }[] = [
  { view: "feed", label: "Fil", href: "/" },
  { view: "explorer", label: "Explorer", href: "/?view=explorer" },
  { view: "alerts", label: "Alertes", href: "/?view=alerts" },
  { view: "sources", label: "Sources", href: "/?view=sources" },
  { view: "admin", label: "Admin", href: "/?view=admin" },
];
const TAB_ICONS: Record<View, ComponentType<{ size?: number }>> = {
  feed: FeedIcon,
  explorer: ExplorerIcon,
  alerts: AlertsIcon,
  sources: SourcesIcon,
  admin: AdminIcon,
};
const TITLES: Record<View, string> = {
  feed: "Fil",
  explorer: "Explorer",
  alerts: "Alertes",
  sources: "Sources",
  admin: "Admin",
};

function urlState() {
  const p = new URLSearchParams(location.search);
  const view = p.get("view") || "";
  return {
    view: (view === "alerts" || view === "admin" || view === "explorer"
      ? view
      : ["sources", "connectors"].includes(view)
        ? "sources"
        : "feed") as View,
    alert: p.get("alert"),
    // A document's timeline on the Admin tab.
    version: p.get("version"),
    // The Explorer keeps its own search in the address.
    query: view === "explorer" ? "" : p.get("q") || "",
    // "Idées proches" is on unless the address turns it off.
    near: p.get("near") !== "0" && p.get("mode") !== "lexical",
    doc:
      p.get("record") && p.get("doc")
        ? { record: p.get("record")!, version: p.get("doc")! }
        : null,
    // The corpora the feed or the Explorer spans, the demo corpus by default.
    corpora: (p.get("corpora") || "").split(",").filter(Boolean),
    // A document open in the Explorer.
    explored: view === "explorer" ? p.get("record") : null,
  };
}

// A tab whose code cannot load (offline, or a page left open across a deploy)
// says so and offers a reload, instead of emptying the whole app.
class TabBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  render() {
    return this.state.failed ? (
      <Notice title="Cette page n’a pas pu se charger." onRetry={() => location.reload()}>
        Rechargez la page pour récupérer la dernière version de la démo.
      </Notice>
    ) : (
      this.props.children
    );
  }
}

// The tab the address opens on loads with the page, not after the session.
const opening = urlState().view;
if (opening !== "feed") void loaders[opening]().catch(() => {});

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
  // Every corpus the demo reads; the feed and the Explorer each span some.
  const [allCorpora, setAllCorpora] = useState<Corpus[]>([]);
  const [feedCorpora, setFeedCorpora] = useState<string[]>(() =>
    initial.view === "feed" && initial.corpora.length ? initial.corpora : [corpus],
  );
  const [explored, setExplored] = useState<string[]>(() =>
    initial.view === "explorer" && initial.corpora.length ? initial.corpora : [corpus],
  );
  const [record, setRecord] = useState<string | null>(initial.explored);
  // The Explorer's own part of the address: its search, filters, range and
  // the document it previews (THE-1204).
  const [explorerParams, setExplorerParams] = useState(() =>
    initial.view === "explorer" ? location.search : "",
  );
  const scope = scopeOf(feedCorpora, corpus);
  const searchRef = useRef<HTMLInputElement>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const query = input.trim();

  // New articles go straight into the timeline, marked unread, unless
  // arrivals are paused from the rail.
  const hold = () => paused;
  const feed = useFeedStream(hold, onUnauthorized, scope);
  const alerts = useAlertList(onUnauthorized);
  const sources = useConnectorList(onUnauthorized);
  // Before any child renders: every label of a source reads these names.
  useMemo(() => rememberSourceNames(sources.connectors), [sources.connectors]);
  const reading = useReadState();
  const titles = useMemo(
    () => new Map(feed.items.map((item) => [item.record_id, item.title])),
    [feed.items],
  );

  useEffect(prefetchTabs, []);
  useEffect(() => {
    const controller = new AbortController();
    fetchCorpora(controller.signal)
      .then((list) => {
        setAllCorpora(list.items);
        // A corpus the address names that the demo no longer reads is dropped.
        const known = new Set(list.items.map((c) => c.corpus_id));
        const keep = (ids: string[]) => {
          const kept = ids.filter((id) => known.has(id));
          return kept.length === ids.length ? ids : kept.length ? kept : [corpus];
        };
        setFeedCorpora(keep);
        setExplored(keep);
      })
      .catch((error) => {
        if (error instanceof APIError && error.status === 401) onUnauthorized();
      });
    return () => controller.abort();
  }, [corpus, onUnauthorized]);
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

  // Opening an article marks it read in this browser: in the same render
  // when opened from a page, which answers the click once (THE-1056), and
  // after it for an article the address or an alert opens.
  const { markRead } = reading;
  const open = useCallback((record: string, version: string) => {
    setView("feed");
    setDoc({ record, version });
    markRead({ record_id: record, version_id: version });
  }, [markRead]);
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
    if (view === "feed" && scope) p.set("corpora", scope);
    if (view === "explorer" && scopeOf(explored, corpus)) p.set("corpora", explored.join(","));
    if (view === "explorer")
      for (const [key, value] of new URLSearchParams(explorerParams)) p.append(key, value);
    if (view === "explorer" && record) p.set("record", record);
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
  }, [view, alert, version, query, near, doc, scope, explored, explorerParams, record, corpus]);

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
        // The Explorer has its own search field, in place of the bar's.
        const field = document.querySelector<HTMLInputElement>("#explorer-search") || searchRef.current;
        field?.focus();
        field?.select();
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
    explorer: "",
    alerts: alertCount ? String(alertCount) : "",
    sources: toCheck ? String(toCheck) : "",
    admin: "",
  };
  const badgeLabels: Record<View, string> = {
    feed: "",
    explorer: "",
    admin: "",
    alerts: `${alertCount} article${alertCount > 1 ? "s" : ""} non lu${alertCount > 1 ? "s" : ""} attrapé${alertCount > 1 ? "s" : ""} par vos alertes`,
    sources: `${toCheck} source${toCheck > 1 ? "s" : ""} à vérifier`,
  };
  const liveLabel = paused ? "En pause" : feed.live ? "En direct" : "Reconnexion…";
  // The tab shown follows `view` in a background render: a click on the rail
  // marks the tab at once, then the page it leaves unmounts and the new one
  // mounts without holding that frame (THE-1056). The address, the title and
  // the other state follow `view` at once.
  const page = useDeferredValue(view);
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
                // The Explorer's tab, clicked on a document, goes back to the list.
                if (target === "explorer") setRecord(null);
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
          <span className="bar-meta" ref={setBarMeta} data-stale={view !== page || undefined} />
        </span>
        {view !== "explorer" && (
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
        )}
        {/* A page's main action, at the top right. Until the page asked for
            is drawn (`page` follows `view`), the previous page's stay hidden. */}
        <div className="bar-actions" ref={setBarActions} data-stale={view !== page || undefined} />
      </header>
      <TabBoundary key={page}>
        <Suspense fallback={<LoadingState label="Chargement…" rows={4} />}>
          {page === "feed" ? (
            <FeedPage
              corpus={corpus}
              corpora={feedCorpora}
              scope={scope}
              allCorpora={allCorpora}
              onCorpora={setFeedCorpora}
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
              onSourcesOpen={() => void sources.reload()}
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
          ) : page === "alerts" ? (
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
          ) : page === "explorer" ? (
            <ExplorerView
              corpora={allCorpora}
              picked={explored}
              onPicked={setExplored}
              record={record}
              onRecord={(id) => {
                setRecord(id);
                window.scrollTo(0, 0);
              }}
              initial={explorerParams}
              onState={setExplorerParams}
              onUnauthorized={onUnauthorized}
            />
          ) : page === "admin" ? (
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
        </Suspense>
      </TabBoundary>
      <ChartTip />
      <div className="toast-region" role="status" aria-live="polite">
        {toast && (
          <p className="toast" key={toast.at}>
            {toast.text}
          </p>
        )}
      </div>
      {adding && (
        <TabBoundary>
          <Suspense
            fallback={
              <div className="toast-region" role="status">
                <p className="toast">Chargement…</p>
              </div>
            }
          >
            <AddText
              corpus={corpus}
              onClose={() => setAdding(false)}
              onAdded={() => undefined}
              onOpen={(record, version) => {
                setAdding(false);
                open(record, version);
              }}
            />
          </Suspense>
        </TabBoundary>
      )}
    </div>
  );
}
