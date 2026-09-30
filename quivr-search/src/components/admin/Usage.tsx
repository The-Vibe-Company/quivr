// How the deployment is used (THE-798): documents received per source,
// searches per mode with their latency, alerts triggered and the most
// frequent queries, over 24 hours or 7 days. Read from the engine's rollups
// through the facade (/demo/admin/stats/{received,searches,matches,
// top-queries}); each block loads, fails and empties on its own.
import { useEffect, useRef, useState, type ReactNode } from "react";
import { ArrowClockwise, EyeSlash } from "@phosphor-icons/react";
import { AdminSection, WindowPicker, type SectionProps } from "./AdminSection";
import { Columns, Sparkline, Trend } from "./UsageChart";
import {
  useAdminStats,
  type StatsStatus,
  type StatsWindow,
} from "../../lib/adminStats";
import { short } from "../../lib/admin";
import {
  WINDOW_LABELS,
  binsOf,
  hourlySpan,
  spanOf,
  coarse,
  count,
  countsIn,
  matchValues,
  modeRows,
  modeSeries,
  plural,
  sourceSeries,
  type Bin,
  type UsageWindow,
} from "../../lib/usage";
import "../../usage.css";

const WINDOWS: UsageWindow[] = ["24h", "7d"];
const TOP_QUERIES = 10;
// Sources read to stack five and fold the next ones into "Autres".
const SOURCES_READ = 20;

