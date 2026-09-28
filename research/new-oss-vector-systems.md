# Recherche — moteurs open source pour la projection de recherche Quivr

> État de la recherche : 3 septembre 2026
> Périmètre : stockage et recherche vectorielle/hybride à plus de 100 M de chunks multimodaux, plusieurs vecteurs par unité, ACL/tenant, ingestion continue, rétention et exploitation Kubernetes.
> Critère de licence : la fondation doit être sous une licence approuvée par l’OSI. BSL/BUSL, SSPL et composants propriétaires nécessaires au passage à l’échelle sont exclus.

## Conclusion courte

Je recommande de ne pas choisir une « base vectorielle » comme source de vérité. L’architecture doit garder les blobs originaux dans un stockage S3-compatible et l’identité, les versions, les droits et la lignée dans PostgreSQL. Le moteur de recherche est une **projection reconstruisible** de ces données canoniques.

Pour le premier bake-off, les deux vrais finalistes sont :

1. **Weaviate** comme hypothèse par défaut : c’est aujourd’hui le compromis le plus cohérent entre BM25 natif, dense, hybride, filtres pré-ANN, plusieurs vecteurs par objet, SDK Python/TypeScript et déploiement local raisonnable.
2. **Qdrant** comme alternative DevX/vector-first : son modèle de payload, son ANN filtré et ses requêtes hybrides sont excellents. Il ne gagne que si le sparse search produit par Quivr satisfait réellement la pertinence éditoriale attendue sans ajouter un second moteur lexical.

**Milvus 3** est le candidat scale-first à conserver comme contrôle : son architecture désagrégée et object-storage-native peut devenir décisive quand le corpus, les coûts de SSD/RAM ou les besoins d’élasticité dépassent ce que les deux premiers tiennent proprement. Son coût d’exploitation est toutefois sensiblement plus élevé.

Chroma Distributed et Infinity méritent une piste expérimentale, pas un choix de fondation en 2026. VectorChord est un challenger PostgreSQL convaincant, mais son absence de sharding horizontal transparent et sa licence AGPL exigent deux validations distinctes. OpenSearch doit servir de baseline mature. ClickHouse convient mieux comme index analytique/archives que comme moteur primaire de retrieval temps réel.

## La décision structurante : vérité canonique et projection

```text
Connecteurs / plugins
        │
        ▼
Journal d'ingestion durable ───────────────┐
        │                                  │ replay
        ├──► S3 : originaux, dérivés       │
        ├──► PostgreSQL : identité,         │
        │    versions, ACL, rétention       │
        │                                  ▼
        └──► workers ──► SearchProjection (Weaviate/Qdrant/Milvus)
                                      │
API Quivr ──► ACL Query Planner ──────┴──► retrieval + reranking
```

La source de vérité doit contenir au minimum : `document_id`, `revision_id`, checksum, URI des blobs, temporalité, tenant/corpus, policy/rights versionnée, état d’ingestion, modèle et version de chaque dérivé, et tombstone. La projection contient les champs strictement utiles au filtrage et au ranking.

Cette séparation est non négociable pour quatre raisons :

- un snapshot de la vector DB n’est ni un historique documentaire ni une preuve de droits ;
- le changement de modèle d’embedding implique de reconstruire une projection entière ou parallèle ;
- la rétention, le passage en stockage froid et le droit de retrait doivent survivre à un moteur indisponible ;
- une abstraction Quivr stable évite que les plugins et les clients dépendent du DSL d’un fournisseur.

L’API interne devrait exposer un `SearchProjection` versionné avec `upsert_revision`, `delete_revision`, `search`, `health`, `checkpoint` et `rebuild`. Le filtre ACL obligatoire est construit dans le cœur Quivr puis composé avec les filtres utilisateur. Ni un plugin ni un appel API externe ne doit pouvoir fournir du DSL natif Qdrant, Weaviate ou Milvus.

