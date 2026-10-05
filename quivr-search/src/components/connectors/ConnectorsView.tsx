import { useCallback, useEffect, useRef, useState } from "react";
import { Key } from "@phosphor-icons/react";
import { APIError } from "../../lib/search";
import {
  connectorMessage,
  createConnector,
  disableConnector,
  fetchConnector,
  fetchConnectors,
  fetchKinds,
  fetchSuggestions,
  pollChanges,
  removeSource,
  renameSource,
  requestRun,
  type Connector,
  type FeedChoice,
  type KindCatalog,
} from "../../lib/connectors";
import { healthLabel } from "./HealthBadge";
import { CreateConnector } from "./CreateConnector";
import { ConnectorDetail } from "./ConnectorDetail";
import { Dialog } from "../Dialog";
import { AddSource } from "./AddSource";
import { NO_ARTICLES, SourceList, groupSources, nameOf, type SourceStats } from "./SourceList";
import { PlusIcon } from "../RailIcons";
import { InBar, LoadingState, Notice, type Bar } from "../ui";
import type { FeedItem } from "../../lib/feed";
import { needsCheck } from "../../lib/format";
import { displayState } from "./HealthBadge";
import { useNumbers } from "../../lib/numbers";
import { fetchSourceStats, weekBounds } from "../../lib/stats";

const LIVE_INTERVAL = 5000;
// How often a retried source is read until its check is recorded.
const RETRY_POLL = 2000;

type Status = "loading" | "ready" | "unavailable" | "error";

/**
 * The page as last shown. Coming back to it, it shows at once (no loading
 * flash between two pages), then reads its sources again.
 */
let visited: {
  corpus: string;
  catalog: KindCatalog;
  connectors: Connector[];
  suggestions: FeedChoice[];
} | null = null;

