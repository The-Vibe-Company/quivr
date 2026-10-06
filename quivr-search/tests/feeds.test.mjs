import { test } from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import { once } from "node:events";
import {
  advertisedFeeds,
  asFeed,
  feedGuard,
  feedLinks,
  imageType,
  pageIcons,
  pageManifest,
  parseSuggestions,
  publicAddress,
} from "../feeds.mjs";

// Synthetic documents only; nothing here names a real publisher.
const RSS = `<?xml version="1.0"?><!-- comment --><rss version="2.0"><channel><title>Example &amp; Co <![CDATA[News]]></title><item><title>Item</title></item></channel></rss>`;
const ATOM = `<?xml version="1.0" encoding="utf-8"?><feed xmlns="http://www.w3.org/2005/Atom"><title type="text">Atom example</title><entry><title>Entry</title></entry></feed>`;
const SITE = `<!doctype html><html><head><title>Example site</title>
<link rel="stylesheet" href="/style.css">
<link rel="alternate" type="application/rss+xml" title="Main feed" href="/feeds/main.xml">
<link type='application/atom+xml' rel='alternate home' href='https://feeds.example.org/atom'>
<link rel="alternate" type="application/rss+xml" href="/feeds/main.xml">
<link rel="alternate" type="text/html" href="/other">
<link rel="alternate" type="application/rss+xml" href="javascript:alert(1)">
</head><body></body></html>`;

test("only public unicast addresses pass, as in internal/netguard", () => {
  for (const ip of ["93.184.216.34", "8.8.8.8", "2606:4700::1111", "::ffff:8.8.8.8"])
    assert.equal(publicAddress(ip), true, ip);
  for (const ip of [
    "127.0.0.1",
    "10.1.2.3",
    "172.16.0.1",
    "192.168.1.1",
    "169.254.169.254",
    "100.64.0.1",
    "0.0.0.0",
    "198.18.0.1",
    "203.0.113.9",
    "224.0.0.1",
    "255.255.255.255",
    "::",
    "::1",
    "fe80::1",
    "fd00::1",
    "2001:db8::1",
    "ff02::1",
    "::ffff:127.0.0.1",
    "::ffff:7f00:1",
    "[::1]",
    "fe80::1%eth0",
    "not-an-ip",
  ])
    assert.equal(publicAddress(ip), false, ip);
});

test("feed documents are recognised with their title", () => {
  assert.deepEqual(asFeed(RSS), { title: "Example & Co News" });
  assert.deepEqual(asFeed(ATOM), { title: "Atom example" });
  assert.deepEqual(
    asFeed('{"version":"https://jsonfeed.org/version/1.1","title":"JSON example"}', "application/feed+json"),
    { title: "JSON example" },
  );
  assert.equal(asFeed(SITE), null);
  assert.equal(asFeed('{"hello":1}', "application/json"), null);
});

test("advertised feeds are resolved, deduplicated and filtered", () => {
  assert.deepEqual(advertisedFeeds(SITE, new URL("https://www.example.org/news/")), [
    { url: "https://www.example.org/feeds/main.xml", title: "Main feed" },
    { url: "https://feeds.example.org/atom", title: "Example site" },
  ]);
  assert.deepEqual(advertisedFeeds("<html></html>", new URL("https://example.org")), []);
});

test("hostile pages parse in linear time", () => {
  // Unclosed constructs repeated to the 2 MiB body cap used to make the
  // patterns quadratic and block the facade for minutes.
  const size = 2 << 20;
  const base = new URL("https://example.org/");
  for (const unit of ["<link", "<link rel=alternate ", "<title>", "<!--", "<?", "<![CDATA["]) {
    const page = unit.repeat(Math.ceil(size / unit.length));
    const started = performance.now();
    advertisedFeeds(page, base);
    pageIcons(page, base);
    feedLinks(page, "", base);
    feedLinks("<rss>" + page, "", base);
    asFeed(page);
    asFeed("<rss>" + page);
    asFeed("<!--" + page);
    assert.ok(performance.now() - started < 1500, `${unit} took too long`);
  }
});

