import { test, expect } from "@playwright/test";

test.beforeEach(async ({ page }) => {
  await page.request.post("/demo/login", {
    data: { password: process.env.QUIVR_DEMO_PASSWORD || "local-browser-demo" },
  });
  await page.goto("/");
  await expect(
    page.getByRole("button", { name: "Ajouter du texte", exact: true }),
  ).toBeVisible();
});

test("mobile sombre, clavier, brouillon et mouvement réduit", async ({
  page,
}, info) => {
  await page.setViewportSize({ width: 375, height: 812 });
  await page.emulateMedia({ colorScheme: "dark", reducedMotion: "reduce" });
  await page.screenshot({
    path: info.outputPath("mobile-dark.png"),
    fullPage: true,
  });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await page.keyboard.press("/");
  await expect(page.getByRole("searchbox")).toBeFocused();
  await page
    .getByRole("button", { name: "Ajouter du texte", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Votre texte").fill("Brouillon à conserver 🌟");
  await page.keyboard.press("Tab");
  expect(
    await page.evaluate(() => !!document.activeElement?.closest("dialog")),
  ).toBe(true);
  await page.screenshot({
    path: info.outputPath("mobile-add.png"),
    fullPage: true,
  });
  await page.keyboard.press("Escape");
  await expect(
    page.getByRole("button", { name: "Ajouter du texte", exact: true }),
  ).toBeFocused();
  await page.reload();
  await page
    .getByRole("button", { name: "Ajouter du texte", exact: true })
    .click();
  await expect(dialog.getByLabel("Votre texte")).toHaveValue(
    "Brouillon à conserver 🌟",
  );
  expect(await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(
    true,
  );
});

test("recherche vide, panne réseau et reprise explicite", async ({
  page,
}, info) => {
  // Exact words only: "Idées proches" off.
  await page.getByRole("searchbox").fill("motintrouvable" + Date.now());
  await page.getByRole("switch", { name: "Idées proches" }).click();
  await expect(page.getByText("Aucun article ne parle de ça.")).toBeVisible();
  await page.route("**/v0/search", (route) => route.abort());
  await page.getByRole("searchbox").fill("ferries");
  await expect(page.getByRole("alert")).toBeVisible();
  await page.screenshot({
    path: info.outputPath("search-error.png"),
    fullPage: true,
  });
  await page.unroute("**/v0/search");
  await page.getByRole("button", { name: "Réessayer", exact: true }).click();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(
    page.getByRole("heading", { name: /articles? sur « ferries »/ }),
  ).toBeVisible();
});

test("un envoi dont la réponse est perdue se rejoue sans nouvel ajout", async ({
  page,
}) => {
  await page
    .getByRole("button", { name: "Ajouter du texte", exact: true })
    .click();
  await page
    .getByLabel("Votre texte")
    .fill("Une réponse réseau perdue ne doit pas dupliquer ce texte.");
  const ids: string[] = [];
  let first = true;
  await page.route("**/v0/records", async (route) => {
    ids.push(route.request().postDataJSON().idempotency_key);
    if (first) {
      first = false;
      await route.fetch();
      await route.abort();
    } else await route.continue();
  });
  await page.getByRole("button", { name: "Ajouter à la démo" }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  await page.getByRole("button", { name: "Ajouter à la démo" }).click();
  await expect(
    page.getByText("Disponible pour la recherche", { exact: true }),
  ).toBeVisible({ timeout: 45000 });
  expect(ids).toHaveLength(2);
  expect(ids[0]).toBe(ids[1]);
});

test("le stockage de brouillon indisponible ne bloque pas un ajout", async ({
  page,
}) => {
  await page.evaluate(() => {
    Storage.prototype.setItem = () => {
      throw new DOMException("Storage unavailable", "QuotaExceededError");
    };
  });
  await page
    .getByRole("button", { name: "Ajouter du texte", exact: true })
    .click();
  await page
    .getByLabel("Votre texte")
    .fill("Un navigateur sans stockage local peut ajouter ce texte.");
  await page.getByRole("button", { name: "Ajouter à la démo" }).click();
  await expect(
    page.getByText("Disponible pour la recherche", { exact: true }),
  ).toBeVisible({ timeout: 45000 });
});

test("un enrichissement bloqué conserve la recherche lexicale sans annoncer une attente", async ({
  page,
}) => {
  // Supported receipt state injected at the public HTTP boundary; no core internals mocked.
  await page.route("**/v0/records", (route) =>
    route.fulfill({
      json: {
        receipt_id: "fixture",
        state: "resolved",
        outcome: "created",
        source: route.request().postDataJSON().source,
        record_id: "record",
        version_id: "version",
        availability: { searchable: true, state: "retrieval_ready", is_current: true },
        processing: { state: "blocked" },
        diagnostics: [{ code: "derivation_conflict", message: "Conflicting derivation output", retryable: false }],
      },
    }),
  );
  await page
    .getByRole("button", { name: "Ajouter du texte", exact: true })
    .click();
  await page
    .getByLabel("Votre texte")
    .fill("Texte dont la recherche par sens est bloquée.");
  await page.getByRole("button", { name: "Ajouter à la démo" }).click();
  await expect(
    page.getByText("Disponible pour la recherche", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText(
      "La recherche par mots-clés reste disponible. La recherche par sens n’a pas pu être préparée.",
    ),
  ).toBeVisible();
  await expect(page.getByText(/se prépare encore/)).toHaveCount(0);
});
