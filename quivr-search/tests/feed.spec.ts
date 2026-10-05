import { test, expect, type Page } from "@playwright/test";

// Synthetic content only. The RSS item comes from the local test site that
// scripts/demo.py starts (scripts/fake_feeds.py), which the facade allows as
// a private feed origin; the tests never reach the internet.
const FEEDS = process.env.QUIVR_DEMO_FEEDS_URL || "";
const run = Date.now().toString(36);
const handTitle = `Note de veille ${run}`;
const rssTitle = "Les cartographes redessinent la lagune";
const rssBody = "Un relevé au sonar corrige les cartes anciennes de la lagune.";
const namespace = `veille-${run}`;

test.beforeEach(async ({ page }) => {
  await page.request.post("/demo/login", {
    data: { password: process.env.QUIVR_DEMO_PASSWORD || "local-browser-demo" },
  });
});

// The old ?view=veille address still opens the feed.
async function openFeed(page: Page) {
  await page.goto("/?view=veille");
  await expect(page.getByRole("heading", { name: "Fil", level: 1 })).toBeAttached();
}

test("un texte ajouté à la main puis un article RSS arrivent en direct, filtrables et lisibles", async ({
  page,
  baseURL,
}, info) => {
  test.skip(!FEEDS, "needs the local test feeds (make verify-demo)");
  test.setTimeout(150000);
  await page.setViewportSize({ width: 1280, height: 900 });
  await openFeed(page);
  // The badge reads "Reconnexion…" until the facade follows the change feed.
  try {
    await expect(page.locator(".bar-live")).toHaveText("En direct", {
      timeout: 30000,
    });
  } catch (error) {
    await page.screenshot({
      path: info.outputPath("veille-not-live.png"),
      fullPage: true,
    });
    console.log(
      "Veille when not live:",
      await page.locator("main").innerText(),
    );
    throw error;
  }
  // Scoped to the list: the live region also announces new titles.
  const list = page.getByRole("list", { name: "Derniers éléments" });
  const items = list.locator(".row");

  // 1. A text added by hand appears at the top without reloading.
  await page
    .getByRole("button", { name: "Ajouter du texte", exact: true })
    .first()
    .click();
  const dialog = page.getByRole("dialog", { name: "Ajouter du texte" });
  await dialog
    .getByLabel("Votre texte")
    .fill(`${handTitle}\nUn texte collé depuis la démo.`);
  await dialog.getByRole("button", { name: "Ajouter à la démo" }).click();
  await expect(dialog.getByText("Texte enregistré")).toBeVisible({
    timeout: 30000,
  });
  await page.keyboard.press("Escape");
  await expect(items.first()).toContainText(handTitle, { timeout: 30000 });
  await expect(items.first()).toContainText("Ajouté à la main");

  // 2. An RSS Connector Instance collects an item, which lands above it.
  const { corpus_id } = await (await page.request.get("/demo/session")).json();
  const created = await page.request.post("/v0/connectors", {
    headers: { Origin: baseURL! },
    data: {
      idempotency_key: `veille-${run}`,
      corpus_id,
      source_namespace: namespace,
      kind: "rss",
      config: { url: `${FEEDS}/feeds/world.xml?run=${namespace}` },
      schedule: { interval_seconds: 2 },
    },
  });
  expect(created.status(), await created.text()).toBe(201);
  const { connector_id } = await created.json();
  try {
    await expect(items.first()).toContainText(rssTitle, { timeout: 90000 });
    await expect(items.first()).toContainText(namespace);
    // The test feed re-dates its item on every poll: a new Version with the
    // same text is not a correction.
    await expect(items.first().locator(".row-updated")).toHaveCount(0);
    const titles = await items.locator(".row-title").allTextContents();
    expect(titles.indexOf(rssTitle)).toBeLessThan(titles.indexOf(handTitle));
    await page.screenshot({
      path: info.outputPath("veille-desktop.png"),
      fullPage: true,
    });

    // 3. A source picked in the Sources menu shows its articles only.
    const filters = page.getByRole("group", { name: "Filtrer le fil" });
    await filters.getByRole("button", { name: /^Sources/ }).click();
    const sources = page.getByRole("dialog", { name: "Sources" });
    const hand = sources.getByRole("button", { name: /^Ajouté à la main/ });
    await hand.click();
    await expect(list.getByText(handTitle)).toBeVisible();
    await expect(list.getByText(rssTitle)).toHaveCount(0);
    await hand.click();
    await sources
      .getByRole("button", { name: new RegExp(`^${namespace}`) })
      .click();
    await page.keyboard.press("Escape");
    await expect(list.getByText(rssTitle)).toBeVisible();
    await expect(list.getByText(handTitle)).toHaveCount(0);
    await expect(items).toHaveCount(1);

    // 4. An item opens in the reader, without its HTML source.
    await page.getByRole("link", { name: rssTitle }).click();
    const reader = page.getByRole("complementary", { name: rssTitle });
    await expect(reader.getByTestId("canonical-text").first()).toBeVisible();
    await expect(reader).toContainText(rssBody);
    await expect(reader).not.toContainText("<");
    await page.keyboard.press("Escape");
    await filters.getByRole("button", { name: "Tout effacer" }).click();

    await page.setViewportSize({ width: 375, height: 812 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath("veille-mobile.png"),
      fullPage: true,
    });
  } finally {
    await page.request.post(`/v0/connectors/${connector_id}/disable`, {
      headers: { Origin: baseURL! },
      data: { idempotency_key: `veille-disable-${run}` },
    });
  }
});

