import { test, expect, type Page } from "@playwright/test";
import { fakeEngine, type Engine } from "./fake-engine";

// The "Recherche approfondie" (the `deep` search profile) against the fake
// engine: never a paid re-ranker, never the live demo.

let engine: Engine;
test.beforeEach(async ({ page }) => {
  engine = await fakeEngine(page);
});
test.afterEach(async () => {
  await engine.close();
});

const DEFAULT = { name: "default", provider: { kind: "plugin", plugin_id: "jev.rerank" } };
const DEEP = { name: "deep", provider: { kind: "plugin", plugin_id: "jev.rerank" } };
const rows = (page: Page) =>
  page.getByRole("list", { name: "Derniers éléments" }).locator(".row");
const deepSwitch = (page: Page) =>
  page.getByRole("switch", { name: "Recherche approfondie" });

test("sans profil deep servi par un plugin, la recherche reste normale", async ({
  page,
}) => {
  engine.options.profiles = [DEFAULT];
  await page.goto("/?q=gr%C3%AAle");
  await expect(page.getByRole("heading", { name: /3 articles sur « grêle »/ })).toBeVisible();
  await expect(page.getByRole("switch", { name: "Idées proches" })).toBeVisible();
  await expect(deepSwitch(page)).toHaveCount(0);
  expect(engine.searches.at(-1)).toEqual({ query: "grêle", mode: "hybrid", limit: 50 });
});

test("la recherche approfondie re-classe, dit pourquoi et ce qu’elle a coûté, au clavier", async ({
  page,
}) => {
  engine.options.profiles = [DEFAULT, DEEP];
  await page.goto("/?q=gr%C3%AAle&near=0");
  await expect(page.getByRole("heading", { name: /3 articles sur « grêle »/ })).toBeVisible();
  await expect(deepSwitch(page)).toHaveAttribute("aria-checked", "false");
  await expect(deepSwitch(page)).toHaveAccessibleDescription(/appel payant/);

  // On by keyboard: hybrid candidates, deep profile, "Idées proches" locked on.
  await deepSwitch(page).focus();
  await page.keyboard.press("Space");
  await expect(deepSwitch(page)).toHaveAttribute("aria-checked", "true");
  await expect(deepSwitch(page)).toBeFocused();
  const near = page.getByRole("switch", { name: "Idées proches" });
  await expect(near).toHaveAttribute("aria-checked", "true");
  await expect(near).toHaveAttribute("aria-disabled", "true");
  await expect
    .poll(() => engine.searches.at(-1))
    .toEqual({ query: "grêle", mode: "hybrid", limit: 50, profile: "deep" });

  // Each result says how likely it answers; the summary, time and paid call.
  const status = page.locator(".deep-status");
  await expect(status).toContainText("re-classée par pertinence");
  await expect(status).toContainText("en 1,4 s");
  await expect(status).toContainText("1 appel payant (0,4 centime)");
  await expect(rows(page).first()).toContainText("Cellule orageuse");
  await expect(rows(page).first().locator(".row-why")).toHaveText(/^89\s%\sde chances de répondre$/);
  await expect(rows(page).last().locator(".row-why")).toHaveText(/^12\s%/);

  // The re-ranker unavailable: hybrid order, said once and on every result.
  engine.options.deep = "fallback";
  await page.getByRole("searchbox").fill("orage");
  await expect(status).toContainText("non re-classée");
  await expect(status).toContainText("aucun appel payant");
  await expect(status).toContainText("il n’a pas répondu dans le délai prévu");
  await expect(rows(page).first().locator(".row-why")).toHaveText("Non re-classé");

  // Past its deadline: an error, and a way back to the normal search.
  engine.options.deep = "deadline";
  await page.getByRole("searchbox").fill("grêle");
  const alert = page.getByRole("alert");
  await expect(alert).toContainText("La recherche approfondie a pris trop de temps");
  await alert.getByRole("button", { name: "Revenir à la recherche normale" }).click();
  await expect(deepSwitch(page)).toHaveAttribute("aria-checked", "false");
  await expect(page.getByRole("heading", { name: /3 articles sur « grêle »/ })).toBeVisible();
  expect(engine.searches.at(-1)).toEqual({ query: "grêle", mode: "lexical", limit: 50 });
  await expect(page.locator(".row-why")).toHaveCount(0);

  // The choice is not remembered: a reload searches with the default profile.
  await deepSwitch(page).click();
  await expect.poll(() => engine.searches.at(-1)?.profile).toBe("deep");
  expect(page.url()).not.toContain("deep");
  await page.reload();
  await expect(deepSwitch(page)).toHaveAttribute("aria-checked", "false");
  await expect(page.getByRole("heading", { name: /3 articles sur « grêle »/ })).toBeVisible();
  expect(engine.searches.at(-1)?.profile).toBeUndefined();
});
