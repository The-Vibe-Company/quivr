// Shared building blocks of the demo's pages, so every tab has the same
// header, live indicator and loading, empty and error states.
import type { ReactNode } from "react";
import { ArrowClockwise, Info, WarningCircle } from "@phosphor-icons/react";

/** A page's title, one-line purpose and an optional aside (a live badge). */
export function PageHeader({
  title,
  description,
  aside,
}: {
  title: string;
  description: ReactNode;
  aside?: ReactNode;
}) {
  return (
    <div className="page-head">
      <div>
        <h1>{title}</h1>
        <p className="page-lede">{description}</p>
      </div>
      {aside}
    </div>
  );
}

/** Whether a page follows its data live; colour is never the only cue. */
export function LiveBadge({
  live,
  offLabel = "Actualisé régulièrement",
}: {
  live: boolean;
  offLabel?: string;
}) {
  return (
    <span className="live-badge" data-live={live}>
      <span className="live-dot" aria-hidden="true" />
      {live ? "En direct" : offLabel}
    </span>
  );
}

/** A skeleton list; the label is announced once, the shapes are not. */
export function LoadingState({
  label,
  rows = 3,
}: {
  label?: string;
  rows?: number;
}) {
  return (
    <div className="loading-state">
      {label && (
        <p role="status" className="visually-hidden">
          {label}
        </p>
      )}
      <div className="results-skeleton" aria-hidden="true">
        {Array.from({ length: rows }, (_, i) => (
          <div key={i}>
            <span />
            <span />
            <span />
          </div>
        ))}
      </div>
    </div>
  );
}

/** Nothing to show yet: what this place is for and how to fill it. */
export function EmptyState({
  icon,
  title,
  children,
  actions,
  className = "",
}: {
  icon?: ReactNode;
  title: string;
  children?: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  return (
    <section className={`empty ${className}`.trim()}>
      {icon && <div className="empty-icon">{icon}</div>}
      <h2>{title}</h2>
      {children && <p>{children}</p>}
      {actions && <div className="empty-actions">{actions}</div>}
    </section>
  );
}

/**
 * A page-level problem. "error" is announced as an alert and offers a retry;
 * "info" (a feature off on this deployment) is a polite status.
 */
export function Notice({
  tone = "error",
  title,
  children,
  onRetry,
  actions,
}: {
  tone?: "error" | "info";
  title: string;
  children?: ReactNode;
  onRetry?: () => void;
  actions?: ReactNode;
}) {
  const Icon = tone === "error" ? WarningCircle : Info;
  return (
    <div
      className="notice"
      data-tone={tone}
      role={tone === "error" ? "alert" : "status"}
    >
      <Icon size={22} className="notice-icon" aria-hidden="true" />
      <div className="notice-body">
        <h2>{title}</h2>
        {children && <p>{children}</p>}
        {(onRetry || actions) && (
          <div className="notice-actions">
            {onRetry && (
              <button className="button" onClick={onRetry}>
                <ArrowClockwise size={16} aria-hidden="true" /> Réessayer
              </button>
            )}
            {actions}
          </div>
        )}
      </div>
    </div>
  );
}
