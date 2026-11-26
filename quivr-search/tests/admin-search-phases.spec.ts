import { test, expect } from "@playwright/test";
import { fakeEngine, type Engine } from "./fake-engine";
import { fakeAdmin } from "./fake-admin";

// The Admin tab's per-phase search latencies against the faked facade route
// (tests/fake-admin.ts); the percentiles themselves are owned by phases.test.mjs.

let engine: Engine;
test.beforeEach(async ({ page }) => {
  engine = await fakeEngine(page);
  await fakeAdmin(page);
});
test.afterEach(async () => {
  await engine.close();
});

test("chaque phase d’une recherche a son p50 et son p95", async ({ page }) => {
  await page.goto("/?view=admin");
  await page.getByRole("tab", { name: "Recherche" }).click();
  const panel = page.getByRole("region", { name: "Phases d’une recherche" });
  await expect(panel).toContainText("Sur les 42 dernières recherches");
  // Each phase of the fixture, with its p50 and p95, in the engine's order.
  for (const [phase, p50, p95] of [
    ["Aiguillage", "1 ms", "2 ms"],
    ["Encodage de la requête", "38 ms", "95 ms"],
    ["Index", "12 ms", "40 ms"],
  ]) {
    const row = panel.getByRole("row", { name: new RegExp(`^${phase}`) });
    await expect(row.getByRole("cell").nth(0)).toHaveText(p50);
    await expect(row.getByRole("cell").nth(1)).toHaveText(p95);
  }
  await expect(panel.getByRole("row")).toHaveCount(4);
});
