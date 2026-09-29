import { test, expect } from "@playwright/test";

// The three live pages share one navigation and one error state: a failure
// reads as an alert with a retry, the retry recovers without reloading, and
// the page fits a phone. The facade is made to fail with a route, so this runs
// on any stack.
test.beforeEach(async ({ page }) => {
  await page.request.post("/demo/login", {
    data: { password: process.env.QUIVR_DEMO_PASSWORD || "local-browser-demo" },
  });
});

const pages = [
  // The snapshot only: the live stream stays up, so nothing retries by itself.
  { tab: "Veille", route: "**/demo/feed" },
  { tab: "Sources", route: "**/v0/connector-kinds" },
  { tab: "Alertes", route: "**/demo/alerts" },
];

test("chaque page annonce sa panne de la même façon et s’en remet, sur mobile", async ({
  page,
}, info) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await page.goto("/");
  const nav = page.getByRole("navigation", { name: "Sections" });
  for (const { tab, route } of pages) {
    await page.route(route, (r) =>
      r.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({ message: "Panne simulée." }),
      }),
    );
    await nav.getByRole("link", { name: tab, exact: true }).click();
    await expect(
      nav.getByRole("link", { name: tab, exact: true }),
    ).toHaveAttribute("aria-current", "page");
    const alert = page.getByRole("alert");
    await expect(alert).toContainText("Panne simulée.");
    await page.screenshot({
      path: info.outputPath(`${tab.toLowerCase()}-error-mobile.png`),
      fullPage: true,
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.unroute(route);
    await alert.getByRole("button", { name: "Réessayer" }).click();
    await expect(page.getByRole("alert")).toHaveCount(0);
    await expect(
      page.getByRole("heading", { name: tab, level: 1 }),
    ).toBeVisible();
  }
  // Between phone and desktop the header keeps one row and still fits.
  await page.setViewportSize({ width: 700, height: 900 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});
