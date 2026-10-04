import { Info } from "@phosphor-icons/react";

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
