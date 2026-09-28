import { formatInterval } from "../../lib/connectors";
import { fieldId } from "./SchemaForm";

/** Polling interval in seconds, bounded by the deployment floor and one day. */
export function IntervalField({
  name,
  defaultValue,
  min,
  error,
  hint,
  label = "Intervalle de collecte (secondes)",
}: {
  name: string;
  defaultValue: number;
  min: number;
  error?: string;
  hint?: string;
  label?: string;
}) {
  const id = fieldId(name);
  return (
    <div className="schema-field">
      <label className="field-label" htmlFor={id}>
        {label}
      </label>
      <input
        id={id}
        name={name}
        type="number"
        inputMode="numeric"
        required
        min={min}
        max={86400}
        step={1}
        defaultValue={defaultValue}
        aria-invalid={error ? true : undefined}
        aria-describedby={`${id}-help${error ? ` ${id}-error` : ""}`}
      />
      <p className="schema-help" id={`${id}-help`}>
        {hint ? `${hint} ` : ""}Entre {formatInterval(min)} et 24 h (86 400 s).
      </p>
      {error && (
        <p className="error-text" id={`${id}-error`}>
          {error}
        </p>
      )}
    </div>
  );
}

/** Optional credential expiry; drives the credential_expiring health state. */
export function ExpiryField({ name, error }: { name: string; error?: string }) {
  const id = fieldId(name);
  return (
    <div className="schema-field">
      <label className="field-label" htmlFor={id}>
        Expiration de l’identifiant (facultatif)
      </label>
      <input
        id={id}
        name={name}
        type="datetime-local"
        aria-invalid={error ? true : undefined}
        aria-describedby={`${id}-help`}
      />
      <p className="schema-help" id={`${id}-help`}>
        Le connecteur signale l’expiration à l’approche de cette date.
      </p>
      {error && <p className="error-text">{error}</p>}
    </div>
  );
}

export function readExpiry(form: HTMLFormElement, name: string) {
  const value = (form.elements.namedItem(name) as HTMLInputElement | null)
    ?.value;
  if (!value) return undefined;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
}
