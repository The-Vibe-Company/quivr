// The Admin tab (THE-796): is everything healthy right now, then the live
// flow of the latest documents through their steps, then one document's
// timeline in a side panel. Read-only. Later slices add bottlenecks, plugins
// and usage below the flow.
import { useCallback, useEffect, useState, type ComponentType } from "react";
import {
  CheckCircle,
  Pulse,
  WarningCircle,
  XCircle,
  Moon,
} from "@phosphor-icons/react";
import { EmptyState, Notice } from "../ui";
import {
  STEPS,
  acceptedAt,
  duration,
  short,
  slower,
  sourceName,
  titleOf,
  useAdminStream,
  verdict,
  type AdminRow,
  type AdminStats,
  type Cell,
} from "../../lib/admin";
import { Throughput } from "./Throughput";
import { TimelinePanel } from "./TimelinePanel";
import type { SectionProps } from "./AdminSection";

// The sections below the flow, in reading order: each is one component in
// its own file under admin/, built on AdminSection (THE-797, THE-798).
const SECTIONS: ComponentType<SectionProps>[] = [];

const clock = new Intl.DateTimeFormat("fr-FR", {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
});
// The live dot pulses this long after an update.
const ACTIVE_MS = 2500;
// A lost stream is reported after this long, so a reconnection that works at
// once shows nothing.
const BANNER_DELAY_MS = 4000;