export function Usage({ onUnauthorized }: SectionProps) {
  const [window, setWindow] = useState<UsageWindow>("24h");
  const received = useAdminStats(
    "received",
    window,
    onUnauthorized,
    SOURCES_READ,
  );
  const searches = useAdminStats("searches", window, onUnauthorized);
  const matches = useAdminStats("matches", window, onUnauthorized);
  const queries = useAdminStats(
    "top-queries",
    window,
    onUnauthorized,
    TOP_QUERIES,
  );
  const reads = [received, searches, matches, queries];
  const now = useMinute();
  // A source keeps its colour for as long as the page is open.
  const slots = useRef(new Map<string, number>()).current;

  // Each block shows its own shapes while it loads.
  const section = reads.every((r) => r.status === "unavailable")
    ? "unavailable"
    : "ready";
  return (
    <AdminSection
      id="usage"
      title="Utilisation"
      status={section}
      aside={
        <WindowPicker
          value={window}
          onChange={(w: StatsWindow) => setWindow(w as UsageWindow)}
          label="Période de l’utilisation"
          options={WINDOWS}
        />
      }
    >
      <div className="usage-grid">
        <Block
          read={received}
          window={window}
          now={now}
          title="Documents reçus"
          skeleton="chart"
          render={(list, v) => {
            const { series, others } = sourceSeries(list, v.bins, slots);
            return (
              <>
                <Figure
                  value={count(list.total)}
                  unit={list.total > 1 ? "documents" : "document"}
                  detail={
                    list.sources
                      ? `de ${plural(list.sources, "source")} en ${v.name}`
                      : `en ${v.name}`
                  }
                />
                {list.total === 0 ? (
                  <Empty>
                    Ajoutez un texte ou une source : chaque nouveau document
                    compte ici dès que Quivr l’accepte, avec sa source.
                  </Empty>
                ) : (
                  <>
                    <Columns
                      bins={v.bins}
                      window={v.window}
                      series={series}
                      unit={["document", "documents"]}
                      label={`Documents reçus par source, ${v.name}`}
                    />
                    <ul className="usage-legend" aria-label="Sources">
                      {series.map((s) => (
                        <li key={s.id} data-muted={s.slot === 0 || undefined}>
                          <span
                            className="usage-swatch"
                            data-slot={s.slot}
                            aria-hidden="true"
                          />
                          <span
                            className="usage-legend-label"
                            title={s.id || undefined}
                          >
                            {s.slot === 0
                              ? plural(
                                  others.sources,
                                  "autre source",
                                  "autres sources",
                                )
                              : s.label}
                          </span>
                          <span className="usage-legend-value">
                            {count(s.total)}
                          </span>
                          <span className="usage-legend-share">
                            {share(s.total, list.total)}
                          </span>
                        </li>
                      ))}
                    </ul>
                  </>
                )}
              </>
            );
          }}
        />
        <Block
          read={searches}
          window={window}
          now={now}
          title="Recherches"
          skeleton="chart"
          render={(list, v) => {
            const rows = modeRows(list, v.bins);
            const total = rows.reduce((sum, r) => sum + r.count, 0);
            const errors = rows.reduce((sum, r) => sum + r.errors, 0);
            const busiest = rows.reduce<(typeof rows)[number] | undefined>(
              (a, b) => (!a || b.count > a.count ? b : a),
              undefined,
            );
            return (
              <>
                <Figure
                  value={count(total)}
                  unit={total > 1 ? "recherches" : "recherche"}
                  detail={
                    busiest?.p95 !== undefined
                      ? `en ${v.name} · p95 ${short(busiest.p95)} (${busiest.label.toLowerCase()})`
                      : `en ${v.name}`
                  }
                  alert={errors ? plural(errors, "échec") : undefined}
                />
                {total === 0 ? (
                  <Empty>
                    Cherchez un mot dans la barre du haut : chaque recherche
                    compte ici avec son temps de réponse.
                  </Empty>
                ) : (
                  <>
                    <Columns
                      bins={v.bins}
                      window={v.window}
                      series={modeSeries(list, v.bins, rows)}
                      unit={["recherche", "recherches"]}
                      label={`Recherches par mode, ${v.name}`}
                    />
                    <table className="usage-modes">
                      <caption className="visually-hidden">
                        Recherches et temps de réponse par mode, {v.name}
                      </caption>
                      <thead>
                        <tr>
                          <th scope="col">Mode</th>
                          <th scope="col">Nombre</th>
                          <th scope="col">p50</th>
                          <th scope="col">p95</th>
                          <th scope="col">
                            Évolution
                            <span className="visually-hidden"> du p95</span>
                          </th>
                        </tr>
                      </thead>
                      <tbody>
                        {rows.map((r) => (
                          <tr key={r.mode}>
                            <th scope="row">
                              <span className="usage-mode">
                                <span
                                  className="usage-swatch"
                                  data-slot={r.slot}
                                  aria-hidden="true"
                                />
                                {r.label}
                                <span className="usage-hint"> {r.hint}</span>
                              </span>
                            </th>
                            <td>{count(r.count)}</td>
                            <td>{r.p50 === undefined ? "—" : short(r.p50)}</td>
                            <td>{r.p95 === undefined ? "—" : short(r.p95)}</td>
                            <td className="usage-modes-trend">
                              <Sparkline
                                values={r.p95s}
                                slot={r.slot}
                                label={`p95 le plus lent : ${short(Math.max(0, ...r.p95s.map((v) => v ?? 0)))}`}
                              />
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </>
                )}
              </>
            );
          }}
        />
        <Block
          read={matches}
          window={window}
          now={now}
          title="Alertes déclenchées"
          skeleton="chart"
          render={(list, v) => (
            <>
              <Figure
                value={count(list.total)}
                unit={list.total > 1 ? "articles attrapés" : "article attrapé"}
                detail={`par vos alertes en ${v.name}`}
              />
              {list.total === 0 ? (
                <Empty>
                  Chaque article qu’une alerte attrape compte ici, y compris la
                  nouvelle version d’un article corrigé.
                </Empty>
              ) : (
                <>
                  <Columns
                    bins={v.bins}
                    window={v.window}
                    tone="hot"
                    series={[
                      {
                        id: "matches",
                        label: "Articles attrapés",
                        slot: 0,
                        values: matchValues(list, v.bins),
                        total: list.total,
                      },
                    ]}
                    unit={["article attrapé", "articles attrapés"]}
                    label={`Articles attrapés par les alertes, ${v.name}`}
                  />
                  {list.items.length > 1 && (
                    <p className="usage-foot">
                      {list.items
                        .map((i) => `${i.evaluator} ${count(i.count)}`)
                        .join(" · ")}
                    </p>
                  )}
                </>
              )}
            </>
          )}
        />
        <Block
          read={queries}
          window={window}
          now={now}
          title="Requêtes fréquentes"
          skeleton="list"
          render={(list, v) =>
            !list.recording ? (
              <Recording />
            ) : list.items.length === 0 ? (
              <Empty>
                Aucune requête en {v.name}. Les recherches faites depuis la
                barre du haut s’afficheront ici, les plus fréquentes d’abord.
              </Empty>
            ) : (
              <ol
                className="usage-queries"
                aria-label={`Requêtes les plus fréquentes, ${v.name}`}
              >
                {list.items.map((q, i) => (
                  <li key={q.query}>
                    <span className="usage-rank" aria-hidden="true">
                      {i + 1}
                    </span>
                    <span className="usage-query" title={q.query}>
                      {q.query}
                    </span>
                    <Trend
                      values={coarse(countsIn(v.bins, q.points), v.window)}
                      label={trendLabel(countsIn(v.bins, q.points))}
                    />
                    <span className="usage-query-count">
                      {count(q.count)}
                      <span className="visually-hidden"> fois</span>
                    </span>
                  </li>
                ))}
              </ol>
            )
          }
        />
      </div>
    </AdminSection>
  );
}

/** The window a block's numbers were read over, as it draws them. */
interface View {
  window: UsageWindow;
  name: string;
  bins: Bin[];
}

interface Read<T> {
  data: T | null;
  status: StatsStatus;
  error: string;
  reload: () => void;
}

/**
 * One block: its title, then its content once read. A new window keeps the
 * previous numbers, dimmed, until the new ones arrive; a failure keeps them
 * too and says so.
 */
function Block<T extends { window: StatsWindow }>({
  read,
  window,
  title,
  skeleton,
  render,
  now,
}: {
  read: Read<T>;
  window: UsageWindow;
  title: string;
  skeleton: "chart" | "list";
  render: (data: T, view: View) => ReactNode;
  now: number;
}) {
  const stale = !!read.data && read.data.window !== window;
  // Numbers kept from the previous window are drawn as that window.
  const shown = (read.data?.window || window) as UsageWindow;
  // A read with from and to is drawn in columns of its own buckets; the top
  // queries, counted per hour, carry neither.
  const data = read.data as {
    from?: string;
    to?: string;
    resolution_seconds?: number;
  } | null;
  const span =
    data?.from && data.to && data.resolution_seconds
      ? spanOf({
          from: data.from,
          to: data.to,
          resolution_seconds: data.resolution_seconds,
        })
      : hourlySpan(shown, now);
  const view: View = {
    window: shown,
    name: WINDOW_LABELS[shown],
    bins: binsOf(span, shown),
  };
  return (
    <section
      className="usage-block"
      aria-label={title}
      aria-busy={read.status === "loading" || undefined}
      data-stale={stale || undefined}
    >
      <h3>{title}</h3>
      {read.status === "unavailable" ? (
        <p className="usage-note">
          Ce chiffre n’est pas activé sur ce déploiement.
        </p>
      ) : read.status === "error" && !read.data ? (
        <Failure message={read.error} onRetry={read.reload} />
      ) : !read.data ? (
        <Skeleton kind={skeleton} />
      ) : (
        <>
          {read.status === "error" && (
            <Failure message={read.error} onRetry={read.reload} />
          )}
          <div className="usage-body">{render(read.data, view)}</div>
        </>
      )}
    </section>
  );
}

function Figure({
  value,
  unit,
  detail,
  alert,
}: {
  value: string;
  unit: string;
  detail: string;
  alert?: string;
}) {
  return (
    <p className="usage-figure">
      <span className="usage-figure-value">{value}</span>{" "}
      <span className="usage-figure-unit">{unit}</span>{" "}
      <span className="usage-figure-detail">
        {detail}
        {alert && <span className="usage-figure-alert"> · {alert}</span>}
      </span>
    </p>
  );
}

function Empty({ children }: { children: ReactNode }) {
  return <p className="usage-empty">{children}</p>;
}

function Failure({
  message,
  onRetry,
}: {
  message: string;
  onRetry: () => void;
}) {
  return (
    <p className="usage-note" role="alert">
      {message || "Chiffres indisponibles."}{" "}
      <button type="button" className="link-button" onClick={onRetry}>
        <ArrowClockwise size={13} aria-hidden="true" /> Réessayer
      </button>
    </p>
  );
}

/** The top queries when the deployment does not keep query text. */
function Recording() {
  return (
    <div className="usage-recording" role="status">
      <EyeSlash
        size={20}
        weight="regular"
        aria-hidden="true"
        className="usage-recording-icon"
      />
      <div>
        <p className="usage-recording-title">
          Le texte des recherches n’est pas enregistré
        </p>
        <p>
          Quivr compte les recherches sans garder ce qui a été tapé. Pour voir
          les requêtes les plus fréquentes, activez{" "}
          <code>observability.record_query_text</code> dans la configuration du
          moteur.
        </p>
        <p className="usage-recording-why">
          Désactivé par défaut : une requête peut contenir un nom ou une
          information confidentielle. Une fois activé, le texte est gardé 7
          jours.
        </p>
      </div>
    </div>
  );
}

function Skeleton({ kind }: { kind: "chart" | "list" }) {
  return (
    <div className="usage-skeleton" data-kind={kind} aria-hidden="true">
      <span className="usage-skeleton-figure" />
      {kind === "chart" ? (
        <span className="usage-skeleton-chart">
          {Array.from({ length: 24 }, (_, i) => (
            <span key={i} style={{ height: `${28 + ((i * 37) % 60)}%` }} />
          ))}
        </span>
      ) : (
        Array.from({ length: 5 }, (_, i) => (
          <span key={i} className="usage-skeleton-row" />
        ))
      )}
    </div>
  );
}

const share = (part: number, total: number) => {
  const pct = total ? (part / total) * 100 : 0;
  return pct >= 1 || pct === 0 ? `${Math.round(pct)} %` : "< 1 %";
};

function trendLabel(values: number[]) {
  const half = Math.floor(values.length / 2);
  const early = values.slice(0, half).reduce((a, b) => a + b, 0);
  const late = values.slice(half).reduce((a, b) => a + b, 0);
  return late > early ? "en hausse" : late < early ? "en baisse" : "stable";
}

/** Now, to the minute: the columns move on as the clock does. */
function useMinute() {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60000);
    return () => clearInterval(timer);
  }, []);
  return now;
}
