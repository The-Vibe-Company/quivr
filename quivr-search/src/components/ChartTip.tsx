import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

/**
 * One tooltip for every small chart of the app. A chart marks itself with
 * `data-tips`, and each of its bars carries its words in `data-tip`. Over the
 * chart, the bar under the pointer shows its words: the whole height of its
 * column counts, so an empty day is as easy to read as the busiest one. From
 * the keyboard, a chart that takes the focus (`tabIndex={0}`) shows its latest
 * bar and ← → Début Fin move along its bars; a bar that is a control of its
 * own shows its words when it takes the focus. In a modal dialog, the words
 * are drawn inside it: anything outside lies under the dialog. While shown,
 * the words follow their bar: new words after a refresh, none once it is gone.
 */
export function ChartTip() {
  const [tip, setTip] = useState<{
    text: string;
    x: number;
    y: number;
    below: boolean;
    host: Element;
  } | null>(null);
  const shown = useRef<Element | null>(null);

  useEffect(() => {
    const place = (bar: Element) => {
      // The page is drawn zoomed on desktop: rectangles come zoomed, fixed
      // positions are zoomed again.
      const zoom = parseFloat(getComputedStyle(document.documentElement).zoom) || 1;
      const r = bar.getBoundingClientRect();
      // Above the bar, or below it when the window's top is too close.
      const below = r.top / zoom < 44;
      const next = {
        text: bar.getAttribute("data-tip") || "",
        x: (r.left + r.width / 2) / zoom,
        y: (below ? r.bottom : r.top) / zoom,
        below,
        host: bar.closest("dialog[open]") || document.body,
      };
      // Unchanged, the tip keeps its state: drawing it is a mutation too.
      setTip((t) =>
        t &&
        t.text === next.text &&
        t.x === next.x &&
        t.y === next.y &&
        t.below === next.below &&
        t.host === next.host
          ? t
          : next,
      );
    };
    // A refresh may change the bar's words or height, move it within its
    // chart, or remove it; other changes of the page leave the tip where it is.
    const watch = new MutationObserver((records) => {
      const bar = shown.current;
      if (!bar?.isConnected) return show(null);
      const chart = bar.closest("[data-tips]") || bar;
      if (records.some((record) => chart.contains(record.target))) place(bar);
    });
    function show(bar: Element | null) {
      if (bar === shown.current) return;
      shown.current?.removeAttribute("data-hover");
      shown.current = bar;
      if (!bar) {
        watch.disconnect();
        return setTip(null);
      }
      bar.setAttribute("data-hover", "");
      place(bar);
      watch.observe(document.body, {
        subtree: true,
        childList: true,
        attributes: true,
        attributeFilter: ["data-tip", "style"],
      });
    }
    const bars = (chart: Element) => [...chart.querySelectorAll("[data-tip]")];
    const barAt = (chart: Element, x: number) => {
      let best: Element | null = null;
      let gap = Infinity;
      for (const bar of bars(chart)) {
        const r = bar.getBoundingClientRect();
        const d = x < r.left ? r.left - x : x > r.right ? x - r.right : 0;
        if (d < gap) [best, gap] = [bar, d];
      }
      return best;
    };
    const move = (event: PointerEvent) => {
      const target = event.target as Element;
      const chart = target.closest?.("[data-tips]");
      if (!chart) return show(null);
      // Right on a bar, or still within the one shown: no need to measure them all.
      if (target.matches("[data-tip]")) return show(target);
      const current = shown.current;
      if (current && chart.contains(current)) {
        const r = current.getBoundingClientRect();
        if (event.clientX >= r.left && event.clientX <= r.right) return;
      }
      show(barAt(chart, event.clientX));
    };
    const focus = (event: FocusEvent) => {
      const target = event.target as Element;
      // A click focuses too: only the keyboard's focus shows the words.
      if (!target.matches?.(":focus-visible")) return show(null);
      if (target.matches("[data-tips]")) return show(bars(target).at(-1) || null);
      show(target.matches("[data-tips] [data-tip]") ? target : null);
    };
    const key = (event: KeyboardEvent) => {
      const chart = document.activeElement;
      // With a modifier, the keys stay the browser's (Alt+← goes back).
      if (!chart?.matches("[data-tips]") || event.altKey || event.ctrlKey || event.metaKey) return;
      if (event.key === "Escape") return show(null);
      const list = bars(chart);
      // From the bar shown, unless the pointer moved it to another chart.
      const at = shown.current && list.includes(shown.current) ? list.indexOf(shown.current) : list.length - 1;
      const to: Record<string, number> = {
        ArrowLeft: at - 1,
        ArrowRight: at + 1,
        Home: 0,
        End: list.length - 1,
      };
      if (!(event.key in to) || !list.length) return;
      event.preventDefault();
      show(list[Math.min(list.length - 1, Math.max(0, to[event.key]))]);
    };
    const scroll = () => {
      const bar = shown.current;
      const active = document.activeElement;
      // Following the keyboard, the words move with their bar (focusing a
      // chart scrolls it into view); under a still pointer, they go.
      if (bar && active?.matches(":focus-visible") && (active === bar || active.contains(bar))) place(bar);
      else show(null);
    };
    const hide = () => show(null);
    // A click may redraw the chart under a still pointer: its words would be stale.
    document.addEventListener("pointermove", move);
    document.addEventListener("pointerdown", hide);
    document.documentElement.addEventListener("pointerleave", hide);
    document.addEventListener("focusin", focus);
    document.addEventListener("focusout", hide);
    document.addEventListener("keydown", key);
    document.addEventListener("scroll", scroll, true);
    return () => {
      watch.disconnect();
      document.removeEventListener("pointermove", move);
      document.removeEventListener("pointerdown", hide);
      document.documentElement.removeEventListener("pointerleave", hide);
      document.removeEventListener("focusin", focus);
      document.removeEventListener("focusout", hide);
      document.removeEventListener("keydown", key);
      document.removeEventListener("scroll", scroll, true);
    };
  }, []);

  if (!tip) return null;
  // Kept inside the window at both ends: half its widest, plus a margin.
  const zoom = parseFloat(getComputedStyle(document.documentElement).zoom) || 1;
  const width = window.innerWidth / zoom;
  const left = Math.min(Math.max(tip.x, 138), width - 138);
  return createPortal(
    <div
      className="chart-tip"
      aria-hidden="true"
      data-below={tip.below || undefined}
      style={{ left, top: tip.y }}
    >
      {tip.text}
    </div>,
    tip.host,
  );
}
