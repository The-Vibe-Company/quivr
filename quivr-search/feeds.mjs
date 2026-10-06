// Feed discovery for the demo facade (THE-732). The browser sends a site or
// feed address; the facade fetches it server-side under the same refusal of
// loopback, private and other non-public destinations as the core's rss kind
// (internal/netguard), checked when connecting and again on every redirect.
import { BlockList, isIP } from "node:net";
import { lookup as dnsLookup } from "node:dns";
import http from "node:http";
import https from "node:https";

const MAX_BYTES = 2 << 20;
const MAX_REDIRECTS = 5;
const TIMEOUT = 5000; // per request
const DEADLINE = 10000; // whole discovery, under the facade's 15 s request timeout
const MAX_FEEDS = 10;
const MAX_SUGGESTIONS = 12;
const MAX_LOGO_BYTES = 512 << 10;
const MAX_LOGO_TRIES = 8;
const MAX_MANIFEST_BYTES = 64 << 10;
// A smaller raster icon blurs once enlarged to a logo tile: the next
// candidate is tried, and without one the page shows the source's initial.
const MIN_LOGO_PIXELS = 32;
const FEED_ACCEPT =
  "application/rss+xml, application/atom+xml, application/feed+json, text/html;q=0.9, application/xml;q=0.8, */*;q=0.5";
const IMAGE_ACCEPT = "image/png, image/webp, image/jpeg, image/gif, image/x-icon;q=0.9, */*;q=0.5";

// Mirrors netguard.Allowed: global unicast only, minus private, loopback,
// link-local and the special-purpose ranges listed there.
const blocked = new BlockList();
for (const [net, prefix] of [
  ["0.0.0.0", 8],
  ["10.0.0.0", 8],
  ["100.64.0.0", 10],
  ["127.0.0.0", 8],
  ["169.254.0.0", 16],
  ["172.16.0.0", 12],
  ["192.0.0.0", 24],
  ["192.0.2.0", 24],
  ["192.88.99.0", 24],
  ["192.168.0.0", 16],
  ["198.18.0.0", 15],
  ["198.51.100.0", 24],
  ["203.0.113.0", 24],
  ["224.0.0.0", 3], // multicast, reserved and broadcast
])
  blocked.addSubnet(net, prefix, "ipv4");
for (const [net, prefix] of [
  ["::", 96], // unspecified, loopback and IPv4-compatible
  ["::1", 128],
  ["::ffff:0:0:0", 96],
  ["64:ff9b::", 96],
  ["64:ff9b:1::", 48],
  ["100::", 64],
  ["2001::", 32],
  ["2001:db8::", 32],
  ["2002::", 16],
  ["fc00::", 7],
  ["fe80::", 10],
  ["fec0::", 10],
  ["ff00::", 8],
])
  blocked.addSubnet(net, prefix, "ipv6");

/** Whether an IP literal is a public destination. */
export function publicAddress(address) {
  let ip = address.replace(/^\[|\]$/g, "");
  if (ip.includes("%")) return false; // zoned addresses are link-local
  // IPv4-mapped IPv6 is judged as the IPv4 address it carries (netip Unmap),
  // in dotted or hexadecimal form (the WHATWG URL parser emits the latter).
  const mapped = ip.match(/^(?:0*:)*:ffff:(\d+\.\d+\.\d+\.\d+)$/i);
  const hex = ip.match(/^(?:0*:)*:ffff:([\da-f]{1,4}):([\da-f]{1,4})$/i);
  if (mapped) ip = mapped[1];
  else if (hex) {
    const [hi, lo] = [parseInt(hex[1], 16), parseInt(hex[2], 16)];
    ip = [hi >> 8, hi & 255, lo >> 8, lo & 255].join(".");
  }
  const family = isIP(ip);
  if (!family) return false;
  return !blocked.check(ip, family === 4 ? "ipv4" : "ipv6");
}

export class FeedError extends Error {
  constructor(code, message, status = 422) {
    super(message);
    this.code = code;
    this.status = status;
  }
}

