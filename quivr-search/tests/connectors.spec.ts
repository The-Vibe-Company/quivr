import { test, expect, type Page } from "@playwright/test";

// Synthetic values only: the fixture kind refuses tokens starting with
// fixture-revoked and accepts any other.
const run = Date.now().toString(36);

test.beforeEach(async ({ page }) => {
  await page.request.post("/demo/login", {
    data: { password: process.env.QUIVR_DEMO_PASSWORD || "local-browser-demo" },
  });
});

async function openConnectors(page: Page, { add = true } = {}) {
  await page.goto("/?view=sources");
  await expect(
    page.getByRole("heading", { name: "Sources", level: 1 }),
  ).toBeVisible();
  if (!add) return;
  // Other kinds are offered from the dialog that adds a source.
  await page.getByRole("button", { name: "Ajouter une source", exact: true }).click();
  await expect(page.getByRole("button", { name: "Ajouter un connecteur" })).toBeVisible();
}

/** The secret must be gone from markup, form values and storage. */
async function expectNoSecret(page: Page, secret: string) {
  expect(await page.content()).not.toContain(secret);
  const leaked = await page.evaluate(
    (s) =>
      [...document.querySelectorAll("input, textarea")].some((el) =>
        (el as HTMLInputElement).value.includes(s),
      ) ||
      JSON.stringify({ ...sessionStorage }).includes(s) ||
      JSON.stringify({ ...localStorage }).includes(s),
    secret,
  );
  expect(leaked).toBe(false);
}

async function startFixture(page: Page, namespace: string, script: string) {
  await page.getByRole("button", { name: "Ajouter un connecteur" }).click();
  const dialog = page.getByRole("dialog", { name: "Ajouter un connecteur" });
  await dialog.getByRole("button", { name: /^Test fixture/ }).click();
  await dialog.getByLabel("Espace de noms").fill(namespace);
  await dialog.getByLabel(/^Script/).fill(script);
  return dialog;
}

test("créer, suivre la santé, remplacer l’identifiant, changer l’intervalle et désactiver", async ({
  page,
}, info) => {
  const revoked = `fixture-revoked-ui-${run}`;
  const valid = `fixture-ok-ui-${run}`;
  await openConnectors(page);
  const dialog = await startFixture(
    page,
    `wire-${run}`,
    JSON.stringify([
      { items: [{ record_key: `ui-${run}`, text: "Dépêche d’exemple" }] },
    ]),
  );
  await dialog.getByLabel("Requires a credential").check();
  await dialog.getByLabel("Intervalle de collecte (secondes)").fill("2");
  await dialog.getByLabel("Cette source demande un identifiant").check();
  await dialog.getByLabel("Token").fill(revoked);
  await page.screenshot({
    path: info.outputPath("connector-create.png"),
    fullPage: true,
  });
  await dialog.getByRole("button", { name: "Créer le connecteur" }).click();

  // The new instance opens in its settings; the secret is gone.
  const detail = page.getByRole("dialog", { name: `Réglages de wire-${run}` });
  await expect(
    detail.getByRole("heading", { name: `wire-${run}`, level: 1 }),
  ).toBeVisible();
  await expect(detail.getByText(/Présent · version 1/)).toBeVisible();
  await expectNoSecret(page, revoked);

  // Live health: the refused credential shows without reloading the page.
  await expect(detail.locator('[data-state="access_error"]')).toBeVisible({
    timeout: 30000,
  });
  await expect(detail.getByText("unauthorized")).toBeVisible();
  await page.screenshot({
    path: info.outputPath("connector-access-error.png"),
    fullPage: true,
  });

  // Rotation: write-only field, metadata only afterwards.
  await detail.getByRole("button", { name: "Remplacer l’identifiant" }).click();
  await detail.getByLabel("Token").fill(valid);
  await detail
    .getByRole("button", { name: "Déposer le nouvel identifiant" })
    .click();
  await expect(
    detail.getByText("Identifiant remplacé.", { exact: false }),
  ).toBeVisible();
  await expect(detail.getByText(/Présent · version 2/)).toBeVisible();
  await expectNoSecret(page, valid);
  await expect(detail.locator('[data-state="active"]')).toBeVisible({
    timeout: 30000,
  });

  // Interval change through the schedule route; saving closes the settings.
  await detail
    .getByRole("group", { name: "Vérifier les nouveautés toutes les…" })
    .getByRole("button", { name: "5 min", exact: true })
    .click();
  await detail.getByRole("button", { name: "Enregistrer" }).click();
  await expect(detail).toHaveCount(0);
  const row = page
    .getByRole("list", { name: "Sources" })
    .getByRole("listitem")
    .filter({ has: page.getByRole("button", { name: `wire-${run}` }) });
  await expect(row.locator(".source-state")).toHaveAttribute(
    "title",
    "Vérifiée toutes les 5 min",
  );

  // Pausing is absorbing for a credentialed source: it stays listed as
  // paused and cannot be resumed (its secret is never read back).
  await row.getByRole("button", { name: `Mettre en pause wire-${run}` }).click();
  await expect(row.locator('[data-state="paused"]')).toBeVisible();
  await expect(row.getByRole("button", { name: /^Reprendre/ })).toHaveCount(0);
  await row.getByRole("button", { name: `Plus d’actions pour wire-${run}` }).click();
  await row.getByRole("menuitem", { name: "Réglages" }).click();
  await expect(detail.locator('[data-state="paused"]')).toBeVisible();
  await expect(
    detail.getByRole("button", { name: "Remplacer l’identifiant" }),
  ).toHaveCount(0);
  await detail.getByRole("button", { name: "Fermer les réglages" }).click();
  await expectNoSecret(page, revoked);
  await expectNoSecret(page, valid);
  await page.screenshot({
    path: info.outputPath("connectors-list.png"),
    fullPage: true,
  });
});

