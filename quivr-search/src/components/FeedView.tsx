import { useEffect, useMemo, useRef, useState } from "react";
import {
  Broadcast,
  NotePencil,
  Plugs,
  Plus,
  RssSimple,
} from "@phosphor-icons/react";
import { APIError } from "../lib/search";
import {
  FEED_STREAM,
  HAND_NAMESPACE,
  fetchFeed,
  newestFirst,
  type FeedItem,
} from "../lib/feed";
import {
  fetchConnectors,
  formatAbsolute,
  formatRelative,
  type Connector,
} from "../lib/connectors";

type Status = "loading" | "ready" | "error";

const HAND_LABEL = "Ajouté à la main";

function Stamp({ item, now }: { item: FeedItem; now: number }) {
  const value = item.received_at || item.published_at;
  if (!value) return <span>Déjà présent</span>;
  return (
    <time dateTime={value} title={formatAbsolute(value)}>
      {item.received_at ? "" : "publié "}
      {formatRelative(value, now)}
    </time>
  );
}

function SourceIcon({ kind }: { kind?: string }) {
  if (kind === HAND_NAMESPACE)
    return <NotePencil size={15} aria-hidden="true" />;
  if (kind === "rss") return <RssSimple size={15} aria-hidden="true" />;
  return <Plugs size={15} aria-hidden="true" />;
}

