import { test, expect, type Page } from "@playwright/test";

// Feeds come from the local test site started by scripts/demo.py
// (scripts/fake_feeds.py); the browser tests never reach the internet.
const FEEDS = process.env.QUIVR_DEMO_FEEDS_URL || "";
const run = Date.now().toString(36);

test.skip(!FEEDS, "needs the local test feeds (make verify-demo)");

test.beforeEach(async ({ page }) => {
  // Several steps wait for real polls (up to 60 s each).
  test.setTimeout(180000);
  await page.request.post("/demo/login", {
    data: { password: process.env.QUIVR_DEMO_PASSWORD || "local-browser-demo" },
  });
});

async function openSources(page: Page) {
  await page.goto("/?view=sources");
  await expect(
    page.getByRole("heading", { name: "Sources", level: 1 }),
  ).toBeVisible();
}

const addressField = (page: Page) =>
  page.getByLabel("Adresse d’un site ou d’un flux RSS");

async function discover(page: Page, address: string) {
  await addressField(page).fill(address);
  await page.getByRole("button", { name: "Ajouter", exact: true }).click();
}

const row = (page: Page, name: string) =>
  page
    .getByRole("list", { name: "Sources" })
    .getByRole("listitem")
    .filter({ has: page.getByRole("button", { name, exact: true }) });

const hits = async (page: Page, feed: string) =>
  (
    await (
      await page.request.get(
        `${FEEDS}/_hits?path=/feeds/ticker.xml&run=${encodeURIComponent(feed)}`,
      )
    ).json()
  ).hits as number;

test("coller l’adresse d’un site trouve son flux, dont les articles deviennent cherchables", async ({
  page,
}, info) => {
  await openSources(page);
  await page.screenshot({ path: info.outputPath("sources-empty.png") });
  await discover(page, `${FEEDS}/site/?run=${run}`);
  const confirm = page.getByRole("form", { name: "Confirmer la source" });
  await expect(confirm.getByText("Flux trouvé")).toBeVisible();
  const name = `Le Journal exemple — À la une ${run}`;
  await expect(confirm.getByLabel("Nom de la source")).toHaveValue(name);
  await expect(confirm.getByLabel("Relevé")).toHaveValue(/\d+/);
  await page.screenshot({
    path: info.outputPath("sources-confirm.png"),
    fullPage: true,
  });
  await confirm.getByRole("button", { name: "Commencer la collecte" }).click();

  const source = row(page, name);
  await expect(source).toBeVisible();
  await expect(addressField(page)).toHaveValue("");
  // Health follows the first run live: active, with the last article time.
  await expect(source.locator('[data-state="active"]')).toBeVisible({
    timeout: 60000,
  });
  await expect(source.getByText("Dernier article")).toBeVisible();
  await expect(source.getByText("aucun pour l’instant")).toHaveCount(0);
  await page.screenshot({
    path: info.outputPath("sources-list.png"),
    fullPage: true,
  });

  // The articles are searchable within a minute.
  await expect(async () => {
    await page.goto("/?q=gardiens%20phare%20ocre&mode=lexical");
    await expect(page.locator(".result").first()).toContainText("teinte ocre", {
      timeout: 3000,
    });
  }).toPass({ timeout: 60000 });
});

test("un site qui annonce plusieurs flux laisse choisir lequel suivre", async ({
  page,
}) => {
  await openSources(page);
  await discover(page, `${FEEDS}/multi/?run=${run}`);
  const confirm = page.getByRole("form", { name: "Confirmer la source" });
  const choices = confirm.getByRole("group", { name: /2 flux trouvés/ });
  await expect(choices.getByRole("radio")).toHaveCount(2);
  await choices.getByRole("radio", { name: /Technologie/ }).check();
  const name = `La Revue exemple — Technologie ${run}`;
  await expect(confirm.getByLabel("Nom de la source")).toHaveValue(name);
  await confirm.getByRole("button", { name: "Commencer la collecte" }).click();
  const source = row(page, name);
  await expect(source).toContainText("/feeds/tech.atom");
  await expect(source.locator('[data-state="active"]')).toBeVisible({
    timeout: 60000,
  });
});

test("une suggestion s’ajoute en un clic", async ({ page }, info) => {
  await openSources(page);
  const chip = page.getByRole("button", { name: "Ajouter Fil continu exemple" });
  await expect(chip).toBeVisible();
  await page.screenshot({ path: info.outputPath("sources-suggestions.png") });
  await chip.click();
  const source = row(page, "Fil continu exemple");
  await expect(source).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Fil continu exemple (déjà suivie)" }),
  ).toBeDisabled();
  await expect(source.locator('[data-state="active"]')).toBeVisible({
    timeout: 60000,
  });
});

