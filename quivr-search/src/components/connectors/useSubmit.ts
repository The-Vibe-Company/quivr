import { useEffect, useRef, useState, type FormEvent } from "react";
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

  // Focus once React has committed the summary. A frame callback scheduled at
  // failure time can run before that commit, when the summary does not exist
  // yet, and leave focus on the body. Each failure is a new object, so a
  // repeated refusal focuses the summary again.
  useEffect(() => {
    if (failure) summary.current?.focus();
  }, [failure]);

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
      setFailure({ message: "Corrigez les champs signalés.", fields: errors });
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
      setFailure({ message, fields });
    } finally {
      options.after?.(form);
      busyRef.current = false;
      setBusy(false);
    }
  }

  return { busy, failure, summary, run };
}
