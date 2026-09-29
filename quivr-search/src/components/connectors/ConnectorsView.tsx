import { useCallback, useEffect, useRef, useState } from "react";
import { Key, RssSimple } from "@phosphor-icons/react";
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
  type Connector,
  type FeedChoice,
  type KindCatalog,
} from "../../lib/connectors";
import { healthLabel } from "./HealthBadge";
import { CreateConnector } from "./CreateConnector";
import { ConnectorDetail } from "./ConnectorDetail";
import { AddSource } from "./AddSource";
import { SourceList, groupSources } from "./SourceList";
import { EmptyState, LiveBadge, LoadingState, Notice, PageHeader } from "../ui";

const LIVE_INTERVAL = 5000;

type Status = "loading" | "ready" | "unavailable" | "error";

export function ConnectorsView({
  corpus,
  onUnauthorized,
}: {
  corpus: string;
  onUnauthorized: () => void;
}) {
  const [status, setStatus] = useState<Status>("loading");
  const [error, setError] = useState("");
  const [catalog, setCatalog] = useState<KindCatalog | null>(null);
  const [connectors, setConnectors] = useState<Connector[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [live, setLive] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  const [, setNow] = useState(Date.now());
  const [attempt, setAttempt] = useState(0);
  const [suggestions, setSuggestions] = useState<FeedChoice[]>([]);
  const [highlight, setHighlight] = useState<string | null>(null);
  const states = useRef(new Map<string, string>());
  const listHeading = useRef<HTMLHeadingElement>(null);

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
        changes.push(`${c.source_namespace} : ${healthLabel(c.health.state)}`);
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
    setStatus("loading");
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
              setLive(true);
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
            setLive(false);
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
      setAnnouncement(`${c.source_namespace} : en pause.`);
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
      upsert(next);
      setAnnouncement(`${c.source_namespace} : collecte reprise.`);
    });
  const onRemove = (c: Connector) =>
    guarded(async () => {
      const { removed } = await removeSource(c.connector_id);
      drop([...removed, c.connector_id]);
      setAnnouncement(`${c.source_namespace} retirée des sources.`);
      // The row is gone: land keyboard focus on the list heading.
      listHeading.current?.focus();
    });

  return (
    <main className="page connectors-page">
      <PageHeader
        title="Sources"
        description="Les sites et flux collectés automatiquement dans l’espace démo."
        aside={status === "ready" && <LiveBadge live={live} />}
      />
      <p className="visually-hidden" role="status" aria-live="polite">
        {announcement}
      </p>
      {status === "loading" && <LoadingState label="Chargement des sources…" />}
      {status === "unavailable" && (
        <Notice
          tone="info"
          title="Les connecteurs ne sont pas activés sur ce déploiement."
        >
          L’administrateur peut les activer côté serveur. La recherche et
          l’ajout de textes restent disponibles.
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
      {status === "ready" && catalog && (
        <>
          <AddSource
            catalog={catalog}
            corpus={corpus}
            suggestions={suggestions}
            existing={connectors}
            onCreated={(c, message) => {
              upsert(c);
              setHighlight(c.connector_id);
              setAnnouncement(message);
            }}
          />
          <div className="sources-head">
            <h2 ref={listHeading} tabIndex={-1}>
              Vos sources
              {sources.length > 0 && (
                <span className="count"> {sources.length}</span>
              )}
            </h2>
          </div>
          {sources.length === 0 ? (
            <EmptyState
              className="sources-empty"
              icon={<RssSimple size={26} aria-hidden="true" />}
              title="Aucune source pour l’instant."
            >
              {catalog.items.length
                ? "Collez l’adresse d’un site d’actualité ci-dessus, ou choisissez une suggestion."
                : "Aucun type de source n’est disponible sur ce déploiement."}
            </EmptyState>
          ) : (
            <SourceList
              sources={sources}
              kindOf={kindOf}
              highlight={highlight}
              onOpen={setSelected}
              onPause={onPause}
              onResume={onResume}
              onRemove={onRemove}
            />
          )}
          {catalog.items.length > 0 && (
            <p className="other-kinds">
              <button
                type="button"
                className="text-button"
                onClick={() => setCreating(true)}
              >
                Ajouter un connecteur d’un autre type
              </button>
            </p>
          )}
          {catalog.credential_deposits === "unavailable" && (
            <p className="inline-note">
              <Key size={16} aria-hidden="true" /> Le dépôt d’identifiants est
              désactivé sur ce déploiement : seules les sources sans identifiant
              peuvent être ajoutées.
            </p>
          )}
        </>
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
          }}
        />
      )}
      {current && catalog && (
        <ConnectorDetail
          connector={current}
          kind={kindOf(current.kind)}
          catalog={catalog}
          onClose={() => setSelected(null)}
          onChanged={upsert}
        />
      )}
    </main>
  );
}
