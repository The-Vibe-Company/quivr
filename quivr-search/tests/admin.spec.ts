import { test, expect } from "@playwright/test";

// The Admin tab against the real core (make verify-demo): a text added by
// hand shows up in the live flow, advances until it is searchable, opens its
// timeline, and counts among today's documents stored. Synthetic content only.
const run = Date.now().toString(36);
const title = `Relevé de suivi ${run}`;

test.beforeEach(async ({ page }) => {
  await page.request.post("/demo/login", {
    data: { password: process.env.QUIVR_DEMO_PASSWORD || "local-browser-demo" },
  });
});

test("un texte ajouté passe dans le flux en direct jusqu’à trouvable, puis ouvre son parcours", async ({
  page,
}, info) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/?view=admin");
  await expect(
    page.getByRole("heading", { name: "Admin", level: 1 }),
  ).toBeVisible();
  await expect(page.locator(".admin-live")).toHaveText("En direct", {
    timeout: 30000,
  });

  await page
    .getByRole("button", { name: "Ajouter du texte", exact: true })
    .first()
    .click();
  const dialog = page.getByRole("dialog", { name: "Ajouter du texte" });
  await dialog
    .getByLabel("Votre texte")
    .fill(`${title}\nUn texte collé pour suivre ses étapes.`);
  await dialog.getByRole("button", { name: "Ajouter à la démo" }).click();
  await expect(dialog.getByText("Texte enregistré")).toBeVisible({
    timeout: 30000,
  });
  await page.keyboard.press("Escape");

  // The row arrives without a reload and fills in step by step.
  const row = page
    .getByRole("list", { name: "Derniers documents" })
    .getByRole("button")
    .filter({ hasText: title });
  await expect(row).toBeVisible({ timeout: 30000 });
  await expect(row).toContainText(/trouvable : en \d/, { timeout: 45000 });
  await expect(row).toContainText("À la main");
  await page.screenshot({
    path: info.outputPath("admin-flow.png"),
    fullPage: true,
  });

  // Its timeline opens beside the flow, with each step's duration.
  await row.click();
  const panel = page.getByRole("complementary", { name: title });
  await expect(panel.getByText("Trouvable en")).toBeVisible();
  const steps = panel
    .getByRole("list", { name: "Étapes" })
    .getByRole("listitem");
  await expect(steps.filter({ hasText: "Découpé" })).toHaveAccessibleName(
    /Découpé : \d/,
  );
  await expect(steps.filter({ hasText: "Trouvable" })).toHaveAccessibleName(
    /Trouvable : \d/,
  );
  await page.screenshot({
    path: info.outputPath("admin-timeline.png"),
    fullPage: true,
  });

  // Échap closes it and gives the focus back to the row.
  await page.keyboard.press("Escape");
  await expect(panel).toBeHidden();
  await expect(row).toBeFocused();

  // The engine's count puts it among today's documents stored.
  await page.getByRole("tab", { name: "En base" }).click();
  const stored = page.getByRole("region", { name: "Documents en base" });
  await expect(stored.locator(".usage-figure").nth(1)).toContainText(
    /^[1-9][\d\s]*aujourd’hui/,
  );
});
