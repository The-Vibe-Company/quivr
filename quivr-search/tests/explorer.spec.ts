import { test, expect, type Page } from "@playwright/test";
import { fakeEngine, type Engine } from "./fake-engine";

// The Explorer (THE-1171, THE-1204) and the corpora the feed spans, against
// the fake engine's two synthetic corpora: the demo corpus and « Dépêches
// d’agence », which maps a field of its own, desk, and whose first dispatch
// was corrected once. Counts and the timeline come from the facade's own
// planning over the fake engine's counts (THE-1184).

let engine: Engine;
test.beforeEach(async ({ page }) => {
  engine = await fakeEngine(page);
  await page.setViewportSize({ width: 1440, height: 900 });
});
test.afterEach(async () => {
  await engine.close();
});

const rows = (page: Page) => page.getByRole("listbox", { name: "Documents" }).getByRole("option");
const headlines = (page: Page) => rows(page).locator(".wire-title");
const corpus = (page: Page, name: string) =>
  page.getByRole("group", { name: "Corpus", exact: true }).getByRole("button", { name: new RegExp(`^${name}`) });
const facet = (page: Page, name: string) =>
  page.getByRole("complementary", { name: "Filtres" }).getByRole("region", { name });
// A facet's values as shown: each label then its count.
const counts = (page: Page, name: string) => facet(page, name).getByRole("listitem");
const pills = (page: Page) => page.getByRole("group", { name: "Filtres actifs" });
const preview = (page: Page) => page.getByRole("complementary", { name: "Aperçu" });
// The address the next Explorer list is read from, its next pages left
// out. Start it before the action.
const listed = (page: Page) =>
  page
    .waitForRequest(
      (r) => new URL(r.url()).pathname === "/demo/explore" && !new URL(r.url()).searchParams.has("cursor"),
      { timeout: 10_000 },
    )
    .then((r) => new URL(r.url()).searchParams);

test("l’Explorer passe d’un corpus à l’autre, filtre par facettes comptées et dit quels corpus il exclut", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("navigation", { name: "Sections" }).getByRole("link", { name: "Explorer" }).click();
  // The demo corpus first; each corpus says how many documents it holds.
  await expect(headlines(page).first()).toHaveText("Orages : la grêle frappe les vergers de la vallée");
  await expect(corpus(page, "Espace démo")).toHaveAttribute("aria-pressed", "true");
  await expect(corpus(page, "Dépêches d’agence")).toHaveText("Dépêches d’agence4");
  await expect(page.getByRole("heading", { name: "Champs de Espace démo" })).toHaveCount(0);

  // One corpus at a time and its own field. Its rows come by date, though
  // the cup final was read last, and the next page loads on its own as the
  // end of the list shows.
  let next = listed(page);
  await corpus(page, "Dépêches d’agence").click();
  expect((await next).get("corpora")).toBe("wires");
  await expect(page.getByRole("searchbox", { name: "Chercher dans les documents" })).toHaveAttribute(
    "placeholder",
    "Chercher dans 4 documents",
  );
  await expect(headlines(page)).toHaveText([
    "Port reopens after three-day closure",
    "Récolte de blé : les prix reculent",
    "Cup final moved to Sunday",
    "Le conseil vote le budget",
  ]);
  await expect(counts(page, "Desk")).toHaveText(["economy2", "politics1", "sport1"]);
  await expect(counts(page, "Langue")).toHaveText(["anglais2", "français2"]);
  await expect(pills(page).getByRole("status")).toHaveText("4 documents");

  // A value narrows the list with the engine's predicate, shows as a pill,
  // and the other facets and the count follow; its own values keep theirs.
  next = listed(page);
  await facet(page, "Langue").getByRole("button", { name: /^anglais/ }).click();
  expect(JSON.parse((await next).get("metadata")!)).toEqual([{ field: "metadata.language", any_of: ["en"] }]);
  await expect(headlines(page)).toHaveText(["Port reopens after three-day closure", "Cup final moved to Sunday"]);
  await expect(counts(page, "Desk")).toHaveText(["economy1", "sport1"]);
  await expect(counts(page, "Langue")).toHaveText(["anglais2", "français2"]);
  await expect(pills(page).getByRole("status")).toHaveText("2 documents datés");

  // The corpus's own field, then every corpus: the field stays picked, the
  // demo corpus is left out and the filters column says so.
  await facet(page, "Desk").getByRole("button", { name: /^sport/ }).click();
  await expect(headlines(page)).toHaveText(["Cup final moved to Sunday"]);
  next = listed(page);
  await corpus(page, "Tous").click();
  expect((await next).get("corpora")).toBe("demo,wires");
  await expect(page.getByRole("note")).toHaveText("Le corpus « Espace démo » est exclu : il n’a pas le champ « Desk ».");
  await expect(facet(page, "Desk")).toHaveCount(0);
  await expect(headlines(page)).toHaveText(["Cup final moved to Sunday"]);
  // Removing the field's pill brings the demo corpus back; "Tout effacer" the rest.
  await pills(page).getByRole("button", { name: /^Desk sport/ }).click();
  await expect(page.getByRole("note")).toHaveCount(0);
  await pills(page).getByRole("button", { name: "Tout effacer" }).click();
  await expect(pills(page).getByRole("button")).toHaveCount(0);
  await expect(headlines(page).first()).toHaveText("Orages : la grêle frappe les vergers de la vallée");
});

