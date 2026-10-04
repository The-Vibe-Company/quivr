import { useEffect, useRef, useState, type ReactNode } from "react";
import { DotsIcon } from "./RailIcons";

/**
 * A "…" button and its small menu of actions (an alert's sheet, a source's
 * card). The arrows, Home and End move between its items; Escape or a click
 * outside closes it, Escape giving the focus back to the button.
 */
export function MoreMenu({
  name,
  className = "",
  children,
}: {
  /** What the actions are about, for the button's label. */
  name: string;
  className?: string;
  children: (close: () => void) => ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const outside = (event: MouseEvent) => {
      if (!box.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", outside);
    box.current?.querySelector<HTMLElement>(".menu-panel button")?.focus();
    return () => document.removeEventListener("mousedown", outside);
  }, [open]);
  return (
    <div
      className={`menu more-menu ${className}`}
      ref={box}
      onKeyDown={(event) => {
        if (!open) return;
        if (event.key === "Escape") {
          event.stopPropagation();
          setOpen(false);
          box.current?.querySelector<HTMLElement>(".icon-button")?.focus();
          return;
        }
        const items = [...(box.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') || [])];
        if (!items.length || !["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
        event.preventDefault();
        const at = items.indexOf(document.activeElement as HTMLElement);
        const next =
          event.key === "Home"
            ? 0
            : event.key === "End"
              ? items.length - 1
              : (at + (event.key === "ArrowDown" ? 1 : items.length - 1)) % items.length;
        items[next].focus();
      }}
    >
      <button
        type="button"
        className="icon-button"
        aria-expanded={open}
        aria-label={`Plus d’actions pour ${name}`}
        data-tip="Plus d’actions"
        onClick={() => setOpen(!open)}
      >
        <DotsIcon />
      </button>
      {open && (
        <div className="menu-panel" role="menu" aria-label={`Actions de ${name}`}>
          {children(() => setOpen(false))}
        </div>
      )}
    </div>
  );
}
