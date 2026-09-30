// Plugins (THE-797): every plugin the engine runs, with a state in words
// (healthy, degraded, down, idle) judged on its last minutes of calls, its
// calls per minute, p95 and error rate over the chosen window, an error-rate
// sparkline and its last error. A row opens on the detail per operation and
// what the error means; connector plugins list the sources they collect.
import { useCallback, useEffect, useId, useState } from "react";
import {
  CaretDown,
  CheckCircle,
  MoonStars,
  WarningCircle,
  XCircle,
} from "@phosphor-icons/react";
import { AdminSection, WindowPicker, type SectionProps } from "./AdminSection";
import "../../health.css";
import { HealthBadge, displayState } from "../connectors/HealthBadge";
import { groupSources } from "../connectors/SourceList";
import {
  useAdminStats,
  type StatsStatus,
  type StatsWindow,
} from "../../lib/adminStats";
import { duration, short } from "../../lib/admin";
import { APIError, request } from "../../lib/search";
import { useConnectorList } from "../../lib/workspace";
import { longTime } from "../../lib/format";
import type { Connector } from "../../lib/connectors";
import {
  byConcern,
  errorMeaning,
  OPERATIONS,
  pluginRows,
  roleLabel,
  type ActivePlugin,
  type PluginRow,
  type PluginState,
} from "../../lib/health";

const STATES: Record<PluginState, { label: string; Icon: typeof CheckCircle }> =
  {
    ok: { label: "Opérationnel", Icon: CheckCircle },
    degraded: { label: "Dégradé", Icon: WarningCircle },
    down: { label: "En panne", Icon: XCircle },
    idle: { label: "Au repos", Icon: MoonStars },
  };

const PLUGINS_MS = 30000;

/** The active plan's plugins through the facade, reread every 30 s. */
function useActivePlugins(onUnauthorized: () => void) {
  const [items, setItems] = useState<ActivePlugin[] | null>(null);
  const [status, setStatus] = useState<StatsStatus>("loading");
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    const read = () =>
      request<{ items: ActivePlugin[] }>(
        "/demo/admin/plugins",
        undefined,
        controller.signal,
      )
        .then((data) => {
          setItems(data.items);
          setStatus("ready");
        })
        .catch((e) => {
          if (controller.signal.aborted) return;
          if (e instanceof APIError && e.status === 401)
            return onUnauthorized();
          setError(e instanceof Error ? e.message : "Liste indisponible.");
          // A failed poll keeps the list already on show; a refusal does not.
          setStatus((s) =>
            e instanceof APIError && e.status === 403
              ? "unavailable"
              : s === "ready"
                ? "ready"
                : "error",
          );
        });
    setStatus((s) => (s === "ready" ? s : "loading"));
    void read();
    const timer = setInterval(() => {
      if (!document.hidden) void read();
    }, PLUGINS_MS);
    return () => {
      controller.abort();
      clearInterval(timer);
    };
  }, [attempt, onUnauthorized]);
  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  return { items, status, error, reload };
}

const percent = (rate: number) =>
  rate === 0
    ? "0 %"
    : rate < 0.001
      ? "< 0,1 %"
      : `${(rate * 100).toLocaleString("fr-FR", { maximumFractionDigits: rate < 0.1 ? 1 : 0 })} %`;
const perMinute = (n: number) =>
  n === 0
    ? "0"
    : n < 0.1
      ? "< 0,1"
      : n.toLocaleString("fr-FR", { maximumFractionDigits: n < 10 ? 1 : 0 });

