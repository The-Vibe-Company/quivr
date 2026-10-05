import type { ReactNode } from "react";
import type { BuilderForm } from "../../lib/queryBuilder";

type Field = Exclude<keyof BuilderForm, "sources">;

// [field, label, what to type, example]
const ROWS: [Field, string, string, string][] = [
  ["all", "Tous ces mots", "Chaque mot doit apparaître.", "orage tempête"],
  ["phrase", "Cette phrase exacte", "Ces mots, côte à côte et dans cet ordre.", "marché aux fleurs"],
  ["any", "Au moins un de ces mots", "Un seul suffit.", "grêle vent"],
  ["none", "Aucun de ces mots", "Écarte les articles qui en parlent.", "football publicité"],
];

/**
 * The guided form of a keyword alert, like a search engine's advanced search:
 * one labelled field per part of the query, and the sources to watch. Each
 * field is typed freely; spaces separate words, "quotes" keep a phrase.
 */
export function QueryBuilder({
  id,
  form,
  onChange,
  sourcesMenu,
}: {
  id: string;
  form: BuilderForm;
  onChange: (form: BuilderForm) => void;
  sourcesMenu: ReactNode;
}) {
  return (
    <fieldset className="query-builder">
      <legend>Trouver les articles qui contiennent…</legend>
      {ROWS.map(([field, label, hint, example]) => (
        <div className="qb-row" key={field} data-field={field}>
          <label htmlFor={`${id}-${field}`}>{label}</label>
          <input
            id={`${id}-${field}`}
            className="form-input"
            value={form[field]}
            autoComplete="off"
            placeholder={example}
            aria-describedby={`${id}-${field}-hint`}
            onChange={(event) => onChange({ ...form, [field]: event.target.value })}
          />
          <p id={`${id}-${field}-hint`} className="qb-hint">
            {hint}
          </p>
        </div>
      ))}
      <div className="qb-row qb-sources">
        <span className="qb-label">Dans ces sources</span>
        {sourcesMenu}
      </div>
    </fieldset>
  );
}