test("la chronologie choisit une période en glissant, et l’adresse garde la vue au rechargement", async ({ page }) => {
  await page.goto("/?view=explorer&corpora=wires");
  const bars = page.getByRole("region", { name: "Chronologie" }).getByRole("group", { name: "Documents par jour" });
  const bar = bars.getByRole("button");
  await expect(bar).toHaveCount(3);
  await expect(rows(page)).toHaveCount(4);

  // A drag from the first day to the second picks both, as one date filter.
  const from = (await bar.nth(0).boundingBox())!;
  const to = (await bar.nth(1).boundingBox())!;
  const next = listed(page);
  await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
  await page.mouse.down();
  await page.mouse.move(to.x + to.width / 2, to.y + to.height / 2, { steps: 4 });
  await page.mouse.up();
  const [range] = JSON.parse((await next).get("metadata")!);
  expect(range).toMatchObject({ field: "metadata.published_at" });
  await expect(bar.nth(0)).toHaveAttribute("aria-pressed", "true");
  await expect(bar.nth(1)).toHaveAttribute("aria-pressed", "true");
  await expect(bar.nth(2)).toHaveAttribute("aria-pressed", "false");
  await expect(headlines(page)).toHaveText([
    "Récolte de blé : les prix reculent",
    "Cup final moved to Sunday",
    "Le conseil vote le budget",
  ]);
  await expect(pills(page).getByRole("button", { name: /^Période/ })).toBeVisible();
  await expect(pills(page).getByRole("status")).toHaveText("3 documents");

  // Zoomed in, the timeline shows the range alone; the overview widens it back.
  await page.getByRole("button", { name: "Zoomer sur la période" }).click();
  await expect(bar).toHaveCount(2);
  await page.getByRole("button", { name: "Vue d’ensemble" }).click();
  await expect(bar).toHaveCount(3);

  // A value and a document picked: the address keeps them all.
  await facet(page, "Langue").getByRole("button", { name: /^français/ }).click();
  await expect(headlines(page)).toHaveText(["Récolte de blé : les prix reculent", "Le conseil vote le budget"]);
  await rows(page).nth(1).click();
  await expect(preview(page).getByRole("heading", { level: 2 })).toHaveText("Le conseil vote le budget");
  await expect(page).toHaveURL(/selected=rec_wvote/);
  await page.reload();
  await expect(headlines(page)).toHaveText(["Récolte de blé : les prix reculent", "Le conseil vote le budget"]);
  await expect(pills(page).getByRole("button", { name: /^Langue français/ })).toBeVisible();
  await expect(bar.nth(1)).toHaveAttribute("aria-pressed", "true");
  await expect(rows(page).nth(1)).toHaveAttribute("aria-selected", "true");
  await expect(preview(page).getByRole("heading", { level: 2 })).toHaveText("Le conseil vote le budget");

  // The period's pill removes the range.
  await pills(page).getByRole("button", { name: /^Période/ }).click();
  await expect(bar.nth(1)).toHaveAttribute("aria-pressed", "false");
  await expect(page).not.toHaveURL(/range=/);
});