const MESSAGES = {
  invalid_url: "Saisissez une adresse web complète, par exemple https://exemple.org.",
  private_address:
    "Cette adresse pointe vers un réseau privé ou local : elle ne peut pas être collectée.",
  not_found: "Cette page n’existe pas (erreur 404).",
  unreachable: "Ce site ne répond pas. Vérifiez l’adresse puis réessayez.",
  no_feed:
    "Aucun flux RSS ou Atom n’a été trouvé à cette adresse. Essayez l’adresse du flux lui-même.",
  too_large: "Cette page est trop volumineuse pour y chercher un flux.",
};
const feedError = (code, detail) =>
  new FeedError(code, detail ? `${MESSAGES[code]} ${detail}` : MESSAGES[code]);

/**
 * Builds the guard. `privateOrigins` lists exact origins (scheme://host:port)
 * exempt from the refusal, for local test servers only.
 */
export function feedGuard({ privateOrigins = [], resolve = dnsLookup } = {}) {
  const exempt = new Set(
    privateOrigins.map((value) => {
      try {
        return new URL(value).origin;
      } catch {
        return "";
      }
    }),
  );
  exempt.delete("");

  function parse(raw) {
    let url;
    try {
      url = new URL(String(raw).trim());
    } catch {
      throw feedError("invalid_url");
    }
    if (!["http:", "https:"].includes(url.protocol) || url.username || url.password)
      throw feedError("invalid_url");
    return url;
  }

  const hostOf = (url) => url.hostname.replace(/^\[|\]$/g, "").replace(/\.$/, "").toLowerCase();

  // Literal hosts never reach DNS; refuse them up front, like CheckLiteral.
  function checkLiteral(url) {
    if (exempt.has(url.origin)) return;
    const host = hostOf(url);
    if (host === "localhost" || host.endsWith(".localhost")) throw feedError("private_address");
    if (isIP(host) && !publicAddress(host)) throw feedError("private_address");
  }

  // A resolver that refuses the whole name if any address is not public, so
  // a name cannot alternate between a public and a private answer.
  function guardedLookup(url) {
    const allowAll = exempt.has(url.origin);
    return (hostname, options, callback) => {
      if (typeof options === "function") [callback, options] = [options, {}];
      resolve(hostname, { ...options, all: true }, (error, addresses) => {
        if (error) return callback(error);
        if (!addresses?.length) return callback(Object.assign(new Error("no address"), { code: "ENOTFOUND" }));
        if (!allowAll && addresses.some((a) => !publicAddress(a.address))) {
          const refused = new Error("destination address is not allowed");
          refused.code = "EPRIVATE";
          return callback(refused);
        }
        if (options.all) return callback(null, addresses);
        callback(null, addresses[0].address, addresses[0].family);
      });
    };
  }

  /** Refuses a feed URL whose host is, or resolves to, a non-public address. */
  async function check(raw) {
    const url = parse(raw);
    checkLiteral(url);
    if (exempt.has(url.origin) || isIP(hostOf(url))) return url;
    await new Promise((done, reject) => {
      const timer = setTimeout(() => reject(feedError("unreachable")), TIMEOUT);
      guardedLookup(url)(hostOf(url), {}, (error) => {
        clearTimeout(timer);
        if (error)
          reject(error.code === "EPRIVATE" ? feedError("private_address") : feedError("unreachable"));
        else done();
      });
    });
    return url;
  }

  function get(url, budget, accept = FEED_ACCEPT, max = MAX_BYTES) {
    return new Promise((resolve, reject) => {
      const client = url.protocol === "https:" ? https : http;
      const request = client.get(
        url,
        {
          lookup: guardedLookup(url),
          agent: false,
          headers: { "User-Agent": "Quivr-Demo/1.0 (feed discovery)", Accept: accept },
        },
        (response) => {
          const status = response.statusCode || 0;
          if (status >= 300 && status < 400) {
            response.resume();
            // A redirect without a target is a broken site, not a page.
            if (!response.headers.location) return reject(feedError("unreachable"));
            return resolve({ status, location: response.headers.location });
          }
          if (status >= 400) {
            response.resume();
            return resolve({ status });
          }
          const chunks = [];
          let size = 0;
          response.on("data", (chunk) => {
            size += chunk.length;
            if (size > max) {
              request.destroy();
              reject(feedError("too_large"));
            } else chunks.push(chunk);
          });
          response.on("end", () => {
            const bytes = Buffer.concat(chunks);
            resolve({
              status,
              type: String(response.headers["content-type"] || "").toLowerCase(),
              bytes,
              body: bytes.toString("utf8"),
            });
          });
          response.on("error", () => reject(feedError("unreachable")));
        },
      );
      const timer = setTimeout(
        () => request.destroy(new Error("timeout")),
        Math.max(1, Math.min(TIMEOUT, budget)),
      );
      request.on("close", () => clearTimeout(timer));
      request.on("error", (error) =>
        reject(
          error instanceof FeedError
            ? error
            : error.code === "EPRIVATE" || error.cause?.code === "EPRIVATE"
              ? feedError("private_address")
              : feedError("unreachable"),
        ),
      );
    });
  }

  // Fetches an address, following redirects, each hop checked again, until
  // the deadline that started at `started`.
  async function follow(raw, started, accept, max) {
    let url = parse(raw);
    for (let hop = 0; ; hop++) {
      const left = DEADLINE - (Date.now() - started);
      if (left <= 0) throw feedError("unreachable");
      checkLiteral(url);
      const response = await get(url, left, accept, max);
      if (!response.location) return { ...response, url };
      if (hop >= MAX_REDIRECTS) throw feedError("unreachable");
      try {
        url = parse(new URL(response.location, url).href);
      } catch {
        throw feedError("unreachable");
      }
    }
  }

  /** Fetches a page or feed and returns the feeds it is or advertises. */
  async function discover(raw) {
    const response = await follow(raw, Date.now());
    const url = response.url;
    if (response.status === 404 || response.status === 410) throw feedError("not_found");
    if (response.status >= 400)
      throw feedError("unreachable", `(erreur ${response.status})`);
    const feed = asFeed(response.body, response.type);
    if (feed) return { feeds: [{ url: url.href, title: feed.title || url.hostname }] };
    const feeds = advertisedFeeds(response.body, url);
    if (!feeds.length) throw feedError("no_feed");
    return { feeds };
  }

  /**
   * The logo of the site behind a feed: the icons of the web app manifest its
   * home page declares (app icons, the largest), then the icons the page
   * itself declares (apple-touch-icon first), then the feed's own image, then
   * the usual /apple-touch-icon.png and /favicon.ico. A home page that refuses
   * (a bot or consent wall) is replaced by the feed's first article, which
   * declares the same icons. Raster images are recognised by their first
   * bytes; an SVG only when served as one and free of scripts. Null when none
   * answers.
   */
  async function logo(raw) {
    const started = Date.now();
    const feed = await follow(raw, started);
    if (feed.status >= 400) return null;
    let page = asFeed(feed.body, feed.type) ? null : feed;
    const links = page ? {} : feedLinks(feed.body, feed.type, feed.url);
    const home = links.site || new URL("/", feed.url);
    if (!page)
      page = await follow(home.href, started).catch(() => null);
    if ((!page || page.status >= 400) && links.article)
      page = await follow(links.article.href, started).catch(() => null);
    const read = page && page.status < 400 ? page : null;
    const manifest = read && pageManifest(read.body, read.url);
    const candidates = [
      ...(manifest ? await manifestIcons(manifest, started) : []),
      ...(read ? pageIcons(read.body, read.url) : []),
      ...(links.image ? [links.image] : []),
    ];
    for (const origin of new Set([home.origin, feed.url.origin]))
      candidates.push(new URL("/apple-touch-icon.png", origin), new URL("/favicon.ico", origin));
    const tried = new Set();
    for (const url of candidates) {
      if (tried.has(url.href)) continue;
      if (tried.size >= MAX_LOGO_TRIES || Date.now() - started >= DEADLINE) break;
      tried.add(url.href);
      try {
        const image = await follow(url.href, started, IMAGE_ACCEPT, MAX_LOGO_BYTES);
        const type = image.status < 300 && imageType(image.bytes, image.type);
        const size = type && rasterSize(image.bytes, type);
        if (type && !(size && Math.min(size.width, size.height) < MIN_LOGO_PIXELS))
          return { type, bytes: image.bytes };
      } catch {
        // Too large, refused or unreachable: try the next one.
      }
    }
    return null;
  }

  /**
   * The icons a web app manifest lists, best first: vector or "any" size,
   * then the largest. A manifest's icons resolve against its own address,
   * whose query a site may require too (a deployment id): an icon without a
   * query is also tried with the manifest's.
   */
  async function manifestIcons(url, started) {
    try {
      const response = await follow(url.href, started, "application/manifest+json, application/json;q=0.9", MAX_MANIFEST_BYTES);
      if (response.status >= 400) return [];
      const icons = JSON.parse(response.body).icons;
      if (!Array.isArray(icons)) return [];
      const ranked = [];
      for (const icon of icons.slice(0, MAX_FEEDS * 2)) {
        const src = typeof icon?.src === "string" && webURL(icon.src, response.url);
        // A monochrome icon is a stencil the system colours, not a logo.
        const purpose = String(icon?.purpose || "any").toLowerCase().split(/\s+/);
        if (!src || !purpose.some((p) => p === "any" || p === "maskable")) continue;
        const sizes = String(icon.sizes || "").toLowerCase().split(/\s+/);
        const vector = String(icon.type || "").includes("svg") || /\.svg$/i.test(src.pathname) || sizes.includes("any");
        const size = Math.max(0, ...sizes.map((s) => parseInt(s, 10) || 0));
        ranked.push({ url: src, rank: vector ? 10000 : Math.min(size, 4096) });
      }
      // The two best: the page's icons and the site's favicon keep tries.
      return ranked
        .sort((a, b) => b.rank - a.rank)
        .slice(0, 2)
        .flatMap(({ url: icon }) => {
          if (icon.search || !response.url.search) return [icon];
          const withQuery = new URL(icon);
          withQuery.search = response.url.search;
          return [icon, withQuery];
        });
    } catch {
      return [];
    }
  }

  return { check, discover, logo, exempt: (url) => exempt.has(new URL(url).origin) };
}

