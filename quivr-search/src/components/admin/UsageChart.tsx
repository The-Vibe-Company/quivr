// The usage section's charts (THE-798): stacked columns over the window with
// a tooltip per column, and the micro-columns a ranked row uses as its trend.
// Pointing at a column, or moving with the arrow keys once the chart has
// focus, shows its range and its values; a visually hidden table carries the
// same numbers for screen readers.
import { useState } from "react";
import {
  binRange,
  count,
  ticksOf,
  type Bin,
  type Series,
  type UsageWindow,
} from "../../lib/usage";

export function Columns({
  bins,
  window,
  series,
  unit,
  label,
  tone,
}: {
  bins: Bin[];
  window: UsageWindow;
  /** Bottom first. A single series needs no legend; the block's title names it. */
  series: Series[];
  /** Singular and plural of what a column counts: ["document", "documents"]. */
  unit: [string, string];
  /** What the chart shows, for its summary and its table. */
  label: string;
  /** A single series' colour, when it is not a categorical slot. */
  tone?: "hot";
}) {
  const [point, setPoint] = useState<number | null>(null);
  // Set only by the keyboard, so pointing with a mouse announces nothing.
  const [spoken, setSpoken] = useState<number | null>(null);
  const totals = bins.map((_, i) =>
    series.reduce((sum, s) => sum + s.values[i], 0),
  );
  const max = Math.max(1, ...totals);
  const peak = totals.indexOf(Math.max(...totals));
  const sum = totals.reduce((a, b) => a + b, 0);
  const noun = (n: number) =>
    n ? `${count(n)} ${n > 1 ? unit[1] : unit[0]}` : `Aucun ${unit[0]}`;
  const summary = sum
    ? `${label} : ${noun(sum)}, au plus ${noun(totals[peak])} (${binRange(bins[peak], window)}).`
    : `${label} : aucun.`;
  const shown = point === null ? null : point;
  const describe = (i: number) =>
    [
      binRange(bins[i], window),
      ...(series.length > 1
        ? series
            .filter((s) => s.values[i] > 0)
            .map((s) => `${s.label} ${count(s.values[i])}`)
        : []),
      noun(totals[i]),
    ].join(", ");
  const ticks = ticksOf(bins, window);
  // The tooltip sits over its column, kept inside the chart at both ends.
  const left = shown === null ? 0 : ((shown + 0.5) / bins.length) * 100;
  return (
    <div className="usage-chart" data-tone={tone}>
      <div
        className="usage-columns"
        role="img"
        aria-label={summary}
        tabIndex={0}
        style={{
          gridTemplateColumns: `repeat(${bins.length}, minmax(0, 1fr))`,
        }}
        onMouseLeave={() => setPoint(null)}
        onBlur={() => {
          setPoint(null);
          setSpoken(null);
        }}
        onKeyDown={(event) => {
          const last = bins.length - 1;
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
        {bins.map((bin, i) => (
          <div
            key={bin.start}
            className="usage-slot"
            data-point={shown === i || undefined}
            data-current={i === bins.length - 1 || undefined}
            onMouseEnter={() => setPoint(i)}
          >
            {totals[i] > 0 && (
              <div
                className="usage-stack"
                style={{ height: `${(totals[i] / max) * 100}%` }}
              >
                {series.map((s) =>
                  s.values[i] > 0 ? (
                    <span
                      key={s.id}
                      className="usage-segment"
                      data-slot={s.slot}
                      style={{ flexGrow: s.values[i] }}
                    />
                  ) : null,
                )}
              </div>
            )}
          </div>
        ))}
        {shown !== null && (
          <div
            className="usage-tip"
            aria-hidden="true"
            style={{
              left: `clamp(84px, ${left}%, calc(100% - 84px))`,
            }}
          >
            <p className="usage-tip-range">{binRange(bins[shown], window)}</p>
            {series.length > 1 &&
              series
                .filter((s) => s.values[shown] > 0)
                .slice()
                .reverse()
                .map((s) => (
                  <p key={s.id} className="usage-tip-row">
                    <span className="usage-swatch" data-slot={s.slot} />
                    <span className="usage-tip-label">{s.label}</span>
                    <span className="usage-tip-value">
                      {count(s.values[shown])}
                    </span>
                  </p>
                ))}
            <p className="usage-tip-total">{noun(totals[shown])}</p>
          </div>
        )}
      </div>
      <p className="visually-hidden" aria-live="polite">
        {spoken === null ? "" : describe(spoken)}
      </p>
      <div
        className="usage-axis"
        aria-hidden="true"
        style={{
          gridTemplateColumns: `repeat(${bins.length}, minmax(0, 1fr))`,
        }}
      >
        {ticks.map((t) => (
          <span
            key={t.index}
            style={{
              gridColumn: `${t.index + 1} / span ${Math.min(4, bins.length - t.index)}`,
            }}
          >
            {t.label}
          </span>
        ))}
      </div>
      {/* A table ignores a 1px width, so the wrapper hides it. */}
      <div className="visually-hidden">
        <table>
          <caption>{label}</caption>
          <thead>
            <tr>
              <th scope="col">Période</th>
              {series.length > 1 &&
                series.map((s) => (
                  <th key={s.id} scope="col">
                    {s.label}
                  </th>
                ))}
              <th scope="col">Total</th>
            </tr>
          </thead>
          <tbody>
            {bins.map((bin, i) =>
              totals[i] > 0 ? (
                <tr key={bin.start}>
                  <th scope="row">{binRange(bin, window)}</th>
                  {series.length > 1 &&
                    series.map((s) => <td key={s.id}>{s.values[i]}</td>)}
                  <td>{totals[i]}</td>
                </tr>
              ) : null,
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

/** A row's trend: one micro-column per chart column, scaled to the row's peak. */
export function Trend({
  values,
  label,
  tips,
}: {
  values: number[];
  label: string;
  /** What each micro-column reads on hover. */
  tips?: string[];
}) {
  const max = Math.max(1, ...values);
  return (
    <span
      className="usage-trend"
      role="img"
      aria-label={label}
      tabIndex={tips ? 0 : undefined}
      data-tips={tips ? "" : undefined}
    >
      {values.map((v, i) => (
        <span
          key={i}
          data-empty={v === 0 || undefined}
          data-tip={tips?.[i]}
          style={{
            height: v ? `${Math.max(12, (v / max) * 100)}%` : undefined,
          }}
        />
      ))}
    </span>
  );
}

/**
 * A latency's trend: a line through the columns that had searches, broken
 * where nobody searched, from 0 to the row's slowest value.
 */
export function Sparkline({
  values,
  slot,
  label,
}: {
  values: (number | undefined)[];
  slot: number;
  label: string;
}) {
  const W = 84;
  const H = 20;
  const max = Math.max(1, ...values.map((v) => v ?? 0));
  const x = (i: number) =>
    values.length > 1 ? (i / (values.length - 1)) * (W - 4) + 2 : W / 2;
  const y = (v: number) => H - 2 - (v / max) * (H - 4);
  const runs: [number, number][][] = [];
  values.forEach((v, i) => {
    if (v === undefined) return;
    const last = runs.at(-1);
    const point: [number, number] = [x(i), y(v)];
    if (last && values[i - 1] !== undefined) last.push(point);
    else runs.push([point]);
  });
  return (
    <svg
      className="usage-sparkline"
      data-slot={slot}
      viewBox={`0 0 ${W} ${H}`}
      width={W}
      height={H}
      role="img"
      aria-label={label}
    >
      {runs.map((run, i) =>
        run.length === 1 ? (
          <circle key={i} cx={run[0][0]} cy={run[0][1]} r="1.75" />
        ) : (
          <polyline key={i} points={run.map((p) => p.join(",")).join(" ")} />
        ),
      )}
    </svg>
  );
}
