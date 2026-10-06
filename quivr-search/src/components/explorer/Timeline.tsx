import { useEffect, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
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
// An axis label needs about this many pixels.
const LABEL_PX = 110;

/** The width an element is drawn at, followed as it changes. */
function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const element = ref.current;
    if (!element) return;
    const observer = new ResizeObserver(([entry]) => setWidth(entry.contentRect.width));
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  return [ref, width] as const;
}

/**
 * The periods the axis names: as many as its width holds, on round steps
 * (Mondays or every other day, quarters, decades) so the eye can count.
 */
export function ticksOf(periods: string[], interval: Interval, width: number) {
  const room = Math.max(2, Math.floor(width / LABEL_PX));
  if (periods.length <= room) return periods.map((_, i) => i);
  const every = periods.length / room;
  const steps = interval === "day" ? [2, 7, 14] : interval === "month" ? [3, 6, 12] : [2, 5, 10, 20, 50];
  const step = steps.find((s) => s >= every) || Math.ceil(every);
  const instant = (p: string) => Date.parse(`${p}T00:00:00Z`);
  // Weeks start on Monday; every other week counts weeks since 1970.
  const on = (p: string) =>
    interval === "day"
      ? step < 7
        ? (Number(p.slice(8)) - 1) % step === 0
        : new Date(instant(p)).getUTCDay() === 1 && Math.floor((instant(p) + 3 * 864e5) / 6048e5) % (step / 7) === 0
      : interval === "month"
        ? (Number(p.slice(5, 7)) - 1) % step === 0
        : Number(p) % step === 0;
  const out = periods.flatMap((p, i) => (on(p) ? [i] : []));
  return out.length >= 2 ? out : periods.flatMap((_, i) => (i % Math.ceil(every) === 0 ? [i] : []));
}

/** An axis label: "14 sept.", "janv. 2026" at a year's start or "mars", "2026". */
function tickLabel(period: string, interval: Interval) {
  if (interval === "year") return period;
  if (interval === "month") return period.endsWith("-01") ? shortPeriod(period) : shortPeriod(period, false);
  return shortPeriod(period, false);
}

/**
 * The timeline (THE-1204, THE-1211): documents per day, month or year under
 * the other filters, across the span shown. Dragging across the bars, or a
 * click and a Shift+click, picks a range of whole periods; the range is
 * tinted, the documents outside it stay drawn quieter. The caption reads
 * the bar under the pointer. "Zoomer" narrows the span shown to the range,
 * and the step follows the span.
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
  const [bars, width] = useWidth<HTMLDivElement>();
  // A drag in progress: the period it started on and the one under the
  // pointer, by name, so new counts arriving meanwhile cannot shift it.
  const [drag, setDrag] = useState<{ anchor: string; at: string } | null>(null);
  const [focused, setFocused] = useState<string>();
  // The period under the pointer, read out in the caption.
  const [hover, setHover] = useState<string>();

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
    const i = indexAt(event.clientX);
    if (i >= 0 && periods[i] !== hover) setHover(periods[i]);
    if (drag && i >= 0 && periods[i] !== drag.at) setDrag({ ...drag, at: periods[i] });
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
  // The caption reads the bar under the pointer, else the range being
  // dragged, else the span and its step.
  const read = drag ? undefined : hover && periods.includes(hover) ? hover : undefined;
  const readCount = read ? counts.get(read) || 0 : 0;
  const draggedCount = dragged
    ? periods.filter((p) => overlaps(p, dragged)).reduce((n, p) => n + (counts.get(p) || 0), 0)
    : 0;
  const ticks = ticksOf(periods, interval, width);
  return (
    <section className="timeline" aria-label="Chronologie" data-stale={stale || undefined}>
      <div className="timeline-head">
        <p className="timeline-caption" aria-hidden={read || dragged ? true : undefined}>
          {read ? (
            <>
              <strong>{periodLabel(read)}</strong>
              <span className="timeline-figure">
                {countLabel(readCount)} document{readCount > 1 ? "s" : ""}
              </span>
            </>
          ) : dragged ? (
            <>
              <strong>{rangeLabel(dragged)}</strong>
              <span className="timeline-figure">
                {countLabel(draggedCount)} document{draggedCount > 1 ? "s" : ""}
              </span>
            </>
          ) : span ? (
            <>
              <strong>{rangeLabel(span)}</strong>
              <span>{STEP[interval]}</span>
            </>
          ) : stale ? (
            "Comptage des dates…"
          ) : (
            "Aucun document daté pour ces filtres."
          )}
        </p>
        <div className="timeline-actions">
          {!range && periods.length > 1 && <span className="timeline-hint">Glissez pour choisir une période</span>}
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
      <div
        ref={bars}
        className="timeline-bars"
        role="group"
        aria-label={`Documents ${STEP[interval]}`}
        data-empty={!periods.length || undefined}
        data-dense={periods.length > 90 || undefined}
        data-dragging={dragged ? true : undefined}
        onPointerDown={periods.length ? down : undefined}
        onPointerMove={periods.length ? move : undefined}
        onPointerUp={up}
        onPointerLeave={() => setHover(undefined)}
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
              data-hover={p === read || undefined}
              tabIndex={p === current ? 0 : -1}
              onFocus={() => setFocused(p)}
              aria-label={`${periodLabel(p)} : ${documents}`}
              onClick={(event) => {
                // A pointer picks on release, over the whole drag; keys pick here.
                if (event.detail !== 0) return;
                onRange(event.shiftKey ? extend(i) : between(i, i));
              }}
            >
              <span
                className="timeline-fill"
                data-empty={count === 0 || undefined}
                style={count ? { height: `${Math.max(4, (count / most) * 100)}%` } : undefined}
              />
            </button>
          );
        })}
      </div>
      <div className="timeline-axis" aria-hidden="true">
        {periods.length > 0 &&
          ticks.map((i) => (
            <span
              key={periods[i]}
              style={{ left: `${((i + 0.5) / periods.length) * 100}%` }}
              // Labels at the ends lean inwards rather than overflow.
              data-edge={i / periods.length < 0.04 ? "start" : i / periods.length > 0.94 ? "end" : undefined}
            >
              {tickLabel(periods[i], interval)}
            </span>
          ))}
      </div>
    </section>
  );
}
