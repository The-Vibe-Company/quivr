import { test, expect, type Page } from "@playwright/test";
import { fakeEngine, type Engine } from "./fake-engine";

// The Explorer and the corpora the feed spans (THE-1171), against the fake
// engine's two synthetic corpora: the demo corpus and « Dépêches d’agence »,
// which maps a field of its own, desk. Facet counts come from the facade's
// own planning over the fake engine's counts (THE-1184).

let engine: Engine;
test.beforeEach(async ({ page }) => {
  engine = await fakeEngine(page);
  await page.setViewportSize({ width: 1440, height: 900 });
});
test.afterEach(async () => {
  await engine.close();
});

const documents = (page: Page) =>
  page.getByRole("list", { name: "Documents" }).getByRole("heading", { level: 3 });
const corpusChip = (page: Page, name: string) =>
  page.getByRole("group", { name: "Corpus explorés" }).getByRole("button", { name });
const facet = (page: Page, name: string) =>
  page.getByRole("complementary", { name: "Filtres" }).getByRole("region", { name });
// A facet's values as shown: each label then its count.
const counts = (page: Page, name: string) => facet(page, name).getByRole("listitem");
// The address the next Explorer page is read from. Start it before the click.
const listed = (page: Page) =>
  page
    .waitForRequest((r) => new URL(r.url()).pathname === "/demo/explore", { timeout: 10_000 })
    .then((r) => new URL(r.url()).searchParams);

test("l’Explorer parcourt un corpus par facettes, garde un champ propre et dit quels corpus il exclut", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("navigation", { name: "Sections" }).getByRole("link", { name: "Explorer" }).click();
  // The demo corpus first, newest first; its own fields are none.
  await expect(documents(page).first()).toHaveText("Orages : la grêle frappe les vergers de la vallée");
  await expect(page.getByRole("heading", { name: "Champs de Espace démo" })).toHaveCount(0);

  // One other corpus: the newest first, a page at a time, and its own field.
  await corpusChip(page, "Dépêches d’agence").click();
  await expect(documents(page)).toHaveCount(3);
  let next = listed(page);
  await corpusChip(page, "Espace démo").click();
  expect((await next).get("corpora")).toBe("wires");
  await expect(documents(page)).toHaveText([
    "Port reopens after three-day closure",
    "Récolte de blé : les prix reculent",
    "Cup final moved to Sunday",
  ]);
  await page.getByRole("button", { name: "Afficher plus de documents" }).click();
  await expect(documents(page)).toHaveCount(4);
  // Each value says how many documents have it, the most frequent first.
  await expect(counts(page, "Desk")).toHaveText(["economy2", "politics1", "sport1"]);
  await expect(counts(page, "Langue")).toHaveText(["anglais2", "français2"]);

  // A date is a histogram: a day's bar narrows the list and the counts; the
  // other days stay offered, and the path widens back to every date.
  const dates = facet(page, "Date de publication");
  next = listed(page);
  await dates.getByRole("button", { name: /: 2 documents$/ }).click();
  expect(JSON.parse((await next).get("metadata")!)[0]).toMatchObject({ field: "metadata.published_at" });
  await expect(documents(page)).toHaveText(["Récolte de blé : les prix reculent", "Cup final moved to Sunday"]);
  await expect(counts(page, "Langue")).toHaveText(["anglais1", "français1"]);
  await expect(dates.getByRole("button", { name: /: 1 document$/ })).toHaveCount(2);
  await dates.getByRole("button", { name: "Toutes les dates" }).click();
  await expect(documents(page)).toHaveCount(3);

  // A common facet narrows the list with the engine's predicate; the other
  // facets count under it, its own values keep theirs.
  next = listed(page);
  await facet(page, "Langue").getByRole("button", { name: /^anglais/ }).click();
  expect(JSON.parse((await next).get("metadata")!)).toEqual([{ field: "metadata.language", any_of: ["en"] }]);
  await expect(documents(page)).toHaveText(["Port reopens after three-day closure", "Cup final moved to Sunday"]);
  await expect(counts(page, "Desk")).toHaveText(["economy1", "sport1"]);
  await expect(counts(page, "Langue")).toHaveText(["anglais2", "français2"]);

  // The corpus's own field, then a second corpus: the field stays picked,
  // the demo corpus is left out and the page says so.
  await facet(page, "Desk").getByRole("button", { name: /^sport/ }).click();
  await expect(documents(page)).toHaveText(["Cup final moved to Sunday"]);
  next = listed(page);
  await corpusChip(page, "Espace démo").click();
  expect((await next).get("corpora")).toBe("wires,demo");
  await expect(page.getByRole("note")).toHaveText(
    "Le corpus « Espace démo » est exclu : il n’a pas le champ « Desk ».",
  );
  await expect(facet(page, "Desk")).toHaveCount(0);
  await expect(documents(page)).toHaveText(["Cup final moved to Sunday"]);
  // Removing the field's filter brings the demo corpus back.
  const picks = page.getByRole("group", { name: "Filtres actifs" });
  await picks.getByRole("button", { name: /Desk : sport/ }).click();
  await expect(page.getByRole("note")).toHaveCount(0);
  await expect(documents(page)).toHaveText(["Port reopens after three-day closure", "Cup final moved to Sunday"]);
  await picks.getByRole("button", { name: "Tout effacer" }).click();
  await expect(documents(page).first()).toHaveText("Orages : la grêle frappe les vergers de la vallée");
});

