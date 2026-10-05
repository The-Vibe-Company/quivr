import { useEffect, useRef, useState, type ReactNode } from "react";
import { ArrowClockwise } from "@phosphor-icons/react";
import {
  formatAbsolute,
  formatInterval,
  formatRelative,
  type Connector,
  type ConnectorKind,
} from "../../lib/connectors";
import { daily, dayLabel } from "../../lib/moments";
import { displayState } from "./HealthBadge";
import { plural, sourceProblem, sourceState } from "../../lib/format";
import { sourceSummary } from "./summary";
import { SourceLogo } from "../feed/SourceLogo";
import { MoreMenu } from "../MoreMenu";
import { PencilIcon } from "../RailIcons";

// Plain words for the failure codes of the rss kind (https://docs.quivr.thevibecompany.co/guides/rss);
// other kinds fall back to their code.
export const FAILURES: Record<string, string> = {
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
      (a.display_name || a.source_namespace).localeCompare(b.display_name || b.source_namespace, "fr"),
  );
}

/** Resuming recreates the instance, so only credential-free ones can. */
export const resumable = (c: Connector, kind?: ConnectorKind) =>
  !c.enabled && !c.credential && kind?.credential !== "required";

/** A source's numbers, counted by the facade over all its articles. */
export interface SourceStats {
  /** Articles of the source in all. */
  all: number;
  /** Of which an alert caught. */
  caught: number;
  /** Articles per day over the last seven days, oldest first. */
  week: number[];
  /** When its oldest article arrived, in milliseconds. */
  first: number;
}

/** A source the facade counted no article for. */
export const NO_ARTICLES: SourceStats = { all: 0, caught: 0, week: [], first: Infinity };

/**
 * Articles per day over the last week, or since the source was added: a
 * resumed source is a new instance, so its first article may be older.
 */
export function weekRates(c: Connector, stats: SourceStats | null | undefined, now: number) {
  const week = daily([], now).map((d, i) => ({ ...d, count: stats?.week[i] || 0 }));
  const lastWeek = week.reduce((n, d) => n + d.count, 0);
  const start = Math.min(Date.parse(c.created_at), stats?.all ? stats.first : Infinity);
  const days = Math.min(7, Math.max(1, Math.ceil((now - start) / 86400000)));
  return { week, perDay: Math.round(lastWeek / days), most: Math.max(1, ...week.map((d) => d.count)) };
}

/** The articles received on each of the last seven days, today last. */
export function WeekSpark({
  week,
  most,
  now,
  counted = true,
}: {
  week: { day: string; count: number }[];
  most: number;
  now: number;
  /** False until the facade counted them: no day claims zero meanwhile. */
  counted?: boolean;
}) {
  return (
    <div
      className="sc-spark"
      role="img"
      tabIndex={0}
      data-tips
      aria-label={`Articles reçus sur 7 jours : ${counted ? week.map((d) => d.count).join(", ") : "comptage en cours"}`}
    >
      {week.map((d, i) => (
        <span
          key={d.day}
          data-now={i === week.length - 1 || undefined}
          data-zero={!d.count || undefined}
          data-tip={counted ? `${dayLabel(d.day, now)} · ${plural(d.count, "article")}` : undefined}
          style={d.count ? { height: `${Math.max(8, (d.count / most) * 100)}%` } : undefined}
        />
      ))}
    </div>
  );
}

/** The name a source shows: the one given to it, else its namespace. */
export const nameOf = (c: Connector) => c.display_name || c.source_namespace;

/**
 * The sources as cards, one per Source Namespace, then `extra` (the card
 * that adds one).
 */
export function SourceList({
  sources,
  kindOf,
  stats,
  highlight,
  now,
  extra,
  onOpen,
  onRename,
  onPause,
  onRetry,
  onResume,
  onRemove,
}: {
  sources: Connector[];
  kindOf: (kind: string) => ConnectorKind | undefined;
  /** Null until the facade counted them. */
  stats: Map<string, SourceStats> | null;
  highlight: string | null;
  now: number;
  extra?: ReactNode;
  onOpen: (id: string) => void;
  onRename: (c: Connector, name: string) => Promise<void>;
  onPause: (c: Connector) => Promise<void>;
  onRetry: (c: Connector) => Promise<void>;
  onResume: (c: Connector) => Promise<void>;
  onRemove: (c: Connector) => Promise<void>;
}) {
  return (
    <ul className="source-cards" aria-label="Sources">
      {sources.map((c) => (
        <SourceCard
          key={c.source_namespace}
          connector={c}
          kind={kindOf(c.kind)}
          stats={stats && (stats.get(c.source_namespace) || NO_ARTICLES)}
          highlight={highlight === c.connector_id}
          now={now}
          onOpen={onOpen}
          onRename={onRename}
          onPause={onPause}
          onRetry={onRetry}
          onResume={onResume}
          onRemove={onRemove}
        />
      ))}
      {extra && <li className="source-card source-add">{extra}</li>}
    </ul>
  );
}