export function ConnectorsView({
  corpus,
  bar,
  feedItems,
  initialSelected,
  onChanged,
  onAdd,
  notify,
  onUnauthorized,
}: {
  corpus: string;
  /** The top bar’s places this page fills. */
  bar: Bar;
  /** The feed's latest articles: their arrival counts again. */
  feedItems: FeedItem[];
  /** A source to open on arrival (from the feed's "Renouveler"). */
  initialSelected: string | null;
  onChanged: () => void;
  onAdd: () => void;
  notify: (text: string) => void;
  onUnauthorized: () => void;
}) {
  const seen = visited?.corpus === corpus ? visited : null;
  const [status, setStatus] = useState<Status>(seen ? "ready" : "loading");
  const [error, setError] = useState("");
  const [catalog, setCatalog] = useState<KindCatalog | null>(seen?.catalog ?? null);
  const [connectors, setConnectors] = useState<Connector[]>(seen?.connectors ?? []);
  const [selected, setSelected] = useState<string | null>(initialSelected);
  const [creating, setCreating] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  const [now, setNow] = useState(Date.now());
  const [attempt, setAttempt] = useState(0);
  const [suggestions, setSuggestions] = useState<FeedChoice[]>(seen?.suggestions ?? []);
  const [highlight, setHighlight] = useState<string | null>(null);
  // Adding a source happens in a dialog, from the header's button or the "+" card.
  const [adding, setAdding] = useState(false);
  const openAdd = () => setAdding(true);
  const states = useRef(new Map<string, string>());
  const listHeading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    if (status === "ready" && catalog) visited = { corpus, catalog, connectors, suggestions };
  });
  // The row is gone: keyboard focus lands on the list heading, once the
  // settings it was removed from have closed and handed the focus back.
  const [removals, setRemovals] = useState(0);
  useEffect(() => {
    if (removals) listHeading.current?.focus();
  }, [removals]);

  const failed = useCallback(
    (e: unknown) => {
      if (e instanceof APIError && e.status === 401) onUnauthorized();
      else if (e instanceof APIError && e.status === 403)
        setStatus("unavailable");
      else {
        setError(connectorMessage(e));
        setStatus("error");
      }
    },
    [onUnauthorized],
  );

  // Announces health transitions politely, without reading every refresh.
  const track = useCallback((list: Connector[]) => {
    const changes: string[] = [];
    for (const c of list) {
      const before = states.current.get(c.connector_id);
      if (before && before !== c.health.state)
        changes.push(`${nameOf(c)} : ${healthLabel(c.health.state)}`);
      states.current.set(c.connector_id, c.health.state);
    }
    if (changes.length) setAnnouncement(changes.join(". "));
  }, []);

  const reload = useCallback(
    async (signal?: AbortSignal) => {
      const list = await fetchConnectors(signal);
      track(list);
      setConnectors(list);
    },
    [track],
  );

  const upsert = useCallback(
    (next: Connector) => {
      track([next]);
      setConnectors((list) =>
        list.some((c) => c.connector_id === next.connector_id)
          ? list.map((c) => (c.connector_id === next.connector_id ? next : c))
          : [...list, next].sort((a, b) =>
              a.connector_id.localeCompare(b.connector_id),
            ),
      );
    },
    [track],
  );

  const drop = useCallback((ids: string[]) => {
    setConnectors((list) => list.filter((c) => !ids.includes(c.connector_id)));
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    // Coming back, the last sources stay on screen while they are read again.
    setStatus((s) => (s === "ready" ? s : "loading"));
    Promise.all([
      fetchKinds(controller.signal),
      reload(controller.signal),
      // Suggestions are optional: the page works without them.
      fetchSuggestions(controller.signal).catch(() => ({ items: [] })),
    ])
      .then(([kinds, , offered]) => {
        setCatalog(kinds);
        setSuggestions(offered.items);
        setStatus("ready");
      })
      .catch((e) => {
        if (!controller.signal.aborted) failed(e);
      });
    return () => controller.abort();
  }, [reload, failed, attempt]);

  // Live health: follow connector.* events of the change feed and reread the
  // instances they name. Without change-feed access, reread the list instead.
  useEffect(() => {
    if (status !== "ready") return;
    const controller = new AbortController();
    let cursor: string | null = null;
    let feed = true;
    let refreshed = Date.now();
    let timer: ReturnType<typeof setTimeout>;
    const tick = async () => {
      if (!document.hidden) {
        try {
          if (!feed) await reload(controller.signal);
          else {
            // Drain the feed; advance the cursor only once the instances named
            // by a page were reread, so a failed refetch retries that page.
            // Health events only mark state changes; a new article or a
            // successful run of an already active source changes no state.
            // Other events of the corpus (Records arriving) and a slow
            // timer reread the list so "last article" stays current.
            let arrivals = false;
            for (let pages = 0; pages < 10; pages++) {
              const page = await pollChanges(cursor, controller.signal);
              const started = cursor !== null;
              const ids = new Set<string>();
              let created = !started;
              for (const event of page.items) {
                if (!event.type.startsWith("connector.")) {
                  arrivals ||= started;
                  continue;
                }
                if (event.type === "connector.created") created = true;
                else ids.add(event.resource.id);
              }
              if (created) {
                await reload(controller.signal);
                refreshed = Date.now();
              }
              for (const id of ids)
                await fetchConnector(id, controller.signal).then(
                  upsert,
                  (e) => {
                    // An instance no longer visible (removed elsewhere) leaves
                    // the list and must not stall the feed.
                    if (!(e instanceof APIError && e.status === 404)) throw e;
                    drop([id]);
                  },
                );
              cursor = page.next_cursor;
              if (!page.has_more) break;
            }
            if (arrivals || Date.now() - refreshed > LIVE_INTERVAL * 3) {
              await reload(controller.signal);
              refreshed = Date.now();
            }
          }
        } catch (e) {
          if (controller.signal.aborted) return;
          if (e instanceof APIError && (e.status === 409 || e.status === 410)) {
            cursor = null;
          } else if (e instanceof APIError && e.status === 403) {
            feed = false;
          } else if (e instanceof APIError && e.status === 401) {
            onUnauthorized();
            return;
          }
        }
      }
      timer = setTimeout(tick, feed ? LIVE_INTERVAL : LIVE_INTERVAL * 3);
    };
    void tick();
    const clock = setInterval(() => setNow(Date.now()), 30000);
    return () => {
      controller.abort();
      clearTimeout(timer);
      clearInterval(clock);
    };
  }, [status, reload, upsert, drop, onUnauthorized]);

  const kindOf = (name: string) => catalog?.items.find((k) => k.kind === name);
  const current = connectors.find((c) => c.connector_id === selected);
  const sources = groupSources(connectors);

  // Row actions report failures in plain words; a lost session logs out.
  const guarded = async (action: () => Promise<void>) => {
    try {
      await action();
    } catch (e) {
      if (e instanceof APIError && e.status === 401) onUnauthorized();
      throw new Error(connectorMessage(e, catalog?.min_interval_seconds));
    }
  };
  const onPause = (c: Connector) =>
    guarded(async () => {
      upsert(await disableConnector(c.connector_id, `pause:${c.connector_id}`));
      notify(`« ${nameOf(c)} » en pause : plus de nouveaux articles jusqu’à la reprise.`);
      onChanged();
    });
  // Asks the core to check the source now, then follows the instance until
  // that check is recorded (its health is evaluated again), a minute past
  // the moment the core scheduled it at most.
  const onRetry = (c: Connector) =>
    guarded(async () => {
      const id = c.connector_id;
      const { run_at } = await requestRun(id, `retry:${id}:${Date.now()}`);
      notify(`Nouvelle vérification de « ${nameOf(c)} » demandée.`);
      const deadline = Math.max(Date.parse(run_at), Date.now()) + 60_000;
      while (Date.now() < deadline) {
        await new Promise((resolve) => setTimeout(resolve, RETRY_POLL));
        const fresh = await fetchConnector(id);
        if (fresh.health.evaluated_at === c.health.evaluated_at) continue;
        upsert(fresh);
        notify(
          displayState(fresh) === "active"
            ? `« ${nameOf(c)} » répond de nouveau.`
            : `« ${nameOf(c)} » ne répond toujours pas.`,
        );
        return;
      }
    });
  // The core cannot re-enable an instance: resuming creates a new one on the
  // same Source Namespace, so collected Records keep their identity.
  const onResume = (c: Connector) =>
    guarded(async () => {
      const next = await createConnector({
        idempotency_key: `resume:${c.connector_id}`,
        corpus_id: corpus,
        source_namespace: c.source_namespace,
        kind: c.kind,
        config: c.config,
        schedule: c.schedule,
      });
      upsert({ ...next, display_name: next.display_name ?? c.display_name });
      notify(`La collecte de « ${nameOf(c)} » reprend.`);
      onChanged();
    });
  const onRemove = (c: Connector) =>
    guarded(async () => {
      const { removed } = await removeSource(c.connector_id);
      drop([...removed, c.connector_id]);
      notify(`« ${nameOf(c)} » retirée. Les articles déjà reçus restent dans le fil.`);
      onChanged();
      setRemovals((n) => n + 1);
    });

  // The name is the facade's, for every instance of the source.
  const onRename = (c: Connector, name: string) =>
    guarded(async () => {
      const { display_name } = await renameSource(c.connector_id, name);
      setConnectors((list) =>
        list.map((x) => (x.source_namespace === c.source_namespace ? { ...x, display_name } : x)),
      );
      notify(
        display_name
          ? `« ${nameOf(c)} » s’appelle maintenant « ${display_name} ».`
          : `« ${nameOf(c)} » reprend son nom d’origine, « ${c.source_namespace} ».`,
      );
      onChanged();
    });

  // Each source's numbers, counted by the facade over every article.
  const week = weekBounds(now);
  const counted = useNumbers(
    week.join(","),
    (signal) => fetchSourceStats(week, signal),
    onUnauthorized,
    feedItems,
  );
  // Only numbers for this week; while the facade still indexes, a source it
  // has not reached yet stays uncounted rather than zero.
  const stats = counted?.current
    ? new Map<string, SourceStats>(
        Object.entries(counted.value.sources).map(([namespace, s]) => [
          namespace,
          { all: s.all, caught: s.caught, week: [...s.days].reverse(), first: s.first },
        ]),
      )
    : null;
  const whole = !!counted?.current && !counted.value.building;
  const toCheck = sources.filter((c) => needsCheck(displayState(c))).length;

  return (
    <main
      className="board board-single sources-board"
    >
      <h1 className="visually-hidden">Sources</h1>
      <p className="visually-hidden" role="status" aria-live="polite">
        {announcement}
      </p>
      {status !== "ready" && (
        <section className="panel panel-pad">
          {status === "loading" && (
            <LoadingState label="Chargement des sources…" />
          )}
          {status === "unavailable" && (
            <Notice
              tone="info"
              title="Les connecteurs ne sont pas activés sur ce déploiement."
            >
              L’administrateur peut les activer côté serveur. Le fil, la
              recherche et l’ajout de textes restent disponibles.
            </Notice>
          )}
          {status === "error" && (
            <Notice
              title="Les sources n’ont pas pu être chargées."
              onRetry={() => setAttempt((n) => n + 1)}
            >
              {error}
            </Notice>
          )}
        </section>
      )}
      {status === "ready" && catalog && (
        <section className="sources-page" aria-labelledby="sources-title">
          {/* The counts and "Ajouter une source" sit in the top bar. */}
          <h2 id="sources-title" className="visually-hidden" ref={listHeading} tabIndex={-1}>
            Vos sources
          </h2>
          <InBar to={bar.meta}>
            {sources.length > 0 && (
              <span className="head-count">
                {sources.length}
                <span className="visually-hidden"> source{sources.length > 1 ? "s" : ""}</span>
              </span>
            )}
            {toCheck > 0 && (
              <span className="list-count" data-tone="warn">
                {toCheck} à vérifier
              </span>
            )}
          </InBar>
          <InBar to={bar.actions}>
            {catalog.items.length > 0 && (
              <button type="button" className="button alerts-new" onClick={openAdd}>
                <PlusIcon size={16} />
                <span className="bar-action-label">Ajouter une source</span>
              </button>
            )}
          </InBar>
          {sources.length === 0 && !catalog.items.length ? (
            <p className="list-empty">Aucun type de source n’est disponible sur ce déploiement.</p>
          ) : (
            <SourceList
              sources={sources}
              kindOf={kindOf}
              stats={stats}
              whole={whole}
              highlight={highlight}
              now={now}
              onOpen={setSelected}
              onRename={onRename}
              onPause={onPause}
              onRetry={onRetry}
              onResume={onResume}
              onRemove={onRemove}
              extra={
                <div className="add-card">
                  <button type="button" className="add-cta" onClick={openAdd}>
                    <span className="add-plus" aria-hidden="true">
                      <PlusIcon size={22} />
                    </span>
                    <span className="add-label">Ajouter une source</span>
                    <span className="add-hint">
                      {catalog.items.some((k) => k.kind === "rss")
                        ? "Un site, un journal ou un flux RSS"
                        : catalog.items.length
                          ? "Un texte ou un connecteur d’un autre type"
                          : "Un texte ajouté à la main"}
                    </span>
                  </button>
                </div>
              }
            />
          )}
        </section>
      )}
      {adding && catalog && (
        <Dialog title="Ajouter une source" closeLabel="Fermer l’ajout de source" onClose={() => setAdding(false)}>
          <div className="add-modal">
            {catalog.items.some((k) => k.kind === "rss") ? (
              <AddSource
                catalog={catalog}
                corpus={corpus}
                suggestions={suggestions}
                existing={connectors}
                onCreated={(c, message) => {
                  upsert(c);
                  // Added: the dialog closes on the new card.
                  setAdding(false);
                  setHighlight(c.connector_id);
                  notify(message);
                  onChanged();
                }}
              />
            ) : (
              <p className="form-note">Les fils d’actualités ne sont pas disponibles sur ce déploiement.</p>
            )}
            <div className="form-aside">
              <p>
                Vous pouvez aussi{" "}
                <button
                  type="button"
                  className="link-button"
                  onClick={() => {
                    setAdding(false);
                    onAdd();
                  }}
                >
                  ajouter un texte à la main
                </button>
                , par exemple une note ou un communiqué : il apparaîtra dans le fil sous « Ajouté à la main ».
              </p>
              {catalog.items.length > 0 && (
                <p>
                  <button
                    type="button"
                    className="link-button"
                    onClick={() => {
                      setAdding(false);
                      setCreating(true);
                    }}
                  >
                    Ajouter un connecteur d’un autre type
                  </button>
                </p>
              )}
              {catalog.credential_deposits === "unavailable" && (
                <p className="inline-note">
                  <Key size={16} aria-hidden="true" /> Le dépôt d’identifiants est désactivé sur ce déploiement :
                  seules les sources sans identifiant peuvent être ajoutées.
                </p>
              )}
            </div>
          </div>
        </Dialog>
      )}
      {creating && catalog && (
        <CreateConnector
          catalog={catalog}
          corpus={corpus}
          onClose={() => setCreating(false)}
          onCreated={(c) => {
            upsert(c);
            setCreating(false);
            setSelected(c.connector_id);
            onChanged();
          }}
        />
      )}
      {current && catalog && (
        <ConnectorDetail
          connector={current}
          kind={kindOf(current.kind)}
          catalog={catalog}
          stats={stats && (stats.get(current.source_namespace) || (whole ? NO_ARTICLES : null))}
          now={now}
          onClose={() => setSelected(null)}
          onChanged={(c) => {
            upsert(c);
            onChanged();
          }}
          onRename={onRename}
          onRemove={onRemove}
        />
      )}
    </main>
  );
}
