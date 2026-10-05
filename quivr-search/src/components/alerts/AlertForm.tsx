import { useEffect, useId, useRef, useState } from "react";
import { APIError } from "../../lib/search";
import {
  DESCRIPTION,
  alertMessage,
  createAlert,
  editAlert,
  type Alert,
  type AlertExpression,
} from "../../lib/alerts";
import type { FeedItem } from "../../lib/feed";
import { print } from "../../lib/notation";
import { sourceName } from "../../lib/alertForm";
import { EMPTY_FORM, build, positiveTerms, unbuild } from "../../lib/queryBuilder";
import { FilterMenu, MenuOption } from "../feed/FilterMenu";
import { SourceLogo } from "../feed/SourceLogo";
import { DescribedIcon, KeywordsIcon, PlusIcon, SourcesIcon } from "../RailIcons";
import { QueryPreview, useParsed } from "./QueryPreview";
import { QueryBuilder } from "./QueryBuilder";
import { QueryEditor } from "./QueryEditor";
import { DescribedNote } from "./DescribedNote";
import { AlertPreview } from "./AlertPreview";

type Kind = "keywords" | "described";

const KINDS: [Kind, string, string][] = [
  ["keywords", "Des mots précis", "dès que ces mots apparaissent"],
  ["described", "Un sujet décrit", "Quivr comprend le sens"],
];

/**
 * Writes or edits an alert: keywords in a guided form like a search engine's
 * advanced search (all these words, this exact phrase, any of these words,
 * none of these words), its query and what it will catch shown live; or the
 * same query written by hand, the form taking it back while it fits; or a
 * described subject when the deployment has a classifier. Its name is the
 * title, filled from the words until it is written. Under it, what the alert
 * would have caught among the newest articles.
 */
