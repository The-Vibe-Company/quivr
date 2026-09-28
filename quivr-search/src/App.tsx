import { useCallback, useEffect, useRef, useState } from "react";
import {
  ArrowRight,
  FileText,
  Plus,
  MagnifyingGlass,
  Sparkle,
  LockKey,
} from "@phosphor-icons/react";
import { Brand } from "./components/Logo";
import { SearchBar } from "./components/SearchBar";
import { ModeSwitch, MODES } from "./components/ModeSwitch";
import { ResultItem } from "./components/ResultItem";
import { DocumentPanel } from "./components/DocumentPanel";
import { AddText } from "./components/AddText";
import { ConnectorsView } from "./components/connectors/ConnectorsView";
import { APIError, login, search, session, tokenize } from "./lib/search";
import type { Mode, SearchResponse } from "./types";

type Auth = "loading" | "login" | "ready" | "error";
type View = "search" | "connectors";
function urlState() {
  const p = new URLSearchParams(location.search);
  return {
    view: (p.get("view") === "connectors" ? "connectors" : "search") as View,
    query: p.get("q") || "",
    mode: (["hybrid", "lexical", "semantic"].includes(p.get("mode") || "")
      ? p.get("mode")
      : "hybrid") as Mode,
    doc:
      p.get("record") && p.get("doc")
        ? { record: p.get("record")!, version: p.get("doc")! }
        : null,
  };
}
export default function App() {
  const [initial] = useState(urlState);
  const [auth, setAuth] = useState<Auth>("loading");
  const [corpus, setCorpus] = useState("");
  const [authError, setAuthError] = useState("");
  const [password, setPassword] = useState("");
  const [loginBusy, setLoginBusy] = useState(false);
  const [input, setInput] = useState(initial.query);
  const [query, setQuery] = useState(initial.query);
  const [mode, setMode] = useState<Mode>(initial.mode);
  const [response, setResponse] = useState<SearchResponse | null>(null);
  const [status, setStatus] = useState<"idle" | "loading" | "ready" | "error">(
    "idle",
  );
  const [searchError, setSearchError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const [doc, setDoc] = useState(initial.doc);
  const [adding, setAdding] = useState(false);
  const [view, setView] = useState<View>(initial.view);
  const inputRef = useRef<HTMLInputElement>(null);
  const terms = tokenize(query);
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
    if (!query.trim() || auth !== "ready") {
      setStatus("idle");
      setResponse(null);
      return;
    }
    const controller = new AbortController();
    setStatus("loading");
    setSearchError("");
    search(query, mode, corpus, controller.signal)
      .then((data) => {
        setResponse(data);
        setStatus("ready");
      })
      .catch((error) => {
        if (controller.signal.aborted) return;
        if (error instanceof APIError && error.status === 401) {
          setAuth("login");
          setDoc(null);
          setAdding(false);
        }
        setSearchError(
          error instanceof APIError && error.status === 503
            ? "La recherche est momentanément indisponible. Réessayez, ou utilisez les mots-clés si la recherche par sens se prépare."
            : error.message,
        );
        setStatus("error");
      });
    return () => controller.abort();
  }, [query, mode, corpus, auth, attempt]);
  useEffect(() => {
    const p = new URLSearchParams();
    if (view === "connectors") p.set("view", "connectors");
    if (query) p.set("q", query);
    if (mode !== "hybrid") p.set("mode", mode);
    if (doc) {
      p.set("record", doc.record);
      p.set("doc", doc.version);
    }
    history.replaceState(
      null,
      "",
      p.size ? "?" + p.toString() : location.pathname,
    );
    document.title =
      view === "connectors"
        ? "Connecteurs — Quivr Search"
        : query
          ? `${query} — Quivr Search`
          : "Quivr Search";
  }, [query, mode, doc, view]);
  useEffect(() => {
    const listener = (event: KeyboardEvent) => {
      document.documentElement.dataset.input = "keyboard";
      const typing =
        event.target instanceof HTMLElement &&
        (["INPUT", "TEXTAREA"].includes(event.target.tagName) ||
          event.target.isContentEditable);
      if (adding || doc || view === "connectors") return;
      if (
        (event.key === "/" && !typing) ||
        ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k")
      ) {
        event.preventDefault();
        inputRef.current?.focus();
        inputRef.current?.select();
      }
      if (event.key === "ArrowDown" && !typing) {
        const links = [
          ...document.querySelectorAll<HTMLAnchorElement>(".result-link"),
        ];
        const index = links.indexOf(
          document.activeElement as HTMLAnchorElement,
        );
        if (links[index + 1]) {
          event.preventDefault();
          links[index + 1].focus();
        }
      }
    };
    const pointer = () => {
      document.documentElement.dataset.input = "pointer";
    };
    window.addEventListener("keydown", listener);
    window.addEventListener("pointerdown", pointer);
    return () => {
      window.removeEventListener("keydown", listener);
      window.removeEventListener("pointerdown", pointer);
    };
  }, [adding, doc, view]);
  useEffect(() => {
    const media = matchMedia("(prefers-color-scheme: dark)");
    const change = () => {
      const style = document.createElement("style");
      style.textContent = "*,*::before,*::after{transition:none!important}";
      document.head.append(style);
      void document.body.offsetHeight;
      requestAnimationFrame(() => requestAnimationFrame(() => style.remove()));
    };
    media.addEventListener("change", change);
    return () => media.removeEventListener("change", change);
  }, []);
  function runSearch(value: string) {
    setInput(value);
    setQuery(value);
    setAttempt((value) => value + 1);
    setDoc(null);
  }
  const onAdded = useCallback(() => setAttempt((value) => value + 1), []);
  const home = () => {
    setView("search");
    setQuery("");
    setInput("");
    setDoc(null);
  };
  const onUnauthorized = useCallback(() => setAuth("login"), []);
  if (auth !== "ready")
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
          <p className="muted">Ajoutez vos textes. Retrouvez ce qui compte.</p>
          {auth === "loading" ? (
            <p role="status">Connexion à votre espace…</p>
          ) : auth === "error" ? (
            <div className="notice" role="alert">
              <p>{authError}</p>
              <button className="button" onClick={() => void connect()}>
                Réessayer
              </button>
            </div>
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
                    error instanceof Error
                      ? error.message
                      : "Connexion impossible.",
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
              <button
                className="button primary"
                disabled={!password || loginBusy}
              >
                {loginBusy ? "Connexion…" : "Ouvrir la démo"}
                <ArrowRight size={17} aria-hidden="true" />
              </button>
            </form>
          )}
        </main>
      </div>
    );
  return (
    <div
      className="app"
      data-view={
        view === "connectors" ? "connectors" : query ? "results" : "home"
      }
    >
      <header className="topbar">
        <a
          className="brand brand-compact"
          href="/"
          onClick={(event) => {
            event.preventDefault();
            home();
          }}
        >
          <Brand />
        </a>
        <nav className="view-tabs" aria-label="Sections">
          <a
            href="/"
            aria-current={view === "search" ? "page" : undefined}
            onClick={(event) => {
              event.preventDefault();
              setView("search");
            }}
          >
            Recherche
          </a>
          <a
            href="/?view=connectors"
            aria-current={view === "connectors" ? "page" : undefined}
            onClick={(event) => {
              event.preventDefault();
              setDoc(null);
              setView("connectors");
            }}
          >
            Connecteurs
          </a>
        </nav>
        <div className="topbar-spacer" />
        <span className="workspace-label">
          <span className="status-dot" />
          Espace démo
        </span>
        <button
          className="button primary add-button"
          onClick={() => setAdding(true)}
        >
          <Plus size={17} weight="bold" aria-hidden="true" />
          Ajouter du texte
        </button>
      </header>
      {view === "connectors" ? (
        <ConnectorsView corpus={corpus} onUnauthorized={onUnauthorized} />
      ) : (
        <main className={query ? "results-page" : "home"}>
          <div className={query ? "query-area" : "home-inner"}>
            {!query && (
              <>
                <div className="eyebrow">
                  <Sparkle size={15} aria-hidden="true" /> Vos textes. Vos
                  idées.
                </div>
                <h1>Retrouvez ce qui compte.</h1>
                <p className="hero-description">
                  Un mot précis ou une idée à explorer.
                  <br />
                  La bonne information est dans vos textes.
                </p>
              </>
            )}
            <SearchBar
              value={input}
              onChange={setInput}
              onSubmit={runSearch}
              inputRef={inputRef}
              busy={status === "loading"}
            />
            <div className="search-options">
              <ModeSwitch mode={mode} onChange={setMode} />
              <span className="mode-hint">
                {MODES.find((item) => item.value === mode)?.hint}
              </span>
            </div>
            {!query && (
              <section className="start-panel">
                <div className="start-icon">
                  <FileText size={24} aria-hidden="true" />
                </div>
                <div>
                  <h2>Commencez avec un texte.</h2>
                  <p>
                    Une note, un article, un compte rendu.
                    <br />
                    Collez-le et lancez votre première recherche.
                  </p>
                  <button
                    className="text-button"
                    onClick={() => setAdding(true)}
                  >
                    Ajouter un texte <ArrowRight size={16} aria-hidden="true" />
                  </button>
                </div>
              </section>
            )}
          </div>
          {query && (
            <section
              className="search-results"
              aria-label="Résultats de recherche"
              aria-busy={status === "loading"}
            >
              <div className="results-head">
                <p role="status">
                  {status === "loading"
                    ? "Recherche en cours…"
                    : status === "ready"
                      ? `${response?.items.length || 0} passage${response?.items.length === 1 ? "" : "s"} affiché${response?.items.length === 1 ? "" : "s"}`
                      : "Recherche"}
                </p>
                <span>Les 10 premiers passages au maximum</span>
              </div>
              {status === "loading" && (
                <div className="results-skeleton" aria-hidden="true">
                  {[0, 1, 2].map((i) => (
                    <div key={i}>
                      <span />
                      <span />
                      <span />
                    </div>
                  ))}
                </div>
              )}
              {status === "error" && (
                <div className="notice" role="alert">
                  <h2>La recherche n’a pas abouti.</h2>
                  <p>{searchError}</p>
                  <button
                    className="button"
                    onClick={() => setAttempt((value) => value + 1)}
                  >
                    Réessayer
                  </button>
                  {mode !== "lexical" && (
                    <button
                      className="text-button"
                      onClick={() => setMode("lexical")}
                    >
                      Passer aux mots-clés
                    </button>
                  )}
                </div>
              )}
              {status === "ready" && response?.items.length === 0 && (
                <div className="empty">
                  <MagnifyingGlass size={30} aria-hidden="true" />
                  <h2>Aucun passage trouvé.</h2>
                  <p>
                    Essayez une autre formulation ou ajoutez un texte à votre
                    espace.
                  </p>
                  <button className="button" onClick={() => setAdding(true)}>
                    <Plus size={17} aria-hidden="true" />
                    Ajouter du texte
                  </button>
                </div>
              )}
              {status === "ready" &&
                response?.items.map((result) => (
                  <ResultItem
                    key={result.segment_id}
                    result={result}
                    terms={terms}
                    onOpen={() =>
                      setDoc({
                        record: result.record_id,
                        version: result.version_id,
                      })
                    }
                  />
                ))}
            </section>
          )}
        </main>
      )}
      <footer className="page-footer">
        <span>Quivr · Démo de recherche</span>
        <span>
          <kbd>/</kbd> pour rechercher
        </span>
      </footer>
      {adding && (
        <AddText
          corpus={corpus}
          onClose={() => setAdding(false)}
          onAdded={onAdded}
          onOpen={(record, version) => {
            setAdding(false);
            setDoc({ record, version });
          }}
        />
      )}
      {doc && (
        <DocumentPanel
          record={doc.record}
          version={doc.version}
          terms={terms}
          onClose={() => setDoc(null)}
        />
      )}
    </div>
  );
}
