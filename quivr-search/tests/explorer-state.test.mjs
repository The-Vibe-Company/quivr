import assert from "node:assert/strict";
import test from "node:test";
import { rangeLabel, readState, writeState } from "../src/lib/explore.ts";

// The Explorer's address (THE-1204): what a shared or reloaded link restores.
test("l’adresse de l’Explorer se relit à l’identique, et une date impossible n’est pas une période", () => {
  const address = "q=port&f=metadata.language%3Afr&f=desk%3Asport&range=2026-10-03..2026-10-05&window=2026-09..2026-10&sort=relevance&selected=rec_1";
  const state = readState(new URLSearchParams(address));
  assert.deepEqual(state, {
    q: "port",
    selection: { "metadata.language": ["fr"], desk: ["sport"] },
    range: { from: "2026-10-03", to: "2026-10-05" },
    window: { from: "2026-09", to: "2026-10" },
    sort: "relevance",
    selected: "rec_1",
  });
  assert.equal(writeState(state).toString(), address);
  for (const range of ["2026-02-31..2026-03-01", "2026-10-05..2026-10-03", "2026-10..2026-10-05"])
    assert.equal(readState(new URLSearchParams({ range })).range, undefined, range);
  assert.equal(rangeLabel({ from: "2026-10-03", to: "2026-10-05" }), "3 – 5 oct. 2026");
});