/** The name, edited in place: Enter or leaving the field saves, Escape cancels. */
function RenameField({
  connector: c,
  onDone,
  onRename,
}: {
  connector: Connector;
  /** `back`: the field was left from the keyboard, the focus returns to the name. */
  onDone: (back: boolean) => void;
  onRename: (c: Connector, name: string) => Promise<void>;
}) {
  const [value, setValue] = useState(nameOf(c));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const field = useRef<HTMLInputElement>(null);
  const done = useRef(false);
  useEffect(() => {
    field.current?.focus();
    field.current?.select();
  }, []);
  // A refused name keeps the field open, focused once it is enabled again.
  useEffect(() => {
    if (error) field.current?.focus();
  }, [error]);
  const save = async (back: boolean) => {
    if (done.current || saving) return;
    if (value.trim() === nameOf(c)) {
      done.current = true;
      onDone(back);
      return;
    }
    setSaving(true);
    setError("");
    try {
      await onRename(c, value);
      done.current = true;
      onDone(back);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Le nom n’a pas pu être enregistré.");
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="sc-rename">
      <input
        ref={field}
        value={value}
        maxLength={80}
        disabled={saving}
        aria-label={`Nouveau nom de ${nameOf(c)}`}
        title="Vide, la source reprend son nom d’origine"
        aria-describedby={`rename-${c.connector_id}`}
        onChange={(event) => setValue(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            void save(true);
          } else if (event.key === "Escape") {
            event.preventDefault();
            event.stopPropagation();
            done.current = true;
            onDone(true);
          }
        }}
        onBlur={() => void save(false)}
      />
      <p
        id={`rename-${c.connector_id}`}
        className="sc-rename-hint"
        data-error={error ? true : undefined}
        role={error ? "alert" : undefined}
      >
        {error || (
          <>
            <kbd>Entrée</kbd> pour valider · <kbd>Échap</kbd> pour annuler
          </>
        )}
      </p>
    </div>
  );
}