// Parsing is deliberately linear-time: a fetched page is untrusted and up to
// MAX_BYTES, so every pattern below is bounded or anchored and only a prefix
// of HTML pages is scanned (feed links live in <head>).
const SCAN = 512 << 10;
const MAX_TITLE = 2000;
const ENTITIES = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: " " };
function decode(text, max = 200) {
  return text
    .slice(0, MAX_TITLE * 2)
    .replace(/<!\[CDATA\[([\s\S]*?)\]\]>/g, "$1")
    .replace(/<[^<>]*>/g, "")
    .replace(/&(#x[\da-f]{1,6}|#\d{1,7}|[a-z]{1,8});/gi, (entity, name) => {
      if (name[0] === "#") {
        const code = name[1].toLowerCase() === "x" ? parseInt(name.slice(2), 16) : Number(name.slice(1));
        return Number.isFinite(code) && code > 0 && code < 0x110000 ? String.fromCodePoint(code) : "";
      }
      return ENTITIES[name.toLowerCase()] ?? entity;
    })
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, max);
}

/** The text of the first <title> element, decoded. */
function titleIn(text) {
  const open = /<title(?:\s[^<>]{0,256})?>/i.exec(text);
  if (!open) return "";
  const start = open.index + open[0].length;
  const end = text.slice(start, start + MAX_TITLE * 2).toLowerCase().indexOf("</title>");
  return end < 0 ? "" : decode(text.slice(start, start + end));
}