export function Plugins({ onUnauthorized }: SectionProps) {
  const [window, setWindow] = useState<StatsWindow>("1h");
  const [open, setOpen] = useState<string | null>(null);
  const active = useActivePlugins(onUnauthorized);
  const stats = useAdminStats("plugins", window, onUnauthorized);
  const hour = useAdminStats("plugins", "1h", onUnauthorized);
  const sources = useConnectorList(onUnauthorized);
  const status =
    active.status !== "ready"
      ? active.status
      : stats.data && stats.status === "loading"
        ? "ready"
        : stats.status;
  const rows = pluginRows(
    active.items || [],
    stats.data,
    hour.data || (window === "1h" ? stats.data : null),
  ).sort(byConcern);
  const counts = rows.reduce<Record<PluginState, number>>(
    (c, r) => ({ ...c, [r.state]: c[r.state] + 1 }),
    { ok: 0, degraded: 0, down: 0, idle: 0 },
  );
  const troubled = counts.down + counts.degraded;
  return (
    <AdminSection
      id="plugins"
      title="Plugins"
      status={status}
      error={active.status !== "ready" ? active.error : stats.error}
      onRetry={active.status !== "ready" ? active.reload : stats.reload}
      aside={
        <>
          {rows.length > 0 && (
            <span
              className="list-count"
              data-tone={troubled ? "warn" : "ok"}
              role="status"
            >
              {troubled
                ? `${troubled} à surveiller sur ${rows.length}`
                : `${rows.length} plugin${rows.length > 1 ? "s" : ""}, aucun problème`}
            </span>
          )}
          <WindowPicker
            value={window}
            onChange={setWindow}
            label="Période des plugins"
          />
        </>
      }
    >
      {rows.length === 0 ? (
        <p className="plug-empty">
          Aucun plugin actif : ce déploiement n’en déclare aucun, et aucun appel
          n’a été compté sur la période.
        </p>
      ) : (
        <>
          <div className="plug-columns" aria-hidden="true">
            <span>Plugin</span>
            <span className="num">Appels / min</span>
            <span className="num">p95</span>
            <span className="num">Erreurs</span>
            <span>Évolution</span>
            <span>Dernière erreur</span>
          </div>
          <ul
            className="plug-list"
            aria-label="Plugins"
            data-refreshing={stats.status === "loading" ? "" : undefined}
          >
            {rows.map((row) => (
              <PluginLine
                key={row.plugin_id}
                row={row}
                window={stats.data?.window ?? window}
                sources={sourcesOf(row, sources.connectors)}
                open={open === row.plugin_id}
                onToggle={() =>
                  setOpen((o) => (o === row.plugin_id ? null : row.plugin_id))
                }
              />
            ))}
          </ul>
        </>
      )}
    </AdminSection>
  );
}

/** The demo corpus's sources a connector plugin collects, by kind. */
function sourcesOf(row: PluginRow, connectors: Connector[]) {
  const kinds = row.roles
    .filter((r) => r.startsWith("connector:"))
    .map((r) => r.slice("connector:".length));
  if (!kinds.length) return [];
  return groupSources(connectors.filter((c) => kinds.includes(c.kind)));
}

const WINDOW_WORDS: Record<StatsWindow, string> = {
  "1h": "sur 1 h",
  "24h": "sur 24 h",
  "7d": "sur 7 jours",
};

