import {
  CheckCircle,
  Clock,
  MinusCircle,
  Moon,
  WarningCircle,
} from "@phosphor-icons/react";
import type { HealthState } from "../../lib/connectors";

const STATES: Record<
  HealthState,
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
};

export const healthLabel = (state: string) =>
  STATES[state as HealthState]?.label || state;

/** Health state as icon plus text, never colour alone. */
export function HealthBadge({ state }: { state: string }) {
  const s = STATES[state as HealthState] || {
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
