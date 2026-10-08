import assert from "node:assert/strict";
import test from "node:test";
import { diffWords, summarize } from "../src/lib/diff.ts";
import { changeSummary } from "../src/lib/explore.ts";

const render = (before, after) =>
  diffWords(before, after)
    .map((c) => (c.kind === "same" ? c.text : c.kind === "removed" ? `[-${c.text}]` : `{+${c.text}}`))
    .join("");

test("un mot changé au milieu d’un texte est seul marqué, et chaque version se relit à l’identique", () => {
  assert.equal(render("Le délai est de six mois.", "Le délai est de huit mois."), "Le délai est de [-six ]{+huit }mois.");
  const changes = diffWords("un deux trois quatre", "un trois cinq quatre");
  assert.equal(
    changes
      .filter((c) => c.kind !== "removed")
      .map((c) => c.text)
      .join(""),
    "un trois cinq quatre",
  );
  assert.equal(
    changes
      .filter((c) => c.kind !== "added")
      .map((c) => c.text)
      .join(""),
    "un deux trois quatre",
  );
  assert.equal(summarize(changes), "1 mot retiré, 1 mot ajouté.");
  assert.equal(summarize(diffWords("même texte", "même  texte")), "Le texte est identique.");
});

// A Version with a title and paragraphs, as the engine returns its Parts.
const version = (title, ...paragraphs) => ({
  manifest: {
    parts: [
      { key: "title", role: "title", content: { kind: "text", text: title } },
      ...paragraphs.map((text, i) => ({ key: `p${i}`, role: "body", content: { kind: "text", text } })),
    ],
  },
});

test("la ligne d’une version dit ce qui a changé depuis la précédente : titre, paragraphes ou mots", () => {
  const before = version("Le port reste fermé", "Le trafic est arrêté.", "Reprise attendue lundi.");
  assert.equal(
    changeSummary(before, version("Le port rouvre", "Le trafic est arrêté.", "Reprise attendue lundi.", "Bilan.", "Suite.")),
    "titre corrigé · +2 §",
  );
  assert.equal(changeSummary(before, version("Le port reste fermé", "Le trafic est arrêté.")), "−1 §");
  assert.equal(
    changeSummary(before, version("Le port reste fermé", "Le trafic a repris.", "Reprise attendue lundi.")),
    "texte retouché (2 mots)",
  );
  assert.equal(changeSummary(before, before), "sans changement de texte");
});