test("suggestions keep valid entries only", () => {
  const warnings = [];
  const warn = (message) => warnings.push(message);
  assert.deepEqual(parseSuggestions("", warn), []);
  assert.deepEqual(parseSuggestions("nope", warn), []);
  assert.deepEqual(parseSuggestions('{"title":"x"}', warn), []);
  assert.deepEqual(
    parseSuggestions(
      JSON.stringify([
        { title: " Example news ", url: "https://news.example.org/rss" },
        { title: "No url" },
        { title: "Bad scheme", url: "ftp://example.org/feed" },
        { title: "Credentials", url: "https://user:pass@example.org/feed" },
      ]),
      warn,
    ),
    [{ title: "Example news", url: "https://news.example.org/rss" }],
  );
  assert.equal(warnings.length, 5);
});

test("discovery follows the page, refuses private hops and reports clear errors", async (t) => {
  let other;
  const server = http.createServer((req, res) => {
    const send = (status, type, body, headers = {}) => {
      res.writeHead(status, { "Content-Type": type, ...headers });
      res.end(body);
    };
    if (req.url === "/site") send(200, "text/html", SITE.replace("https://feeds.example.org/atom", "/feeds/atom"));
    else if (req.url === "/feeds/main.xml") send(200, "application/rss+xml", RSS);
    else if (req.url === "/moved") send(301, "text/plain", "", { Location: "/feeds/main.xml" });
    else if (req.url === "/to-private") send(302, "text/plain", "", { Location: `http://127.0.0.1:${other}/` });
    else if (req.url === "/to-metadata") send(302, "text/plain", "", { Location: "http://169.254.169.254/latest" });
    else if (req.url === "/loop") send(302, "text/plain", "", { Location: "/loop" });
    else if (req.url === "/nowhere") send(302, "text/plain", "");
    else if (req.url === "/plain") send(200, "text/html", "<html><title>No feed</title></html>");
    else if (req.url === "/broken") send(500, "text/plain", "boom");
    else send(404, "text/plain", "missing");
  });
  const second = http.createServer((req, res) => res.end(RSS));
  server.listen(0, "127.0.0.1");
  second.listen(0, "127.0.0.1");
  await Promise.all([once(server, "listening"), once(second, "listening")]);
  other = second.address().port;
  t.after(() => {
    server.close();
    second.close();
  });
  const base = `http://127.0.0.1:${server.address().port}`;
  const guard = feedGuard({ privateOrigins: [base, "not a url"] });
  const code = async (promise) => {
    try {
      await promise;
    } catch (error) {
      return error.code;
    }
    return "ok";
  };

  assert.deepEqual(await guard.discover(`${base}/site`), {
    feeds: [
      { url: `${base}/feeds/main.xml`, title: "Main feed" },
      { url: `${base}/feeds/atom`, title: "Example site" },
    ],
  });
  assert.deepEqual(await guard.discover(`  ${base}/moved `), {
    feeds: [{ url: `${base}/feeds/main.xml`, title: "Example & Co News" }],
  });
  assert.equal(await code(guard.discover(`${base}/to-private`)), "private_address");
  assert.equal(await code(guard.discover(`${base}/to-metadata`)), "private_address");
  assert.equal(await code(guard.discover(`${base}/loop`)), "unreachable");
  assert.equal(await code(guard.discover(`${base}/nowhere`)), "unreachable");
  assert.equal(await code(guard.discover(`${base}/plain`)), "no_feed");
  assert.equal(await code(guard.discover(`${base}/nothing`)), "not_found");
  assert.equal(await code(guard.discover(`${base}/broken`)), "unreachable");
  assert.equal(await code(guard.discover("example.org")), "invalid_url");
  assert.equal(await code(guard.discover("ftp://example.org/feed")), "invalid_url");
  assert.equal(await code(guard.discover("http://user:pw@example.org/")), "invalid_url");
  for (const target of [
    "http://localhost/feed",
    "http://app.localhost/feed",
    "http://10.0.0.1/feed",
    "http://[::1]/feed",
    "http://[::ffff:127.0.0.1]/feed",
    `http://127.0.0.1:${other}/`,
  ]) {
    assert.equal(await code(guard.discover(target)), "private_address", target);
    assert.equal(await code(guard.check(target)), "private_address", target);
  }
  assert.equal(await code(guard.check(`${base}/feeds/main.xml`)), "ok");
  // Without the exemption, the local server is refused like any private address.
  assert.equal(await code(feedGuard().discover(`${base}/site`)), "private_address");

  // Names are judged on every address they resolve to, when checking and
  // when connecting: one private answer refuses the whole name.
  const answers = {
    "public.test": [{ address: "93.184.216.34", family: 4 }],
    "mixed.test": [
      { address: "93.184.216.34", family: 4 },
      { address: "10.0.0.7", family: 4 },
    ],
    "loop.test": [{ address: "::1", family: 6 }],
  };
  const resolved = feedGuard({
    resolve: (name, options, callback) =>
      answers[name]
        ? callback(null, answers[name])
        : callback(Object.assign(new Error("nx"), { code: "ENOTFOUND" })),
  });
  assert.equal(await code(resolved.check("https://public.test/feed")), "ok");
  assert.equal(await code(resolved.check("https://mixed.test/feed")), "private_address");
  assert.equal(await code(resolved.check("https://loop.test/feed")), "private_address");
  assert.equal(await code(resolved.check("https://missing.test/feed")), "unreachable");
  assert.equal(await code(resolved.discover("http://mixed.test/")), "private_address");
  assert.equal(await code(resolved.discover("http://loop.test/")), "private_address");
});

