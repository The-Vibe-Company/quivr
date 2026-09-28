import type { RefObject } from "react";
import { focusField } from "./SchemaForm";
import type { Failure } from "./useSubmit";

/** Focusable failure summary linking each rejected field. */
export function ErrorSummary({
  failure,
  summary,
  labels,
}: {
  failure: Failure | null;
  summary: RefObject<HTMLDivElement | null>;
  labels: (name: string) => string;
}) {
  if (!failure) return null;
  const fields = Object.entries(failure.fields);
  return (
    <div className="error-summary" role="alert" tabIndex={-1} ref={summary}>
      <p>{failure.message}</p>
      {fields.length > 0 && (
        <ul>
          {fields.map(([name, message]) => (
            <li key={name}>
              <button
                type="button"
                className="text-button"
                onClick={(event) => {
                  const form = event.currentTarget.closest("form");
                  if (form) focusField(form, name);
                }}
              >
                {labels(name)}
              </button>
              {message !== failure.message && <span> — {message}</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** Human label of a form name, from the rendered label or legend. */
export function labelFor(name: string): string {
  const el = document.getElementsByName(name)[0];
  if (el instanceof HTMLFieldSetElement)
    return el.querySelector("legend")?.textContent || name;
  if (el?.id) {
    const text = document.querySelector(`label[for="${el.id}"]`)?.textContent;
    if (text) return text.replace(/\s*\*\s*$/, "").replace(/ · JSON$/, "");
  }
  return el?.closest("label")?.textContent || name;
}
