// "Sujets du moment": the words and names that come back most in a set of
// titles, counted once per title. Computed in the browser, from the feed.

const STOPWORDS = new Set(
  (
    "le la les un une des du de d l à a au aux en et ou où pour par sur sous dans avec sans chez " +
    "ce cet cette ces son sa ses leur leurs mon ma mes ton ta tes notre nos votre vos qui que quoi dont " +
    "il elle ils elles on nous vous je tu se ne pas plus moins très trop tout tous toute toutes " +
    "être est sont été était sera seront avoir ont avait fait faire font comme mais donc car si " +
    "après avant entre contre depuis pendant selon vers lors alors aussi encore déjà même autre autres " +
    "deux trois quatre cinq dix cent mille premier première dernier dernière nouveau nouvelle nouveaux nouvelles " +
    "ans an année années jour jours semaine mois fois heure heures quand comment pourquoi combien " +
    "ça cela celui celle ceux celles y va vont peut peuvent doit faut veut dit selon face " +
    "c qu s n j m t lui leur eux là ici bien non oui rien quelque quelques chaque " +
    "annonce annonces acte marge direct vidéo vidéos photos info infos explications témoignages " +
    "affirme affirment estime estiment appel appelle appellent devant demande demandent explique " +
    "réagit raconte dénonce dénoncent souhaite veut veulent reste restent " +
    "the of and to in for on with"
  )
    .split(" ")
    .map((word) => fold(word)),
);

function fold(word: string) {
  return word.normalize("NFD").replace(/\p{M}/gu, "").toLowerCase();
}

// Words of a title, without elided articles: "l’Espagne" gives "Espagne".
function words(title: string) {
  return (title.match(/[\p{L}][\p{L}’'-]*/gu) || [])
    .map((w) => w.replace(/^(?:[ldjmntsc]|qu|jusqu|lorsqu|puisqu)['’]/i, "").replace(/['’-]+$/, ""))
    .filter(Boolean);
}

const capital = (word: string) => /^\p{Lu}/u.test(word);
const acronym = (word: string) => /^\p{Lu}{2,5}$/u.test(word);
const kept = (word: string) =>
  !STOPWORDS.has(fold(word)) && (word.length >= 4 || acronym(word));

/**
 * The topics of a set of titles, most frequent first: names of two or three
 * capitalised words ("Christa Pike") and single words, each counted once per
 * title. A word met mostly inside a name is not repeated on its own.
 */
export function topics(titles: string[], limit = 8, least = 2) {
  const counts = new Map<string, { label: string; count: number }>();
  const add = (label: string, seen: Set<string>) => {
    const key = fold(label);
    if (seen.has(key)) return;
    seen.add(key);
    const known = counts.get(key);
    if (!known) counts.set(key, { label, count: 1 });
    else {
      known.count += 1;
      // "lycées" over "Lycées", met first at the start of a title.
      if (!label.includes(" ") && !capital(label) && capital(known.label)) known.label = label;
    }
  };
  for (const title of titles) {
    const seen = new Set<string>();
    const list = words(title);
    for (let i = 0; i < list.length; i++) {
      // A name: capitalised words in a row ("Christa Pike"), not starting
      // with a word such as "En" or "Le".
      if (capital(list[i]) && !acronym(list[i]) && !STOPWORDS.has(fold(list[i]))) {
        let j = i;
        while (j + 1 < list.length && j - i < 2 && capital(list[j + 1]) && !acronym(list[j + 1])) j++;
        if (j > i) add(list.slice(i, j + 1).join(" "), seen);
      }
      if (kept(list[i])) add(list[i], seen);
    }
  }
  const names = [...counts.values()].filter((t) => t.label.includes(" ") && t.count >= least);
  return [...counts.values()]
    .filter(
      (t) =>
        t.count >= least &&
        (t.label.includes(" ") ||
          // A word met mostly inside a name is left to the name.
          !names.some((n) => fold(n.label).split(" ").includes(fold(t.label)) && n.count * 2 > t.count)),
    )
    .sort((a, b) => b.count - a.count || a.label.localeCompare(b.label, "fr"))
    .slice(0, limit);
}
