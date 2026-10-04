import {
  CheckCircle,
  CircleDashed,
  Clock,
  MinusCircle,
  Moon,
  PauseCircle,
  WarningCircle,
} from "@phosphor-icons/react";
import type { Connector, HealthState } from "../../lib/connectors";

// Besides the core's Connector Health states, the Sources list shows
// "paused", "failing" (the latest run failed after the latest success) and
// "starting" (no run finished yet); see displayState.
export type DisplayState = HealthState | "paused" | "failing" | "starting";

const STATES: Record<
  DisplayState,
  { label: string; Icon: typeof CheckCircle; tone: string }
> = {
  active: { label: "Actif", Icon: CheckCircle, tone: "ok" },
  silent: { label: "Silencieux", Icon: Moon, tone: "quiet" },
  access_error: { label: "Accès refusé", Icon: WarningCircle, tone: "error" },
  credential_expiring: {
    label: "Identifiant bientôt expiré",
    Icon: Clock,
    tone: "warn",
  },
  disabled: { label: "Désactivé", Icon: MinusCircle, tone: "quiet" },
  paused: { label: "En pause", Icon: PauseCircle, tone: "quiet" },
  failing: { label: "En échec", Icon: WarningCircle, tone: "error" },
  starting: { label: "Premier relevé…", Icon: CircleDashed, tone: "quiet" },
};

export const healthLabel = (state: string) =>
  STATES[state as DisplayState]?.label || state;

/** How a source reads in the list: paused, failing or starting first. */
export function displayState(c: Connector): DisplayState {
  if (!c.enabled) return "paused";
  const { state, last_error, last_success_at } = c.health;
  if (state === "access_error" || state === "credential_expiring")
    return state;
  if (
    last_error &&
    (!last_success_at || Date.parse(last_error.at) > Date.parse(last_success_at))
  )
    return "failing";
  if (!last_success_at) return "starting";
  return state;
}

/** Health state as icon plus text, never colour alone. */
export function HealthBadge({ state }: { state: string }) {
  const s = STATES[state as DisplayState] || {
    label: state,
    Icon: WarningCircle,
    tone: "quiet",
  };
  return (
    <span className="health-badge" data-tone={s.tone} data-state={state}>
      <s.Icon size={14} weight="bold" aria-hidden="true" />
      {s.label}
    </span>
  );
}
