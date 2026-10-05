import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { CheckIcon, ChevronDownIcon } from "../RailIcons";

/**
 * A filter of the feed's top bar: a button that names what is picked, and a
 * panel of options under it. Escape or a click outside closes the panel.
 */
export function FilterMenu({
  title,
  summary,
  placeholder,
  icon,
  onOpen,
  children,
}: {
  title: string;
  /** What is picked, shown in place of the title; none when nothing is. */
  summary?: string;
  /** What the button says when nothing is picked, if not the title. */
  placeholder?: string;
  icon?: ReactNode;
  /** Called each time the panel opens. */
  onOpen?: () => void;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const id = useId();
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
      className="menu"
      ref={box}
      onKeyDown={(event) => {
        if (event.key !== "Escape" || !open) return;
        event.preventDefault();
        event.stopPropagation();
        setOpen(false);
        box.current?.querySelector<HTMLElement>(".menu-button")?.focus();
      }}
    >
      <button
        type="button"
        className="menu-button"
        data-active={summary ? true : undefined}
        aria-expanded={open}
        aria-controls={id}
        aria-label={summary || placeholder ? `${title} : ${summary || placeholder}` : title}
        title={summary || placeholder || title}
        onClick={() => {
          if (!open) onOpen?.();
          setOpen(!open);
        }}
      >
        {icon && <span className="menu-icon">{icon}</span>}
        <span className="menu-summary">{summary || placeholder || title}</span>
        <span className="menu-chevron">
          <ChevronDownIcon />
        </span>
      </button>
      {open && (
        <div id={id} className="menu-panel" role="dialog" aria-label={title}>
          {children}
        </div>
      )}
    </div>
  );
}

/** One option of a filter menu: a box to tick, or a dot for a single pick. */
export function MenuOption({
  pressed,
  onClick,
  count,
  lead,
  single = false,
  children,
}: {
  pressed: boolean;
  onClick: () => void;
  count?: number;
  lead?: ReactNode;
  single?: boolean;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      className="menu-option"
      aria-pressed={pressed}
      onClick={onClick}
    >
      <span className="menu-check" data-single={single || undefined} aria-hidden="true">
        {pressed && !single && <CheckIcon size={12} />}
      </span>
      {lead}
      <span className="menu-label">{children}</span>
      {count !== undefined && <span className="menu-count">{count}</span>}
    </button>
  );
}
