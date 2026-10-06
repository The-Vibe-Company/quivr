import { test, expect } from "@playwright/test";

test("ajouter du texte, le retrouver et lire la source exacte", async ({
  page,
}, info) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  const stamp = `Témoignage démo ${Date.now()}`;
  const source = `À Marseille, les ferries pour la Corse circulent normalement.\n\nLe navire Étoile 🌟 reprend ses traversées après inspection. ${stamp}.`;
  await page.goto("/");
  // Verification always sets a password, so the page asks for it.
  const password = process.env.QUIVR_DEMO_PASSWORD;
  if (password) {
    await page.getByLabel("Mot de passe").fill(password);
    await page.getByRole("button", { name: "Ouvrir la démo" }).click();
  }
  await expect(
    page.getByRole("button", { name: "Ajouter du texte", exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: info.outputPath("desktop-home.png"),
    fullPage: true,
  });
  await page
    .getByRole("button", { name: "Ajouter du texte", exact: true })
    .first()
    .click();
  const dialog = page.getByRole("dialog", { name: "Ajouter du texte" });
  await expect(
    dialog.getByRole("button", { name: "Ajouter à la démo" }),
  ).toBeDisabled();
  await dialog.getByLabel("Votre texte").fill(source);
  await dialog.getByRole("button", { name: "Ajouter à la démo" }).click();
  await expect(
    dialog.getByText("Disponible pour la recherche", { exact: true }),
  ).toBeVisible();
  await dialog.getByRole("button", { name: "Lire le document" }).click();
  await expect(page.getByTestId("canonical-text")).toHaveText(source);
  expect(await page.getByTestId("canonical-text").textContent()).toBe(source);
  await page.keyboard.press("Escape");
  // Search is typed in the top bar; results replace the feed.
  await page
    .getByRole("searchbox", { name: "Rechercher dans le fil" })
    .fill("ferries Corse");
  await expect(
    page.getByRole("heading", { name: /^[1-9]\d*\+? articles? sur « ferries Corse »$/ }),
  ).toBeVisible();
  await expect(page.locator(".row").first()).toContainText("ferries pour la Corse");
  await page.screenshot({
    path: info.outputPath("desktop-results.png"),
    fullPage: true,
  });
  await page.locator(".row-link").first().click();
  await expect(page.getByTestId("canonical-text")).toContainText(stamp);
  await page.screenshot({
    path: info.outputPath("desktop-document.png"),
    fullPage: true,
  });
  await page.keyboard.press("Escape");
  await expect(page.locator(".row-link").first()).toBeFocused();
});
