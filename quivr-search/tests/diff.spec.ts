import { test, expect } from "@playwright/test";
import { diffWords, summarize } from "../src/lib/diff";

const render = (before: string, after: string) =>
  diffWords(before, after)
    .map((c) => (c.kind === "same" ? c.text : c.kind === "removed" ? `[-${c.text}]` : `{+${c.text}}`))
    .join("");

test("un mot changé au milieu d’un texte est seul marqué, et chaque version se relit à l’identique", () => {
  expect(render("Le délai est de six mois.", "Le délai est de huit mois.")).toBe(
    "Le délai est de [-six ]{+huit }mois.",
  );
  const changes = diffWords("un deux trois quatre", "un trois cinq quatre");
  expect(changes.filter((c) => c.kind !== "removed").map((c) => c.text).join("")).toBe(
    "un trois cinq quatre",
  );
  expect(changes.filter((c) => c.kind !== "added").map((c) => c.text).join("")).toBe(
    "un deux trois quatre",
  );
  expect(summarize(changes)).toBe("1 mot retiré, 1 mot ajouté.");
  expect(summarize(diffWords("même texte", "même  texte"))).toBe("Le texte est identique.");
});