/** Skips a leading XML declaration, processing instructions, comments and doctype. */
function skipProlog(text) {
  let i = 0;
  for (let n = 0; n < 100; n++) {
    while (i < text.length && " \t\r\n".includes(text[i])) i++;
    const close = text.startsWith("<?", i)
      ? "?>"
      : text.startsWith("<!--", i)
        ? "-->"
        : text.slice(i, i + 9).toLowerCase() === "<!doctype"
          ? ">"
          : null;
    if (!close) break;
    const end = text.indexOf(close, i + 2);
    if (end < 0) return "";
    i = end + close.length;
  }
  return text.slice(i);
}

/** A feed document's title, or null when the body is not a feed. */
export function asFeed(body = "", type = "") {
  const text = body.replace(/^﻿/, "").trimStart();
  if (type.includes("json") || text.startsWith("{")) {
    try {
      const data = JSON.parse(text);
      if (typeof data.version === "string" && data.version.startsWith("https://jsonfeed.org/version/"))
        return { title: typeof data.title === "string" ? decode(data.title) : "" };
    } catch {
      /* not JSON */
    }
    return null;
  }
  const head = skipProlog(text.slice(0, SCAN));
  const root = /^<([\w:.-]{1,64})/.exec(head)?.[1]?.toLowerCase();
  if (!root || !["rss", "rdf:rdf", "feed"].includes(root)) return null;
  const first = head.search(root === "feed" ? /<entry[\s>]/i : /<item[\s>]/i);
  return { title: titleIn(first < 0 ? head : head.slice(0, first)) };
}