const PNG = Buffer.from("89504e470d0a1a0a0000000d49484452", "hex");
const SVG = Buffer.from(`<?xml version="1.0"?>\n<svg xmlns="http://www.w3.org/2000/svg" width="96" height="96"><path d="M0 0h96v96H0z"/></svg>`);

test("a feed names its site and image, and a page its icons, best first", () => {
  const base = new URL("https://news.example.org/feeds/main.xml");
  assert.deepEqual(
    feedLinks(
      `<rss><channel><title>T</title><atom:link rel="self" href="/feeds/main.xml"/><link>https://www.example.org/news/</link><image><url>/img/logo.png</url></image><item><link>https://www.example.org/a</link></item></channel></rss>`,
      "application/rss+xml",
      base,
    ),
    {
      site: new URL("https://www.example.org/news/"),
      image: new URL("https://news.example.org/img/logo.png"),
      article: new URL("https://www.example.org/a"),
    },
  );
  assert.deepEqual(
    feedLinks(`<feed><link rel="self" href="/atom"/><link href="https://example.org/"/><icon>/i.png</icon><entry/></feed>`, "", base),
    { site: new URL("https://example.org/"), image: new URL("https://news.example.org/i.png") },
  );
  assert.deepEqual(
    feedLinks('{"version":"https://jsonfeed.org/version/1.1","home_page_url":"https://example.org/","icon":"javascript:x"}', "application/feed+json", base),
    { site: new URL("https://example.org/") },
  );
  const page = `<head><link rel="icon" href="/favicon.ico"><link rel="icon" type="image/svg+xml" href="/icon.svg">
<link rel="icon" sizes="192x192" href="/icon-192.png"><link rel="apple-touch-icon" href="/touch.png"><link rel="mask-icon" href="/m.svg"></head>`;
  assert.deepEqual(
    pageIcons(page, new URL("https://example.org/")).map((u) => u.pathname),
    ["/touch.png", "/icon.svg", "/icon-192.png", "/favicon.ico"],
  );
  assert.equal(
    pageManifest('<link rel="icon" href="/i.png"><link rel="manifest" href="/app.json?v=2">', new URL("https://example.org/")).href,
    "https://example.org/app.json?v=2",
  );
  assert.equal(imageType(PNG), "image/png");
  // An SVG only when served as one, and without anything that could run.
  assert.equal(imageType(SVG, "image/svg+xml"), "image/svg+xml");
  assert.equal(
    imageType(Buffer.from('\uFEFF<!-- c --><!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "x"><svg width="9"></svg>'), "image/svg+xml; charset=utf-8"),
    "image/svg+xml",
  );
  assert.equal(imageType(SVG), null);
  assert.equal(imageType(PNG, "image/svg+xml"), null);
  // Many comments ahead of a non-SVG root: read at once, not backtracked over.
  assert.equal(imageType(Buffer.from("<!--a-->".repeat(40) + "<html>"), "image/svg+xml"), null);
  // A stylesheet instruction and an internal subset may precede the root;
  // an unclosed subset ends the scan.
  const svg = (text) => imageType(Buffer.from(text), "image/svg+xml");
  assert.equal(svg('<?xml version="1.0"?><?xml-stylesheet href="a.css"?><!DOCTYPE svg [ <!ENTITY a "b"> ]><svg/>'), "image/svg+xml");
  assert.equal(svg("<?x?><!DOCTYPE svg [ <svg/>"), null);
  // A link scheme hidden behind character references or blanks still counts.
  assert.equal(svg('<svg><a href="&#106;avascript:x()"/></svg>'), null);
  assert.equal(svg('<svg><a href="java\tscript:x()"/></svg>'), null);
  assert.equal(imageType(Buffer.from("<svg xmlns='http://www.w3.org/2000/svg' onload='x()'/>"), "image/svg+xml"), null);
  assert.equal(imageType(Buffer.from("<svg><script>x()</script></svg>"), "image/svg+xml"), null);
  assert.equal(imageType(Buffer.from("<html>not an image</html>"), "image/svg+xml"), null);
});

