import { Fragment, useMemo } from "react";

const ACCENT_CLASSES: Record<string, string> = {
  a: "aàâä",
  c: "cç",
  e: "eéèêë",
  i: "iîï",
  o: "oôö",
  u: "uùûü",
  y: "yÿ",
  n: "nñ",
};

/**
 * Construit une expression insensible aux accents : les termes de la requête
 * sont normalisés, le texte affiché ne l’est pas.
 */
function buildPattern(terms: string[]): RegExp | null {
  const parts = terms
    .filter((term) => term.length > 1)
    .map((term) =>
      term
        .split("")
        .map((char) =>
          ACCENT_CLASSES[char]
            ? `[${ACCENT_CLASSES[char]}]`
            : char.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"),
        )
        .join(""),
    );
  if (parts.length === 0) return null;
  return new RegExp(
    `(?<![\\p{L}\\p{N}])(?:${parts.join("|")})[\\p{L}\\p{N}]*`,
    "giu",
  );
}

export function Highlight({ text, terms }: { text: string; terms: string[] }) {
  const pattern = useMemo(() => buildPattern(terms), [terms]);

  if (!pattern) return <>{text}</>;

  const nodes: Array<string | { match: string }> = [];
  let cursor = 0;
  for (const match of text.matchAll(pattern)) {
    const start = match.index ?? 0;
    if (start > cursor) nodes.push(text.slice(cursor, start));
    nodes.push({ match: match[0] });
    cursor = start + match[0].length;
  }
  if (cursor < text.length) nodes.push(text.slice(cursor));

  return (
    <>
      {nodes.map((node, index) =>
        typeof node === "string" ? (
          <Fragment key={index}>{node}</Fragment>
        ) : (
          <mark key={index}>{node.match}</mark>
        ),
      )}
    </>
  );
}
