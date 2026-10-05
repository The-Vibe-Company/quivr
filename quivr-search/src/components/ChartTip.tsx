import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

/**
 * One tooltip for every small chart of the app. A chart marks itself with
 * `data-tips`, and each of its bars carries its words in `data-tip`. Over the
 * chart, the bar under the pointer shows its words: the whole height of its
 * column counts, so an empty day is as easy to read as the busiest one. A
 * bar that takes the keyboard focus shows its words too. In a modal dialog,
 * the words are drawn inside it: anything outside lies under the dialog.
 */
export function ChartTip() {
  const [tip, setTip] = useState<{ text: string; x: number; y: number; host: Element } | null>(null);
  const shown = useRef<Element | null>(null);

  useEffect(() => {
    const show = (bar: Element | null) => {
      if (bar === shown.current) return;
      shown.current?.removeAttribute("data-hover");
      shown.current = bar;
      if (!bar) return setTip(null);
      bar.setAttribute("data-hover", "");
      // The page is drawn zoomed on desktop: rectangles come zoomed, fixed
      // positions are zoomed again.
      const zoom = parseFloat(getComputedStyle(document.documentElement).zoom) || 1;
      const r = bar.getBoundingClientRect();
      setTip({
        text: bar.getAttribute("data-tip") || "",
        x: (r.left + r.width / 2) / zoom,
        y: r.top / zoom,
        host: bar.closest("dialog[open]") || document.body,
      });
    };
    const barAt = (chart: Element, x: number) => {
      let best: Element | null = null;
      let gap = Infinity;
      for (const bar of chart.querySelectorAll("[data-tip]")) {
        const r = bar.getBoundingClientRect();
        const d = x < r.left ? r.left - x : x > r.right ? x - r.right : 0;
        if (d < gap) [best, gap] = [bar, d];
      }
      return best;
    };
    const move = (event: PointerEvent) => {
      const chart = (event.target as Element).closest?.("[data-tips]");
      show(chart ? barAt(chart, event.clientX) : null);
    };
    const focus = (event: FocusEvent) => {
      const target = event.target as Element;
      // A click focuses its bar too: only the keyboard's focus shows the words.
      show(target.matches?.("[data-tips] [data-tip]:focus-visible") ? target : null);
    };
    const hide = () => show(null);
    // A click may redraw the chart under a still pointer: its words would be stale.
    document.addEventListener("pointermove", move);
    document.addEventListener("pointerdown", hide);
    document.documentElement.addEventListener("pointerleave", hide);
    document.addEventListener("focusin", focus);
    document.addEventListener("focusout", hide);
    document.addEventListener("scroll", hide, true);
    return () => {
      document.removeEventListener("pointermove", move);
      document.removeEventListener("pointerdown", hide);
      document.documentElement.removeEventListener("pointerleave", hide);
      document.removeEventListener("focusin", focus);
      document.removeEventListener("focusout", hide);
      document.removeEventListener("scroll", hide, true);
    };
  }, []);

  if (!tip) return null;
  // Kept inside the window at both ends: half its widest, plus a margin.
  const zoom = parseFloat(getComputedStyle(document.documentElement).zoom) || 1;
  const width = window.innerWidth / zoom;
  const left = Math.min(Math.max(tip.x, 138), width - 138);
  return createPortal(
    <div className="chart-tip" aria-hidden="true" style={{ left, top: tip.y }}>
      {tip.text}
    </div>,
    tip.host,
  );
}
