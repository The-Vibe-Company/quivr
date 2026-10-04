// Renders form fields from a connector kind's JSON Schema, so any kind the
// core publishes (built in today, plugin-provided later) gets a form without a
// UI change. JSON Schema patterns use another regex dialect than HTML, so
// pattern checks stay with the server, whose 422 names the field. Inputs are uncontrolled: values, secrets included, are read from
// the DOM once at submit time and never kept in React state or storage.
import { useState } from "react";
import type { JSONSchema } from "../../lib/connectors";

export type FieldErrors = Record<string, string>;

type Shape =
  | "object"
  | "variants"
  | "string"
  | "text"
  | "number"
  | "boolean"
  | "enum"
  | "list"
  | "json";

const typeOf = (s: JSONSchema) =>
  Array.isArray(s.type) ? s.type.find((t) => t !== "null") : s.type;

function shape(s: JSONSchema): Shape {
  if (Array.isArray(s.enum) && s.enum.length > 0) return "enum";
  const type = typeOf(s);
  if (type === "string") return (s.maxLength ?? 0) > 4096 ? "text" : "string";
  if (type === "integer" || type === "number") return "number";
  if (type === "boolean") return "boolean";
  if (type === "array" && s.items && typeOf(s.items) === "string")
    return "list";
  if (s.oneOf?.length && s.oneOf.every((b) => b.properties) && !s.properties)
    return "variants";
  if ((type === "object" || type === undefined) && s.properties)
    return "object";
  return "json";
}

export const fieldId = (name: string) =>
  "field-" + name.replace(/[^\w-]/g, "-");

const label = (name: string, s: JSONSchema) =>
  s.title || name.split("/").pop() || name;

function example(s: JSONSchema): string | undefined {
  const value = s.examples?.[0];
  if (value === undefined) return undefined;
  return typeof value === "string" ? value : JSON.stringify(value);
}

function Help({
  id,
  text,
  error,
}: {
  id: string;
  text?: string;
  error?: string;
}) {
  return (
    <>
      {text && (
        <p className="schema-help" id={`${id}-help`}>
          {text}
        </p>
      )}
      {error && (
        <p className="error-text" id={`${id}-error`}>
          {error}
        </p>
      )}
    </>
  );
}

function describedBy(id: string, s: JSONSchema, error?: string) {
  return (
    [s.description && `${id}-help`, error && `${id}-error`]
      .filter(Boolean)
      .join(" ") || undefined
  );
}

export function SchemaFields({
  schema,
  name,
  errors,
  required = false,
  secret = false,
  legend,
  describe = true,
}: {
  schema: JSONSchema;
  name: string;
  errors: FieldErrors;
  required?: boolean;
  secret?: boolean;
  legend?: string;
  /** false when the caller already shows the schema's description. */
  describe?: boolean;
}) {
  const [variant, setVariant] = useState(0);
  const id = fieldId(name);
  const error = errors[name];
  const kind = shape(schema);
  if (kind === "object" || kind === "variants") {
    const branches = schema.oneOf || [];
    const alternatives =
      kind === "object" && branches.length > 1
        ? branches.map((b, i) => b.title || `option ${i + 1}`).join(", ou ")
        : "";
    const body =
      kind === "variants"
        ? branches[Math.min(variant, branches.length - 1)]
        : schema;
    return (
      <fieldset
        className="schema-group"
        name={name}
        aria-describedby={error ? `${id}-error` : undefined}
        data-invalid={error ? "true" : undefined}
      >
        {(legend || schema.title) && <legend>{legend || schema.title}</legend>}
        <Help
          id={id}
          text={
            [
              describe && schema.description,
              alternatives && `Renseignez : ${alternatives}.`,
            ]
              .filter(Boolean)
              .join(" ") || undefined
          }
          error={error}
        />
        {kind === "variants" && (
          <div
            className="variant-choice"
            role="radiogroup"
            aria-label={`${legend || schema.title || "Format"} : format`}
          >
            {branches.map((b, i) => (
              <label key={i} className="choice">
                <input
                  type="radio"
                  name={`${name}#variant`}
                  value={i}
                  checked={variant === i}
                  onChange={() => setVariant(i)}
                />
                {b.title || `Option ${i + 1}`}
              </label>
            ))}
          </div>
        )}
        {Object.entries(body.properties || {}).map(([key, child]) => (
          <SchemaFields
            key={`${variant}:${key}`}
            schema={child}
            name={`${name}/${key}`}
            errors={errors}
            required={body.required?.includes(key)}
            secret={secret}
          />
        ))}
      </fieldset>
    );
  }
  const text = label(name, schema);
  const common = {
    id,
    name,
    "aria-invalid": error ? true : undefined,
    "aria-describedby": describedBy(id, schema, error),
  } as const;
  if (kind === "boolean")
    return (
      <div className="schema-field">
        <label className="choice">
          <input type="checkbox" {...common} />
          {text}
        </label>
        <Help id={id} text={schema.description} error={error} />
      </div>
    );
  const mark = required ? (
    <span className="required-mark" aria-hidden="true">
      {" "}
      *
    </span>
  ) : null;
  let control;
  if (kind === "enum")
    control = (
      <select {...common} required={required} defaultValue="">
        <option value="">—</option>
        {schema.enum!.map((v) => (
          <option key={String(v)} value={String(v)}>
            {String(v)}
          </option>
        ))}
      </select>
    );
  else if (kind === "number")
    control = (
      <input
        {...common}
        type="number"
        inputMode="numeric"
        required={required}
        min={schema.minimum}
        max={schema.maximum}
        step={typeOf(schema) === "integer" ? 1 : "any"}
        placeholder={example(schema)}
      />
    );
  else if (kind === "string" && !(secret && schema.writeOnly))
    control = (
      <input
        {...common}
        type="text"
        required={required}
        minLength={schema.minLength}
        maxLength={schema.maxLength}
        placeholder={example(schema)}
        autoComplete="off"
        spellCheck={false}
      />
    );
  else if (kind === "string")
    control = (
      <input
        {...common}
        type="password"
        required={required}
        minLength={schema.minLength}
        maxLength={schema.maxLength}
        autoComplete="off"
        data-1p-ignore
        data-lpignore="true"
        spellCheck={false}
      />
    );
  else
    control = (
      <textarea
        {...common}
        className="schema-textarea"
        required={required}
        rows={kind === "json" ? 6 : 4}
        spellCheck={false}
        autoComplete="off"
        placeholder={
          kind === "list"
            ? "Une valeur par ligne"
            : kind === "json"
              ? example(schema) || "Valeur JSON"
              : undefined
        }
      />
    );
  return (
    <div className="schema-field">
      <label className="field-label" htmlFor={id}>
        {text}
        {mark}
        {kind === "json" && <span className="field-format"> · JSON</span>}
      </label>
      {control}
      <Help id={id} text={schema.description} error={error} />
    </div>
  );
}