function PluginLine({
  row,
  window,
  sources,
  open,
  onToggle,
}: {
  row: PluginRow;
  window: StatsWindow;
  sources: Connector[];
  open: boolean;
  onToggle: () => void;
}) {
  const id = useId();
  const state = STATES[row.state];
  const lastSuccess = sources
    .map((c) => c.health.last_success_at)
    .filter((at): at is string => !!at)
    .sort()
    .at(-1);
  const readToday = sources.reduce(
    (n, c) => n + (c.health.usage?.items_read || 0),
    0,
  );
  const reads = sources.some((c) => c.health.usage);
  const failing = sources.filter((c) =>
    ["failing", "access_error"].includes(displayState(c)),
  ).length;
  return (
    <li className="plug" data-state={row.state} data-open={open || undefined}>
      <button
        type="button"
        className="plug-row"
        aria-expanded={open}
        aria-controls={id}
        onClick={onToggle}
      >
        <span className="plug-who">
          <span className="plug-state" data-state={row.state}>
            <state.Icon size={14} weight="bold" aria-hidden="true" />
            {state.label}
          </span>
          <span className="plug-name">
            <span className="plug-id">{row.plugin_id}</span>
            {row.version && <span className="plug-version">{row.version}</span>}
          </span>
          <span className="plug-role">
            {row.roles.length
              ? roleLabel(row.roles)
              : "Plus dans le plan actif"}
            {sources.length > 0 && (
              <>
                {" · "}
                {sources.length} source{sources.length > 1 ? "s" : ""}
                {lastSuccess &&
                  ` · dernier relevé réussi ${longTime(lastSuccess)}`}
                {reads &&
                  ` · ${readToday.toLocaleString("fr-FR")} lus aujourd’hui`}
                {failing > 0 && (
                  <span className="plug-failing">
                    {" · "}
                    {failing} en échec
                  </span>
                )}
              </>
            )}
          </span>
        </span>
        <span className="num plug-metric" data-label="Appels / min">
          <span className="visually-hidden">, appels par minute : </span>
          {perMinute(row.per_minute)}
        </span>
        <span className="num plug-metric" data-label="p95">
          <span className="visually-hidden">, p95 : </span>
          {row.p95 !== undefined ? short(row.p95) : "—"}
        </span>
        <span
          className="num plug-metric plug-rate"
          data-label="Erreurs"
          data-bad={row.error_rate >= 0.05 || undefined}
        >
          <span className="visually-hidden">, erreurs : </span>
          {row.count ? percent(row.error_rate) : "—"}
        </span>
        <Spark values={row.spark} window={window} />
        <span className="plug-last">
          <span className="visually-hidden">, dernière erreur : </span>
          {row.last_error_code ? (
            <>
              <code>{row.last_error_code}</code>
              <span className="plug-when">{longTime(row.last_error_at!)}</span>
            </>
          ) : (
            <span className="plug-none">aucune</span>
          )}
        </span>
        <CaretDown size={14} className="plug-caret" aria-hidden="true" />
      </button>
      <div className="plug-detail" id={id} hidden={!open}>
        <p className="plug-reason">
          <strong>{state.label}.</strong>{" "}
          {row.reason || "Aucun appel sur la période."}
        </p>
        {row.last_error_code && (
          <p className="plug-meaning">
            Dernière erreur <code>{row.last_error_code}</code>{" "}
            {longTime(row.last_error_at!)}. {errorMeaning(row.last_error_code)}
          </p>
        )}
        {row.operations.length > 0 && (
          <table className="plug-ops">
            <caption className="visually-hidden">
              Appels de {row.plugin_id} par opération {WINDOW_WORDS[window]}
            </caption>
            <thead>
              <tr>
                <th scope="col">Opération</th>
                <th scope="col" className="num">
                  Appels
                </th>
                <th scope="col" className="num">
                  Erreurs
                </th>
                <th scope="col" className="num">
                  p50
                </th>
                <th scope="col" className="num">
                  p95
                </th>
                <th scope="col">Dernière erreur</th>
              </tr>
            </thead>
            <tbody>
              {row.operations.map((op) => (
                <tr key={op.operation + op.version}>
                  <th scope="row">
                    {OPERATIONS[op.operation] || op.operation}
                    {op.version !== row.version && (
                      <span className="plug-version">{op.version}</span>
                    )}
                  </th>
                  <td className="num">{op.count.toLocaleString("fr-FR")}</td>
                  <td className="num">{op.errors.toLocaleString("fr-FR")}</td>
                  <td className="num">
                    {op.p50 !== undefined ? duration(op.p50) : "—"}
                  </td>
                  <td className="num">
                    {op.p95 !== undefined ? duration(op.p95) : "—"}
                  </td>
                  <td>
                    {op.last_error_code ? (
                      <>
                        <code>{op.last_error_code}</code>{" "}
                        <span className="plug-when">
                          {longTime(op.last_error_at!)}
                        </span>
                      </>
                    ) : (
                      "—"
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {row.others.length > 0 && (
          <p className="plug-note">
            Aussi appelé {WINDOW_WORDS[window]} en version{" "}
            {row.others.join(", ")}.
          </p>
        )}
        {sources.length > 0 && (
          <ul
            className="plug-sources"
            aria-label={`Sources collectées par ${row.plugin_id}`}
          >
            {sources.map((c) => (
              <li key={c.connector_id}>
                <span className="plug-source-name">{c.source_namespace}</span>
                <HealthBadge state={displayState(c)} />
                <span className="plug-source-meta">
                  <span className="plug-when">
                    {c.health.last_success_at
                      ? `relevé réussi ${longTime(c.health.last_success_at)}`
                      : "aucun relevé réussi"}
                  </span>
                  {c.health.usage && (
                    <span className="plug-when">
                      {c.health.usage.items_read.toLocaleString("fr-FR")} lus
                      aujourd’hui
                    </span>
                  )}
                  {c.health.last_error && (
                    <span className="plug-when">
                      <code>{c.health.last_error.code}</code>{" "}
                      {longTime(c.health.last_error.at)}
                    </span>
                  )}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </li>
  );
}

/**
 * The error share of each bucket of the window: a bar per bucket that had
 * calls, taller as more of them failed; a dot where there were none.
 */
function Spark({
  values,
  window,
}: {
  values: (number | null)[];
  window: StatsWindow;
}) {
  const width = 96;
  const height = 24;
  const n = Math.max(values.length, 1);
  const step = width / n;
  const called = values.filter((v) => v !== null) as number[];
  const worst = called.length ? Math.max(...called) : 0;
  const label = !called.length
    ? `Aucun appel ${WINDOW_WORDS[window]}`
    : worst === 0
      ? `Aucune erreur ${WINDOW_WORDS[window]}`
      : `Taux d’erreur ${WINDOW_WORDS[window]} : jusqu’à ${percent(worst)}${values.at(-1) ? `, ${percent(values.at(-1)!)} à l’instant` : ""}`;
  return (
    <span className="plug-spark">
      <svg
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={label}
      >
        <line
          x1="0"
          x2={width}
          y1={height - 0.5}
          y2={height - 0.5}
          className="spark-base"
        />
        {values.map((v, i) =>
          v === null ? null : (
            <rect
              key={i}
              x={i * step + step * 0.15}
              width={Math.max(step * 0.7, 1)}
              y={height - 1 - Math.max(v * (height - 2), 1.5)}
              height={Math.max(v * (height - 2), 1.5)}
              rx={Math.min(1, step * 0.3)}
              className={v > 0 ? "spark-bad" : "spark-ok"}
            />
          ),
        )}
      </svg>
    </span>
  );
}