export function AdminView({
  titles,
  selected,
  onSelect,
  onOpen,
  onAdd,
  onSources,
  onUnauthorized,
}: {
  /** Titles the Fil already knows, for documents without a title Part. */
  titles: Map<string, string>;
  selected: string | null;
  onSelect: (version: string | null) => void;
  onOpen: (record: string, version: string) => void;
  onAdd: () => void;
  onSources: () => void;
  onUnauthorized: () => void;
}) {
  const admin = useAdminStream(onUnauthorized);
  const running = admin.rows.some((r) => r.flow.some((c) => c.state === "run"));
  const now = useNow(running);
  const active = useRecent(admin.lastEvent, ACTIVE_MS);
  const offline = useLater(
    admin.status === "ready" && (!admin.connected || !admin.live),
    BANNER_DELAY_MS,
  );
  const row = admin.rows.find((r) => r.version_id === selected);

  const close = useCallback(() => {
    const version = selected;
    onSelect(null);
    // Back to the row that opened the panel, or to the flow if it left.
    requestAnimationFrame(() =>
      (
        document.querySelector<HTMLElement>(
          `[data-version="${CSS.escape(version || "")}"]`,
        ) || document.getElementById("flow-title")
      )?.focus(),
    );
  }, [selected, onSelect]);

  return (
    <main className="board admin-board" aria-labelledby="admin-title">
      <header className="admin-head">
        <h1 id="admin-title">Admin</h1>
        {admin.status === "ready" && (
          <span
            className="admin-live"
            data-state={
              !admin.connected
                ? "off"
                : !admin.live
                  ? "polling"
                  : active
                    ? "active"
                    : "live"
            }
          >
            <span className="admin-live-dot" aria-hidden="true" />
            {!admin.connected
              ? "Reconnexion…"
              : admin.live
                ? "En direct"
                : "Toutes les 5 s"}
          </span>
        )}
        <span className="admin-scope">Espace démo · lecture seule</span>
      </header>
      <p className="visually-hidden" role="status" aria-live="polite">
        {admin.announcement}
      </p>
      {admin.status === "loading" ? (
        <AdminSkeleton />
      ) : admin.status === "unavailable" ? (
        <Notice
          tone="info"
          title="Le suivi n’est pas activé sur ce déploiement."
        >
          {admin.error}
        </Notice>
      ) : admin.status === "error" ? (
        <Notice title="Suivi indisponible." onRetry={admin.reload}>
          {admin.error}
        </Notice>
      ) : (
        <>
          <div className="admin-top">
            {admin.stats && <Health stats={admin.stats} />}
            {admin.stats && <Throughput stats={admin.stats} now={now} />}
          </div>
          <div className="admin-main" data-open={selected ? "" : undefined}>
            <section className="panel admin-flow" aria-labelledby="flow-title">
              <div className="panel-head">
                <h2 id="flow-title" tabIndex={-1}>
                  Flux en direct
                </h2>
                <span className="list-count">
                  {admin.rows.length > 0 &&
                    `${admin.rows.length} dernier${admin.rows.length > 1 ? "s" : ""} document${admin.rows.length > 1 ? "s" : ""}`}
                </span>
                {admin.rows.length > 0 && <Legend />}
              </div>
              {offline && (
                <p className="admin-banner" role="status">
                  <span className="admin-banner-dot" aria-hidden="true" />
                  {!admin.connected
                    ? "Connexion au suivi perdue. Reconnexion automatique…"
                    : "Le flux des changements est coupé : la liste se met à jour toutes les 5 s en attendant."}
                </p>
              )}
              {admin.busy > 0 && (
                <p className="admin-banner" data-tone="busy" role="status">
                  Débit élevé : {admin.busy} documents en 10 s. La liste garde
                  les 50 plus récents, sans animation.
                </p>
              )}
              {admin.rows.length === 0 ? (
                <EmptyState
                  className="admin-empty"
                  icon={<Pulse size={26} aria-hidden="true" />}
                  title="Aucun document pour l’instant"
                  actions={
                    <>
                      <button
                        type="button"
                        className="button primary"
                        onClick={onAdd}
                      >
                        Ajouter du texte
                      </button>
                      <button
                        type="button"
                        className="button"
                        onClick={onSources}
                      >
                        Ajouter une source
                      </button>
                    </>
                  }
                >
                  Chaque document apparaît ici dès sa réception, puis avance
                  étape par étape : lu, découpé, trouvable, vecteurs, alertes.
                  Ajoutez un texte pour le voir passer.
                </EmptyState>
              ) : (
                <div className="flow-table">
                  <div className="flow-grid flow-columns" aria-hidden="true">
                    <span>Document</span>
                    {STEPS.map((s) => (
                      <span key={s.key}>{s.label}</span>
                    ))}
                    <span>Trouvable en</span>
                  </div>
                  <ol className="flow-rows" aria-label="Derniers documents">
                    {admin.rows.map((r) => (
                      <FlowRow
                        key={r.version_id}
                        row={r}
                        title={titleOf(r, titles)}
                        fresh={admin.fresh.has(r.version_id)}
                        selected={r.version_id === selected}
                        now={now}
                        onSelect={onSelect}
                      />
                    ))}
                  </ol>
                </div>
              )}
            </section>
            {selected && (
              <TimelinePanel
                key={selected}
                version={selected}
                row={row}
                titles={titles}
                now={now}
                onClose={close}
                onOpen={onOpen}
                onUnauthorized={onUnauthorized}
              />
            )}
          </div>
          {SECTIONS.map((Section, index) => (
            <Section
              key={index}
              onUnauthorized={onUnauthorized}
              stats={admin.stats}
            />
          ))}
        </>
      )}
    </main>
  );
}

/** The current time, ticking every second while a step is running. */
function useNow(ticking: boolean) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    setNow(Date.now());
    if (!ticking) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [ticking]);
  return now;
}

/** True for `ms` after each change of `at`. */
function useRecent(at: number, ms: number) {
  const [recent, setRecent] = useState(false);
  useEffect(() => {
    if (!at) return;
    setRecent(true);
    const timer = setTimeout(() => setRecent(false), ms);
    return () => clearTimeout(timer);
  }, [at, ms]);
  return recent;
}

/** True once `condition` has held for `ms`. */
function useLater(condition: boolean, ms: number) {
  const [later, setLater] = useState(false);
  useEffect(() => {
    if (!condition) return setLater(false);
    const timer = setTimeout(() => setLater(true), ms);
    return () => clearTimeout(timer);
  }, [condition, ms]);
  return later;
}