type Form = HTMLFormElement;

function element(form: Form, name: string) {
  return form.elements.namedItem(name) as
    | HTMLInputElement
    | HTMLTextAreaElement
    | HTMLSelectElement
    | RadioNodeList
    | null;
}

/** Reads the value described by schema at name; errors collects local parse failures. */
export function collect(
  form: Form,
  schema: JSONSchema,
  name: string,
  errors: FieldErrors,
): unknown {
  const kind = shape(schema);
  if (kind === "object") {
    const out: Record<string, unknown> = {};
    for (const [key, child] of Object.entries(schema.properties || {})) {
      const value = collect(form, child, `${name}/${key}`, errors);
      if (value !== undefined) out[key] = value;
    }
    return out;
  }
  if (kind === "variants") {
    const choice = element(form, `${name}#variant`) as RadioNodeList | null;
    const index = Number(choice?.value || 0);
    return collect(
      form,
      { ...schema.oneOf![index], type: "object" },
      name,
      errors,
    );
  }
  const el = element(form, name);
  if (!el || el instanceof RadioNodeList) return undefined;
  if (kind === "boolean") return (el as HTMLInputElement).checked || undefined;
  const raw = el.value;
  if (kind === "string" || kind === "text") return raw === "" ? undefined : raw;
  if (raw.trim() === "") return undefined;
  if (kind === "number") {
    const n = Number(raw);
    if (Number.isNaN(n)) errors[name] = "Un nombre est attendu.";
    return n;
  }
  if (kind === "enum") return schema.enum!.find((v) => String(v) === raw);
  if (kind === "list")
    return raw
      .split("\n")
      .map((line) => line.trim())
      .filter(Boolean);
  try {
    return JSON.parse(raw);
  } catch {
    errors[name] = "Ce contenu n’est pas un JSON valide.";
    return undefined;
  }
}

/** Empties every input under name: secrets never outlive a submit attempt. */
export function clearFields(form: Form, name: string) {
  for (const el of Array.from(form.elements))
    if (
      (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) &&
      (el.name === name || el.name.startsWith(name + "/")) &&
      el.type !== "radio" &&
      el.type !== "checkbox"
    )
      el.value = "";
}

/**
 * Maps a JSON Pointer from a 422 onto the closest named field of the form.
 * rebase rewrites the request location into form names (e.g. /secret → credential/secret).
 */
export function fieldForPointer(
  form: Form,
  pointer: string,
  rebase: (path: string) => string = (p) => p,
): string | null {
  if (!pointer) return null;
  const parts = rebase(pointer.replace(/^\//, ""))
    .split("/")
    .map((p) => p.replace(/~1/g, "/").replace(/~0/g, "~"));
  for (let n = parts.length; n > 0; n--) {
    const name = parts.slice(0, n).join("/");
    if (form.elements.namedItem(name)) return name;
  }
  return null;
}

/** Moves focus to a field, or to the first control of a group. */
export function focusField(form: Form, name: string) {
  const el = form.elements.namedItem(name);
  const target =
    el instanceof HTMLFieldSetElement
      ? (el.querySelector("input,select,textarea") as HTMLElement | null)
      : el instanceof RadioNodeList
        ? (el[0] as HTMLElement)
        : (el as HTMLElement | null);
  target?.focus();
}
