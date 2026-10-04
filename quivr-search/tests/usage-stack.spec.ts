import { test, expect } from "@playwright/test";

// The usage section against the real core (make verify-demo), which records
// query text and flushes its counts every 200 ms: searches made through the
// facade show up in the counts and in the top queries. Synthetic content only.
const run = Date.now().toString(36);
const query = `relevé des marées ${run}`;
const SEARCHES = 5;

test.beforeEach(async ({ page }) => {
  await page.request.post("/demo/login", {
    data: { password: process.env.QUIVR_DEMO_PASSWORD || "local-browser-demo" },
  });
});

test("des recherches faites sur la démo apparaissent dans l’utilisation et les requêtes fréquentes", async ({
  page,
}, info) => {
  const { corpus_id } = await (await page.request.get("/demo/session")).json();
  // The same search several times, as the top bar sends it: more often than
  // any query the earlier specs typed, so it ranks in the top ten shown.
  for (let i = 0; i < SEARCHES; i++) {
    const response = await page.request.post("/v0/search", {
      // The facade takes writes from its own origin only.
      headers: { Origin: new URL(test.info().project.use.baseURL!).origin },
      data: {
        query: `  ${query.toUpperCase()} `,
        mode: "lexical",
        profile: "default",
        limit: 10,
        corpus_ids: [corpus_id],
      },
    });
    expect(response.status(), await response.text()).toBe(200);
  }

  await page.setViewportSize({ width: 1440, height: 1600 });
  const usage = page.getByRole("region", { name: "Utilisation" });
  const top = usage.getByRole("region", { name: "Requêtes fréquentes" });
  // The core flushes its counts every 200 ms, but the facade caches a read
  // for 10 s and an earlier spec may have read this one: reload until the
  // counts show. At worst about 11 s, inside the browser budget.
  await expect(async () => {
    await page.goto("/?view=admin");
    await page.getByRole("tab", { name: "Utilisation" }).click();
    await expect(
      top
        .getByRole("listitem")
        .filter({ hasText: query })
        .locator(".usage-query-count"),
    ).toHaveText(new RegExp(`^${SEARCHES}\\s*fois$`), { timeout: 2000 });
  }).toPass({ timeout: 15000 });
  const searches = usage.getByRole("region", { name: "Recherches" });
  await expect(
    searches
      .getByRole("table", { name: /temps de réponse par mode/ })
      .getByRole("row", { name: /Mots exacts/ }),
  ).toBeVisible();
  // Every read of the section is relayed: documents per source and alerts too.
  await expect(
    usage.getByRole("region", { name: "Documents reçus" }),
  ).toContainText(/\d documents? /);
  await expect(
    usage.getByRole("region", { name: "Alertes déclenchées" }),
  ).toContainText(/\d articles? attrapés?/);
  await usage.screenshot({ path: info.outputPath("usage-stack.png") });
});
