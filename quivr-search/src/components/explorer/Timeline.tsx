import { useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import {
  countLabel,
  overlaps,
  periodLabel,
  periodsBetween,
  rangeAt,
  rangeLabel,
  shortPeriod,
  type Facet,
  type Interval,
  type Range,
} from "../../lib/explore";

const STEP: Record<Interval, string> = { day: "par jour", month: "par mois", year: "par année" };

/**
 * The timeline (THE-1204): documents per day, month or year under the other
 * filters, across the span shown. Dragging across the bars, or a click and a
 * Shift+click, picks a range of whole periods; the range is tinted, the
 * documents outside it stay drawn. "Zoomer" narrows the span shown to the
 * range, and the step follows the span.
 */
export function Timeline({
  facet,
  stale,
  range,
  window: shown,
  onRange,
  onWindow,
}: {
  facet?: Facet;
  stale: boolean;
  range?: Range;
  window?: Range;
  onRange: (range: Range | undefined) => void;
  onWindow: (window: Range | undefined) => void;
}) {
  const interval = facet?.interval || "day";
  const counts = new Map((facet?.values || []).map(({ value, count }) => [String(value), count]));
  // The span shown: the window, else from the first dated document to the last.
  const span = shown
    ? rangeAt(shown, interval)
    : facet?.values.length
      ? { from: String(facet.values[0].value), to: String(facet.values.at(-1)!.value) }
      : undefined;
  const periods = span
    ? periodsBetween(span.from, span.to, interval) || (facet?.values || []).map(({ value }) => String(value))
    : [];
  const most = Math.max(1, ...counts.values());
  const bars = useRef<HTMLDivElement>(null);
  // A drag in progress: the period it started on and the one under the
  // pointer, by name, so new counts arriving meanwhile cannot shift it.
  const [drag, setDrag] = useState<{ anchor: string; at: string } | null>(null);
  const [focused, setFocused] = useState<string>();

  const between = (i: number, j: number): Range => ({
    from: periods[Math.min(i, j)],
    to: periods[Math.max(i, j)],
  });
  // A drag over periods no longer drawn is dropped.
  const dragged =
    drag && periods.includes(drag.anchor) && periods.includes(drag.at)
      ? between(periods.indexOf(drag.anchor), periods.indexOf(drag.at))
      : undefined;
  const picked = dragged || range;
  const indexAt = (x: number) => {
    const box = bars.current?.getBoundingClientRect();
    if (!box || !periods.length) return -1;
    return Math.max(0, Math.min(periods.length - 1, Math.floor(((x - box.left) / box.width) * periods.length)));
  };
  // A Shift+click extends the range from its first period.
  const extend = (i: number) => {
    const from = range ? periods.findIndex((p) => overlaps(p, range)) : -1;
    return from >= 0 ? between(from, i) : between(i, i);
  };

  const down = (event: PointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return;
    const i = indexAt(event.clientX);
    if (i < 0) return;
    event.currentTarget.setPointerCapture(event.pointerId);
    setDrag({ anchor: periods[i], at: periods[i] });
  };
  const move = (event: PointerEvent<HTMLDivElement>) => {
    if (!drag) return;
    const i = indexAt(event.clientX);
    if (i >= 0 && periods[i] !== drag.at) setDrag({ ...drag, at: periods[i] });
  };
  const up = (event: PointerEvent<HTMLDivElement>) => {
    if (!drag) return;
    setDrag(null);
    if (!dragged) return;
    onRange(event.shiftKey && drag.anchor === drag.at ? extend(periods.indexOf(drag.at)) : dragged);
  };
  // One stop in the tab order: ← → Début Fin move along the bars.
  const current = focused && periods.includes(focused) ? focused : periods.find((p) => range && overlaps(p, range)) || periods.at(-1);
  const keys = (event: KeyboardEvent<HTMLDivElement>) => {
    const buttons = [...event.currentTarget.querySelectorAll<HTMLButtonElement>("button")];
    const at = buttons.indexOf(document.activeElement as HTMLButtonElement);
    const to =
      event.key === "ArrowLeft" ? at - 1
      : event.key === "ArrowRight" ? at + 1
      : event.key === "Home" ? 0
      : event.key === "End" ? buttons.length - 1
      : undefined;
    if (to === undefined || at < 0) return;
    event.preventDefault();
    buttons[Math.max(0, Math.min(buttons.length - 1, to))]?.focus();
  };

  // A range can be zoomed into unless it is one day already drawn alone.
  const zoomable =
    !!range && (periods.filter((p) => overlaps(p, range)).length > 1 || interval !== "day");
  return (
    <section className="timeline" aria-label="Chronologie" data-stale={stale || undefined}>
      <div className="timeline-head">
        <p className="timeline-caption">
          {span ? (
            <>
              <strong>{rangeLabel(span)}</strong> · {STEP[interval]}
            </>
          ) : stale ? (
            "Comptage des dates…"
          ) : (
            "Aucun document daté pour ces filtres."
          )}
        </p>
        <div className="timeline-actions">
          {range && zoomable && (!shown || rangeLabel(shown) !== rangeLabel(range)) && (
            <button type="button" className="link-button" onClick={() => onWindow(range)}>
              Zoomer sur la période
            </button>
          )}
          {shown && (
            <button type="button" className="link-button" onClick={() => onWindow(undefined)}>
              Vue d’ensemble
            </button>
          )}
        </div>
      </div>
      {periods.length > 0 ? (
        <>
          <div
            ref={bars}
            className="timeline-bars"
            role="group"
            aria-label={`Documents ${STEP[interval]}`}
            data-tips
            data-dragging={dragged ? true : undefined}
            onPointerDown={down}
            onPointerMove={move}
            onPointerUp={up}
            onPointerCancel={() => setDrag(null)}
            onKeyDown={keys}
          >
            {periods.map((p, i) => {
              const count = counts.get(p) || 0;
              const documents = `${countLabel(count)} ${count > 1 ? "documents" : "document"}`;
              const inside = !!picked && overlaps(p, picked);
              return (
                <button
                  key={p}
                  type="button"
                  className="timeline-bar"
                  aria-pressed={inside}
                  data-outside={(picked && !inside) || undefined}
                  tabIndex={p === current ? 0 : -1}
                  onFocus={() => setFocused(p)}
                  aria-label={`${periodLabel(p)} : ${documents}`}
                  data-tip={`${periodLabel(p)} · ${documents}`}
                  onClick={(event) => {
                    // A pointer picks on release, over the whole drag; keys pick here.
                    if (event.detail !== 0) return;
                    onRange(event.shiftKey ? extend(i) : between(i, i));
                  }}
                >
                  <span
                    className="timeline-fill"
                    data-empty={count === 0 || undefined}
                    style={count ? { height: `${Math.max(6, (count / most) * 100)}%` } : undefined}
                  />
                </button>
              );
            })}
          </div>
          <div className="timeline-axis" aria-hidden="true">
            <span>{shortPeriod(periods[0])}</span>
            {periods.length > 2 && <span>{shortPeriod(periods[Math.floor((periods.length - 1) / 2)])}</span>}
            {periods.length > 1 && <span>{shortPeriod(periods.at(-1)!)}</span>}
          </div>
        </>
      ) : (
        <div className="timeline-bars timeline-empty" aria-hidden="true" />
      )}
    </section>
  );
}
