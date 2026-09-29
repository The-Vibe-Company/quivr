import { Info } from "@phosphor-icons/react";
import { DESCRIPTION } from "../../lib/alerts";

/**
 * What a described alert does with the articles: a classifier outside Quivr
 * reads them once they are enriched, so a catch can take a minute.
 */
export function DescribedNote({ id }: { id?: string }) {
  return (
    <p id={id} className="described-note">
      <Info size={16} aria-hidden="true" />
      <span>
        Jev, un classifieur de TypeSafe, lit le titre et le texte de chaque
        nouvel article et juge s’il correspond à la description, même formulé
        autrement ou dans une autre langue.{" "}
        <strong>Le texte des articles est envoyé à ce service externe.</strong>{" "}
        Un article peut mettre une minute à apparaître.
      </span>
    </p>
  );
}

/** Whether a description can be sent, and what to say when it cannot. */
export function describedState(text: string) {
  const value = text.trim();
  return value.length === 0
    ? "empty"
    : value.length < DESCRIPTION.min
      ? "short"
      : "valid";
}

export function DescribedError({ text }: { text: string }) {
  const state = describedState(text);
  if (state === "valid") return null;
  return (
    <p className="error-text" role="alert">
      {state === "empty"
        ? "Décrivez ce que l’alerte doit surveiller."
        : `Écrivez au moins ${DESCRIPTION.min} caractères.`}
    </p>
  );
}
