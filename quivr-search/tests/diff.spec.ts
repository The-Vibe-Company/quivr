import { test, expect } from "@playwright/test";
import { diffWords, summarize } from "../src/lib/diff";
import { changeSummary, type VersionDetail } from "../src/lib/explore";

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

// A Version with a title and paragraphs, as the engine returns its Parts.
const version = (title: string, ...paragraphs: string[]) =>
  ({
    manifest: {
      parts: [
        { key: "title", role: "title", content: { kind: "text", text: title } },
        ...paragraphs.map((text, i) => ({ key: `p${i}`, role: "body", content: { kind: "text", text } })),
      ],
    },
  }) as VersionDetail;

test("la ligne d’une version dit ce qui a changé depuis la précédente : titre, paragraphes ou mots", () => {
  const before = version("Le port reste fermé", "Le trafic est arrêté.", "Reprise attendue lundi.");
  expect(changeSummary(before, version("Le port rouvre", "Le trafic est arrêté.", "Reprise attendue lundi.", "Bilan.", "Suite."))).toBe(
    "titre corrigé · +2 §",
  );
  expect(changeSummary(before, version("Le port reste fermé", "Le trafic est arrêté."))).toBe("−1 §");
  expect(changeSummary(before, version("Le port reste fermé", "Le trafic a repris.", "Reprise attendue lundi."))).toBe(
    "texte retouché (2 mots)",
  );
  expect(changeSummary(before, before)).toBe("sans changement de texte");
});