// The Date filter reads Quivr itself: its counts and a day's articles come
// from the core's date listing, not from the articles the feed loaded.
test("le filtre Date compte et liste aujourd’hui depuis Quivr", async ({ page }, info) => {
  test.setTimeout(90000);
  await page.setViewportSize({ width: 1280, height: 900 });
  await openFeed(page);
  const title = `Note du jour ${run}`;
  await page.getByRole("button", { name: "Ajouter du texte", exact: true }).first().click();
  const dialog = page.getByRole("dialog", { name: "Ajouter du texte" });
  await dialog.getByLabel("Votre texte").fill(`${title}\nUn texte daté par Quivr.`);
  await dialog.getByRole("button", { name: "Ajouter à la démo" }).click();
  await expect(dialog.getByText("Texte enregistré")).toBeVisible({ timeout: 30000 });
  await page.keyboard.press("Escape");
  const list = page.getByRole("list", { name: "Derniers éléments" });
  await expect(list.getByText(title)).toBeVisible({ timeout: 30000 });

  const filters = page.getByRole("group", { name: "Filtrer le fil" });
  const counted = page.waitForResponse((r) => new URL(r.url()).pathname === "/demo/feed/days");
  await filters.getByRole("button", { name: /^Date/ }).click();
  expect((await counted).status()).toBe(200);
  // Quivr may have counted before the text arrived (the server keeps counts a
  // minute): the page adds what arrived since, so today shows it either way.
  const dates = page.getByRole("dialog", { name: "Date" });
  const today = dates.getByRole("button", { name: /^Aujourd’hui/ });
  await expect
    .poll(async () => Number(await today.locator(".menu-count").textContent()))
    .toBeGreaterThanOrEqual(1);
  const listed = page.waitForResponse((r) => new URL(r.url()).pathname === "/demo/feed/page");
  await today.click();
  const day = await listed;
  expect(day.status()).toBe(200);
  expect((await day.json()).items.map((i: { title: string }) => i.title)).toContain(title);
  await page.keyboard.press("Escape");
  await expect(list.getByText(title)).toBeVisible();
  await page.screenshot({ path: info.outputPath("veille-today.png") });
});

// The verify Corpus is shared by every spec, so an empty feed is simulated.
test("un fil vide explique comment ajouter une source ou un texte", async ({
  page,
}, info) => {
  await page.route("**/demo/feed", (route) =>
    route.fulfill({ json: { items: [], live: true } }),
  );
  await page.route("**/demo/feed/stream", (route) =>
    route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body: "retry: 60000\n\n",
    }),
  );
  await openFeed(page);
  await expect(
    page.getByRole("heading", { name: "Rien n’est encore arrivé." }),
  ).toBeVisible();
  await expect(page.getByText(/un flux RSS, ou collez un texte/)).toBeVisible();
  await page.screenshot({
    path: info.outputPath("veille-empty.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "Ajouter du texte" }).last().click();
  await expect(
    page.getByRole("dialog", { name: "Ajouter du texte" }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Ajouter une source" }).click();
  await expect(
    page.getByRole("heading", { name: "Sources", level: 1 }),
  ).toBeAttached();
});
