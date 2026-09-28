import { useRef, useState, type FormEvent } from "react";
import { APIError } from "../../lib/search";
import { connectorMessage } from "../../lib/connectors";
import { fieldForPointer, type FieldErrors } from "./SchemaForm";

export type Failure = { message: string; fields: FieldErrors };

/**
 * Runs a connector form submission: native constraint checks first, then the
 * request; a 422 is mapped onto the field its JSON Pointer designates. The
 * summary element receives focus on failure so keyboard and screen-reader
 * users land on the explanation.
 */
export function useSubmit(minInterval: number) {
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure | null>(null);
  const summary = useRef<HTMLDivElement>(null);
  const busyRef = useRef(false);

  function fail(next: Failure) {
    setFailure(next);
    requestAnimationFrame(() => summary.current?.focus());
  }

  async function run(
    event: FormEvent<HTMLFormElement>,
    build: (
      form: HTMLFormElement,
      errors: FieldErrors,
    ) => (() => Promise<void>) | null,
    options: {
      rebase?: (path: string) => string;
      after?: (form: HTMLFormElement) => void;
    } = {},
  ) {
    event.preventDefault();
    if (busyRef.current) return;
    const form = event.currentTarget;
    const errors: FieldErrors = {};
    for (const el of Array.from(form.elements))
      if (
        (el instanceof HTMLInputElement ||
          el instanceof HTMLTextAreaElement ||
          el instanceof HTMLSelectElement) &&
        el.name &&
        !el.checkValidity()
      )
        errors[el.name] = el.validationMessage;
    const send = build(form, errors);
    if (Object.keys(errors).length || !send) {
      fail({ message: "Corrigez les champs signalés.", fields: errors });
      return;
    }
    busyRef.current = true;
    setBusy(true);
    setFailure(null);
    try {
      await send();
    } catch (error) {
      const message = connectorMessage(error, minInterval);
      const fields: FieldErrors = {};
      if (error instanceof APIError) {
        const name = fieldForPointer(form, error.field, options.rebase);
        if (name) fields[name] = message;
      }
      fail({ message, fields });
    } finally {
      options.after?.(form);
      busyRef.current = false;
      setBusy(false);
    }
  }

  return { busy, failure, summary, run };
}
