import { test, expect, type Page } from "@playwright/test";
import { fakeEngine, type Engine } from "./fake-engine";
import { fakeAdmin, type AdminFake } from "./fake-admin";

// The Admin tab's Goulots and Plugins sections (THE-797) against faked
// facade routes (tests/fake-admin.ts): the real bundle and synthetic
// rollups. The sections refresh every 15 s; page.clock moves the browser's
// time instead of waiting for it.

let engine: Engine;
let admin: AdminFake;
test.beforeEach(async ({ page }) => {
  engine = await fakeEngine(page);
  admin = await fakeAdmin(page);
});
test.afterEach(async () => {
  await engine.close();
});

const necks = (page: Page) =>
  page.getByRole("region", { name: "Goulots par étape" });
const plugins = (page: Page) => page.getByRole("region", { name: "Plugins" });
// A step of the pipeline: its card, named by its heading.
const step = (page: Page, name: string) =>
  necks(page)
    .getByRole("listitem")
    .filter({ has: page.getByRole("heading", { name, exact: true }) });
// Plugins and usage live in tabs under the pipeline.
const openTab = (page: Page, name: string) =>
  page.getByRole("tab", { name: new RegExp(`^${name}`) }).click();
const plugin = (page: Page, id: string) =>
  plugins(page).getByRole("listitem").filter({ hasText: id });

test("les goulots nomment l’étape ralentie, avec ses durées et sa file", async ({
  page,
}, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/?view=admin");
  const section = necks(page);
  await expect(section.getByRole("status")).toHaveText(
    /^L’étape « Vecteurs » ralentit tout : \d,\d\ss au p95 ces 10 dernières minutes, contre 1,5\ss d’habitude\. 6 documents attendent à cette étape\.$/,
  );
  const vectors = step(page, "Vecteurs");
  await expect(vectors).toContainText("Ralenti");
  await expect(vectors).toContainText(/6 en attente depuis 3\s*min/);
  const cut = step(page, "Découpé");
  await expect(cut).not.toContainText("Ralenti");
  // The step's p50, then its p95.
  await expect(cut.getByRole("definition").first()).toHaveText("40 ms");

  // A week reads the same steps over 2-hour buckets.
  await section.getByRole("button", { name: "7 j" }).click();
  await expect(section.getByRole("button", { name: "7 j" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await expect
    .poll(() => admin.reads.some((r) => r.includes("steps?window=7d")))
    .toBe(true);
  // Eight slow minutes vanish in a week of 2-hour buckets; the queue stays.
  await expect(section.getByRole("status")).toHaveText(
    /s’accumulent à l’étape « Vecteurs »/,
  );
  await section.screenshot({ path: info.outputPath("bottlenecks.png") });
});

test("un plugin arrêté passe en panne avec sa dernière erreur, puis redevient opérationnel", async ({
  page,
}, info) => {
  await page.clock.install();
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/?view=admin");
  await openTab(page, "Plugins");
  const alerts = plugin(page, "alerts");
  await expect(alerts.getByRole("button")).toContainText("Opérationnel");
  await expect(
    plugins(page).getByText("5 plugins, aucun problème"),
  ).toBeVisible();

  admin.stop("alerts");
  await page.clock.runFor(16000);
  const row = alerts.getByRole("button");
  await expect(row).toContainText("En panne");
  await expect(row).toContainText("plugin_unavailable");
  await expect(row).toContainText(/à l’instant|il y a/);
  await expect(plugins(page).getByText("1 à surveiller sur 5")).toBeVisible();
  // Problems come first, and the row opens on what the error means.
  await expect(plugins(page).getByRole("listitem").first()).toContainText(
    "alerts",
  );
  await row.click();
  await expect(row).toHaveAttribute("aria-expanded", "true");
  await expect(alerts.getByText(/Le plugin ne répond pas/)).toBeVisible();
  await expect(
    alerts.getByRole("row", { name: /Décision d’alerte/ }),
  ).toContainText("plugin_unavailable");
  await page.screenshot({
    path: info.outputPath("plugin-down.png"),
    fullPage: true,
  });

  admin.restart("alerts");
  await page.clock.runFor(16000);
  await expect(row).toContainText("Opérationnel");
  await expect(
    alerts.getByText(/Rétabli : les appels réussissent de nouveau/),
  ).toBeVisible();
});

test("un connecteur montre ses sources, leur dernier relevé et ce qu’elles ont lu", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/?view=admin");
  await openTab(page, "Plugins");
  const rss = plugin(page, "connector.rss");
  await expect(rss.getByRole("button")).toContainText(
    /Collecte rss · 3 sources · dernier relevé réussi il y a \d min · 177 lus aujourd’hui/,
  );
  await rss.getByRole("button").click();
  const sources = rss.getByRole("list", {
    name: "Sources collectées par connector.rss",
  });
  await expect(sources.getByRole("listitem")).toHaveCount(3);
  await expect(
    sources.getByRole("listitem").filter({ hasText: "Météo locale" }),
  ).toContainText("En échec");
});

test("mobile sombre : les deux sections tiennent dans l’écran", async ({
  page,
}, info) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({ colorScheme: "dark", reducedMotion: "reduce" });
  await page.goto("/?view=admin");
  await expect(necks(page).getByRole("status")).toContainText("Vecteurs");
  await openTab(page, "Plugins");
  await expect(plugin(page, "core.ingest")).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: info.outputPath("mobile-dark.png"),
    fullPage: true,
  });
});
