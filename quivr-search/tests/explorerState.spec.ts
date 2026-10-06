import { test, expect } from "@playwright/test";
import { rangeLabel, readState, writeState } from "../src/lib/explore";

// The Explorer's address (THE-1204): what a shared or reloaded link restores.
test("l’adresse de l’Explorer se relit à l’identique, et une date impossible n’est pas une période", () => {
  const address = "q=port&f=metadata.language%3Afr&f=desk%3Asport&range=2026-10-03..2026-10-05&window=2026-09..2026-10&sort=relevance&selected=rec_1";
  const state = readState(new URLSearchParams(address));
  expect(state).toEqual({
    q: "port",
    selection: { "metadata.language": ["fr"], desk: ["sport"] },
    range: { from: "2026-10-03", to: "2026-10-05" },
    window: { from: "2026-09", to: "2026-10" },
    sort: "relevance",
    selected: "rec_1",
  });
  expect(writeState(state).toString()).toBe(address);
  for (const range of ["2026-02-31..2026-03-01", "2026-10-05..2026-10-03", "2026-10..2026-10-05"])
    expect(readState(new URLSearchParams({ range })).range, range).toBeUndefined();
  expect(rangeLabel({ from: "2026-10-03", to: "2026-10-05" })).toBe("3 – 5 oct. 2026");
});
