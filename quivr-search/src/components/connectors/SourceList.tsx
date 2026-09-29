import { useEffect, useRef, useState } from "react";
import { Pause, Play, Trash } from "@phosphor-icons/react";
import {
  formatAbsolute,
  formatInterval,
  formatRelative,
  type Connector,
  type ConnectorKind,
} from "../../lib/connectors";
import { displayState } from "./HealthBadge";
import { sourceProblem, sourceState } from "../../lib/format";
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

export interface SourceStats {
  /** Articles of the source in the feed. */
  all: number;
  /** Of which an alert caught. */
  caught: number;
}

export function SourceList({
  sources,
  kindOf,
  stats,
  highlight,
  onOpen,
  onPause,
  onResume,
  onRemove,
}: {
  sources: Connector[];
  kindOf: (kind: string) => ConnectorKind | undefined;
  stats: Map<string, SourceStats>;
  highlight: string | null;
  onOpen: (id: string) => void;
  onPause: (c: Connector) => Promise<void>;
  onResume: (c: Connector) => Promise<void>;
  onRemove: (c: Connector) => Promise<void>;
}) {
  return (
    <ul className="source-items" aria-label="Sources">
      {sources.map((c) => (
        <SourceRow
          key={c.source_namespace}
          connector={c}
          kind={kindOf(c.kind)}
          stats={stats.get(c.source_namespace)}
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
  stats,
  highlight,
  onOpen,
  onPause,
  onResume,
  onRemove,
}: {
  connector: Connector;
  kind?: ConnectorKind;
  stats?: SourceStats;
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
  const status = sourceState(state);
  const source = sourceSummary(c, kind);
  const problem = sourceProblem(state);
  const renew = state === "access_error" || state === "credential_expiring";

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

  const meta = [
    h.last_item_at ? (
      <span key="last">
        Dernier article <Time value={h.last_item_at} />
      </span>
    ) : (
      <span key="last">
        {state === "starting"
          ? "Premier relevé en cours…"
          : "Aucun article pour l’instant"}
      </span>
    ),
    c.enabled ? (
      <span key="every">
        vérifié toutes les {formatInterval(c.schedule.interval_seconds)}
      </span>
    ) : (
      c.disabled_at && (
        <span key="paused">
          en pause depuis <Time value={c.disabled_at} />
        </span>
      )
    ),
    stats && stats.all > 0 && (
      <span key="count">
        {stats.all} article{stats.all > 1 ? "s" : ""} dans le fil
        {stats.caught > 0 &&
          ` · ${stats.caught} attrapé${stats.caught > 1 ? "s" : ""} par une alerte`}
      </span>
    ),
  ].filter(Boolean);

  return (
    <li
      ref={row}
      className="source-item"
      data-connector={c.connector_id}
      data-highlight={highlight || undefined}
      data-enabled={c.enabled}
    >
      <div className="source-item-head">
        <span className="state-dot" data-tone={status.tone} aria-hidden="true" />
        <h3 className="source-item-name">
          <button
            type="button"
            className="connector-link"
            onClick={() => onOpen(c.connector_id)}
          >
            {c.source_namespace}
          </button>
        </h3>
        <span className="source-state" data-state={state} data-tone={status.tone}>
          {status.label}
        </span>
      </div>
      <p className="source-item-meta">
        {meta.map((part, i) => (
          <span key={i}>
            {i > 0 && " · "}
            {part}
          </span>
        ))}
      </p>
      <p className="source-item-where">
        {c.kind === "rss" ? "Fil RSS" : kind?.title || c.kind}
        {source && <> · {source}</>}
      </p>
      {c.enabled && (problem || h.last_error) && state !== "active" && (
        <p className="source-item-problem" data-tone={status.tone}>
          {problem}
          {h.last_error && (
            <>
              {problem && " "}
              <span className="muted">
                Dernière erreur :{" "}
                {FAILURES[h.last_error.code] || <code>{h.last_error.code}</code>}
                , <Time value={h.last_error.at} />.
              </span>
            </>
          )}
        </p>
      )}
      <div className="row-actions source-actions">
        {confirming ? (
          <div
            className="row-actions"
            role="group"
            aria-label={`Retirer ${c.source_namespace}`}
          >
            <span className="row-confirm">
              Retirer cette source ? Les articles déjà reçus restent.
            </span>
            <button
              ref={confirmRef}
              type="button"
              className="button danger small"
              disabled={busy}
              onClick={act(() => onRemove(c))}
            >
              {busy ? "Retrait…" : "Oui, retirer"}
            </button>
            <button
              type="button"
              className="button small"
              onClick={() => setConfirming(false)}
            >
              Non
            </button>
          </div>
        ) : (
          <>
            {renew && c.enabled && (
              <button
                type="button"
                className="button small"
                data-tone={status.tone}
                onClick={() => onOpen(c.connector_id)}
              >
                Renouveler la connexion
                <span className="visually-hidden"> de {c.source_namespace}</span>
              </button>
            )}
            {c.enabled ? (
              <button
                ref={toggleRef}
                type="button"
                className="button small"
                disabled={busy}
                onClick={toggle(() => onPause(c))}
                aria-label={`Mettre en pause ${c.source_namespace}`}
              >
                <Pause size={14} weight="bold" aria-hidden="true" />
                Mettre en pause
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
                  <Play size={14} weight="bold" aria-hidden="true" />
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
              <Trash size={14} aria-hidden="true" />
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
