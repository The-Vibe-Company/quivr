# Quivr V2

**Transformez n’importe quel flux de contenu en recherche et veille multimodales.**

Quivr V2 est un backend open source qui ingère du texte, des images, de l’audio et de la vidéo, les rend rapidement recherchables, puis les enrichit au fil du traitement. Vous pouvez ensuite rechercher l’information, suivre un sujet en continu ou déclencher une alerte lorsqu’un contenu pertinent arrive.

Le cœur reste volontairement générique. Les formats, modèles d’IA et règles métier sont ajoutés sous forme de plugins : chacun peut adapter Quivr à son contexte sans forker toute la plateforme.

[Explorer l’architecture animée](https://prlens.dev/c/Cff5W9_hqnDIwiBtsp1MYA) · [Voir le film Remotion — 25 s](./docs/assets/quivr-v2-explainer.mp4) · [Lire le scope du MVP](./docs/quivr-v2-backend-mvp-scope.md)

[![Architecture animée de Quivr V2 : un contenu traverse l’API, Temporal, un plugin, le stockage et la recherche](https://prlens.dev/c/Cff5W9_hqnDIwiBtsp1MYA.svg)](https://prlens.dev/c/Cff5W9_hqnDIwiBtsp1MYA)

_Cliquez sur le schéma pour explorer le parcours d’un contenu et voir comment un plugin rejoint le flux._

## Comment un contenu traverse Quivr

1. **Accepter** — l’API reçoit le contenu avec une clé d’idempotence et confirme sa prise en charge durable.
2. **Orchestrer** — Temporal enchaîne les étapes, retente les erreurs et reprend après une panne.
3. **Comprendre** — les plugins normalisent ou enrichissent le contenu : OCR, transcription, embeddings, règles métier.
4. **Conserver** — PostgreSQL et S3 portent les versions canoniques, les blobs et leur provenance.
5. **Rendre utile** — Weaviate sert la recherche hybride ; les Saved Queries transforment ensuite le retrieval en veille continue.

Un contenu n’attend pas la fin de tous les traitements pour devenir utile. Une vidéo peut être visible avec ses métadonnées, puis gagner une transcription, des timecodes et des embeddings au fur et à mesure.

## Les plugins sont le produit d’extension

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
| Orchestration durable | Temporal |
| Catalogue transactionnel | PostgreSQL |
| Médias et artefacts lourds | Stockage S3-compatible |
| Recherche lexicale et vectorielle | Weaviate |
| Distribution des plugins | OCI |
| Local → distribué | Docker Compose → Kubernetes |

Ces choix forment la stack de départ, pas des dépendances exposées aux produits clients. Les briques internes pourront donc évoluer sans casser leurs intégrations.

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
- [Vocabulaire et modèle de domaine](./CONTEXT.md)
- [Historique de la réflexion d’architecture](./docs/conversations/2026-09-03-quivr-v2-architecture-discovery.md)
- [Recherches et comparatifs techniques](./research/)
- [Sources et commandes de l’animation Remotion](./video/)

Les dossiers [`Agency-doc/`](./Agency-doc/) et [`multimodal-rag/`](./multimodal-rag/) restent des références de cadrage et d’exploration, pas l’architecture cible.
