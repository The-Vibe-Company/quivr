// A word-level diff between two texts of an article, for the reader's
// correction notice. Versions are immutable, so both texts are read as they
// are; the comparison runs in the browser.

export interface Change {
  kind: "same" | "removed" | "added";
  text: string;
}

/** Above this many word pairs, only the common start and end are matched. */
const MAX_CELLS = 4_000_000;

// Each word keeps its trailing spaces, so runs join back into the text.
const words = (text: string) => text.trim().match(/\S+\s*/g) || [];

function push(out: Change[], kind: Change["kind"], text: string) {
  const last = out.at(-1);
  if (last?.kind === kind) last.text += text;
  else out.push({ kind, text });
}

/** The runs of words kept, removed from `before` and added in `after`. */
export function diffWords(before: string, after: string): Change[] {
  const a = words(before);
  const b = words(after);
  const key = (w: string) => w.trimEnd();
  let start = 0;
  while (start < a.length && start < b.length && key(a[start]) === key(b[start]))
    start++;
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && key(a[endA - 1]) === key(b[endB - 1])) {
    endA--;
    endB--;
  }
  const out: Change[] = [];
  for (const w of b.slice(0, start)) push(out, "same", w);
  const midA = a.slice(start, endA);
  const midB = b.slice(start, endB);
  const n = midA.length;
  const m = midB.length;
  if (n * m > MAX_CELLS) {
    for (const w of midA) push(out, "removed", w);
    for (const w of midB) push(out, "added", w);
  } else {
    // Longest common subsequence, read from the start.
    const lcs = new Uint32Array((n + 1) * (m + 1));
    const at = (i: number, j: number) => i * (m + 1) + j;
    for (let i = n - 1; i >= 0; i--)
      for (let j = m - 1; j >= 0; j--)
        lcs[at(i, j)] =
          key(midA[i]) === key(midB[j])
            ? lcs[at(i + 1, j + 1)] + 1
            : Math.max(lcs[at(i + 1, j)], lcs[at(i, j + 1)]);
    let i = 0;
    let j = 0;
    while (i < n || j < m) {
      if (i < n && j < m && key(midA[i]) === key(midB[j])) {
        push(out, "same", midB[j]);
        i++;
        j++;
      } else if (i < n && (j === m || lcs[at(i + 1, j)] >= lcs[at(i, j + 1)])) {
        push(out, "removed", midA[i++]);
      } else {
        push(out, "added", midB[j++]);
      }
    }
  }
  for (const w of b.slice(endB)) push(out, "same", w);
  return out;
}

const count = (changes: Change[], kind: Change["kind"]) =>
  changes
    .filter((c) => c.kind === kind)
    .reduce((sum, c) => sum + words(c.text).length, 0);

const plural = (n: number, word: string) =>
  `${n} ${word}${n > 1 ? "s" : ""}`;

/** One line saying how much changed, in plain French. */
export function summarize(changes: Change[]) {
  const removed = count(changes, "removed");
  const added = count(changes, "added");
  if (!removed && !added) return "Le texte est identique.";
  const parts = [];
  if (removed) parts.push(`${plural(removed, "mot")} retiré${removed > 1 ? "s" : ""}`);
  if (added) parts.push(`${plural(added, "mot")} ajouté${added > 1 ? "s" : ""}`);
  return parts.join(", ") + ".";
}
