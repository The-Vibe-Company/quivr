import { test, expect } from "@playwright/test";
import { parse, print } from "../src/lib/notation";
import { EMPTY_FORM, build, fieldTerms, unbuild } from "../src/lib/queryBuilder";
import { sentence } from "../src/lib/explain";

// The guided form of a keyword alert writes the plugin's notation, and reads
// back any query of its shape, so the form and the advanced query stay in sync.

test("chaque champ du formulaire guidé écrit sa part de la requête", () => {
  const form = {
    all: "orage, Orage  tempête",
    phrase: ' "marché aux fleurs" ',
    any: 'grêle "coup de vent',
    none: "football",
    sources: ["Météo locale"],
  };
  expect(print(build(form)!.match)).toBe(
    'orage AND tempête AND "marché aux fleurs" AND (grêle OR "coup de vent") AND source:"Météo locale" AND NOT football',
  );
  expect(fieldTerms(' ,; "" ')).toEqual([]);
  // Quotes as phones and French keyboards type them keep a phrase too.
  expect(fieldTerms("« coup de vent » “marché aux fleurs” orage")).toEqual(["coup de vent", "marché aux fleurs", "orage"]);
  // Only words to avoid would catch everything else: not an alert yet.
  expect(build({ ...EMPTY_FORM, none: "football" })).toBeNull();
});

test("une requête de la forme du formulaire y revient, les autres restent des requêtes avancées", () => {
  const fits: [string, Partial<typeof EMPTY_FORM>][] = [
    ["orage NOT football grêle", { all: "orage grêle", none: "football" }],
    ['élection "second tour" "tour de France"', { all: 'élection "tour de France"', phrase: "second tour" }],
    ["orage OR grêle OR vent", { any: "orage grêle vent" }],
    ["(port OR quai) (source:a OR source:b)", { any: "port quai", sources: ["a", "b"] }],
  ];
  for (const [query, expected] of fits)
    expect(unbuild(parse(query).match), query).toEqual({ ...EMPTY_FORM, ...expected });
  for (const query of [
    "(port OR quai) AND grève AND NOT (football OR rugby)",
    "(a OR b) AND (c OR d)",
    "(a b) OR c",
    "NOT football",
    "orage orage",
    // Two bare source filters ask for both at once, not for any of them.
    "orage source:a source:b",
    "auteur:x orage",
  ])
    expect(unbuild(parse(query).match), query).toBeNull();
});

test("la phrase dit en français ce que l’alerte attrapera", () => {
  const cases: [string, string][] = [
    [
      'élection présidentielle "second tour" NOT football NOT rugby',
      "Articles qui contiennent « élection », « présidentielle » et la phrase « second tour », sauf ceux qui parlent de « football » ou de « rugby ».",
    ],
    ["orage OR grêle", "Articles qui contiennent « orage » ou « grêle »."],
    [
      'tempête AND (grêle OR vent) AND source:"Météo locale"',
      "Articles qui contiennent « tempête » et au moins un des mots « grêle » ou « vent », venus de « Météo locale ».",
    ],
    [
      "(port AND grève) OR aéroport NOT (football OR rugby)",
      "Articles qui contiennent soit (à la fois « port » et « grève »), soit (« aéroport » mais sans « football » ni « rugby »).",
    ],
    // A group is never read as going on into what follows it.
    [
      "(orage OR (grêle vent)) tempête",
      "Articles qui contiennent « tempête » et soit « orage », soit (à la fois « grêle » et « vent »).",
    ],
    [
      "(orage OR grêle) (port OR quai)",
      "Articles qui contiennent (au moins un des mots « orage » ou « grêle ») et au moins un des mots « port » ou « quai ».",
    ],
    ["NOT NOT orage", "Articles qui contiennent « orage »."],
    // Two source filters ask for both: only the first reads "venus de".
    [
      "orage source:a (source:b OR source:c)",
      "Articles qui contiennent « orage » et soit la source « b », soit la source « c », venus de « a ».",
    ],
    [
      "tempête NOT (port quai) NOT NOT NOT grêle",
      "Articles qui contiennent « tempête », sauf ceux qui parlent de (à la fois « port » et « quai ») ou de « grêle ».",
    ],
  ];
  for (const [query, expected] of cases) expect(sentence(parse(query).match), query).toBe(expected);
});
