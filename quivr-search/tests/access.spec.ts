import { test, expect } from "@playwright/test";

test("la façade protège la session et borne les routes au corpus de démo", async ({
  request,
}) => {
  expect((await request.get("/demo/session")).status()).toBe(401);
  expect(
    (
      await request.post("/demo/login", { data: { password: "incorrect" } })
    ).status(),
  ).toBe(401);
  const login = await request.post("/demo/login", {
    data: { password: process.env.QUIVR_DEMO_PASSWORD || "local-browser-demo" },
  });
  expect(login.status()).toBe(200);
  expect(login.headers()["set-cookie"]).toContain("HttpOnly");
  expect(login.headers()["set-cookie"]).toContain("SameSite=Strict");
  const session = await (await request.get("/demo/session")).json();
  // Same-origin, so the corpus fence (not the origin check) must refuse these.
  const origin = { Origin: new URL(test.info().project.use.baseURL!).origin };
  expect(session.corpus_id).toBeTruthy();
  expect(
    (
      await request.post("/v0/search", {
        headers: origin,
        data: { query: "texte", corpus_ids: ["another-corpus"] },
      })
    ).status(),
  ).toBe(403);
  // A write to another corpus is one the session no longer knows: the page reloads.
  const write = await request.post("/v0/records", {
    headers: origin,
    data: {
      source: { corpus_id: "another-corpus", namespace: "web-demo" },
    },
  });
  expect(write.status()).toBe(409);
  expect((await write.json()).code).toBe("demo_corpus_changed");
  expect((await request.get("/v0/corpora")).status()).toBe(404);
  expect(
    (await request.get("/v0/records/unknown/versions/unknown")).status(),
  ).toBe(404);
  expect((await request.get("/v0/ingestion-receipts/unknown")).status()).toBe(
    404,
  );
  expect(
    (
      await request.post("/v0/search", {
        headers: { Origin: "https://untrusted.example" },
        data: { query: "texte", corpus_ids: [session.corpus_id] },
      })
    ).status(),
  ).toBe(403);
});
