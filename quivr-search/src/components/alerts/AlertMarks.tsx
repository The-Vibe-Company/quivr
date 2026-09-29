import { BellRinging } from "@phosphor-icons/react";
import type { Alert } from "../../lib/alerts";
import "../../alerts.css";

/** The alerts that caught an article of the Veille feed, each opening its alert. */
export function AlertMarks({
  alerts,
  onAlert,
}: {
  alerts: Alert[];
  onAlert: (id: string) => void;
}) {
  return (
    <ul className="alert-marks" aria-label="Alertes déclenchées">
      {alerts.map((alert) => (
        <li key={alert.alert_id}>
          <a
            href={`?view=alerts&alert=${encodeURIComponent(alert.alert_id)}`}
            onClick={(event) => {
              if (!event.metaKey && !event.ctrlKey && !event.shiftKey) {
                event.preventDefault();
                onAlert(alert.alert_id);
              }
            }}
          >
            <BellRinging size={13} weight="fill" aria-hidden="true" />
            {alert.name}
          </a>
        </li>
      ))}
    </ul>
  );
}
