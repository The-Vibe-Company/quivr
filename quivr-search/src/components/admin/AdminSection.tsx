// The frame every section of the Admin tab shares: a panel with its title,
// an optional aside (a window picker, a count), and the same loading,
// unavailable and error states. A section is one file under admin/ mounted
// with one line in AdminView's SECTIONS.
import type { ReactNode } from "react";
import { ArrowClockwise } from "@phosphor-icons/react";
import type { StatsStatus, StatsWindow } from "../../lib/adminStats";
import type { AdminStats } from "../../lib/admin";

export interface SectionProps {
  onUnauthorized: () => void;
  /** The header's live numbers, so a section's counts match them. */
  stats?: AdminStats | null;
}

export function AdminSection({
  id,
  title,
  aside,
  status = "ready",
  error,
  onRetry,
  children,
}: {
  /** Unique; names the heading (`${id}-title`) for assistive technology. */
  id: string;
  title: string;
  aside?: ReactNode;
  /** A stats hook's status: loading shows still shapes, never a spinner. */
  status?: StatsStatus;
  error?: string;
  onRetry?: () => void;
  children?: ReactNode;
}) {
  return (
    <section className="panel admin-section" aria-labelledby={`${id}-title`}>
      <div className="panel-head">
        <h2 id={`${id}-title`}>{title}</h2>
        {aside && <div className="admin-section-aside">{aside}</div>}
      </div>
      {status === "loading" ? (
        <div className="admin-section-loading" aria-busy="true">
          <p role="status" className="visually-hidden">
            Chargement…
          </p>
          <span aria-hidden="true" />
          <span aria-hidden="true" />
          <span aria-hidden="true" />
        </div>
      ) : status === "unavailable" ? (
        <p className="admin-section-note">
          Ces chiffres ne sont pas activés sur ce déploiement.
        </p>
      ) : status === "error" ? (
        <p className="admin-section-note" role="alert">
          {error || "Chiffres indisponibles."}{" "}
          {onRetry && (
            <button type="button" className="link-button" onClick={onRetry}>
              <ArrowClockwise size={13} aria-hidden="true" /> Réessayer
            </button>
          )}
        </p>
      ) : (
        <div className="admin-section-body">{children}</div>
      )}
    </section>
  );
}

const WINDOWS: { value: StatsWindow; label: string }[] = [
  { value: "1h", label: "1 h" },
  { value: "24h", label: "24 h" },
  { value: "7d", label: "7 j" },
];

/** Which window a section reads: 1 h, 24 h or 7 days, or only some of them. */
export function WindowPicker({
  value,
  onChange,
  label,
  options,
}: {
  value: StatsWindow;
  onChange: (value: StatsWindow) => void;
  /** What the choice applies to, for assistive technology. */
  label: string;
  options?: StatsWindow[];
}) {
  return (
    <div className="segmented admin-window" role="group" aria-label={label}>
      {WINDOWS.filter((w) => !options || options.includes(w.value)).map((w) => (
        <button
          key={w.value}
          type="button"
          aria-pressed={value === w.value}
          onClick={() => onChange(w.value)}
        >
          {w.label}
        </button>
      ))}
    </div>
  );
}
