import { readFileSync } from "node:fs";
import { test, expect } from "@playwright/test";
import { NotationError, parse, print } from "../src/lib/notation";

// The alerts plugin's Python parser is held to the same cases
// (plugins/alerts/tests/test_notation.py), so the demo reads a query exactly
// as the plugin documents it.
const cases = JSON.parse(
  readFileSync(
    new URL(
      "../../plugins/alerts/tests/notation-cases.json",
      import.meta.url,
    ),
    "utf8",
  ),
) as { valid: { query: string; match: unknown }[]; invalid: string[] };

test("la notation se lit comme le parseur de référence du plugin", () => {
  for (const { query, match } of cases.valid)
    expect(parse(query), query).toEqual({ kind: "keywords", match });
  for (const query of cases.invalid)
    expect(() => parse(query), query.slice(0, 40)).toThrow(NotationError);
});

test("une alerte réécrite en texte pour la modifier redonne la même alerte", () => {
  for (const { query, match } of cases.valid) {
    const text = print(match as never);
    expect(text, query).not.toBeNull();
    expect(parse(text!).match, `${query} → ${text}`).toEqual(match);
  }
});
