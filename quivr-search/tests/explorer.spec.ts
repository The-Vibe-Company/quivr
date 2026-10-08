import { test, expect, type Page } from "@playwright/test";
import type { Facet } from "../src/lib/explore";
import { daysAgo, fakeEngine, type Engine } from "./fake-engine";

// The Explorer (THE-1171, THE-1204) and the corpora the feed spans, against
// the fake engine's two synthetic corpora: the demo corpus and « Dépêches
// d’agence », which maps a field of its own, desk, and whose first dispatch
// was corrected once. Each step says what the facade answers next; how the
// facade filters, pages and counts is tested in server.test.mjs and
// explore.test.mjs. These specs check what the page asks and shows.

// The four dispatches, as the facade lists them: the last read first.
const WIRES = ["rec_wcup", "rec_wport", "rec_wgrain", "rec_wvote"];
const facet = (field: string, values: [string, number][], interval?: Facet["interval"]): Facet => ({
  field,
  type: interval ? "datetime" : "string",
  ...(interval ? { interval } : {}),
  values: values.map(([value, count]) => ({ value, count })),
});
// The dispatches' publication days, by UTC period like the facade's counts.
const day = (n: number) => daysAgo(n, 12).slice(0, 10);
const WIRE_FACETS = {
  total: 4,
  fields: [
    facet("metadata.language", [["en", 2], ["fr", 2]]),
    facet("metadata.published_at", [[day(2), 1], [day(1), 2], [day(0), 1]], "day"),
    facet("desk", [["economy", 2], ["politics", 1], ["sport", 1]]),
  ],
};

let engine: Engine;
// What the facade answers the Explorer's next list and facets.
const answer = (listed: string[], facets: Engine["explorer"]["facets"] = WIRE_FACETS, excluded?: Engine["explorer"]["excluded"]) =>
  Object.assign(engine.explorer, { listed, facets, excluded });
test.beforeEach(async ({ page }) => {
  engine = await fakeEngine(page);
  answer(WIRES);
  await page.setViewportSize({ width: 1440, height: 900 });
});
test.afterEach(async () => {
  await engine.close();
});

const rows = (page: Page) => page.getByRole("listbox", { name: "Documents" }).getByRole("option");
const headlines = (page: Page) => rows(page).locator(".wire-title");
const corpus = (page: Page, name: string) =>
  page.getByRole("group", { name: "Corpus", exact: true }).getByRole("button", { name: new RegExp(`^${name}`) });
const facets = (page: Page, name: string) =>
  page.getByRole("complementary", { name: "Filtres" }).getByRole("region", { name });
// A facet's values as shown: each label then its count.
const counts = (page: Page, name: string) => facets(page, name).getByRole("listitem");
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
// The address the next facets are read from. Start it before the action.
const counted = (page: Page) =>
  page
    .waitForRequest((r) => new URL(r.url()).pathname === "/demo/explore/facets", { timeout: 10_000 })
    .then((r) => new URL(r.url()).searchParams);