test("une adresse privée, cassée ou sans flux reçoit une erreur claire", async ({
  page,
}, info) => {
  await openSources(page);
  const alert = page.getByRole("alert");
  await discover(page, "http://10.0.0.1/feed.xml");
  await expect(alert).toContainText("réseau privé ou local");
  await expect(alert).toBeFocused();
  await page.screenshot({ path: info.outputPath("sources-private.png") });
  await discover(page, "http://169.254.169.254/latest/meta-data");
  await expect(alert).toContainText("réseau privé ou local");
  await discover(page, `${FEEDS}/missing/`);
  await expect(alert).toContainText("n’existe pas");
  await discover(page, `${FEEDS}/plain/`);
  await expect(alert).toContainText("Aucun flux RSS ou Atom");
  await discover(page, "http://");
  await expect(alert).toContainText("adresse web complète");
  await expect(
    page.getByRole("form", { name: "Confirmer la source" }),
  ).toHaveCount(0);
});

test("la pause arrête la collecte, la reprise la relance, le retrait masque la source", async ({
  page,
}, info) => {
  const feed = `${run}-pause`;
  await openSources(page);
  await discover(page, `${FEEDS}/feeds/ticker.xml?run=${feed}`);
  const confirm = page.getByRole("form", { name: "Confirmer la source" });
  const name = `Fil continu exemple ${feed}`;
  await confirm.getByLabel("Nom de la source").fill(name);
  // The shortest interval this deployment allows (1 s in the test stack).
  const shortest = await confirm
    .getByLabel("Relevé")
    .locator("option")
    .first()
    .getAttribute("value");
  await confirm.getByLabel("Relevé").selectOption(shortest!);
  await confirm.getByRole("button", { name: "Commencer la collecte" }).click();
  const source = row(page, name);
  await expect.poll(() => hits(page, feed), { timeout: 60000 }).toBeGreaterThan(2);

  await source.getByRole("button", { name: `Mettre en pause ${name}` }).click();
  await expect(source.locator('[data-state="paused"]')).toBeVisible();
  await expect(
    source.getByRole("button", { name: `Reprendre ${name}` }),
  ).toBeFocused();
  // A run already in flight may still finish; after that, nothing is fetched.
  await page.waitForTimeout(2000);
  const paused = await hits(page, feed);
  await page.waitForTimeout(6000);
  expect(await hits(page, feed)).toBe(paused);
  await page.screenshot({
    path: info.outputPath("sources-paused.png"),
    fullPage: true,
  });

  // Resuming is the same row, collecting again.
  await source.getByRole("button", { name: `Reprendre ${name}` }).click();
  await expect(source.locator('[data-state="paused"]')).toHaveCount(0);
  await expect.poll(() => hits(page, feed), { timeout: 60000 }).toBeGreaterThan(paused);
  await expect(page.getByRole("button", { name, exact: true })).toHaveCount(1);

  // Removing asks first, stops collection and survives a reload.
  await source.getByRole("button", { name: `Retirer ${name}` }).click();
  await source
    .getByRole("group", { name: `Retirer ${name}` })
    .getByRole("button", { name: "Retirer", exact: true })
    .click();
  await expect(row(page, name)).toHaveCount(0);
  await expect(page.getByRole("heading", { name: /^Vos sources/ })).toBeFocused();
  await page.waitForTimeout(2000);
  const removed = await hits(page, feed);
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Sources", level: 1 }),
  ).toBeVisible();
  await expect(page.getByRole("list", { name: "Sources" })).toBeVisible();
  await expect(page.getByRole("button", { name, exact: true })).toHaveCount(0);
  await page.waitForTimeout(3000);
  expect(await hits(page, feed)).toBe(removed);
});

test("mobile sombre : ajout et liste lisibles", async ({ page }, info) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await page.emulateMedia({ colorScheme: "dark", reducedMotion: "reduce" });
  await openSources(page);
  await discover(page, `${FEEDS}/multi/?run=${run}-mobile`);
  await expect(
    page.getByRole("form", { name: "Confirmer la source" }),
  ).toBeVisible();
  await page.screenshot({
    path: info.outputPath("mobile-dark-sources-pick.png"),
    fullPage: true,
  });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});