const HEALTH_ICONS = {
  ok: CheckCircle,
  quiet: Moon,
  warn: WarningCircle,
  bad: XCircle,
};

function Health({ stats }: { stats: AdminStats }) {
  const { tone, text } = verdict(stats);
  const Icon = HEALTH_ICONS[tone];
  const perMinute = stats.per_minute.toLocaleString("fr-FR", {
    maximumFractionDigits: 1,
  });
  return (
    <section className="panel admin-health" aria-labelledby="health-title">
      <h2 id="health-title" className="visually-hidden">
        État de la dernière heure
      </h2>
      <p className="health-verdict" data-tone={tone}>
        <Icon size={20} weight="fill" aria-hidden="true" />
        <span>{text}</span>
      </p>
      <dl className="admin-kpis">
        <Kpi
          label="Documents / min"
          value={perMinute}
          note={
            stats.per_minute_window === "1h"
              ? "moyenne sur 1\u00a0h"
              : "moyenne sur 10\u00a0min"
          }
        />
        <Kpi
          label="Reçu → trouvable"
          value={
            stats.searchable_p95_ms === null
              ? "—"
              : short(stats.searchable_p95_ms)
          }
          note={
            stats.searchable_day_p95_ms === null
              ? "p95 sur 1\u00a0h"
              : `p95 sur 1\u00a0h · ${short(stats.searchable_day_p95_ms)} sur 24\u00a0h`
          }
          tone={slower(stats) ? "warn" : undefined}
        />
        <Kpi
          label="En attente"
          value={String(stats.waiting)}
          note={
            stats.stuck
              ? `dont ${stats.stuck} plus lent${stats.stuck > 1 ? "s" : ""} que d’habitude`
              : "pas encore trouvables"
          }
          tone={stats.stuck ? "warn" : undefined}
        />
        <Kpi
          label="Erreurs"
          value={String(stats.errors)}
          note={"en quarantaine (1\u00a0h)"}
          tone={stats.errors ? "bad" : undefined}
        />
      </dl>
    </section>
  );
}

function Kpi({
  label,
  value,
  note,
  tone,
}: {
  label: string;
  value: string;
  note: string;
  tone?: "warn" | "bad";
}) {
  return (
    <div className="admin-kpi" data-tone={tone}>
      <dt>{label}</dt>
      <dd>
        <span className="admin-kpi-value">{value}</span>
        <span className="admin-kpi-note">{note}</span>
      </dd>
    </div>
  );
}

function FlowRow({
  row,
  title,
  fresh,
  selected,
  now,
  onSelect,
}: {
  row: AdminRow;
  title: string;
  fresh: boolean;
  selected: boolean;
  now: number;
  onSelect: (version: string) => void;
}) {
  const at = acceptedAt(row);
  const accepted = Date.parse(at);
  const ready = row.steps.retrieval_ready_at
    ? Date.parse(row.steps.retrieval_ready_at)
    : undefined;
  const failed = row.state === "quarantined";
  const withdrawn = row.state === "withdrawn";
  const total = ready !== undefined ? ready - accepted : undefined;
  const waited =
    !failed && !withdrawn && total === undefined ? now - accepted : undefined;
  return (
    <li data-fresh={fresh || undefined}>
      <button
        type="button"
        className="flow-grid flow-row"
        data-version={row.version_id}
        data-selected={selected || undefined}
        data-state={failed ? "error" : withdrawn ? "withdrawn" : undefined}
        aria-expanded={selected}
        aria-controls={selected ? "timeline-panel" : undefined}
        onClick={() => onSelect(row.version_id)}
      >
        <span className="flow-doc">
          <span className="flow-title">{title}</span>
          <span className="flow-meta">
            <span className="flow-source" title={row.source_namespace}>
              {sourceName(row.source_namespace)}
            </span>
            {at && (
              <time
                dateTime={at}
                title={`Reçu à ${clock.format(new Date(at))}`}
              >
                {clock.format(new Date(at))}
              </time>
            )}
            {failed && (
              <span className="flow-badge" data-tone="bad">
                En erreur
              </span>
            )}
            {withdrawn && <span className="flow-badge">Retiré</span>}
            {!row.is_current && !withdrawn && (
              <span className="flow-badge">Remplacé</span>
            )}
          </span>
        </span>
        {row.flow.map((cell, index) => (
          <StepCell
            key={cell.key}
            cell={cell}
            label={STEPS[index].long}
            now={now}
          />
        ))}
        <span className="flow-total">
          {total !== undefined ? (
            <span className="flow-total-value">{short(total)}</span>
          ) : failed ? (
            <span className="flow-total-note" data-tone="bad">
              échec
            </span>
          ) : waited !== undefined && waited >= 1000 ? (
            <span className="flow-total-note">{short(waited)}…</span>
          ) : (
            <span className="flow-total-note">—</span>
          )}
        </span>
      </button>
    </li>
  );
}