test("l’Explorer passe d’un corpus à l’autre, filtre par facettes et dit quels corpus il exclut", async ({ page }) => {
  // A test page for now: no tab leads to it, its address opens it.
  await page.goto("/");
  await expect(page.getByRole("navigation", { name: "Sections" }).getByRole("link", { name: "Explorer" })).toHaveCount(0);
  answer(engine.ws.articles.map((a) => a.record_id), { fields: [] });
  await page.goto("/?view=explorer");
  // The demo corpus first; each corpus says how many documents it holds.
  await expect(headlines(page).first()).toHaveText("Orages : la grêle frappe les vergers de la vallée");
  await expect(corpus(page, "Espace démo")).toHaveAttribute("aria-pressed", "true");
  await expect(corpus(page, "Dépêches d’agence")).toHaveText("Dépêches d’agence4");
  await expect(page.getByRole("heading", { name: "Champs de Espace démo" })).toHaveCount(0);

  // One corpus at a time and its own field. Its rows come by date, though
  // the cup final was read last, and the next page loads on its own as the
  // end of the list shows.
  answer(WIRES);
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

  // A value asks for the engine's predicate and shows as a pill; with a
  // filter, the count is the dated documents the facade counted.
  answer(["rec_wcup", "rec_wport"], { ...WIRE_FACETS, total: 2 });
  next = listed(page);
  let asked = counted(page);
  await facets(page, "Langue").getByRole("button", { name: /^anglais/ }).click();
  expect(JSON.parse((await next).get("metadata")!)).toEqual([{ field: "metadata.language", any_of: ["en"] }]);
  const facetsAsked = await asked;
  expect([facetsAsked.get("corpora"), JSON.parse(facetsAsked.get("metadata")!)]).toEqual([
    "wires",
    [{ field: "metadata.language", any_of: ["en"] }],
  ]);
  await expect(headlines(page)).toHaveText(["Port reopens after three-day closure", "Cup final moved to Sunday"]);
  await expect(pills(page).getByRole("button", { name: /^Langue anglais/ })).toBeVisible();
  await expect(pills(page).getByRole("status")).toHaveText("2 documents datés");

  // The corpus's own field, then every corpus: the field stays picked, the
  // demo corpus is left out and the filters column says so.
  answer(["rec_wcup"], { ...WIRE_FACETS, total: 1 });
  next = listed(page);
  await facets(page, "Desk").getByRole("button", { name: /^sport/ }).click();
  expect(JSON.parse((await next).get("metadata")!)).toContainEqual({ field: "desk", any_of: ["sport"] });
  await expect(headlines(page)).toHaveText(["Cup final moved to Sunday"]);
  answer(["rec_wcup"], { ...WIRE_FACETS, total: 1 }, [{ corpus_id: "demo", fields: ["desk"] }]);
  next = listed(page);
  await corpus(page, "Tous").click();
  expect((await next).get("corpora")).toBe("demo,wires");
  await expect(page.getByRole("note")).toHaveText("Le corpus « Espace démo » est exclu : il n’a pas le champ « Desk ».");
  // A corpus's own field is offered only while it is picked alone.
  await expect(facets(page, "Desk")).toHaveCount(0);
  await expect(headlines(page)).toHaveText(["Cup final moved to Sunday"]);
  // Removing the field's pill drops its predicate; "Tout effacer" the rest.
  answer(["rec_wcup", "rec_wport"], { ...WIRE_FACETS, total: 2 });
  next = listed(page);
  await pills(page).getByRole("button", { name: /^Desk sport/ }).click();
  expect(JSON.parse((await next).get("metadata")!)).toEqual([{ field: "metadata.language", any_of: ["en"] }]);
  await expect(page.getByRole("note")).toHaveCount(0);
  answer([...engine.ws.articles, ...engine.ws.wires].map((a) => a.record_id));
  next = listed(page);
  await pills(page).getByRole("button", { name: "Tout effacer" }).click();
  expect((await next).has("metadata")).toBe(false);
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
  answer(["rec_wcup", "rec_wgrain", "rec_wvote"], { ...WIRE_FACETS, total: 3 });
  let next = listed(page);
  await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
  await page.mouse.down();
  await page.mouse.move(to.x + to.width / 2, to.y + to.height / 2, { steps: 4 });
  await page.mouse.up();
  const [range] = JSON.parse((await next).get("metadata")!);
  expect(range).toMatchObject({ field: "metadata.published_at", gte: expect.stringContaining(day(2)), lte: expect.stringContaining(day(1)) });
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

  // Zoomed in, the timeline asks for the range's span and shows it alone;
  // the overview widens it back.
  let asked = counted(page);
  await page.getByRole("button", { name: "Zoomer sur la période" }).click();
  expect((await asked).get("window")).toBe(`${range.gte},${range.lte}`);
  await expect(bar).toHaveCount(2);
  asked = counted(page);
  await page.getByRole("button", { name: "Vue d’ensemble" }).click();
  expect((await asked).has("window")).toBe(false);
  await expect(bar).toHaveCount(3);

  // A value and a document picked: the address keeps them all.
  answer(["rec_wgrain", "rec_wvote"], { ...WIRE_FACETS, total: 2 });
  next = listed(page);
  await facets(page, "Langue").getByRole("button", { name: /^français/ }).click();
  expect(JSON.parse((await next).get("metadata")!)).toContainEqual({ field: "metadata.language", any_of: ["fr"] });
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
  answer(["rec_wport"]);
  let next = listed(page);
  await search.fill("port");
  const asked = await next;
  expect([asked.get("q"), asked.get("corpora")]).toEqual(["port", "wires"]);
  await expect(headlines(page)).toHaveText(["Port reopens after three-day closure"]);
  await expect(pills(page).getByRole("status")).toHaveText("1 résultat pour « port »");
  await expect(page).toHaveURL(/q=port/);

  answer([]);
  next = listed(page);
  await search.fill("introuvable");
  await next;
  await expect(page.getByRole("heading", { name: "Aucun document pour ces critères." })).toBeVisible();
  answer(WIRES);
  next = listed(page);
  await page.getByRole("button", { name: "Effacer la recherche" }).click();
  expect((await next).has("q")).toBe(false);
  await expect(rows(page)).toHaveCount(4);
});