export function AlertForm({
  described,
  editing,
  sources,
  logoOf,
  feedItems,
  onSaved,
  onCancel,
  onUnauthorized,
}: {
  described: boolean;
  editing: Alert | null;
  /** Source Namespaces an alert can watch. */
  sources: string[];
  logoOf: Map<string, string>;
  /** The feed's articles, to date the preview's articles. */
  feedItems: FeedItem[];
  onSaved: (alert: Alert, created: boolean) => void;
  onCancel: () => void;
  onUnauthorized: () => void;
}) {
  const id = useId();
  const [kind, setKind] = useState<Kind>("keywords");
  // The guided form's fields; its sources are `watched`, shared with described alerts.
  const [fields, setFields] = useState(EMPTY_FORM);
  const [watched, setWatched] = useState<string[]>([]);
  const [advanced, setAdvanced] = useState(false);
  const [query, setQuery] = useState("");
  const [description, setDescription] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // One key per alert being written, so a retried submit is not a second alert.
  const idempotency = useRef(crypto.randomUUID());
  const parsed = useParsed(query);
  // Switching between the guided form and the advanced query removes the
  // button pressed: focus goes to the first field of the side shown.
  const switched = useRef(false);
  useEffect(() => {
    if (!switched.current) return;
    switched.current = false;
    document.getElementById(advanced ? "alert-query" : `${id}-all`)?.focus();
  }, [advanced]);

  const reset = (next: Alert | null) => {
    setError("");
    idempotency.current = crypto.randomUUID();
    setName(next?.name || "");
    setDescription(next?.kind === "described" ? next.expression.description : "");
    setKind(next?.kind === "described" ? "described" : "keywords");
    const form = next?.kind === "keywords" ? unbuild(next.expression.match) : null;
    setFields(form || EMPTY_FORM);
    setWatched(next?.kind === "described" ? next.expression.sources || [] : form?.sources || []);
    const needsAdvanced = next?.kind === "keywords" && !form;
    setAdvanced(needsAdvanced);
    setQuery(needsAdvanced && next?.kind === "keywords" ? (print(next.expression.match) ?? "") : "");
  };
  useEffect(() => reset(editing), [editing]);

  const changed = () => {
    setError("");
    if (!editing) idempotency.current = crypto.randomUUID();
  };
  const built = build({ ...fields, sources: watched });
  const builtQuery = built ? (print(built.match) ?? "") : "";
  const isDescribed = kind === "described";
  const expression: AlertExpression | null = isDescribed
    ? description.trim().length >= DESCRIPTION.min
      ? { kind: "described", description: description.trim(), ...(watched.length ? { sources: watched } : {}) }
      : null
    : advanced
      ? parsed.state === "valid"
        ? parsed.expression
        : null
      : built;
  // The raw query goes back to the form only when the form shows it as it is.
  const fitting = advanced && parsed.state === "valid" ? unbuild(parsed.expression.match) : null;
  const terms = expression?.kind === "keywords" ? positiveTerms(expression.match) : [];
  const fallbackName = (isDescribed ? description : advanced ? query : terms.join(", ")).trim().slice(0, 80);

  async function submit() {
    if (busy) return;
    if (!expression) {
      setError(
        isDescribed
          ? `Décrivez le sujet en au moins ${DESCRIPTION.min} caractères.`
          : advanced
            ? parsed.state === "invalid"
              ? parsed.message
              : "Écrivez la requête à surveiller."
            : "Remplissez « Tous ces mots », « Cette phrase exacte » ou « Au moins un de ces mots ».",
      );
      return;
    }
    setBusy(true);
    setError("");
    try {
      const saved = editing
        ? await editAlert(editing.alert_id, expression, name.trim() || editing.name)
        : await createAlert(name.trim() || fallbackName, expression, idempotency.current);
      onSaved(saved, !editing);
      reset(null);
    } catch (e) {
      if (e instanceof APIError && e.status === 401) onUnauthorized();
      setError(alertMessage(e, expression.kind));
    } finally {
      setBusy(false);
    }
  }

  const sourcesMenu = (
    <FilterMenu
      title="Sources surveillées"
      icon={<SourcesIcon size={15} />}
      placeholder="Toutes les sources"
      summary={
        watched.length === 0
          ? undefined
          : watched.length === 1
            ? sourceName(watched[0])
            : `${watched.length} sources`
      }
    >
      <MenuOption
        single
        pressed={watched.length === 0}
        onClick={() => {
          setWatched([]);
          changed();
        }}
      >
        Toutes les sources
      </MenuOption>
      {[...new Set([...sources, ...watched])].map((ns) => (
        <MenuOption
          key={ns}
          pressed={watched.includes(ns)}
          lead={<SourceLogo namespace={ns} connectorId={logoOf.get(ns)} size="small" />}
          onClick={() => {
            setWatched(watched.includes(ns) ? watched.filter((w) => w !== ns) : [...watched, ns]);
            changed();
          }}
        >
          {sourceName(ns)}
        </MenuOption>
      ))}
    </FilterMenu>
  );

  return (
    <form
      className="alert-form"
      aria-labelledby={`${id}-title`}
      noValidate
      onSubmit={(event) => {
        event.preventDefault();
        void submit();
      }}
    >
      <div className="af-title">
        <span className="kind-tile" data-kind={kind} aria-hidden="true">
          {isDescribed ? <DescribedIcon size={18} /> : <KeywordsIcon size={18} />}
        </span>
        <div className="af-title-text">
          <h2 id={`${id}-title`} tabIndex={-1}>
            {editing ? "Modifier l’alerte" : "Nouvelle alerte"}
          </h2>
          <label className="visually-hidden" htmlFor={`${id}-name`}>
            Nom de l’alerte
          </label>
          <input
            id={`${id}-name`}
            className="af-name"
            value={name}
            maxLength={120}
            placeholder={fallbackName || "Sans nom"}
            title="Le nom de l’alerte ; par défaut, ce qu’elle surveille"
            onChange={(event) => {
              setName(event.target.value);
              changed();
            }}
          />
        </div>
      </div>
      {described ? (
        <div className="af-kinds" role="group" aria-label="Type d’alerte">
          {KINDS.map(([value, label, hint]) => (
            <button
              key={value}
              type="button"
              className="af-kind"
              aria-pressed={kind === value}
              disabled={!!editing && kind !== value}
              onClick={() => {
                setKind(value);
                changed();
              }}
            >
              <span className="kind-tile" aria-hidden="true">
                {value === "described" ? <DescribedIcon size={16} /> : <KeywordsIcon size={16} />}
              </span>
              <span>
                <b>{label}</b>
                <span className="af-kind-hint">{hint}</span>
              </span>
            </button>
          ))}
        </div>
      ) : (
        <p className="form-note alert-kind-off">
          Les alertes sur un sujet décrit en une phrase ne sont pas activées sur ce déploiement : elles
          demandent un classifieur externe.
        </p>
      )}
      {isDescribed ? (
        <div className="af-sentence">
          <p className="af-line">Prévenez-moi quand un article parle de</p>
          <label className="visually-hidden" htmlFor={`${id}-description`}>
            Décrivez le sujet en une phrase
          </label>
          <textarea
            id={`${id}-description`}
            className="af-describe"
            value={description}
            maxLength={DESCRIPTION.max}
            rows={2}
            placeholder="des grèves dans les ports"
            aria-describedby="alert-described-note"
            onChange={(event) => {
              setDescription(event.target.value);
              changed();
            }}
          />
          <DescribedNote id="alert-described-note" />
          <div className="af-line af-sources">dans {sourcesMenu}</div>
        </div>
      ) : advanced ? (
        <QueryEditor
          value={query}
          onChange={(next) => {
            setQuery(next);
            changed();
          }}
          parsed={parsed}
          footer={
            parsed.state === "empty" || fitting ? (
              <button
                type="button"
                className="link-button"
                onClick={() => {
                  setFields(fitting || EMPTY_FORM);
                  setWatched(fitting?.sources || []);
                  switched.current = true;
                  setAdvanced(false);
                  changed();
                }}
              >
                Revenir au formulaire guidé
              </button>
            ) : (
              <p className="form-note query-unfit">
                {parsed.state === "valid"
                  ? "Le formulaire guidé ne sait pas afficher cette requête : elle reste telle quelle ici."
                  : "Corrigez la requête pour pouvoir revenir au formulaire guidé."}
              </p>
            )
          }
        />
      ) : (
        <>
          <QueryBuilder
            id={id}
            form={fields}
            onChange={(next) => {
              setFields(next);
              changed();
            }}
            sourcesMenu={sourcesMenu}
          />
          <div className="query-built">
            <div className="query-built-head">
              <span className="query-built-label">Requête</span>
              <button
                type="button"
                className="link-button"
                onClick={() => {
                  setQuery(builtQuery);
                  switched.current = true;
                  setAdvanced(true);
                  changed();
                }}
              >
                Écrire une requête avancée
              </button>
            </div>
            {built && <code className="query-built-code">{builtQuery}</code>}
            <QueryPreview
              id={`${id}-sentence`}
              parsed={built ? { state: "valid", expression: built } : { state: "empty" }}
              empty={
                fields.none.trim()
                  ? "Ajoutez aussi des mots à chercher : « Aucun de ces mots » seul attraperait tout le reste."
                  : "La requête et ce qu’elle attrapera s’écrivent ici à mesure que vous remplissez les champs."
              }
            />
          </div>
        </>
      )}
      {!isDescribed && !advanced && !expression && (
        <p className="alert-preview alert-preview-title" data-empty="true">
          Remplissez un champ pour voir ce que l’alerte aurait repéré.
        </p>
      )}
      {(isDescribed || !advanced || expression) && (
        <AlertPreview
          expression={expression}
          onDemand={isDescribed}
          terms={terms}
          logoOf={logoOf}
          feedItems={feedItems}
          onUnauthorized={onUnauthorized}
        />
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="form-actions af-actions">
        <button className="button primary" data-ready={!!expression || undefined} disabled={busy}>
          {!editing && <PlusIcon size={16} />}
          {busy ? (editing ? "Enregistrement…" : "Création…") : editing ? "Enregistrer" : "Créer l’alerte"}
        </button>
        <button type="button" className="button quiet" onClick={onCancel}>
          Annuler
        </button>
      </div>
    </form>
  );
}
