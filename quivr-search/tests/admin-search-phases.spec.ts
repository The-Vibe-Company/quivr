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
  const row = panel.getByRole("row", { name: /Encodage de la requête/ });
  await expect(row.getByRole("cell").nth(0)).toHaveText("38 ms");
  await expect(row.getByRole("cell").nth(1)).toHaveText("95 ms");
});
