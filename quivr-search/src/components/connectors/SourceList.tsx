import { useEffect, useRef, useState } from "react";
import { Pause, Play, Trash } from "@phosphor-icons/react";
import {
  formatAbsolute,
  formatInterval,
  formatRelative,
  type Connector,
  type ConnectorKind,
} from "../../lib/connectors";
import { HealthBadge, displayState } from "./HealthBadge";
import { sourceSummary } from "./summary";

// Plain words for the failure codes of the rss kind (docs/connectors/rss.md);
// other kinds fall back to their code.
const FAILURES: Record<string, string> = {
  not_found: "flux introuvable (404)",
  gone: "flux supprimé par le site (410)",
  unauthorized: "accès refusé par le site",
  forbidden: "accès refusé par le site",
  http_status: "le site a répondu par une erreur",
  malformed_feed: "le document n’est pas un flux lisible",
  response_too_large: "flux trop volumineux",
  timeout: "le site ne répond pas",
  dns_error: "adresse introuvable",
  connection_error: "connexion impossible",
  tls_error: "certificat du site invalide",
  too_many_redirects: "trop de redirections",
  insecure_redirect: "redirection non sécurisée",
  address_not_allowed: "adresse privée refusée",
};

/**
 * One row per Source Namespace: a paused then resumed source has several
 * instances, and the newest one speaks for it.
 */
export function groupSources(connectors: Connector[]) {
  const newest = new Map<string, Connector>();
  const lastItem = new Map<string, string>();
  for (const c of connectors) {
    const seen = newest.get(c.source_namespace);
    if (!seen || Date.parse(c.created_at) > Date.parse(seen.created_at))
      newest.set(c.source_namespace, c);
    // The last article of the source, whichever instance collected it.
    const item = c.health.last_item_at;
    const known = lastItem.get(c.source_namespace);
    if (item && (!known || Date.parse(item) > Date.parse(known)))
      lastItem.set(c.source_namespace, item);
  }
  return [...newest.values()]
    .map((c) => {
      const item = lastItem.get(c.source_namespace);
      return item && item !== c.health.last_item_at
        ? { ...c, health: { ...c.health, last_item_at: item } }
        : c;
    })
    .sort(
    (a, b) =>
      Number(b.enabled) - Number(a.enabled) ||
      a.source_namespace.localeCompare(b.source_namespace, "fr"),
  );
}

/** Resuming recreates the instance, so only credential-free ones can. */
export const resumable = (c: Connector, kind?: ConnectorKind) =>
  !c.enabled && !c.credential && kind?.credential !== "required";

export function SourceList({
  sources,
  kindOf,
  highlight,
  onOpen,
  onPause,
  onResume,
  onRemove,
}: {
  sources: Connector[];
  kindOf: (kind: string) => ConnectorKind | undefined;
  highlight: string | null;
  onOpen: (id: string) => void;
  onPause: (c: Connector) => Promise<void>;
  onResume: (c: Connector) => Promise<void>;
  onRemove: (c: Connector) => Promise<void>;
}) {
  return (
    <ul className="connector-list source-list" aria-label="Sources">
      {sources.map((c) => (
        <SourceRow
          key={c.source_namespace}
          connector={c}
          kind={kindOf(c.kind)}
          highlight={highlight === c.connector_id}
          onOpen={onOpen}
          onPause={onPause}
          onResume={onResume}
          onRemove={onRemove}
        />
      ))}
    </ul>
  );
}