test("le clavier parcourt la liste, l’aperçu montre les versions, Entrée ouvre le document", async ({ page }) => {
  await page.goto("/?view=explorer&corpora=wires");
  // The first row is previewed: corrected once, with what changed.
  const card = preview(page);
  await expect(card.getByRole("heading", { level: 2 })).toHaveText("Port reopens after three-day closure");
  await expect(card.getByText("corrigée", { exact: true })).toBeVisible();
  await expect(rows(page).first()).toContainText("v2");
  const versions = card.getByRole("region", { name: "Versions" }).getByRole("listitem");
  await expect(versions).toHaveCount(2);
  await expect(versions.first()).toContainText("titre corrigé");
  await expect(versions.last()).toContainText("première version");
  await expect(card.getByRole("definition").first()).toHaveText("transport");

  // ↓ and ↑ move the selection and the preview follows, without moving the
  // list; on a desktop only the columns scroll, never the page.
  const list = page.getByRole("region", { name: "Documents trouvés" });
  const frame = await list.boundingBox();
  await rows(page).first().focus();
  await page.keyboard.press("ArrowDown");
  await expect(rows(page).nth(1)).toHaveAttribute("aria-selected", "true");
  await expect(rows(page).nth(1)).toBeFocused();
  await expect(card.getByRole("heading", { level: 2 })).toHaveText("Récolte de blé : les prix reculent");
  expect(await list.boundingBox()).toEqual(frame);
  expect(await page.evaluate(() => document.documentElement.scrollHeight <= window.innerHeight)).toBe(true);
  await expect(card.getByText("corrigée", { exact: true })).toHaveCount(0);
  await page.keyboard.press("ArrowUp");
  await expect(card.getByRole("heading", { level: 2 })).toHaveText("Port reopens after three-day closure");

  // The source opens on demand.
  await card.getByRole("button", { name: "Source XML" }).click();
  await expect(card.getByRole("region", { name: "Source XML" }).locator("pre")).toContainText('"desk": "economy"');

  // Enter opens the full document.
  await rows(page).first().focus();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/record=rec_wport/);
  await expect(page.getByRole("article").getByRole("heading", { level: 2 })).toHaveText(
    "Port reopens after three-day closure",
  );
});

test("la recherche cherche dans le corpus choisi, et une liste vide propose d’élargir", async ({ page }) => {
  await page.goto("/?view=explorer&corpora=wires");
  await expect(rows(page)).toHaveCount(4);
  const search = page.getByRole("searchbox", { name: "Chercher dans les documents" });
  let next = listed(page);
  await search.fill("port");
  const asked = await next;
  expect([asked.get("q"), asked.get("corpora")]).toEqual(["port", "wires"]);
  await expect(headlines(page)).toHaveText(["Port reopens after three-day closure"]);
  await expect(pills(page).getByRole("status")).toHaveText("1 résultat pour « port »");
  await expect(page).toHaveURL(/q=port/);

  next = listed(page);
  await search.fill("introuvable");
  await next;
  await expect(page.getByRole("heading", { name: "Aucun document pour ces critères." })).toBeVisible();
  await page.getByRole("button", { name: "Effacer la recherche" }).click();
  await expect(rows(page)).toHaveCount(4);
});

test("sur un écran étroit, l’Explorer tient en une colonne et une ligne ouvre son document", async ({ page }) => {
  await page.setViewportSize({ width: 800, height: 900 });
  await page.goto("/?view=explorer&corpora=wires");
  await expect(rows(page)).toHaveCount(4);
  await expect(preview(page)).toHaveCount(0);
  const list = (await page.getByRole("region", { name: "Documents trouvés" }).boundingBox())!;
  const filters = (await page.getByText("Filtres", { exact: true }).boundingBox())!;
  // One column: the folded filters above the list, both full width.
  expect(filters.y).toBeLessThan(list.y);
  await rows(page).first().click();
  await expect(page).toHaveURL(/record=rec_wport/);
});

test("un document de l’Explorer montre son texte, ses métadonnées, ce qui a changé et sa source brute", async ({
  page,
}) => {
  await page.goto("/?view=explorer&corpora=wires");
  await preview(page).getByRole("button", { name: "Ouvrir" }).click();
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
  await expect(rows(page)).toHaveCount(4);
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