function attributes(tag) {
  const out = {};
  for (const [, name, , , a, b, c] of tag.matchAll(/([\w:-]+)\s*(=\s*("([^"]*)"|'([^']*)'|([^\s"'>]+)))?/g))
    out[name.toLowerCase()] = decode(a ?? b ?? c ?? "", 4096);
  return out;
}

const FEED_TYPES = new Set([
  "application/rss+xml",
  "application/atom+xml",
  "application/rdf+xml",
  "application/feed+json",
]);

/** Feeds advertised by an HTML page through link rel="alternate". */
export function advertisedFeeds(page, base) {
  const html = page.slice(0, SCAN);
  const pageTitle = titleIn(html);
  const seen = new Set();
  const feeds = [];
  for (const [tag] of html.matchAll(/<link\b[^<>]{0,4096}>/gi)) {
    const attrs = attributes(tag.slice(5, -1));
    const rel = (attrs.rel || "").toLowerCase().split(/\s+/);
    const type = (attrs.type || "").toLowerCase().split(";")[0].trim();
    if (!rel.includes("alternate") || !FEED_TYPES.has(type) || !attrs.href) continue;
    let url;
    try {
      url = new URL(attrs.href, base);
    } catch {
      continue;
    }
    if (!["http:", "https:"].includes(url.protocol) || url.username || url.password) continue;
    if (seen.has(url.href)) continue;
    seen.add(url.href);
    feeds.push({ url: url.href, title: attrs.title || pageTitle || url.hostname });
    if (feeds.length >= MAX_FEEDS) break;
  }
  return feeds;
}

function webURL(value, base) {
  try {
    const url = new URL(value.trim(), base);
    return ["http:", "https:"].includes(url.protocol) && !url.username && !url.password
      ? url
      : null;
  } catch {
    return null;
  }
}

/** The site a feed belongs to and its image, from the part before the first item. */
export function feedLinks(body = "", type = "", base) {
  const text = body.replace(/^\uFEFF/, "").trimStart();
  const out = {};
  const keep = (name, value) => {
    const url = typeof value === "string" && webURL(decode(value, 2048), base);
    if (url && !out[name]) out[name] = url;
  };
  if (type.includes("json") || text.startsWith("{")) {
    try {
      const data = JSON.parse(text);
      keep("site", data.home_page_url);
      keep("image", data.icon);
      keep("image", data.favicon);
      keep("article", Array.isArray(data.items) ? data.items[0]?.url : undefined);
    } catch {
      /* not JSON */
    }
    return out;
  }
  const head = text.slice(0, SCAN);
  const first = head.search(/<(item|entry)[\s>]/i);
  const channel = first < 0 ? head : head.slice(0, first);
  // RSS: <link>address</link>; Atom: <link href> without rel or rel="alternate".
  keep("site", /<link>([^<]{1,2048})<\/link>/i.exec(channel)?.[1]);
  for (const [tag] of channel.matchAll(/<link\b[^<>]{0,4096}>/gi)) {
    const attrs = attributes(tag.slice(5, -1).replace(/\/$/, ""));
    const rel = (attrs.rel || "alternate").toLowerCase();
    const kind = (attrs.type || "text/html").toLowerCase();
    if (rel === "alternate" && kind.startsWith("text/html")) keep("site", attrs.href);
  }
  const image = channel.search(/<image[\s>]/i);
  if (image >= 0)
    keep("image", /<url>([^<]{1,2048})<\/url>/i.exec(channel.slice(image, image + 4096))?.[1]);
  keep("image", /<(?:atom:)?logo>([^<]{1,2048})<\//i.exec(channel)?.[1]);
  keep("image", /<(?:atom:)?icon>([^<]{1,2048})<\//i.exec(channel)?.[1]);
  // The first article: RSS <link>address</link>, Atom <link href>.
  if (first >= 0) {
    const item = head.slice(first, first + 8192);
    keep("article", /<link>([^<]{1,2048})<\/link>/i.exec(item)?.[1]);
    for (const [tag] of item.matchAll(/<link\b[^<>]{0,4096}>/gi)) {
      const attrs = attributes(tag.slice(5, -1).replace(/\/$/, ""));
      if ((attrs.rel || "alternate").toLowerCase() === "alternate") keep("article", attrs.href);
    }
  }
  return out;
}

/**
 * The icons an HTML page declares, best first: apple-touch-icon (a full
 * colour app icon), an SVG icon (sharp at any size, though often a plain
 * glyph), then the largest declared icon. A mask-icon is a monochrome
 * stencil, not a logo.
 */
export function pageIcons(page, base) {
  const html = page.slice(0, SCAN);
  const found = [];
  for (const [tag] of html.matchAll(/<link\b[^<>]{0,4096}>/gi)) {
    const attrs = attributes(tag.slice(5, -1));
    const rel = (attrs.rel || "").toLowerCase().split(/\s+/);
    const touch = rel.some((r) => r.startsWith("apple-touch-icon"));
    if (!touch && !rel.includes("icon")) continue;
    const url = attrs.href && webURL(attrs.href, base);
    if (!url) continue;
    const vector = (attrs.type || "").includes("svg") || /\.svg$/i.test(url.pathname);
    const size = Math.max(
      0,
      ...(attrs.sizes || "").split(/\s+/).map((s) => parseInt(s, 10) || 0),
    );
    found.push({
      url,
      rank: touch ? 1000 + Math.min(size || 180, 512) : vector ? 600 : Math.min(size || 16, 512),
    });
    if (found.length >= MAX_FEEDS * 2) break;
  }
  return found.sort((a, b) => b.rank - a.rank).map((icon) => icon.url);
}

/** The web app manifest an HTML page declares, or null. */
export function pageManifest(page, base) {
  for (const [tag] of page.slice(0, SCAN).matchAll(/<link\b[^<>]{0,4096}>/gi)) {
    const attrs = attributes(tag.slice(5, -1));
    if ((attrs.rel || "").toLowerCase().split(/\s+/).includes("manifest"))
      return (attrs.href && webURL(attrs.href, base)) || null;
  }
  return null;
}

/**
 * The type of an image, or null. A raster image is read from its first
 * bytes. An SVG counts only when served as one (`declared`, its
 * Content-Type), without scripts, event handlers, javascript: links or
 * entities that expand into others. That filter is a best effort: what keeps
 * an SVG inert is that it is drawn in an <img>, where nothing runs, and
 * served under a sandbox policy.
 */
export function imageType(bytes, declared = "") {
  if (!bytes || bytes.length < 12) return null;
  if (declared.includes("image/svg+xml")) {
    const text = bytes.toString("utf8");
    // Character references and blanks could hide a tag or a scheme: read past them.
    const plain = text.replace(/&#(?:x([0-9a-f]+)|(\d+));?/gi, (_, hex, decimal) => {
      const code = parseInt(hex || decimal, hex ? 16 : 10);
      return code > 0 && code <= 0x10ffff ? String.fromCodePoint(code) : "";
    });
    const active =
      /<script|<foreignObject|\bon[a-z]+\s*=/i.test(plain) ||
      /javascript:/i.test(plain.replace(/[\x00-\x20]+/g, "")) ||
      // An entity built from entities is how an expansion bomb starts; the
      // doctype that could declare one opens the document.
      /<!ENTITY[^>]*&/i.test(text.slice(0, 4096));
    return svgRoot(text.slice(0, 4096)) && !active ? "image/svg+xml" : null;
  }
  const at = (offset, ...values) => values.every((v, i) => bytes[offset + i] === v);
  if (at(0, 0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a)) return "image/png";
  if (at(0, 0xff, 0xd8, 0xff)) return "image/jpeg";
  if (at(0, 0x47, 0x49, 0x46, 0x38)) return "image/gif";
  if (at(0, 0x52, 0x49, 0x46, 0x46) && at(8, 0x57, 0x45, 0x42, 0x50)) return "image/webp";
  if (at(0, 0x00, 0x00, 0x01, 0x00) && bytes[4] + bytes[5] > 0) return "image/x-icon";
  return null;
}

/**
 * Whether a document's root element is <svg>, past what may precede it:
 * processing instructions (the XML declaration, a stylesheet), comments and
 * a doctype, an internal subset included. A scan rather than a pattern: a
 * regular expression over repeated comments can backtrack for hours on a
 * crafted file, and this runs on the server's single thread. Each step
 * moves forward, so it reads the text once.
 */
function svgRoot(text) {
  let at = text.charCodeAt(0) === 0xfeff ? 1 : 0;
  const after = (close, from) => {
    const end = text.indexOf(close, from);
    return end < 0 ? -1 : end + close.length;
  };
  for (;;) {
    while (at < text.length && /\s/.test(text[at])) at++;
    let next;
    if (text.startsWith("<?", at)) next = after("?>", at + 2);
    else if (text.startsWith("<!--", at)) next = after("-->", at + 4);
    else if (text.slice(at, at + 9).toUpperCase() === "<!DOCTYPE") {
      const close = text.indexOf(">", at);
      const subset = text.indexOf("[", at);
      if (subset < 0 || (close >= 0 && close < subset)) next = after(">", at);
      else {
        const end = after("]", subset);
        next = end < 0 ? -1 : after(">", end);
      }
    } else return /^<svg[\s/>]/i.test(text.slice(at, at + 5));
    if (next <= at) return false;
    at = next;
  }
}

/**
 * The pixel size of a raster image, from its header (an icon file: its
 * largest image), or null when it cannot be read (an SVG has none).
 */
export function rasterSize(bytes, type) {
  try {
    if (type === "image/png") return { width: bytes.readUInt32BE(16), height: bytes.readUInt32BE(20) };
    if (type === "image/gif") return { width: bytes.readUInt16LE(6), height: bytes.readUInt16LE(8) };
    if (type === "image/x-icon") {
      let width = 0;
      let height = 0;
      for (let i = 0, count = bytes.readUInt16LE(4); i < count && 6 + 16 * i + 1 < bytes.length; i++) {
        width = Math.max(width, bytes[6 + 16 * i] || 256);
        height = Math.max(height, bytes[6 + 16 * i + 1] || 256);
      }
      return width && height ? { width, height } : null;
    }
    if (type === "image/webp") {
      const chunk = bytes.toString("latin1", 12, 16);
      if (chunk === "VP8 ") return { width: bytes.readUInt16LE(26) & 0x3fff, height: bytes.readUInt16LE(28) & 0x3fff };
      if (chunk === "VP8L") {
        const bits = bytes.readUInt32LE(21);
        return { width: (bits & 0x3fff) + 1, height: ((bits >> 14) & 0x3fff) + 1 };
      }
      if (chunk === "VP8X") return { width: bytes.readUIntLE(24, 3) + 1, height: bytes.readUIntLE(27, 3) + 1 };
      return null;
    }
    if (type === "image/jpeg") {
      // The frame header (SOFn, not DHT, JPG or DAC) carries the size; each
      // other segment is skipped by its length, so the scan moves forward.
      for (let at = 2; at + 9 < bytes.length; ) {
        if (bytes[at] !== 0xff) return null;
        const marker = bytes[at + 1];
        if (marker >= 0xc0 && marker <= 0xcf && ![0xc4, 0xc8, 0xcc].includes(marker))
          return { width: bytes.readUInt16BE(at + 7), height: bytes.readUInt16BE(at + 5) };
        at += 2 + bytes.readUInt16BE(at + 2);
      }
    }
  } catch {
    // A truncated header: size unknown.
  }
  return null;
}

/** Parses DEMO_FEED_SUGGESTIONS: a JSON array of {title, url}. */
export function parseSuggestions(raw, warn = () => {}) {
  if (!raw || !raw.trim()) return [];
  let data;
  try {
    data = JSON.parse(raw);
  } catch {
    warn("DEMO_FEED_SUGGESTIONS is not valid JSON; no suggestions are shown.");
    return [];
  }
  if (!Array.isArray(data)) {
    warn("DEMO_FEED_SUGGESTIONS must be a JSON array; no suggestions are shown.");
    return [];
  }
  const out = [];
  for (const item of data) {
    const title = typeof item?.title === "string" ? item.title.trim().slice(0, 120) : "";
    let url;
    try {
      url = new URL(item?.url);
    } catch {
      url = null;
    }
    if (!title || !url || !["http:", "https:"].includes(url.protocol) || url.username || url.password) {
      warn("DEMO_FEED_SUGGESTIONS: skipped an entry without a title and an http(s) url.");
      continue;
    }
    out.push({ title, url: url.href });
    if (out.length === MAX_SUGGESTIONS) break;
  }
  return out;
}