function SourceCard({
  connector: c,
  kind,
  stats,
  highlight,
  now,
  onOpen,
  onRename,
  onPause,
  onRetry,
  onResume,
  onRemove,
}: {
  connector: Connector;
  kind?: ConnectorKind;
  /** Null until the facade counted them. */
  stats: SourceStats | null;
  highlight: boolean;
  now: number;
  onOpen: (id: string) => void;
  onRename: (c: Connector, name: string) => Promise<void>;
  onPause: (c: Connector) => Promise<void>;
  onRetry: (c: Connector) => Promise<void>;
  onResume: (c: Connector) => Promise<void>;
  onRemove: (c: Connector) => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  // A check can take a minute; it leaves the other actions available.
  const [checking, setChecking] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [error, setError] = useState("");
  const card = useRef<HTMLLIElement>(null);
  const confirmRef = useRef<HTMLButtonElement>(null);
  const h = c.health;
  const name = nameOf(c);
  const state = displayState(c);
  const status = sourceState(state);
  const problem = sourceProblem(state);
  const renew = state === "access_error" || state === "credential_expiring";
  const retryable = c.enabled && (state === "failing" || state === "access_error" || state === "silent");
  const troubled = c.enabled && (problem || h.last_error) && state !== "active";

  // A new card is scrolled to and takes the focus: the dialog that added it
  // gave the focus back to its opener first (its cleanup runs before this).
  useEffect(() => {
    if (!highlight) return;
    card.current?.scrollIntoView({ block: "nearest" });
    card.current?.querySelector<HTMLElement>(".connector-link")?.focus({ preventScroll: true });
  }, [highlight]);
  // Keyboard focus follows the controls it came from: into the removal
  // confirmation and back to the card's menu, and stays on the switch.
  const toggleRef = useRef<HTMLButtonElement>(null);
  const wasConfirming = useRef(false);
  const refocus = useRef(false);
  useEffect(() => {
    if (confirming) confirmRef.current?.focus();
    else if (wasConfirming.current) card.current?.querySelector<HTMLElement>(".more-menu .icon-button")?.focus();
    wasConfirming.current = confirming;
  }, [confirming]);
  useEffect(() => {
    if (!refocus.current) return;
    refocus.current = false;
    toggleRef.current?.focus();
  }, [c.enabled, c.connector_id]);

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
  const toggle = act(async () => {
    refocus.current = true;
    try {
      await (c.enabled ? onPause(c) : onResume(c));
    } catch (e) {
      refocus.current = false;
      throw e;
    }
  });
  const retry = async () => {
    if (checking) return;
    setChecking(true);
    setError("");
    try {
      await onRetry(c);
    } catch (e) {
      setError(e instanceof Error ? e.message : "La demande a échoué.");
    } finally {
      setChecking(false);
    }
  };

  const { week, perDay, most } = weekRates(c, stats, now);
  // How often it is read, or since when it is paused: the state's tooltip.
  const schedule = c.enabled
    ? `Vérifiée toutes les ${formatInterval(c.schedule.interval_seconds)}`
    : c.disabled_at
      ? `En pause depuis le ${formatAbsolute(c.disabled_at)}`
      : undefined;

  return (
    <li
      ref={card}
      className="source-card"
      data-connector={c.connector_id}
      data-highlight={highlight || undefined}
      data-enabled={c.enabled}
      data-tone={troubled ? status.tone : undefined}
    >
      <div className="sc-head">
        <SourceLogo
          namespace={c.source_namespace}
          connectorId={c.kind === "rss" ? c.connector_id : undefined}
        />
        <div className="sc-name">
          {renaming ? (
            <RenameField
              connector={c}
              onRename={onRename}
              onDone={(back) => {
                setRenaming(false);
                // Back on the name, renamed or not, unless a click moved the focus elsewhere.
                if (back)
                  requestAnimationFrame(() => card.current?.querySelector<HTMLElement>(".connector-link")?.focus());
              }}
            />
          ) : (
            <h3 className="source-item-name">
              <button
                type="button"
                className="connector-link"
                title={[name, sourceSummary(c, kind)].filter(Boolean).join("\n")}
                onClick={() => onOpen(c.connector_id)}
              >
                {name}
              </button>
              <button
                type="button"
                className="sc-pen"
                aria-label={`Renommer ${name}`}
                title="Renommer"
                onClick={() => setRenaming(true)}
              >
                <PencilIcon size={14} />
              </button>
            </h3>
          )}
        </div>
        {(c.enabled || resumable(c, kind)) && (
          <button
            ref={toggleRef}
            type="button"
            className="sc-switch"
            data-on={c.enabled}
            disabled={busy}
            aria-label={c.enabled ? `Mettre en pause ${name}` : `Reprendre ${name}`}
            title={c.enabled ? "Mettre en pause" : "Reprendre la collecte"}
            onClick={toggle}
          >
            <span className="switch-track" aria-hidden="true">
              <span className="switch-knob" />
            </span>
          </button>
        )}
      </div>
      {troubled ? (
        <div className="sc-problem" data-tone={status.tone}>
          <p>
            {problem}
            {h.last_error && (
              <>
                {problem && " "}
                <span className="muted">
                  Dernière erreur : {FAILURES[h.last_error.code] || <code>{h.last_error.code}</code>},{" "}
                  <Time value={h.last_error.at} />.
                </span>
              </>
            )}
          </p>
          {(retryable || renew) && (
            <div className="sc-problem-actions">
              {retryable && (
                <button
                  type="button"
                  className="button small"
                  disabled={checking}
                  onClick={retry}
                  aria-label={`Réessayer ${name}`}
                >
                  <ArrowClockwise size={14} weight="bold" aria-hidden="true" />
                  {checking ? "Vérification…" : "Réessayer"}
                </button>
              )}
              {renew && (
                <button type="button" className="button small" onClick={() => onOpen(c.connector_id)}>
                  Renouveler la connexion
                  <span className="visually-hidden"> de {name}</span>
                </button>
              )}
            </div>
          )}
        </div>
      ) : (
        <dl className="sc-stats">
          <div>
            <dt>par jour</dt>
            <dd>{stats ? perDay : "—"}</dd>
          </div>
          <div>
            <dt>dans le fil</dt>
            <dd>{stats ? stats.all : "—"}</dd>
          </div>
          <div>
            <dt>repérés</dt>
            <dd>{stats ? stats.caught : "—"}</dd>
          </div>
        </dl>
      )}
      <WeekSpark week={week} most={most} now={now} counted={!!stats} />
      {confirming ? (
        <div className="sc-foot row-actions" role="group" aria-label={`Retirer ${name}`}>
          <span className="row-confirm">Retirer cette source ? Les articles déjà reçus restent.</span>
          <button
            ref={confirmRef}
            type="button"
            className="button danger small"
            disabled={busy}
            onClick={act(() => onRemove(c))}
          >
            {busy ? "Retrait…" : "Oui, retirer"}
          </button>
          <button type="button" className="button small" onClick={() => setConfirming(false)}>
            Non
          </button>
        </div>
      ) : (
        <div className="sc-foot">
          {/* In words: when the last article came, or what keeps them from coming. */}
          <p className="source-state" data-state={state} data-tone={status.tone} title={schedule}>
            {state === "active" || state === "silent" ? (
              h.last_item_at ? (
                <>
                  Dernier article <Time value={h.last_item_at} />
                </>
              ) : (
                "Aucun article pour l’instant"
              )
            ) : (
              status.label
            )}
          </p>
          {schedule && <span className="visually-hidden">{schedule}</span>}
          <MoreMenu name={name} className="sc-more">
            {(close) => (
              <>
                <button
                  type="button"
                  role="menuitem"
                  className="menu-option"
                  onClick={() => {
                    close();
                    setRenaming(true);
                  }}
                >
                  Renommer
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="menu-option"
                  onClick={() => {
                    close();
                    onOpen(c.connector_id);
                  }}
                >
                  Réglages
                </button>
                <button
                  type="button"
                  role="menuitem"
                  className="menu-option"
                  data-tone="danger"
                  onClick={() => {
                    close();
                    setConfirming(true);
                  }}
                >
                  Retirer
                </button>
              </>
            )}
          </MoreMenu>
        </div>
      )}
      {error && (
        <p className="error-text" role="alert">
          {error}
        </p>
      )}
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