Le stockage froid s’applique ainsi à la vérité canonique : originaux et dérivés peuvent passer vers une classe archive, tandis que la projection retire les chunks expirés. La restauration d’un corpus est alors une opération de replay explicite. Il ne faut pas confondre ce mécanisme avec le « tenant offloading » d’un moteur, qui déplace son propre index mais ne remplace pas l’archive canonique.

## Comparaison condensée

| Système | Licence de la fondation | Hybride lexical/vectoriel | ACL avant ANN | Plusieurs vecteurs | Scale/HA self-hosted | DX local → K8s | Verdict |
|---|---|---|---|---|---|---|---|
| Weaviate | BSD-3-Clause | BM25 + dense, fusion native | Oui, allow-list puis HNSW/ACORN | Named vectors et multivectors | Shards, réplication data, Raft metadata | Très bonne → correcte | Finaliste par défaut |
| Qdrant | Apache-2.0 | Dense+sparse, RRF/DBSF ; lexical général limité | Oui, payload indexes + filterable HNSW/ACORN | Named vectors, sparse, multivectors | Shards, replicas, consensus | Excellente → correcte | Finaliste vector-first |
| Milvus | Apache-2.0 | BM25 function + dense/sparse fusion | Oui pour le filtering standard | Plusieurs champs vectoriels | Désagrégé, object storage, nombreux services | Moyenne → complexe | Contrôle scale-first |
| OpenSearch | Apache-2.0 | BM25 + k-NN + RRF | Efficient filtering selon moteur | Oui, dont nested | Très mature | Moyenne → lourde | Baseline mature |
| VectorChord | AGPL-3.0 ou ELv2 | Via PostgreSQL/FTS/extensions | Prefilter opt-in, restrictions | MaxSim natif | HA PostgreSQL ; pas de sharding natif transparent | Très bonne → moyenne | Challenger avec réserves |
| pgvector | PostgreSQL License | PostgreSQL FTS + vector | Filtre souvent après scan ANN | Types dense/sparse, schéma libre | Réplicas ; sharding externe | Excellente → complexe à 100 M | Pas le défaut |
| Chroma Distributed | Apache-2.0 | Vector/full-text selon surfaces | À prouver à cette échelle | API flexible | Architecture object-store/SPANN encore jeune | Excellente local, K8s distribué immature | Wildcard expérimental |
| ClickHouse | Apache-2.0 | Full-text + vector | Pré/post-filtrage, limites actuelles | Colonnes multiples | Excellent stockage distribué | Moyenne | Secondaire analytique |
| Vald | Apache-2.0 | Dense ANN seulement | Non : filtres gRPC autour des candidats | Un espace par cluster | K8s natif | Mauvaise en local | Éliminé |
| Typesense | GPL-3.0 | Très bon hybride | Filtres natifs | Vecteurs dans documents | Réplication complète ; sharding applicatif | Excellente | Trop de routage à notre charge |
| Infinity | Apache-2.0 | Full-text+dense+sparse+tensor | Annoncé, à qualifier | Oui | Cluster jeune, un leader writer | Excellente local | À surveiller |

Ce tableau indique la disponibilité d’une capacité, pas sa performance réelle avec les distributions d’ACL de Quivr. Une implémentation qui « supporte les filtres » peut s’effondrer lorsque seuls 0,1 % des points sont autorisés.

## Analyse des candidats

### 1. Weaviate — le meilleur équilibre unifié à tester en premier

