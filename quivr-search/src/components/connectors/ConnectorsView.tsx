import { useCallback, useEffect, useRef, useState } from "react";
import { ArrowClockwise, Key, Plugs, Plus } from "@phosphor-icons/react";
import { APIError } from "../../lib/search";
import {
  connectorMessage,
  fetchConnector,
  fetchConnectors,
  fetchKinds,
  formatAbsolute,
  formatInterval,
  formatRelative,
  pollChanges,
  type Connector,
  type ConnectorKind,
  type KindCatalog,
} from "../../lib/connectors";
import { HealthBadge, healthLabel } from "./HealthBadge";
import { CreateConnector } from "./CreateConnector";
import { ConnectorDetail } from "./ConnectorDetail";
import { sourceSummary } from "./summary";

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
  const states = useRef(new Map<string, string>());

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

  useEffect(() => {
    const controller = new AbortController();
    setStatus("loading");
    Promise.all([fetchKinds(controller.signal), reload(controller.signal)])
      .then(([kinds]) => {
        setCatalog(kinds);
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
    let timer: ReturnType<typeof setTimeout>;
    const tick = async () => {
      if (!document.hidden) {
        try {
          if (!feed) await reload(controller.signal);
          else {
            // Drain the feed; advance the cursor only once the instances named
            // by a page were reread, so a failed refetch retries that page.
            for (let pages = 0; pages < 10; pages++) {
              const page = await pollChanges(cursor, controller.signal);
              const started = cursor !== null;
              const ids = new Set<string>();
              let created = !started;
              for (const event of page.items) {
                if (!event.type.startsWith("connector.")) continue;
                if (event.type === "connector.created") created = true;
                else ids.add(event.resource.id);
              }
              if (created) await reload(controller.signal);
              for (const id of ids)
                await fetchConnector(id, controller.signal).then(
                  upsert,
                  (e) => {
                    // An instance no longer visible must not stall the feed.
                    if (!(e instanceof APIError && e.status === 404)) throw e;
                  },
                );
              cursor = page.next_cursor;
              setLive(true);
              if (!page.has_more) break;
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
  }, [status, reload, upsert, onUnauthorized]);

  const kindOf = (name: string) => catalog?.items.find((k) => k.kind === name);
  const current = connectors.find((c) => c.connector_id === selected);
  const enabled = connectors.filter((c) => c.enabled);
  const disabled = connectors.filter((c) => !c.enabled);

  return (
    <main className="connectors-page">
      <div className="connectors-head">
        <div>
          <h1>Connecteurs</h1>
          <p className="muted">
            Des sources collectées automatiquement dans l’espace démo.
          </p>
        </div>
        {status === "ready" && catalog && catalog.items.length > 0 && (
          <button className="button primary" onClick={() => setCreating(true)}>
            <Plus size={17} weight="bold" aria-hidden="true" />
            Ajouter un connecteur
          </button>
        )}
      </div>
      <p className="visually-hidden" role="status" aria-live="polite">
        {announcement}
      </p>
      {status === "loading" && (
        <p role="status" className="muted">
          Chargement des connecteurs…
        </p>
      )}
      {status === "unavailable" && (
        <div className="notice" role="status">
          <h2>Les connecteurs ne sont pas activés sur ce déploiement.</h2>
          <p>
            L’administrateur peut les activer côté serveur. La recherche et
            l’ajout de textes restent disponibles.
          </p>
        </div>
      )}
      {status === "error" && (
        <div className="notice" role="alert">
          <h2>Les connecteurs n’ont pas pu être chargés.</h2>
          <p>{error}</p>
          <button className="button" onClick={() => setAttempt((n) => n + 1)}>
            <ArrowClockwise size={16} aria-hidden="true" /> Réessayer
          </button>
        </div>
      )}
      {status === "ready" && catalog && (
        <>
          {catalog.credential_deposits === "unavailable" && (
            <p className="inline-note">
              <Key size={16} aria-hidden="true" /> Le dépôt d’identifiants est
              désactivé sur ce déploiement : seules les sources sans identifiant
              peuvent être ajoutées.
            </p>
          )}
          <div className="live-line muted">
            {live ? (
              <>
                <span className="status-dot" aria-hidden="true" /> Santé suivie
                en direct
              </>
            ) : (
              "Santé actualisée régulièrement"
            )}
          </div>
          {connectors.length === 0 ? (
            <div className="empty">
              <Plugs size={30} aria-hidden="true" />
              <h2>Aucun connecteur pour l’instant.</h2>
              <p>
                {catalog.items.length
                  ? "Ajoutez une source à collecter, par exemple un flux RSS."
                  : "Aucun type de connecteur n’est disponible sur ce déploiement."}
              </p>
            </div>
          ) : (
            <>
              <ConnectorList
                label="Connecteurs actifs"
                items={enabled}
                kindOf={kindOf}
                onOpen={setSelected}
              />
              {disabled.length > 0 && (
                <>
                  <h2 className="list-heading">Désactivés</h2>
                  <ConnectorList
                    label="Connecteurs désactivés"
                    items={disabled}
                    kindOf={kindOf}
                    onOpen={setSelected}
                  />
                </>
              )}
            </>
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

function ConnectorList({
  label,
  items,
  kindOf,
  onOpen,
}: {
  label: string;
  items: Connector[];
  kindOf: (kind: string) => ConnectorKind | undefined;
  onOpen: (id: string) => void;
}) {
  if (!items.length) return null;
  return (
    <ul className="connector-list" aria-label={label}>
      {items.map((c) => {
        const kind = kindOf(c.kind);
        const h = c.health;
        const source = sourceSummary(c, kind);
        return (
          <li
            key={c.connector_id}
            className="connector-row"
            data-connector={c.connector_id}
          >
            <div className="connector-main">
              <button
                type="button"
                className="connector-link"
                onClick={() => onOpen(c.connector_id)}
              >
                {c.source_namespace}
              </button>
              <span className="connector-kind">
                {kind?.title || c.kind}
                {source && (
                  <span className="connector-source"> · {source}</span>
                )}
              </span>
            </div>
            <HealthBadge state={h.state} />
            <dl className="connector-meta">
              <div>
                <dt>Intervalle</dt>
                <dd>{formatInterval(c.schedule.interval_seconds)}</dd>
              </div>
              <div>
                <dt>Dernier succès</dt>
                <dd>
                  {h.last_success_at ? (
                    <Time value={h.last_success_at} />
                  ) : (
                    "Jamais"
                  )}
                </dd>
              </div>
              <div>
                <dt>Dernier élément</dt>
                <dd>
                  {h.last_item_at ? <Time value={h.last_item_at} /> : "Aucun"}
                </dd>
              </div>
              {h.last_error && (
                <div>
                  <dt>Dernière erreur</dt>
                  <dd>
                    <code>{h.last_error.code}</code>{" "}
                    <Time value={h.last_error.at} />
                  </dd>
                </div>
              )}
              {c.credential && (
                <div>
                  <dt>Identifiant</dt>
                  <dd>
                    v{c.credential.version}
                    {c.credential.expires_at
                      ? ` · expire le ${formatAbsolute(c.credential.expires_at)}`
                      : ""}
                  </dd>
                </div>
              )}
            </dl>
          </li>
        );
      })}
    </ul>
  );
}

function Time({ value }: { value: string }) {
  return (
    <time dateTime={value} title={formatAbsolute(value)}>
      {formatRelative(value)}
    </time>
  );
}