function SourceRow({
  connector: c,
  kind,
  highlight,
  onOpen,
  onPause,
  onResume,
  onRemove,
}: {
  connector: Connector;
  kind?: ConnectorKind;
  highlight: boolean;
  onOpen: (id: string) => void;
  onPause: (c: Connector) => Promise<void>;
  onResume: (c: Connector) => Promise<void>;
  onRemove: (c: Connector) => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState("");
  const row = useRef<HTMLLIElement>(null);
  const confirmRef = useRef<HTMLButtonElement>(null);
  const h = c.health;
  const state = displayState(c);
  const source = sourceSummary(c, kind);
  const rss = c.kind === "rss";

  useEffect(() => {
    if (highlight) row.current?.scrollIntoView({ block: "nearest" });
  }, [highlight]);
  // Keyboard focus follows the controls it came from: into the removal
  // confirmation and back, and onto the counterpart of Pause/Reprendre.
  const toggleRef = useRef<HTMLButtonElement>(null);
  const removeRef = useRef<HTMLButtonElement>(null);
  const wasConfirming = useRef(false);
  const refocus = useRef(false);
  useEffect(() => {
    if (confirming) confirmRef.current?.focus();
    else if (wasConfirming.current) removeRef.current?.focus();
    wasConfirming.current = confirming;
  }, [confirming]);
  useEffect(() => {
    if (!refocus.current) return;
    refocus.current = false;
    (toggleRef.current || removeRef.current)?.focus();
  }, [c.enabled, c.connector_id]);

  const toggle = (action: () => Promise<void>) =>
    act(async () => {
      refocus.current = true;
      try {
        await action();
      } catch (e) {
        refocus.current = false;
        throw e;
      }
    });
  const act = (action: () => Promise<void>) => async () => {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await action();
    } catch (e) {
      setError(e instanceof Error ? e.message : "La demande a échoué.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <li
      ref={row}
      className="connector-row source-row"
      data-connector={c.connector_id}
      data-highlight={highlight || undefined}
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
          {rss ? "Flux RSS" : kind?.title || c.kind}
          {source && <span className="connector-source"> · {source}</span>}
        </span>
      </div>
      <HealthBadge state={state} />
      <dl className="connector-meta">
        <div>
          <dt>Dernier article</dt>
          <dd>
            {h.last_item_at ? (
              <Time value={h.last_item_at} />
            ) : (
              "aucun pour l’instant"
            )}
          </dd>
        </div>
        {c.enabled ? (
          <div>
            <dt>Relevé</dt>
            <dd>toutes les {formatInterval(c.schedule.interval_seconds)}</dd>
          </div>
        ) : (
          c.disabled_at && (
            <div>
              <dt>En pause</dt>
              <dd>
                <Time value={c.disabled_at} />
              </dd>
            </div>
          )
        )}
        {h.last_error && (
          <div className={state === "failing" ? "meta-error" : undefined}>
            <dt>Dernière erreur</dt>
            <dd>
              {FAILURES[h.last_error.code] || <code>{h.last_error.code}</code>}
              {" · "}
              <Time value={h.last_error.at} />
            </dd>
          </div>
        )}
      </dl>
      <div className="source-actions">
        {confirming ? (
          <div
            className="remove-confirm"
            role="group"
            aria-label={`Retirer ${c.source_namespace}`}
          >
            <p>
              Retirer cette source ? La collecte s’arrête ; les articles déjà
              collectés restent consultables.
            </p>
            <button
              ref={confirmRef}
              type="button"
              className="button danger small"
              disabled={busy}
              onClick={act(() => onRemove(c))}
            >
              {busy ? "Retrait…" : "Retirer"}
            </button>
            <button
              type="button"
              className="button small"
              onClick={() => setConfirming(false)}
            >
              Annuler
            </button>
          </div>
        ) : (
          <>
            {c.enabled ? (
              <button
                ref={toggleRef}
                type="button"
                className="button small"
                disabled={busy}
                onClick={toggle(() => onPause(c))}
                aria-label={`Mettre en pause ${c.source_namespace}`}
              >
                <Pause size={15} weight="bold" aria-hidden="true" />
                Pause
              </button>
            ) : (
              resumable(c, kind) && (
                <button
                  ref={toggleRef}
                  type="button"
                  className="button small"
                  disabled={busy}
                  onClick={toggle(() => onResume(c))}
                  aria-label={`Reprendre ${c.source_namespace}`}
                >
                  <Play size={15} weight="bold" aria-hidden="true" />
                  Reprendre
                </button>
              )
            )}
            <button
              ref={removeRef}
              type="button"
              className="button small quiet"
              disabled={busy}
              onClick={() => setConfirming(true)}
              aria-label={`Retirer ${c.source_namespace}`}
            >
              <Trash size={15} aria-hidden="true" />
              Retirer
            </button>
          </>
        )}
        {error && (
          <p className="error-text" role="alert">
            {error}
          </p>
        )}
      </div>
    </li>
  );
}

function Time({ value }: { value: string }) {
  return (
    <time dateTime={value} title={formatAbsolute(value)}>
      {formatRelative(value)}
    </time>
  );
}