Weaviate est sous [BSD-3-Clause](https://github.com/weaviate/weaviate/blob/main/LICENSE). Il se lance avec un conteneur et fournit des [clients officiels, dont Python et TypeScript](https://docs.weaviate.io/weaviate/client-libraries).

Sa force pour Quivr est l’intégration des briques qui obligeraient autrement à maintenir deux projections :

- [BM25 et recherche hybride dense](https://docs.weaviate.io/weaviate/concepts/search/hybrid-search), avec fusion par score ou par rang ;
- [préfiltrage](https://docs.weaviate.io/weaviate/concepts/filtering) via index inversé/bitmap avant la traversée HNSW, avec ACORN pour les filtres restrictifs ;
- [named vectors et multivectors](https://docs.weaviate.io/weaviate/manage-collections/vector-config), ce qui couvre texte, image, vidéo et représentations de type ColBERT/ColPali sans dupliquer l’objet métier ;
- [sharding et réplication](https://docs.weaviate.io/weaviate/concepts/replication-architecture), réplication leaderless des données et consensus Raft pour la métadonnée.

Ses limites doivent être testées et non masquées : le coût HNSW se multiplie avec les espaces vectoriels ; l’ajout ultérieur d’un named vector ne backfill pas automatiquement les anciens objets ; les index récents comme HFresh ne doivent pas être mis sur le chemin critique sans bake-off. Le [tenant offloading](https://docs.weaviate.io/deploy/configuration/tenant-offloading) déplace un shard tenant entier vers S3 et le rend indisponible jusqu’à son rechargement ; ce n’est donc pas une solution transparente pour chaque article de plus de deux ans. Les [backups](https://docs.weaviate.io/deploy/configuration/backups) sont disponibles vers S3-compatible, GCS, Azure ou filesystem, mais les interactions backup/offloading font partie du test de restauration.

**Disqualifiant** : échec du zéro-fuite ACL, coût RAM/SSD non soutenable avec deux ou trois vecteurs par chunk, ou nDCG lexical insuffisant sur les champs/langues du corpus.

### 2. Qdrant — excellente DX, à condition que le sparse suffise

Qdrant est [Apache-2.0](https://github.com/qdrant/qdrant). Son démarrage en un conteneur, ses APIs REST/gRPC et ses [SDK Python et TypeScript](https://qdrant.tech/documentation/quick-start/) donnent la meilleure boucle locale du groupe.

Qdrant offre des [payload indexes et un HNSW filtrable](https://qdrant.tech/documentation/manage-data/indexing/), ACORN pour les filtres fortement restrictifs, des [named dense vectors, sparse vectors, multivectors et requêtes hybrides](https://qdrant.tech/documentation/search/hybrid-queries/), ainsi que du sharding tenant-aware. Le guide de [distributed deployment](https://qdrant.tech/documentation/scaling/distributed_deployment/) couvre shards, réplication et transferts de shards. Les upserts/deletes passent par WAL et deviennent rapidement recherchables.

Le point de vigilance est produit, pas seulement technique. Qdrant se décrit comme vector-first et ne vise pas un moteur lexical général avec l’étendue d’analyseurs/ranking d’un moteur de recherche. Ses filtres full-text, sparse vectors et fusions RRF/DBSF peuvent être suffisants si Quivr calcule BM25/SPLADE, mais c’est à démontrer sur noms propres, dépêches courtes, multilingue, fraîcheur et recherche exacte. Ajouter Meilisearch ou OpenSearch uniquement pour compenser ce point doublerait la synchronisation, les tombstones et les chemins de reprise.

Le stockage actif reste centré sur disque local/mmap ; S3 intervient notamment pour les [snapshots](https://qdrant.tech/documentation/operations/snapshots/), pas comme couche active désagrégée équivalente à Milvus. Enfin, la documentation d’[installation self-hosted](https://qdrant.tech/documentation/installation/) précise que le chart Helm ne fournit pas seul les automatismes opérateur de l’offre privée : upgrades sans interruption, autoscaling, monitoring et DR restent à industrialiser.

**Disqualifiant** : besoin d’un deuxième moteur pour atteindre la pertinence lexicale, recall qui s’effondre sous ACL à 0,1 %, ou coût opérationnel/SSD excessif au palier 100 M.

### 3. Milvus 3 — le candidat scale-first

Milvus est [Apache-2.0](https://github.com/milvus-io/milvus). Son [architecture](https://milvus.io/docs/architecture_overview.md) sépare streaming, query et data nodes ; etcd porte la métadonnée, les entités et index résident en object storage, et Woodpecker fournit un WAL adossé à l’object storage. Cette désagrégation est un avantage réel pour un corpus immense dont compute et stockage doivent évoluer séparément.

Milvus prend en charge [plusieurs champs vectoriels et la recherche hybride](https://milvus.io/docs/multi-vector-search.md), dense/sparse, une [fonction BM25 intégrée](https://milvus.io/docs/bm25-function.md), plusieurs rankers et le [filtered search](https://milvus.io/docs/filtered-search.md). Python et Node sont officiellement supportés.

Cette capacité a un prix : le mode standalone est déjà composé de Milvus, etcd et MinIO ; le cluster Kubernetes ajoute des rôles spécialisés, leur observabilité et leurs procédures de reprise. Le [guide Helm](https://milvus.io/docs/install_cluster-helm.md) montre mieux la réalité opérationnelle que le quickstart. Pour une équipe qui privilégie la maintenabilité, Milvus ne doit gagner que par une différence mesurée de coût ou de passage à l’échelle, pas par son plafond théorique.

**Disqualifiant** : équipe incapable d’assurer l’astreinte d’un système distribué multi-composants, restauration/upgrade non maîtrisés, ou avantage de coût/performance trop faible face à Weaviate/Qdrant.

### 4. OpenSearch — baseline crédible, mais lourde

OpenSearch est [Apache-2.0](https://github.com/opensearch-project/OpenSearch), dispose de clients officiels Python/JavaScript et combine le modèle lexical mature de Lucene avec k-NN, RRF et vecteurs imbriqués. Il offre un [filtrage efficace dans la requête k-NN](https://docs.opensearch.org/latest/vector-search/filter-search-knn/index/) selon l’engine, et ses [engines vectoriels](https://docs.opensearch.org/latest/mappings/supported-field-types/knn-methods-engines/) incluent Lucene, Faiss et JVector/DiskANN. Le [mode disque et la quantification](https://docs.opensearch.org/latest/vector-search/optimizing-storage/disk-based-vector-search/) sont utiles au test 100 M.

Il est le meilleur contrôle pour savoir ce que l’on sacrifie en choisissant une DX plus simple. En revanche, mappings, pipelines d’ingestion, shards, JVM, tuning, upgrades et DSL exposent une surface d’exploitation importante. Quivr doit encapsuler ce DSL exactement comme celui des vector DBs.

**Disqualifiant** : coût opérateur et ressources supérieurs sans gain clair de pertinence/résilience.

### 5. PostgreSQL : pgvector et VectorChord

[pgvector](https://github.com/pgvector/pgvector) est sous licence PostgreSQL et apporte HNSW, IVFFlat, half vectors, sparse vectors et recherche hybride avec le full-text PostgreSQL. C’est imbattable pour le prototype et les petits déploiements. À grande échelle, il faut toutefois regarder la mécanique exacte : avec un index approximatif, les filtres sont généralement appliqués après le scan ANN ; les iterative scans depuis 0.8 compensent partiellement, tandis que partitions ou tables séparées sont recommandées pour certains découpages. Le sharding horizontal, le placement des données et le fan-out viennent d’un autre produit ou de Quivr. Les mises à jour/vacuum/reindex HNSW peuvent concurrencer la base transactionnelle.

[VectorChord](https://github.com/tensorchord/VectorChord) est plus ambitieux : RaBitQ, index disque, [benchmarks officiels à 100 M × 768](https://docs.vectorchord.ai/vectorchord/usage/partitioning-tuning.html), et [MaxSim multivector natif](https://docs.vectorchord.ai/vectorchord/usage/indexing-with-maxsim-operators.html). Son [préfiltrage](https://docs.vectorchord.ai/vectorchord/usage/prefilter.html) est opt-in et n’est pas supporté par tous les index. La page de [scalabilité](https://docs.vectorchord.ai/vectorchord/admin/scalability.html) s’appuie sur CloudNativePG, standby et read replicas : c’est une bonne HA PostgreSQL, mais pas un sharding horizontal transparent des données.

La licence est un point de gouvernance : VectorChord est proposé sous **AGPL-3.0 ou Elastic License v2**. Seule l’AGPL est compatible avec le critère OSI ; son copyleft réseau doit être explicitement accepté par le juridique. L’ELv2 ne peut pas servir de fondation « open source » au sens demandé.

VectorChord mérite un POC car il pourrait offrir la meilleure DX « une base ». Mais la projection vectorielle doit alors vivre dans une base/cluster séparé de PostgreSQL canonique : faire porter identité, orchestration, ACL et HNSW de 100 M de chunks au même primaire créerait un blast radius inutile.

**Disqualifiant** : refus AGPL, write primary saturé, maintenance bloquante, filtre ACL trop coûteux, ou nécessité pour Quivr de construire lui-même le sharding/rebalancing.

### 6. Chroma Distributed — architecture moderne, maturité à prouver

Chroma est [Apache-2.0](https://github.com/chroma-core/chroma). Son architecture distribuée documente gateway, log/WAL, executors, compactor, system DB et object store ; SPANN est destiné aux très grands index. La séparation compute/storage, le cache SSD local et l’API Python/TypeScript sont précisément la direction souhaitable.

Mais les [documents d’architecture OSS](https://docs.trychroma.com/docs/overview/oss) indiquent encore des sous-systèmes de stockage différents entre local et distribué, avec la parité comme objectif actif. Le [développement distribué self-hosted](https://github.com/chroma-core/chroma/blob/main/DEVELOP.md) passe par Kubernetes, Tilt et Helm. La qualité du chemin de production self-hosted — HA, rolling upgrades, sauvegarde/restauration, filtres ACL adversariaux et comportement d’une collection à 100 M — doit donc être démontrée, pas inférée de Chroma Cloud.

**Disqualifiant** : toute fonction essentielle disponible seulement dans le cloud, absence de procédure self-hosted reproductible de DR/upgrade, ou rupture de sémantique entre le mode local et distribué.

### 7. ClickHouse — excellent complément, ANN primaire encore mouvant

ClickHouse est [Apache-2.0](https://github.com/ClickHouse/ClickHouse). Son stockage distribué, ses TTL, tiering et scans analytiques conviennent très bien à une projection historique/analytique. Il sait indexer des jeux de 100 M de vecteurs et possède désormais du full-text.

Pour autant, l’index HNSW incrémental interagit avec les merges de parts et consomme beaucoup de mémoire. Surtout, l’équipe ClickHouse travaille encore sur un [RFC Vector Similarity Index 2.0](https://github.com/ClickHouse/ClickHouse/issues/104122) qui revoit précisément build incrémental, filtrage, stockage et recherche distribuée ; un [problème actuel de requête hybride](https://github.com/ClickHouse/ClickHouse/issues/105516) montre aussi que des plans `ORDER BY` composites peuvent contourner l’index vectoriel. Ce sont de bons signes d’investissement, mais de mauvais motifs pour mettre cette couche sur le chemin primaire aujourd’hui.

**Usage recommandé** : analytics, audit de corpus, recherche archive non interactive ou génération de datasets de rebuild. **Pas** le moteur primaire du premier release.

### 8. Vald — élimination nette

Vald est [Apache-2.0](https://github.com/vdaas/vald), conçu pour Kubernetes et un ANN dense distribué. Il exige AVX2 et déploie de nombreux composants. Ses [filtres](https://vald.vdaas.org/docs/user-guides/filtering-configuration/) sont des services gRPC en amont/aval plutôt qu’un riche index scalaire qui construit une allow-list ACL avant ANN. Sa [FAQ](https://vald.vdaas.org/docs/support/faq/) recommande plusieurs clusters pour plusieurs espaces d’embeddings.

Cela échoue simultanément aux critères ACL-before-ANN, multivector/hybride et local simple. Il ne doit pas entrer dans le benchmark.

## Autres projets récents ou souvent proposés

### Typesense

Typesense est sous [GPL-3.0](https://github.com/typesense/typesense/blob/v31/LICENSE.txt), donc OSI, et sa DX ainsi que son [hybride lexical/vectoriel](https://typesense.org/docs/latest/api/vector-search.html) sont séduisants. Mais la [HA](https://typesense.org/docs/guide/high-availability.html) réplique le dataset complet ; le [sharding](https://typesense.org/docs/guide/organizing-collections.html) consiste à répartir manuellement les données entre collections et à orchestrer le fan-out applicatif. À 100 M avec plusieurs vecteurs, Quivr deviendrait le coordinateur distribué. À réserver aux déploiements plus petits.

### Meilisearch

Meilisearch reste une référence de DX et de recherche lexicale, mais son dépôt utilise désormais une [licence mixte MIT et BUSL-1.1](https://github.com/meilisearch/meilisearch/blob/main/LICENSE). La [recherche réseau/distribuée](https://www.meilisearch.com/docs/reference/api/search/search-with-get) nécessaire au sharding est une capacité Enterprise. Elle ne peut donc pas fonder la variante open source à cette échelle. Un couple Meilisearch + Qdrant créerait en outre deux projections, deux modèles de filtrage et deux flux de tombstones. Il n’est justifié que si les tests prouvent qu’aucun moteur unifié n’atteint la qualité lexicale requise.

### Vespa

[Vespa](https://github.com/vespa-engine/vespa) est Apache-2.0 et probablement le système le plus programmable pour filtrage, ANN, lexical, ranking multi-phase et mises à jour temps réel. Il supporte plusieurs tenseurs/vecteurs par document. Son coût est conceptuel et opérationnel : schémas, langage de ranking, déploiement et tuning ont une courbe plus forte, et l’écosystème TypeScript est moins direct que Python. À réexaminer si le ranking spécifique aux dépêches devient un avantage produit qui justifie cette complexité.

### Infinity

[Infinity](https://github.com/infiniflow/infinity) est un projet Apache-2.0 récent, encore en 0.x, qui réunit full-text, dense, sparse, tensor/multivector et hybride dans un binaire simple. Son [mode cluster](https://infiniflow.org/docs/set_up_cluster) repose actuellement sur un leader writer, quelques followers/learners et MinIO partagé. C’est une excellente piste de veille et un wildcard de laboratoire, mais pas encore une fondation raisonnable pour Quivr : jeunesse, single-writer et absence d’un SDK TypeScript de premier rang doivent être levées.

### LanceDB et ParadeDB

[LanceDB](https://github.com/lancedb/lancedb) est Apache-2.0, local/embedded, multimodal et object-store-friendly. Le serveur distribué self-hosted n’est cependant pas encore une fondation turnkey ; l’adopter obligerait Quivr à construire le control plane qu’il cherche précisément à éviter.

[ParadeDB](https://github.com/paradedb/paradedb) est AGPL-3.0 et renforce PostgreSQL pour BM25. Son README décrit encore la recherche vectorielle/hybride native comme à venir et renvoie à pgvector aujourd’hui. C’est un complément à surveiller, pas le moteur vectoriel du projet.

Les produits propriétaires object-storage-first comme Turbopuffer sont utiles comme inspiration architecturale — index durable dans l’object storage, SSD comme cache, compute élastique — mais ne doivent ni être recommandés ni entrer dans le socle open source.

## Trois architectures candidates

### A. Projection unifiée Weaviate — recommandation provisoire

- S3-compatible : originaux, thumbnails/keyframes, transcriptions, dérivés et exports de rebuild.
- PostgreSQL : identité, version, ACL, rétention, état des workflows et outbox.
- Temporal : orchestration durable des traitements, pas bus de diffusion ni source de vérité.
- Kafka-compatible : flux à fort fan-out/replay si le volume ou le nombre de consommateurs le justifie.
- Weaviate : BM25, dense, sparse éventuel, named/multivectors, métadonnées de filtre.
- Reranker Quivr : top 50–200 candidats, avec politique de coût/latence séparée.

**Pourquoi elle est première** : une projection sert le lexical et le vectoriel, donc moins de synchronisation et d’astreinte. **Quand l’abandonner** : filtres ACL, TCO HNSW multivector ou qualité BM25 échouent aux seuils ci-dessous.

### B. Projection Qdrant vector-first — option simplicité/performance

Même socle canonique et orchestration. Qdrant reçoit dense, sparse BM25/SPLADE produit par Quivr, named vectors et payloads ACL. La fusion et le reranking restent dans le pipeline Quivr ou dans la Query API.

**Pourquoi elle peut gagner** : excellente boucle locale, API nette, très bon filtrage et possibilité de contrôler entièrement la représentation sparse. **Condition stricte** : aucun deuxième moteur lexical sur le chemin de production. Si Meilisearch/OpenSearch est nécessaire, l’avantage de simplicité disparaît et l’architecture A ou C doit être préférée.

### C. Projection Milvus désagrégée — option plafond/coût

Le socle canonique reste identique, mais Milvus utilise lui-même object storage pour ses segments/index et sépare streaming/query/data nodes. Le BM25 intégré ou sparse externe sert le lexical ; le reranking reste Quivr.

**Pourquoi elle peut gagner** : si 100 M n’est qu’un palier initial, si les corpus froids dominent ou si compute et stockage doivent scaler indépendamment. **Condition stricte** : avantage d’au moins 40 % en coût à SLO égal, ou échec des autres candidats au palier de charge, et équipe explicitement dotée pour opérer le cluster.

VectorChord peut remplacer le moteur de l’architecture A dans un POC « PostgreSQL projection », mais pas avant validation AGPL et sans le séparer du PostgreSQL canonique. OpenSearch est testé comme contrôle, pas comme quatrième architecture par défaut.

## Benchmark de décision

### Dataset et charge

Construire un corpus synthétique à partir de distributions réelles d’un corpus d’actualité, anonymisées si nécessaire :

- paliers 10 M puis 100 M de chunks ; ne pas extrapoler uniquement depuis 1 M ;
- au moins deux vecteurs denses par chunk (texte 768/1024 dimensions et image 512/768), un signal lexical/sparse et les métadonnées réelles ;
- contenu français, anglais et autres langues significatives, noms propres, dépêches très courtes, doublons et corrections ;
- filtres tenant/corpus/groupes de droits/embargo avec sélectivité 100 %, 10 %, 1 % et 0,1 %, y compris des cas où ACL et voisinage vectoriel sont anticorrélés ;
- ingestion nominale et 2× pic, avec 5–10 % d’updates/tombstones, recherche concurrente, backfill et reconstruction d’un espace vectoriel ;
- état chaud, cache froid, redémarrage et perte d’un nœud.

Les requêtes éditoriales doivent avoir des jugements de pertinence. Pour le vectoriel, calculer un ground truth exact sur un sous-ensemble et ne pas comparer uniquement les latences des ANN entre eux.

### Mesures obligatoires

| Axe | Mesures |
|---|---|
| Sécurité | fuite ACL absolue, complétude de `k`, application des tombstones/embargos |
| Qualité | recall@10/50 dense, nDCG@10 et MRR lexical/hybride, diversité et fraîcheur |
| Latence | p50/p95/p99 query, ingest-to-search p95/p99, update/delete-to-visibility |
| Débit | recherches/s et documents/chunks/s avec indexation, compaction et reranking concurrents |
| Résilience | perte nœud/AZ, rolling upgrade, corruption simulée, snapshot/restore, replay complet |
| Élasticité | débit et latence à 1, 2 et 4 nœuds ; temps de rebalance |
| Coût | RAM, SSD, object storage, trafic/GET, CPU/GPU embedding/rerank, coût mensuel à SLO égal |
| Exploitation | temps d’installation, upgrade, ajout de shard, diagnostic, restauration et heures opérateur |

### Seuils éliminatoires

- **ACL : zéro résultat non autorisé sur au moins 1 M de requêtes adversariales.** Une seule fuite élimine le moteur/configuration.
- **Qualité dense : recall@10 ≥ 0,95** au profil de latence choisi, sur chacun des niveaux de sélectivité ACL.
- **Qualité hybride :** jamais inférieure au meilleur baseline lexical sur les requêtes exactes ; viser au moins +5 % de nDCG@10 sur le jeu hybride. Si le gain n’existe pas, ne pas payer sa complexité.
- **Latence API : p95 < 1 s** au pic cible, avec objectif interne retrieval < 250 ms hors reranking lourd ; mesurer p99, pas seulement la moyenne.
- **Fraîcheur : p95 ingest-to-search < 60 s**, très en dessous du maximum produit de cinq minutes ; update/delete/embargo effectif p95 < 30 s.
- **Résilience :** aucune perte d’écriture acquittée après perte d’un nœud ; reprise du service en moins de 15 minutes ; rolling upgrade sans indisponibilité globale.
- **Rebuild :** projection 100 M reconstruite depuis PostgreSQL/S3 en moins de 24 h avec un budget d’infrastructure explicitement plafonné. La vérité canonique donne un RPO logique de zéro ; le RTO doit être vérifié.
- **Scalabilité :** quatre fois les ressources doivent fournir au moins 3,2× le débit utile à SLO constant, sans routage manuel par tenant dans Quivr.
- **DX :** environnement local utilisable en moins de cinq minutes sur une machine de 16 Go ; procédures production automatisables, testées et compatibles avec une seule équipe d’astreinte.

### Règle de choix

1. Éliminer tout candidat qui échoue à une condition de sécurité, licence, reprise ou pertinence.
2. Choisir **Weaviate** s’il passe tous les seuils et reste à moins de 25 % du TCO du meilleur candidat : la réduction de composants et d’heures opérateur vaut cette marge.
3. Choisir **Qdrant** s’il ne nécessite aucun moteur lexical additionnel et apporte soit au moins 30 % de TCO en moins, soit 2× le débit, avec une nDCG hybride à moins de 2 % de Weaviate.
4. Choisir **Milvus** si Weaviate/Qdrant échouent le palier 100 M ou si sa désagrégation apporte au moins 40 % de coût en moins à SLO égal, après validation explicite de l’effort SRE.
5. Ne promouvoir **Chroma Distributed**, **Infinity** ou **VectorChord** que si une release épinglée — pas une roadmap — passe exactement les mêmes tests HA, backup/restore, upgrade et ACL.

## Décisions à prendre après le bake-off

- cardinalité réelle : « 100 M unités » signifie-t-il documents, chunks ou vecteurs physiques ? Avec trois représentations, le dimensionnement varie d’un facteur trois ou davantage ;
- niveau d’ACL : listes de groupes bornées et indexables, ou policies dynamiques nécessitant une phase d’autorisation externe ;
- exigences lexicales : stemming, tolérance aux fautes, phrases, proximité, boosting de fraîcheur, facettes et highlighting par langue ;
- SLO séparés pour veille temps réel, recherche historique et exploration multimodale ;
- durée d’index chaud par corpus et temps accepté pour réhydrater une archive ;
- stratégie de migration d’embeddings : double écriture, deux named vectors dans une même collection, ou deux projections blue/green ;
- budget maximal de RAM/SSD et taille réelle de l’équipe qui opérera le cluster.

La recommandation finale ne devrait être figée qu’après ces réponses et le benchmark 100 M. En attendant, l’investissement durable n’est pas un choix prématuré de moteur : c’est le contrat `SearchProjection`, l’ACL Query Planner, l’outbox/replay et un harness reproductible capable d’exécuter les mêmes scénarios contre Weaviate, Qdrant et Milvus.