for (const view of ["explorer", "feed"] as const) {
  test(`${view} annonce le repli par mots-clés et efface l’avis après reprise`, async ({ page }, info) => {
    const path = view === "explorer" ? "/demo/explore" : "/v0/search";
    const seed = page.waitForResponse((r) => new URL(r.url()).pathname === path);
    await page.goto(view === "explorer" ? "/?view=explorer&corpora=wires" : "/?corpora=demo,wires&q=port");
    const data = await (await seed).json();
    let degraded = true;
    let empty = false;
    let refused = false;
    await page.route(view === "explorer" ? "**/demo/explore?**" : "**/v0/search", (route) =>
      route.fulfill({
        status: refused ? 422 : 200,
        json: refused ? { code: "unsupported_search" } : {
          ...data,
          items: empty ? [] : data.items,
          next_cursor: undefined,
          retrieval_profile: {
            name: "default", version: "v",
            ...(degraded ? { degraded: [{ reason: "vectors_unavailable", corpus_ids: ["wires"] }] } : {}),
          },
        },
      }),
    );
    const search = page.getByRole("searchbox", { name: view === "explorer" ? "Chercher dans les documents" : "Rechercher dans le fil" });
    await search.fill("port encore");
    const notice = page.getByRole("status").filter({ hasText: "Recherche par sens indisponible pendant la reconstruction, résultats par mots-clés" });
    await expect(notice).toBeVisible();
    const results = view === "explorer" ? rows(page) : page.getByRole("list", { name: "Derniers éléments" }).locator(".row");
    await expect(results.filter({ hasText: "Port reopens after three-day closure" })).toHaveCount(1);
    await page.screenshot({ path: info.outputPath(`${view}-fallback-desktop.png`), fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(notice).toBeVisible();
    await page.screenshot({ path: info.outputPath(`${view}-fallback-mobile.png`), fullPage: true });

    empty = true;
    await search.fill("introuvable");
    await expect(page.getByRole("heading", { name: view === "explorer" ? "Aucun document pour ces critères." : "Aucun article ne parle de ça." })).toBeVisible();
    await expect(notice).toBeVisible();
    degraded = false;
    empty = false;
    await search.fill("reprise");
    await expect(results.filter({ hasText: "Port reopens after three-day closure" })).toHaveCount(1);
    await expect(notice).toHaveCount(0);
    refused = true;
    await search.fill("sens");
    await expect(page.getByRole("alert")).toContainText("La recherche par sens est indisponible. Réessayez par mots-clés.");
    await search.fill("");
    await expect(notice).toHaveCount(0);
  });
}

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
