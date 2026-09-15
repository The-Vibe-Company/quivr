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
  expect(session.corpus_id).toBeTruthy();
  expect(
    (
      await request.post("/v0/search", {
        data: { query: "texte", corpus_ids: ["another-corpus"] },
      })
    ).status(),
  ).toBe(403);
  expect(
    (
      await request.post("/v0/records", {
        data: {
          source: { corpus_id: "another-corpus", namespace: "web-demo" },
        },
      })
    ).status(),
  ).toBe(403);
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
