import { useState } from "react";
import { NotePencil } from "@phosphor-icons/react";
import { HAND_NAMESPACE } from "../../lib/feed";

// Deep tones for the initial shown until a logo loads, or instead of one.
const TONES = ["#0a3d62", "#b4232c", "#1f2321", "#0f6e8a", "#5b3a8c", "#9a4b16", "#2c6b3f", "#7a2848"];
// Each source's logo, made round once per page; null when it has none, which
// is asked again after a while (the site, or the session, may come back).
const ready = new Map<string, string | null>();
const RETRY_MS = 10 * 60 * 1000;
const SCAN = 64;
const OUT = 96;

function tone(name: string) {
  let hash = 0;
  for (const char of name) hash = (hash * 31 + char.codePointAt(0)!) >>> 0;
  return TONES[hash % TONES.length];
}

/** The first letter of a name, past a leading article. */
export function initial(name: string) {
  const words = name
    .trim()
    .replace(/^(le|la|les|l’|l'|the)\s*/i, "")
    .split(/\s+/);
  return (words[0]?.[0] || "?").toUpperCase();
}

/**
 * Fits a site icon in a circle. An icon on a plain background (its four
 * corners share one colour) is trimmed to its mark and centred on that
 * colour. Otherwise transparent margins are trimmed: a tile with solid
 * edges, rounded or not, fills the circle, and a lone mark sits on white.
 */
function round(image: HTMLImageElement) {
  const { naturalWidth: w, naturalHeight: h } = image;
  const scan = document.createElement("canvas");
  scan.width = scan.height = SCAN;
  const context = scan.getContext("2d", { willReadFrequently: true });
  if (!w || !h || !context) return image.src;
  context.drawImage(image, 0, 0, SCAN, SCAN);
  const data = context.getImageData(0, 0, SCAN, SCAN).data;
  const at = (x: number, y: number) => (y * SCAN + x) * 4;
  const corners = [at(0, 0), at(SCAN - 1, 0), at(0, SCAN - 1), at(SCAN - 1, SCAN - 1)];
  const near = (i: number, j: number, tolerance: number) =>
    Math.abs(data[i] - data[j]) + Math.abs(data[i + 1] - data[j + 1]) + Math.abs(data[i + 2] - data[j + 2]) <
    tolerance;
  const plain =
    corners.every((i) => data[i + 3] > 200) &&
    corners.every((i) => near(i, corners[0], 48));
  const empty = (x: number, y: number) =>
    data[at(x, y) + 3] < 17 || (plain && near(at(x, y), corners[0], 60));
  let [x0, y0, x1, y1] = [SCAN, SCAN, -1, -1];
  for (let y = 0; y < SCAN; y++)
    for (let x = 0; x < SCAN; x++)
      if (!empty(x, y)) {
        x0 = Math.min(x0, x);
        y0 = Math.min(y0, y);
        x1 = Math.max(x1, x);
        y1 = Math.max(y1, y);
      }
  if (x1 < 0) return null;
  // The middle half of each edge, one pixel in from its antialiasing,
  // decides: rounded corners do not count.
  let edge = 0;
  let solid = 0;
  const count = (x: number, y: number) => {
    edge++;
    if (data[at(x, y) + 3] > 160) solid++;
  };
  const [qx, qy] = [(x1 - x0) / 4, (y1 - y0) / 4];
  for (let x = Math.round(x0 + qx); x <= x1 - qx; x++)
    [Math.min(y0 + 1, y1), Math.max(y1 - 1, y0)].forEach((y) => count(x, y));
  for (let y = Math.round(y0 + qy); y <= y1 - qy; y++)
    [Math.min(x0 + 1, x1), Math.max(x1 - 1, x0)].forEach((x) => count(x, y));
  const fill = !plain && edge > 0 && solid / edge >= 0.6;
  const [sx, sy] = [(x0 * w) / SCAN, (y0 * h) / SCAN];
  const [sw, sh] = [((x1 - x0 + 1) * w) / SCAN, ((y1 - y0 + 1) * h) / SCAN];
  const out = document.createElement("canvas");
  out.width = out.height = OUT;
  const draw = out.getContext("2d");
  if (!draw) return image.src;
  draw.imageSmoothingQuality = "high";
  if (!fill) {
    const [r, g, b] = plain ? data.slice(corners[0], corners[0] + 3) : [255, 255, 255];
    draw.fillStyle = `rgb(${r} ${g} ${b})`;
    draw.fillRect(0, 0, OUT, OUT);
  }
  // A mark fits inside the circle, corners included, with a margin.
  const scale = fill ? Math.max(OUT / sw, OUT / sh) : (OUT * 0.84) / Math.hypot(sw, sh);
  const [dw, dh] = [sw * scale, sh * scale];
  draw.drawImage(image, sx, sy, sw, sh, (OUT - dw) / 2, (OUT - dh) / 2, dw, dh);
  return out.toDataURL("image/png");
}

/**
 * A source's logo: the icon of the site behind an RSS source, fetched by the
 * demo server, over a tile with the source's initial while it loads or when
 * the site has none. Decorative: the source's name is always written beside.
 */
export function SourceLogo({
  namespace,
  connectorId,
  size = "large",
}: {
  namespace: string;
  connectorId?: string;
  size?: "large" | "small";
}) {
  const [, setLoaded] = useState(0);
  if (namespace === HAND_NAMESPACE)
    return (
      <span className="logo" data-size={size} data-kind="hand" aria-hidden="true">
        <NotePencil size={size === "large" ? 15 : 13} />
      </span>
    );
  // A search hit older than the feed: its source is not known here.
  if (!namespace)
    return <span className="logo" data-size={size} data-kind="unknown" aria-hidden="true" />;
  const logo = connectorId ? ready.get(connectorId) : null;
  return (
    <span
      className="logo"
      data-size={size}
      style={{ background: tone(namespace) }}
      aria-hidden="true"
    >
      {initial(namespace)}
      {logo && <img src={logo} alt="" />}
      {connectorId && logo === undefined && (
        <img
          className="logo-source"
          src={`/demo/sources/logo/${encodeURIComponent(connectorId)}`}
          alt=""
          decoding="async"
          onLoad={(event) => {
            if (!ready.has(connectorId)) {
              const image = event.currentTarget;
              try {
                ready.set(connectorId, round(image));
              } catch {
                ready.set(connectorId, image.src);
              }
            }
            setLoaded((n) => n + 1);
          }}
          onError={() => {
            if (!ready.has(connectorId)) {
              ready.set(connectorId, null);
              setTimeout(() => ready.delete(connectorId), RETRY_MS);
            }
            setLoaded((n) => n + 1);
          }}
        />
      )}
    </span>
  );
}
