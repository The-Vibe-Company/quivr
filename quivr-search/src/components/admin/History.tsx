// The documents stored in the demo corpus (THE-1034): the total since the
// first document, today's count, the days without any, and one column per
// day with its exact count on hover, on focus and in the table by day. A day
// without a document shows as zero, so a gap in the history stands out.
import { useState } from "react";
import { AdminSection, type SectionProps } from "./AdminSection";
import { count, plural } from "../../lib/usage";
import {
  dayLabel,
  dayName,
  dayTick,
  useHistory,
  type History as HistoryData,
} from "../../lib/history";
import "../../usage.css";

// About this many dates under the columns.
const TICKS = 6;

export function History({ onUnauthorized, bare }: SectionProps) {
  const history = useHistory(onUnauthorized);
  return (
    <AdminSection
      id="history"
      title="Documents en base"
      bare={bare}
      status={history.data ? "ready" : history.status}
      error={history.error}
      onRetry={history.reload}
    >
      {history.data && <Stored data={history.data} />}
    </AdminSection>
  );
}

const documents = (n: number) => plural(n, "document");

function Stored({ data }: { data: HistoryData }) {
  const { days } = data;
  const today = days.at(-1)?.count ?? 0;
  const empty = days.filter((d) => d.count === 0).length;
  const dated = days.reduce((n, d) => n + d.count, 0);
  const undated = data.total - dated;
  return (
    <div className="history">
      <div className="history-figures">
        <Figure
          value={count(data.total)}
          unit={data.total > 1 ? "documents" : "document"}
          detail={`en base depuis le ${dayName(data.first_day)}, retirés compris`}
        />
        <Figure
          value={count(today)}
          unit="aujourd’hui"
          detail={`${dayLabel(data.today)}, jusqu’à maintenant`}
        />
        <Figure
          value={count(empty)}
          unit={empty > 1 ? "jours sans document" : "jour sans document"}
          detail={`sur ${plural(days.length, "jour")}`}
          tone={empty ? "warn" : undefined}
        />
      </div>
      <DayColumns days={days} />
      <p className="history-note">
        Un document compte au jour où Quivr a reçu sa version actuelle, dans le
        fuseau {data.time_zone} : une correction le déplace au jour de sa
        nouvelle version. Les documents retirés restent comptés.
        {undated > 0 &&
          ` Le total compte aussi ${documents(undated)} sans version actuelle, donc sans jour.`}
        {data.truncated &&
          ` Les jours avant le ${dayName(data.first_day)} ne sont pas montrés : un an au plus.`}
      </p>
      <details className="history-days">
        <summary>Détail par jour ({plural(days.length, "jour")})</summary>
        <table className="history-table">
          <caption className="visually-hidden">
            Documents en base par jour, du plus récent au plus ancien
          </caption>
          <thead>
            <tr>
              <th scope="col">Jour</th>
              <th scope="col">Documents</th>
            </tr>
          </thead>
          <tbody>
            {[...days].reverse().map((d) => (
              <tr key={d.day} data-empty={d.count === 0 || undefined}>
                <th scope="row">{dayLabel(d.day)}</th>
                <td>{count(d.count)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </div>
  );
}

function Figure({
  value,
  unit,
  detail,
  tone,
}: {
  value: string;
  unit: string;
  detail: string;
  tone?: "warn";
}) {
  return (
    <p className="usage-figure" data-tone={tone}>
      <span className="usage-figure-value">{value}</span>{" "}
      <span className="usage-figure-unit">{unit}</span>{" "}
      <span className="usage-figure-detail">{detail}</span>
    </p>
  );
}

/**
 * One column per day, scaled to the busiest; an empty day keeps a flat mark
 * on the baseline. Pointing at a column, or moving with the arrow keys once
 * the chart has focus, shows its day and its count.
 */
function DayColumns({ days }: { days: HistoryData["days"] }) {
  const [point, setPoint] = useState<number | null>(null);
  // Set only by the keyboard, so pointing with a mouse announces nothing.
  const [spoken, setSpoken] = useState<number | null>(null);
  const max = Math.max(1, ...days.map((d) => d.count));
  const peak = days.findIndex((d) => d.count === max);
  const describe = (i: number) =>
    `${dayLabel(days[i].day)} : ${days[i].count ? documents(days[i].count) : "aucun document"}`;
  const summary =
    peak >= 0
      ? `Documents en base par jour, du ${dayName(days[0].day)} à aujourd’hui : au plus ${documents(max)} (${dayLabel(days[peak].day)}). Le détail par jour suit le graphique.`
      : `Documents en base par jour, du ${dayName(days[0].day)} à aujourd’hui : aucun.`;
  const step = Math.max(1, Math.ceil(days.length / TICKS));
  const ticks = days
    .map((_, i) => i)
    .filter((i) => i % step === 0 && (i === 0 || days.length - i >= step / 2));
  const left = point === null ? 0 : ((point + 0.5) / days.length) * 100;
  const columns = { gridTemplateColumns: `repeat(${days.length}, minmax(0, 1fr))` };
  return (
    <div className="usage-chart history-chart">
      <div
        className="usage-columns"
        role="img"
        aria-label={summary}
        tabIndex={0}
        data-dense={days.length > 60 || undefined}
        style={columns}
        onMouseLeave={() => setPoint(null)}
        onBlur={() => {
          setPoint(null);
          setSpoken(null);
        }}
        onKeyDown={(event) => {
          const last = days.length - 1;
          const moves: Record<string, (p: number | null) => number> = {
            ArrowLeft: (p) => Math.max(0, (p ?? last + 1) - 1),
            ArrowRight: (p) => Math.min(last, (p ?? last) + 1),
            Home: () => 0,
            End: () => last,
          };
          if (event.key === "Escape") return setPoint(null);
          const move = moves[event.key];
          if (!move) return;
          event.preventDefault();
          const next = move(point);
          setPoint(next);
          setSpoken(next);
        }}
      >
        {days.map((d, i) => (
          <div
            key={d.day}
            className="usage-slot"
            data-point={point === i || undefined}
            data-current={i === days.length - 1 || undefined}
            data-day={d.day}
            onMouseEnter={() => setPoint(i)}
          >
            {d.count > 0 ? (
              <div
                className="usage-stack"
                style={{ height: `${(d.count / max) * 100}%` }}
              >
                <span className="usage-segment" style={{ flexGrow: 1 }} />
              </div>
            ) : (
              <span className="history-zero" />
            )}
          </div>
        ))}
        {point !== null && (
          <div
            className="usage-tip"
            aria-hidden="true"
            style={{ left: `clamp(84px, ${left}%, calc(100% - 84px))` }}
          >
            <p className="usage-tip-range">{dayLabel(days[point].day)}</p>
            <p className="usage-tip-total">
              {days[point].count ? documents(days[point].count) : "Aucun document"}
            </p>
          </div>
        )}
      </div>
      <p className="visually-hidden" aria-live="polite">
        {spoken === null ? "" : describe(spoken)}
      </p>
      <div className="usage-axis" aria-hidden="true" style={columns}>
        {ticks.map((i) => (
          <span
            key={i}
            style={{ gridColumn: `${i + 1} / span ${Math.min(step, days.length - i)}` }}
          >
            {dayTick(days[i].day)}
          </span>
        ))}
      </div>
    </div>
  );
}