export function FeedView({
  onOpen,
  onAdd,
  onSources,
  onUnauthorized,
}: {
  onOpen: (record: string, version: string) => void;
  onAdd: () => void;
  onSources: () => void;
  onUnauthorized: () => void;
}) {
  const [items, setItems] = useState<FeedItem[]>([]);
  const [status, setStatus] = useState<Status>("loading");
  const [error, setError] = useState("");
  const [live, setLive] = useState(false);
  const [filter, setFilter] = useState<string | null>(null);
  const [connectors, setConnectors] = useState<Connector[]>([]);
  const [now, setNow] = useState(() => Date.now());
  const [attempt, setAttempt] = useState(0);
  const [announcement, setAnnouncement] = useState("");
  // Items that arrived while the page was open, for the arrival highlight.
  const fresh = useRef(new Set<string>());

  useEffect(() => {
    const controller = new AbortController();
    let retry: ReturnType<typeof setTimeout>;
    // Live changes seen while a snapshot is in flight, replayed over it so an
    // item that arrives meanwhile is not dropped (null marks a removal).
    let pending: Map<string, FeedItem | null> | null = null;
    let loads = 0;
    const apply = (list: FeedItem[], id: string, next: FeedItem | null) => {
      const rest = list.filter((i) => i.record_id !== id);
      return next ? [...rest, next].sort(newestFirst) : rest;
    };
    const change = (id: string, next: FeedItem | null) => {
      pending?.set(id, next);
      setItems((current) => apply(current, id, next));
    };
    const load = () => {
      const ticket = ++loads;
      pending = new Map();
      return fetchFeed(controller.signal)
        .then((data) => {
          if (ticket !== loads) return; // A newer snapshot is on its way.
          let list = [...data.items].sort(newestFirst);
          for (const [id, next] of pending || []) list = apply(list, id, next);
          pending = null;
          setItems(list);
          // Liveness comes only from the stream's own ordered status events:
          // a snapshot taken just before the facade reconnected would
          // otherwise overwrite a newer "live" with a stale value.
          setStatus("ready");
        })
        .catch((e) => {
          if (controller.signal.aborted || ticket !== loads) return;
          pending = null;
          if (e instanceof APIError && e.status === 401)
            return onUnauthorized();
          setError(
            e instanceof Error ? e.message : "La veille est indisponible.",
          );
          setStatus("error");
        });
    };
    // Every (re)connection rereads the snapshot, so nothing that arrived
    // while the stream was down is missed.
    const source = new EventSource(FEED_STREAM);
    source.onopen = () => void load();
    source.onerror = () => {
      setLive(false);
      if (source.readyState === EventSource.CLOSED) {
        // The facade refused the stream: the snapshot says why.
        void load();
        retry = setTimeout(() => setAttempt((n) => n + 1), 3000);
      }
    };
    source.addEventListener("status", (event) =>
      setLive(JSON.parse((event as MessageEvent).data).live === true),
    );
    source.addEventListener("item", (event) => {
      const item: FeedItem = JSON.parse((event as MessageEvent).data);
      fresh.current.add(item.record_id + item.version_id);
      change(item.record_id, item);
      setNow(Date.now());
      setAnnouncement(`Nouveau : ${item.title}`);
    });
    source.addEventListener("remove", (event) =>
      change(JSON.parse((event as MessageEvent).data).record_id, null),
    );
    source.addEventListener("reset", () => void load());
    return () => {
      source.close();
      controller.abort();
      clearTimeout(retry);
    };
  }, [attempt, onUnauthorized]);

  useEffect(() => {
    const clock = setInterval(() => setNow(Date.now()), 30000);
    return () => clearInterval(clock);
  }, []);

  const connectorOf = useMemo(() => {
    const byNamespace = new Map<string, Connector>();
    for (const c of connectors)
      if (c.enabled || !byNamespace.has(c.source_namespace))
        byNamespace.set(c.source_namespace, c);
    return byNamespace;
  }, [connectors]);
  // A Connector Instance is named by its namespace; hand-added texts share one.
  const label = (namespace: string) =>
    namespace === HAND_NAMESPACE ? HAND_LABEL : namespace;
  const kindOf = (namespace: string) =>
    namespace === HAND_NAMESPACE
      ? HAND_NAMESPACE
      : connectorOf.get(namespace)?.kind;

  const sources = useMemo(() => {
    const counts = new Map<string, number>();
    for (const item of items)
      counts.set(item.namespace, (counts.get(item.namespace) || 0) + 1);
    return [...counts.entries()].sort(([a], [b]) =>
      a === HAND_NAMESPACE ? -1 : b === HAND_NAMESPACE ? 1 : a.localeCompare(b),
    );
  }, [items]);
  // Reread the instances when a source without a known kind shows up.
  const unknown = sources
    .map(([namespace]) => namespace)
    .filter((ns) => ns !== HAND_NAMESPACE && !connectorOf.has(ns))
    .join("\n");
  useEffect(() => {
    if (!unknown) return;
    const controller = new AbortController();
    fetchConnectors(controller.signal).then(setConnectors, () => {});
    return () => controller.abort();
  }, [unknown]);
  const shown = filter
    ? items.filter((item) => item.namespace === filter)
    : items;

  return (
    <main className="feed-page">
      <div className="feed-head">
        <div>
          <h1>Veille</h1>
          <p className="muted">
            Tout ce qui entre dans l’espace démo, le plus récent en premier.
          </p>
        </div>
        {status === "ready" && (
          <span className="live-badge" data-live={live}>
            <span className="live-dot" aria-hidden="true" />
            {live ? "En direct" : "Reconnexion…"}
          </span>
        )}
      </div>
      <p className="visually-hidden" role="status" aria-live="polite">
        {announcement}
      </p>
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
          <h2>La veille ne s’affiche pas.</h2>
          <p>{error}</p>
          <button
            className="button"
            onClick={() => {
              setStatus("loading");
              setAttempt((n) => n + 1);
            }}
          >
            Réessayer
          </button>
        </div>
      )}
      {status === "ready" && items.length === 0 && (
        <section className="empty feed-empty">
          <Broadcast size={30} aria-hidden="true" />
          <h2>Rien n’est encore arrivé.</h2>
          <p>
            Ajoutez une source, par exemple un flux RSS, ou collez un texte.
            <br />
            Chaque nouvel élément apparaît ici en quelques secondes, sans
            recharger la page.
          </p>
          <div className="feed-empty-actions">
            <button className="button primary" onClick={onSources}>
              <Plus size={17} weight="bold" aria-hidden="true" />
              Ajouter une source
            </button>
            <button className="button" onClick={onAdd}>
              <NotePencil size={17} aria-hidden="true" />
              Ajouter du texte
            </button>
          </div>
        </section>
      )}
      {status === "ready" && items.length > 0 && (
        <>
          <div
            className="source-filter"
            role="group"
            aria-label="Filtrer par source"
          >
            <button
              aria-pressed={filter === null}
              onClick={() => setFilter(null)}
            >
              Toutes les sources <span className="count">{items.length}</span>
            </button>
            {sources.map(([namespace, count]) => (
              <button
                key={namespace}
                aria-pressed={filter === namespace}
                onClick={() => setFilter(namespace)}
              >
                <SourceIcon kind={kindOf(namespace)} />
                {label(namespace)} <span className="count">{count}</span>
              </button>
            ))}
          </div>
          {shown.length === 0 ? (
            <div className="empty">
              <h2>Plus rien de cette source pour l’instant.</h2>
              <button className="text-button" onClick={() => setFilter(null)}>
                Voir toutes les sources
              </button>
            </div>
          ) : (
            <ol className="feed-list" aria-label="Derniers éléments">
              {shown.map((item) => {
                const href = `?view=veille&record=${encodeURIComponent(item.record_id)}&doc=${encodeURIComponent(item.version_id)}`;
                return (
                  <li
                    key={item.record_id + item.version_id}
                    className="feed-item"
                    data-fresh={
                      fresh.current.has(item.record_id + item.version_id) ||
                      undefined
                    }
                  >
                    <div className="feed-meta">
                      <SourceIcon kind={kindOf(item.namespace)} />
                      <span className="feed-source">
                        {label(item.namespace)}
                      </span>
                      <span aria-hidden="true">·</span>
                      <Stamp item={item} now={now} />
                    </div>
                    <h2 className="feed-title">
                      <a
                        href={href}
                        onClick={(event) => {
                          if (
                            !event.metaKey &&
                            !event.ctrlKey &&
                            !event.shiftKey
                          ) {
                            event.preventDefault();
                            onOpen(item.record_id, item.version_id);
                          }
                        }}
                      >
                        {item.title || "Sans titre"}
                      </a>
                    </h2>
                    {item.excerpt && (
                      <p className="feed-excerpt">{item.excerpt}</p>
                    )}
                  </li>
                );
              })}
            </ol>
          )}
        </>
      )}
    </main>
  );
}
