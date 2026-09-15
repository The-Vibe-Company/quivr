# Quivr V2

## Parcours implémentés : Corpora, ingestion et recherche lexicale

Le cœur Go permet de créer, lister et lire des Corpora avec contrôle d'accès,
rejeu idempotent et persistance PostgreSQL. Il accepte aussi du texte en ligne,
le matérialise via Temporal et S3, et expose des Versions immuables. La recherche
et la veille décrites plus bas restent la cible produit.

Sur Linux, installer Go 1.27.1, Docker avec Compose v2, Python avec `venv`, et
Node/npm (22 ou ultérieur pour les vérifications de contrats), puis lancer :

```bash
make dev
make verify
make down
```

`dev` compile un seul binaire `quivr`, démarre PostgreSQL, Temporal et SeaweedFS via Compose, applique les
migrations et lance API/worker comme processus locaux. L'adresse de l'API et le
chemin de configuration s'affichent au démarrage. Les ports sont dynamiques et
liés à loopback. Les clés jetables, paramètres et logs restent dans le répertoire
privé `.scratch/quivr-dev-…` ; ne pas le publier. Le champ `keys` de `config.json`
associe chaque Bearer token à une Organization, des actions et des Corpora (`*`
autorise toute l'Organization). Créer un nouveau Corpus requiert `corpora:write`
et le scope `*` ; une clé bornée à des Corpora existants ne peut en créer d'autres.

Avec l'adresse et une clé locale, envoyer `POST /v0/corpora` avec un JSON contenant
`name` et `idempotency_key`, puis lire `GET /v0/corpora` et
`GET /v0/corpora/{corpus_id}`. Un rejeu équivalent conserve l'identité ; modifier
la demande sous la même clé produit un conflit. Les champs de mapping explicites
sont conservés et validés ; un `plugin_profile` non installé est refusé. Cette
première tranche ne construit pas encore d'index de recherche.

Pour ingérer, envoyer `POST /v0/records` avec `idempotency_key`, `source`
(`corpus_id`, `namespace`, `record_key`) et `content` (`kind: "text"`, `text`).
L'API renvoie 202 après commit du Receipt et du travail à déclencher, même si
Temporal ou S3 sont indisponibles. Lire ensuite l'URL `Location` du Receipt,
puis `/v0/records/{record_id}/versions/{version_id}` pour le Manifest et son texte.
Les permissions sont `content:write` et `content:read`, limitées aux Corpora autorisés.

Le Receipt résout `created`, `duplicate` ou `conflict` pour cette tranche ; il ne
passe jamais à un état « failed » pour une panne technique. Sans révision source,
le digest canonique fournit l'identité de Version. Réutiliser une révision avec
un contenu différent conserve l'historique et signale un conflit. `source_position`
accepte un entier décimal de 1 à 1000 chiffres ; les zéros initiaux sont normalisés.
Les commandes sont limitées à 1 MiB. Uploads, Manifests explicites et extensions
non vides sont refusés jusqu'à leurs tickets dédiés, sans fausse acceptation.

Les Parts de texte court (au plus 256 octets UTF-8, sans NUL) passent par le
traitement local `quivr.normalized-text.short-whole-part.v1` : une Segmentation
immuable couvre toute la Part, sans transformation ni titre inféré. Cette limite
conservatrice est propre à THE-644 ; le découpage au tokenizer épinglé et les
textes longs arrivent avec THE-645. Les textes plus longs restent acceptés et
lisibles, avec traitement bloqué, diagnostic `short_text_limit` et disponibilité
`quarantined` ; ils ne sont jamais tronqués.

Après publication vérifiée dans Weaviate, Content commit atomiquement la couverture
lexicale, la promotion de la révision souhaitée et son événement. Une panne laisse
l’ancienne Version courante utilisable et la nouvelle en reprise. Les modes sémantique
et hybride (y compris le mode hybride par défaut) répondent explicitement 422.

```http
POST /v0/search
Authorization: Bearer <clé avec content:read et search:query>
Content-Type: application/json

{"query":"éclipse","corpus_ids":["<corpus_id>"],"mode":"lexical","profile":"balanced","limit":10}
```

Le profil résolu `balanced.lexical-short.v1` accepte des requêtes non vides de
256 octets UTF-8 maximum, sans troncature. Le mode lexical doit être explicite ;
`balanced` et 10 résultats sont les valeurs par défaut, 50 le maximum. Les autres
profils renvoient 422. Toute la liste de Corpora doit être autorisée. PostgreSQL
sélectionne la génération logique et le routage physique ; la réhydratation relit
les octets S3, valide les extraits et revérifie accès, version courante, quarantaine
et Tombstone. Les coordonnées sont en points de code Unicode ; aucun score brut,
nom de collection physique ou faux vecteur n’est exposé. Une panne renvoie 503.

La migration 003 nécessite de redémarrer API/workers. Elle remet les Receipts
existants dans l’outbox sous une nouvelle identité de workflow pour indexer aussi
les Versions déjà matérialisées. Les anciens workflows ne doivent plus être servis
par d’anciens workers pendant cette migration d’évaluation.
 PostgreSQL conserve les faits et références ;
les lectures vérifient le checksum des objets S3. L'initialiseur crée le bucket,
et lui seul applique les migrations. Le journal interne sérialise les commits
par Organization ; `record.accepted` invalide le catalogue dès l'acceptation,
puis `record.materialized` à la publication. L'exposition polling/SSE arrive dans
son propre ticket.

`make verify` régénère/compare les transports, contrôle les exemples dans les
trois langages et exécute le parcours HTTP contre un PostgreSQL isolé, incluant
redémarrage, isolation, pagination et concurrence, puis ingestion, doublons,
conflits de révision et reprise après arrêt de Temporal/S3 et interruption du
worker, puis recherche lexicale, extraits Unicode, droits, limites et panne Weaviate. Les tests d’adaptateurs vérifient aussi perte de réponse S3 et atomicité
de publication et de promotion PostgreSQL, ainsi que les barrières de réhydratation. Les rapports restent dans
`.scratch/quivr-verify-…` après suppression des processus, conteneurs et volumes
du test. La première préparation télécharge les dépendances et images épinglées.
Les requêtes/réponses synthétiques peuvent être exportées ; jamais les fichiers
`state.json`, `config.json`, `worker.json` ou `s3.json`, qui contiennent les clés.

`make down` conserve les volumes de développement ; `make reset` les supprime
explicitement. `make migrate` applique les migrations versionnées à cette pile.
Les migrations à chaud peuvent casser des processus pendant l'évaluation ; noter
les redémarrages requis. `GO=/chemin/vers/go` sélectionne un outil Go local.
`make generate` met à jour les bindings après une modification du contrat.
Les logs locaux sont bornés à quatre fichiers de 1 MiB par processus ; PostgreSQL
et les autres services Compose conservent trois fichiers de 1 MiB chacun. Les probes privées `/healthz` et `/readyz` utilisent un port séparé ; elles ne
font pas partie de l'API publique. Aucun service de modèle ni clé externe n'est
nécessaire pour cette tranche.

**Transformez n’importe quel flux de contenu en recherche et veille multimodales.**

Quivr V2 est un backend open source qui ingère du texte, des images, de l’audio et de la vidéo, les rend rapidement recherchables, puis les enrichit au fil du traitement. Vous pouvez ensuite rechercher l’information, suivre un sujet en continu ou déclencher une alerte lorsqu’un contenu pertinent arrive.

Le cœur reste volontairement générique. Les formats, modèles d’IA et règles métier sont ajoutés sous forme de plugins : chacun peut adapter Quivr à son contexte sans forker toute la plateforme.

[Lire le scope du MVP](./docs/quivr-v2-backend-mvp-scope.md) · [Comprendre l’architecture en détail](./docs/quivr-v2-architecture-overview.md)

![Flux animé de Quivr V2 : un contenu multimodal traverse l’API, Temporal, les plugins, le stockage et la recherche](./docs/assets/quivr-v2-flow-animated.svg)

_SVG animé généré avec le renderer PR Lens, versionné avec le projet et sans lien externe._

## Comment un contenu traverse Quivr

1. **Accepter** — l’API reçoit le contenu avec une clé d’idempotence et confirme sa prise en charge durable.
2. **Orchestrer** — Temporal enchaîne les étapes, retente les erreurs et reprend après une panne.
3. **Comprendre** — les plugins normalisent ou enrichissent le contenu : OCR, transcription, embeddings, règles métier.
4. **Conserver** — PostgreSQL et S3 portent les versions canoniques, les blobs et leur provenance.
5. **Rendre utile** — Weaviate sert la recherche hybride ; les Saved Queries transforment ensuite le retrieval en veille continue.

Un contenu n’attend pas la fin de tous les traitements pour devenir utile. Une vidéo peut être visible avec ses métadonnées, puis gagner une transcription, des timecodes et des embeddings au fur et à mesure.

## Les plugins sont le produit d’extension

![Architecture animée des plugins Quivr V2 : un plugin de transcription enrichit le document sans modifier le cœur](./docs/assets/quivr-v2-plugins-animated.svg)

Un plugin peut apporter une ou plusieurs capacités :

- `Connector` — récupérer une source ou comprendre son protocole ;
- `Normalizer` — convertir un format vers le modèle canonique ;
- `Enricher` — produire OCR, transcript, captions, embeddings ou relations ;
- `Retriever` — ajouter une stratégie de recherche ou de reranking ;
- `Delivery` — envoyer un match vers un webhook ou un canal métier.

Chaque plugin est un worker externe distribué comme package OCI. Il utilise le SDK Quivr, déclare sa compatibilité avec le moteur en SemVer et ne dépend pas des détails internes de Temporal ou Weaviate.

Lors d’une mise à jour, une nouvelle génération reçoit les nouveaux travaux pendant que l’ancienne termine les siens. Pas besoin d’arrêter toute la plateforme pour ajouter une capacité ou revenir à la version précédente.

## Pourquoi cette architecture

- **Utile rapidement** — le contenu devient recherchable avant la fin des enrichissements coûteux.
- **Données récupérables** — PostgreSQL et S3 permettent de reconstruire index et embeddings.
- **Extensible sans fork** — intégrations, modèles et règles métier évoluent hors du cœur.
- **Pensé pour les développeurs** — REST/OpenAPI, SDK Python et TypeScript, Docker Compose local et licence permissive.

## Stack de référence

| Besoin | Choix actuel |
| --- | --- |
| Cœur du moteur | Go, en monolithe modulaire |
| Orchestration durable | Temporal |
| Catalogue transactionnel | PostgreSQL |
| Médias et artefacts lourds | Stockage S3-compatible |
| Recherche lexicale et vectorielle | Weaviate |
| Distribution des plugins | OCI |
| Local → distribué | Docker Compose → Kubernetes |

Le cœur Go possède l'API, le domaine, les transactions et les workers Temporal. Python et TypeScript restent les langages prioritaires des SDK et plugins externes. Ces choix forment la stack de départ, pas des dépendances exposées aux produits clients : les briques internes pourront évoluer sans casser leurs intégrations.

## Premier terrain : Agency Customer

Agency Customer est le premier cas d’usage. Il confronte Quivr à l’ingestion continue de millions d’articles, à la multimodalité, aux corrections et à la rétention, tandis que les formats et règles propres à l’Agency restent dans des plugins privés.

## Où en est le projet ?

Quivr V2 est en phase de conception active. Le MVP vise d’abord une chaîne complète :

```text
ingérer → normaliser → rendre searchable → enrichir → rechercher → matcher → notifier
```

L’optimisation extrême du scale viendra ensuite, guidée par la mesure plutôt que par l’anticipation.

## Aller plus loin

- [Vue d’ensemble de l’architecture](./docs/quivr-v2-architecture-overview.md)
- [Scope détaillé du backend MVP](./docs/quivr-v2-backend-mvp-scope.md)
- [Modèle de données canonique et cycles de vie](./docs/quivr-v2-canonical-data-model.md)
- [Vocabulaire et modèle de domaine](./CONTEXT.md)
- [Historique de la réflexion d’architecture](./docs/conversations/2026-09-03-quivr-v2-architecture-discovery.md)
- [Recherches et comparatifs techniques](./research/)

Les dossiers [`Agency-doc/`](./Agency-doc/) et [`multimodal-rag/`](./multimodal-rag/) restent des références de cadrage et d’exploration, pas l’architecture cible.
