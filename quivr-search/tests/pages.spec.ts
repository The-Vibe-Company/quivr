import { test, expect } from "@playwright/test";

// The three pages share one navigation and one error state: a failure
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
  { tab: "Fil", route: "**/demo/feed" },
  { tab: "Sources", route: "**/v0/connector-kinds" },
  { tab: "Alertes", route: "**/demo/alerts" },
];

test("chaque page annonce sa panne de la même façon et s’en remet, sur mobile", async ({
  page,
}, info) => {
  await page.setViewportSize({ width: 375, height: 812 });
  const nav = page.getByRole("navigation", { name: "Sections" });
  for (const { tab, route } of pages) {
    await page.route(route, (r) =>
      r.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({ message: "Panne simulée." }),
      }),
    );
    // A tab's name also carries its badge ("Alertes 3, …"). The feed is
    // read when the app opens, so its failure shows on the first load.
    const link = nav.getByRole("link", { name: new RegExp(`^${tab}`) });
    if (tab === "Fil") await page.goto("/");
    else await link.click();
    await expect(link).toHaveAttribute("aria-current", "page");
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
  // Between phone and desktop the header still fits.
  await page.setViewportSize({ width: 700, height: 900 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});

// The design's Geist font comes from the demo itself: the facade's policy
// allows no other font origin, and the text renders in it on a phone and on
// a desktop screen.
test("le texte s’affiche en Geist, servi par la démo", async ({
  page,
  baseURL,
}) => {
  await page.addInitScript(() => {
    const seen: string[] = [];
    (window as unknown as { cspViolations: string[] }).cspViolations = seen;
    document.addEventListener("securitypolicyviolation", (e) =>
      seen.push(`${e.violatedDirective} ${e.blockedURI}`),
    );
  });
  const fonts: { url: string; type?: string }[] = [];
  page.on("response", (response) => {
    if (response.request().resourceType() === "font")
      fonts.push({
        url: response.url(),
        type: response.headers()["content-type"],
      });
  });
  for (const size of [
    { width: 375, height: 812 },
    { width: 1440, height: 900 },
  ]) {
    await page.setViewportSize(size);
    await page.goto("/");
    await expect(
      page.getByRole("navigation", { name: "Sections" }),
    ).toBeVisible();
    await expect
      .poll(() =>
        page.evaluate(async () => {
          await document.fonts.ready;
          return [...document.fonts].some(
            (face) =>
              face.family.replace(/"/g, "") === "Geist" &&
              face.status === "loaded",
          );
        }),
      )
      .toBe(true);
    expect(
      await page.evaluate(() => getComputedStyle(document.body).fontFamily),
    ).toMatch(/^"?Geist"?,/);
    expect(
      await page.evaluate(
        () => (window as unknown as { cspViolations: string[] }).cspViolations,
      ),
    ).toEqual([]);
  }
  const origin = new URL(baseURL!).origin;
  expect(fonts.length).toBeGreaterThan(0);
  for (const font of fonts) {
    expect(new URL(font.url).origin, font.url).toBe(origin);
    expect(font.type, font.url).toBe("font/woff2");
  }
});