test("les refus de validation désignent le champ fautif, au clavier", async ({
  page,
}) => {
  await openConnectors(page);
  // Unique per repetition, so this test can run with `--repeat-each`.
  const dialog = await startFixture(
    page,
    `invalid-${run}-${test.info().repeatEachIndex}`,
    '[{"items":"not a list"}]',
  );
  await dialog.getByRole("button", { name: "Créer le connecteur" }).focus();
  await page.keyboard.press("Enter");
  // The core answers 422 invalid_config at /config/script/0/items; the form
  // maps it onto the Script field and focuses the summary.
  const summary = dialog.getByRole("alert");
  await expect(summary).toContainText("n’est pas acceptée");
  await expect(summary).toBeFocused();
  await expect(dialog.getByLabel(/^Script/)).toHaveAttribute(
    "aria-invalid",
    "true",
  );
  await summary.getByRole("button", { name: "Script" }).press("Enter");
  await expect(dialog.getByLabel(/^Script/)).toBeFocused();

  // A second, keyboard-only correction goes through.
  await page.keyboard.press("ControlOrMeta+a");
  await page.keyboard.type("[]");
  await dialog.getByRole("button", { name: "Créer le connecteur" }).focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("dialog", { name: /^Réglages de invalid-/ }),
  ).toBeVisible();
});

test("sans dépôt d’identifiants, un type inconnu s’affiche depuis son seul schéma", async ({
  page,
}) => {
  await page.route("**/v0/connector-kinds", (route) =>
    route.fulfill({
      json: {
        credential_deposits: "unavailable",
        min_interval_seconds: 30,
        items: [
          {
            kind: "example_source",
            title: "Example source",
            description: "A kind this web app has never seen.",
            config_schema: {
              type: "object",
              required: ["endpoint"],
              properties: {
                endpoint: { type: "string", title: "Endpoint" },
                tags: {
                  type: "array",
                  items: { type: "string" },
                  title: "Tags",
                },
                depth: { type: "integer", title: "Depth", minimum: 1 },
                mode: { enum: ["fast", "full"], title: "Mode" },
              },
            },
            credential_schema: {
              type: "object",
              properties: { key: { type: "string", writeOnly: true } },
            },
            credential: "optional",
            default_interval_seconds: 600,
          },
          {
            kind: "example_private",
            title: "Example private source",
            config_schema: { type: "object", properties: {} },
            credential_schema: { type: "object", properties: {} },
            credential: "required",
            default_interval_seconds: 600,
          },
        ],
      },
    }),
  );
  await openConnectors(page);
  await expect(
    page.getByText("Le dépôt d’identifiants est désactivé sur ce déploiement", {
      exact: false,
    }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Ajouter un connecteur" }).click();
  const dialog = page.getByRole("dialog", { name: "Ajouter un connecteur" });
  await expect(
    dialog.getByRole("button", { name: /^Example private source/ }),
  ).toBeDisabled();
  await dialog.getByRole("button", { name: /^Example source/ }).click();
  for (const label of ["Endpoint", "Tags", "Depth", "Mode"])
    await expect(
      dialog.getByLabel(label, { exact: false }).first(),
    ).toBeVisible();
  await expect(
    dialog.getByLabel("Intervalle de collecte (secondes)"),
  ).toHaveValue("600");
  await expect(dialog.getByText("Identifiant", { exact: true })).toHaveCount(0);
  await expect(
    dialog.getByText("ce connecteur sera créé sans identifiant", {
      exact: false,
    }),
  ).toBeVisible();
  await expect(dialog.locator('input[type="password"]')).toHaveCount(0);
});

test("un déploiement sans permission connecteurs l’annonce", async ({
  page,
}) => {
  await page.route("**/v0/connector-kinds", (route) =>
    route.fulfill({
      status: 403,
      json: { code: "forbidden", message: "forbidden", retryable: false },
    }),
  );
  await openConnectors(page, { add: false });
  await expect(
    page.getByText("Les connecteurs ne sont pas activés sur ce déploiement."),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Ajouter un connecteur" }),
  ).toHaveCount(0);
});

test("mobile sombre : liste et détail lisibles", async ({ page }, info) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await page.emulateMedia({ colorScheme: "dark", reducedMotion: "reduce" });
  await openConnectors(page);
  const dialog = await startFixture(page, `mobile-${run}`, "[]");
  await page.screenshot({
    path: info.outputPath("mobile-dark-create.png"),
    fullPage: true,
  });
  await dialog.getByRole("button", { name: "Créer le connecteur" }).click();
  const detail = page.getByRole("dialog", { name: `Réglages de mobile-${run}` });
  await expect(
    detail.getByRole("heading", { name: `mobile-${run}`, level: 1 }),
  ).toBeVisible();
  await page.screenshot({
    path: info.outputPath("mobile-dark-detail.png"),
    fullPage: true,
  });
  await detail.getByRole("button", { name: "Fermer les réglages" }).click();
  await expect(
    page.getByRole("button", { name: `mobile-${run}`, exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.screenshot({
    path: info.outputPath("mobile-dark-connectors.png"),
    fullPage: true,
  });
});