function StepCell({
  cell,
  label,
  now,
}: {
  cell: Cell;
  label: string;
  now: number;
}) {
  let state = cell.state;
  let text = cell.ms !== undefined ? short(cell.ms) : "";
  let hint = "";
  const usual =
    cell.limit === null
      ? ""
      : `d’habitude ${short(cell.limit)} au plus (p95 sur 24\u00a0h)`;
  if (cell.state === "run") {
    const since = cell.since ? Date.parse(cell.since) : NaN;
    const waited = Number.isNaN(since) ? 0 : Math.max(0, now - since);
    const late = cell.limit !== null && waited > cell.limit;
    text = waited >= 1000 || late ? short(waited) : "";
    if (late) {
      state = "slow";
      hint = `en cours depuis ${short(waited)}, plus que d’habitude`;
    } else
      hint = waited >= 1000 ? `en cours depuis ${short(waited)}` : "en cours";
  } else if (cell.state === "done") hint = `en ${duration(cell.ms ?? 0)}`;
  else if (cell.state === "slow")
    hint = `en ${duration(cell.ms ?? 0)}, lent : ${usual}`;
  else if (cell.state === "none") hint = "non enregistré";
  else if (cell.state === "error") hint = "échec, document mis en quarantaine";
  else hint = "à venir";
  if (cell.state === "error") text = "échec";
  return (
    <span
      className="cell"
      data-state={state}
      data-running={cell.state === "run" || undefined}
      title={`${label[0].toUpperCase()}${label.slice(1)} : ${hint}`}
    >
      <span className="visually-hidden">
        {label} : {hint}
      </span>
      <span className="cell-body" aria-hidden="true">
        {state === "slow" && <WarningCircle size={13} weight="bold" />}
        {cell.state === "run" && state !== "slow" && (
          <span className="cell-pulse" />
        )}
        {text && <span className="cell-text">{text}</span>}
        {cell.state === "none" && <span className="cell-text">—</span>}
      </span>
    </span>
  );
}

function Legend() {
  return (
    <ul className="flow-legend" aria-label="Légende">
      {(
        [
          ["done", "terminé"],
          ["run", "en cours"],
          ["slow", "plus lent que d’habitude"],
          ["error", "erreur"],
        ] as const
      ).map(([state, label]) => (
        <li key={state}>
          <span
            className="legend-swatch"
            data-state={state}
            aria-hidden="true"
          />
          {label}
        </li>
      ))}
    </ul>
  );
}

function AdminSkeleton() {
  return (
    <div className="admin-skeleton" aria-busy="true">
      <p role="status" className="visually-hidden">
        Chargement du suivi…
      </p>
      <div className="admin-top" aria-hidden="true">
        <div className="panel skeleton-block skeleton-health" />
        <div className="panel skeleton-block skeleton-chart" />
      </div>
      <div className="panel skeleton-rows" aria-hidden="true">
        {Array.from({ length: 6 }, (_, i) => (
          <div key={i} className="skeleton-row">
            <span />
            <span />
            <span />
            <span />
            <span />
            <span />
          </div>
        ))}
      </div>
    </div>
  );
}