test("a source's logo comes from its site's icons, an SVG only when served as one and inert", async (t) => {
  const server = http.createServer((req, res) => {
    const send = (status, type, body) => {
      res.writeHead(status, { "Content-Type": type });
      res.end(body);
    };
    const host = `http://${req.headers.host}`;
    if (req.url === "/rss") send(200, "application/rss+xml", `<rss><channel><link>${host}/home</link><item/></channel></rss>`);
    else if (req.url === "/home") send(200, "text/html", `<link rel="apple-touch-icon" href="/fake.png"><link rel="icon" sizes="64x64" href="/real.png">`);
    else if (req.url === "/fake.png") send(200, "image/png", "<svg onload=alert(1)>");
    else if (req.url === "/real.png") send(200, "image/png", PNG);
    else if (req.url === "/bare") send(200, "application/rss+xml", "<rss><channel><item/></channel></rss>");
    // A home page behind a bot wall: its first article declares a manifest,
    // whose vector icon needs the manifest's own query.
    else if (req.url === "/walled") send(200, "application/rss+xml", `<rss><channel><link>${host}/wall</link><item><link>${host}/article</link></item></channel></rss>`);
    else if (req.url === "/wall") send(403, "text/html", "refused");
    else if (req.url === "/article") send(200, "text/html", '<link rel="icon" href="/tiny.ico"><link rel="manifest" href="/app/manifest.json?d=7">');
    else if (req.url === "/app/manifest.json?d=7")
      send(200, "application/manifest+json", JSON.stringify({ icons: [{ src: "/app/small.png", sizes: "16x16" }, { src: "/app/logo.svg", type: "image/svg+xml", sizes: "512x512" }] }));
    else if (req.url === "/app/logo.svg") send(400, "text/plain", "deployment id is required");
    else if (req.url === "/app/logo.svg?d=7") send(200, "image/svg+xml", SVG);
    else send(404, "text/plain", "missing");
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  t.after(() => server.close());
  const base = `http://127.0.0.1:${server.address().port}`;
  const guard = feedGuard({ privateOrigins: [base] });
  const logo = await guard.logo(`${base}/rss`);
  assert.equal(logo.type, "image/png");
  assert.deepEqual(logo.bytes, PNG);
  assert.equal(await guard.logo(`${base}/bare`), null);
  const vector = await guard.logo(`${base}/walled`);
  assert.equal(vector.type, "image/svg+xml");
  assert.deepEqual(vector.bytes, SVG);
  assert.equal(await guard.logo(`${base}/missing`), null);
  // Without the exemption, the local site is refused like any private address.
  await assert.rejects(feedGuard().logo(`${base}/rss`), { code: "private_address" });
});
