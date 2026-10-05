import { test, expect } from "@playwright/test";
import { fakeEngine, type Engine } from "./fake-engine";
import { fakeAdmin, type AdminFake } from "./fake-admin";

// The Admin tab's documents stored per day (THE-1034) against the faked
// facade route (tests/fake-admin.ts): three weeks, two days without a
// document, and Records the days do not count. The facade's counting is
// covered in admin.test.mjs, the real count API in admin.spec.ts.

let engine: Engine;
let admin: AdminFake;
test.use({ timezoneId: "Europe/Paris" });
test.beforeEach(async ({ page }) => {
  engine = await fakeEngine(page);
  admin = await fakeAdmin(page);
});
test.afterEach(async () => {
  await engine.close();
});

test("le total, aujourd’hui et chaque jour, un jour vide compté zéro", async ({
  page,
}, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/?view=admin");
  await page.getByRole("tab", { name: "En base" }).click();
  const panel = page.getByRole("region", { name: "Documents en base" });
  await expect(panel.locator(".usage-figure").nth(0)).toHaveText(
    /^136\sdocuments en base depuis le .+, retirés compris$/,
  );
  await expect(panel.locator(".usage-figure").nth(1)).toContainText(
    /^12\saujourd’hui/,
  );
  await expect(panel.locator(".usage-figure").nth(2)).toContainText(
    /^2\sjours sans document\ssur 21\sjours$/,
  );
  expect(admin.reads).toContain("/demo/admin/history?tz=Europe%2FParis");
  await expect(panel).toContainText("fuseau Europe/Paris");
  await expect(panel).toContainText(
    /Le total compte aussi 3\sdocuments sans version actuelle/,
  );

  // An empty day keeps its column, and reads zero on hover and in the table.
  const columns = panel.locator(".usage-slot");
  await expect(columns).toHaveCount(21);
  const empty = columns.nth(9);
  await expect(empty.locator(".history-zero")).toBeVisible();
  await empty.hover();
  await expect(panel.locator(".usage-tip")).toContainText("Aucun document");
  await columns.nth(20).hover();
  await expect(panel.locator(".usage-tip")).toContainText(/12\sdocuments$/);
  await page.screenshot({ path: info.outputPath("history-light.png") });

  await panel.getByText(/^Détail par jour \(21\sjours\)$/).click();
  const rows = panel.getByRole("table").getByRole("row");
  await expect(rows).toHaveCount(22);
  await expect(rows.nth(1).getByRole("cell")).toHaveText("12");
  await expect(rows.nth(12).getByRole("cell")).toHaveText("0");

  await page.emulateMedia({ colorScheme: "dark" });
  await page.screenshot({ path: info.outputPath("history-dark.png") });
  await page.emulateMedia({ colorScheme: "light" });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(panel.locator(".usage-figure").nth(1)).toBeVisible();
  await page.screenshot({
    path: info.outputPath("history-mobile.png"),
    fullPage: true,
  });
});