test("un document de l’Explorer montre son texte, ses métadonnées, ce qui a changé et sa source brute", async ({
  page,
}) => {
  await page.goto("/?view=explorer&corpora=wires");
  await page.getByRole("link", { name: "Port reopens after three-day closure" }).click();
  await expect(page).toHaveURL(/record=rec_wport/);
  const article = page.getByRole("article");
  await expect(article.getByRole("heading", { level: 2 })).toHaveText("Port reopens after three-day closure");
  const meta = article.getByRole("region", { name: "Métadonnées" });
  for (const [label, value] of [
    ["Langue", "anglais"],
    ["Sujets", "transport"],
    ["Desk", "economy"],
    ["Corpus", "Dépêches d’agence"],
  ])
    await expect(meta.locator("dl > div").filter({ has: page.getByRole("term").getByText(label, { exact: true }) })).toHaveText(label + value);

  // Two Versions: the current one compared with the earlier, word by word.
  const changes = article.getByRole("region", { name: /Changements depuis la version/ });
  await expect(changes.locator("del").first()).toContainText("remains closed");
  await expect(changes.locator("ins").first()).toContainText("reopens");
  await article.getByRole("button", { name: /Version 1/ }).click();
  await expect(article.getByRole("heading", { level: 2 })).toHaveText("Port remains closed for a third day");
  await expect(article.getByText("Vous lisez une version antérieure.")).toBeVisible();

  // The raw source opens on demand: the current Version's source file
  // described, and its extensions.
  await article.getByRole("button", { name: "Revenir à la version actuelle" }).click();
  await expect(article.getByRole("heading", { level: 2 })).toHaveText("Port reopens after three-day closure");
  const raw = article.locator("details");
  await expect(raw.locator("pre")).toBeHidden();
  await raw.getByText("Source brute").click();
  await expect(raw.getByRole("list", { name: "Fichiers sources" })).toContainText("application/vnd.iptc.g2.newsitem+xml");
  await expect(raw.locator("pre")).toContainText('"desk": "economy"');

  await article.getByRole("button", { name: "Tous les documents" }).click();
  await expect(documents(page)).toHaveCount(3);
});

test("le fil et la recherche couvrent les corpus choisis, et une dépêche arrive en direct", async ({ page }) => {
  await page.goto("/");
  const rows = page.getByRole("list", { name: "Derniers éléments" }).locator(".row");
  await expect(rows).toHaveCount(8);
  const filters = page.getByRole("group", { name: "Filtrer le fil" });
  await filters.getByRole("button", { name: /^Corpus/ }).click();
  await page.getByRole("dialog", { name: "Corpus" }).getByRole("button", { name: "Dépêches d’agence" }).click();
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/corpora=demo%2Cwires|corpora=demo,wires/);
  await expect(rows).toHaveCount(12);
  // A row of the other corpus names it.
  await expect(rows.filter({ hasText: "Port reopens" }).locator(".row-corpus")).toHaveText("Dépêches d’agence");
  await expect(rows.filter({ hasText: "Orages : la grêle" }).locator(".row-corpus")).toHaveCount(0);

  // A new item of that corpus arrives live in the feed.
  engine.arriveWire();
  await expect(rows.first()).toContainText("Flash : le tunnel rouvre à la circulation");

  // Search spans both corpora.
  const searched = page.waitForRequest((r) => new URL(r.url()).pathname === "/v0/search");
  await page.getByRole("searchbox", { name: "Rechercher dans le fil" }).fill("port");
  expect((await searched).postDataJSON().corpus_ids).toEqual(["demo", "wires"]);
  await expect(rows.filter({ hasText: "Port reopens after three-day closure" })).toHaveCount(1);
});
