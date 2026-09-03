# Recherche moderne et DX pour Quivr

_Recherche effectuée le 3 septembre 2026 à partir des documentations, dépôts et licences officiels. Les verdicts sont des inférences architecturales pour Quivr/Agency, pas des résultats de benchmark. Cette note réexamine explicitement la proposition « OpenSearch par défaut » : elle ne doit pas être considérée comme validée._

## Conclusion courte

Il n'existe pas un moteur qui réunisse aujourd'hui les quatre propriétés recherchées : **une seule dépendance locale**, **excellente recherche texte**, **hybride vectoriel filtré**, et **scale horizontal/HA crédible jusqu'au corpus Agency**.

La trajectoire la plus saine est donc :

1. **PostgreSQL-first pour le MVP**, avec `tsvector`/GIN, `pgvector`, SQL et RLS, limité au corpus de veille récent ;
2. une **frontière `SearchProjection`** dès le premier jour, alimentée par l'outbox et reconstruisible depuis les données canoniques ;
3. un benchmark représentatif **Weaviate vs Vespa** avant le choix de production Agency ;
4. **Weaviate** comme candidat « meilleur compromis DX/scale », et **Vespa** comme candidat « meilleur moteur de retrieval/ranking à long terme » ;
5. ne pas intégrer Typesense, ParadeDB, Quickwit ou LanceDB comme backend officiel avant qu'ils ne répondent à leurs disqualifiants respectifs.

Ce n'est pas une stratégie « supportons six bases ». Quivr ne doit livrer que deux implémentations maintenues au départ : `postgres` et, après benchmark, **une seule** implémentation distribuée. La frontière existe pour préserver le choix, pas pour promettre une matrice infinie.

## Ce que « top DX » doit vouloir dire

Le meilleur DX n'est pas seulement une jolie API de recherche. Pour Quivr, il comprend :

- `docker compose up` sans JVM de 4 Go ni cluster à trois nœuds ;
- une ingestion transactionnelle sans dual-write fragile ;
- une requête de recherche stable et indépendante du moteur ;
- des filtres de visibilité impossibles à oublier ;
- des migrations et rebuilds observables ;
- une voie de production qui ne force pas à réécrire le modèle métier ;
- des diagnostics compréhensibles quand une projection prend du retard.

Un moteur peut donc avoir un excellent SDK et une mauvaise DX système s'il oblige Quivr à opérer une synchronisation, une réplication intégrale en RAM ou un processus manuel de reindexation.

## 1. PostgreSQL-first : le meilleur point de départ, pas la destination garantie

### Ce que l'on obtient réellement

PostgreSQL possède une recherche plein texte native avec `tsvector`/`tsquery`, des requêtes booléennes et de phrase, `ts_rank`/`ts_rank_cd`, et recommande les index GIN pour la recherche textuelle ([types de recherche](https://www.postgresql.org/docs/current/datatype-textsearch.html), [fonctions et ranking](https://www.postgresql.org/docs/current/functions-textsearch.html), [index GIN](https://www.postgresql.org/docs/current/textsearch-indexes.html)). `pgvector` ajoute recherche exacte, HNSW et IVFFlat ; son API reste du SQL et l'extension est distribuée sous licence PostgreSQL ([dépôt officiel](https://github.com/pgvector/pgvector)).

Pour les ACL, c'est le candidat le plus sûr : la row-level security ajoute les politiques directement aux requêtes et passe en _default deny_ lorsqu'aucune politique applicable n'existe ([RLS PostgreSQL](https://www.postgresql.org/docs/current/sql-createpolicy.html)). Les updates, tombstones et suppressions sont transactionnels avec le catalogue Quivr. Il n'y a aucun CDC ni double état à réparer.

Le lifecycle peut commencer proprement par partition temporelle. PostgreSQL permet de détacher rapidement une partition, de l'archiver ou de la placer sur un stockage moins coûteux sans effectuer des millions de `DELETE` individuels ([partitionnement](https://www.postgresql.org/docs/current/ddl-partitioning.html)).

### Le design MVP proposé

```text
PostgreSQL
  record_versions        vérité durable et métadonnées
  search_documents       projection dénormalisée, partitionnée par temps/corpus
    text_search           tsvector + GIN
    embedding             pgvector HNSW (seulement quand prêt)
    ACL/corpus/time       B-tree/GIN/partition pruning

Search API
  autorisation -> Query AST -> PostgresSearchAdapter -> résultats versionnés
```

Le lexical minimal devient disponible avant les embeddings. La recherche hybride est faite en deux sous-requêtes bornées — candidats lexicaux et vectoriels — puis fusionnée par RRF dans SQL ou dans la couche retrieval. Il faut versionner la recette de ranking, limiter chaque liste candidate, et ne jamais scanner l'ensemble des résultats.

### Ses limites à prendre au sérieux

- PostgreSQL fournit `ts_rank` et `ts_rank_cd`, pas un moteur BM25 riche avec typo tolerance, facettes et merchandising de niveau Typesense/Meilisearch. Les fonctions sont personnalisables, mais améliorer le ranking devient vite notre travail.
- Avec pgvector HNSW, les filtres sont appliqués **après** le scan approximatif. Les iterative scans corrigent une partie du problème ; la documentation recommande aussi des index partiels ou du partitionnement. C'est un risque direct pour les ACL sélectives et les nombreux corpus ([filtrage pgvector](https://github.com/pgvector/pgvector#filtering)).
- Le graphe HNSW consomme mémoire et temps de build ; les writes d'ingestion, GIN, HNSW, catalogue et requêtes se disputent les mêmes ressources.
- PostgreSQL scale verticalement et par read replicas beaucoup plus naturellement qu'il ne distribue un index de recherche. Le sharding ferait entrer Citus ou une couche équivalente dans l'architecture.

**Sweet spot :** démarrer la veille « from now », quelques millions de parts recherchables, un seul modèle d'embedding, une équipe qui veut livrer vite et mesurer.

**Disqualifiant :** prétendre, sans benchmark, que le même nœud PostgreSQL portera les dizaines de millions de dépêches, les dérivés de 90 M de photos, plusieurs vecteurs par part et des backfills concurrents tout en respectant le p95. PostgreSQL-first est crédible seulement si la projection est explicitement remplaçable.

## 2. Typesense : la meilleure sensation développeur, une limite de scale structurelle

Typesense possède probablement la meilleure prise en main du groupe : image Docker officielle, API REST claire, clients officiels JavaScript, PHP, Python et Ruby, import bulk, upsert, filtres/facettes, typo tolerance, highlighting et clés de recherche scopées ([installation](https://typesense.org/docs/guide/install-typesense.html), [clients](https://typesense.org/docs/guide/installing-a-client.html), [contrôle d'accès](https://typesense.org/docs/guide/data-access-control.html)). Il fait du vectoriel et de l'hybride par fusion lexical/vectoriel, avec pondération et reranking optionnel ([vector/hybrid search](https://typesense.org/docs/latest/api/vector-search.html)). Le serveur est GPL-3.0 ; ses clients sont Apache-2.0 ([dépôt et explication officielle](https://github.com/typesense/typesense)).

Les identifiants explicites permettent upsert/delete. Les aliases autorisent un blue/green index, mais une évolution de schéma zéro-downtime exige la création d'une nouvelle collection, le dual-write, le rechargement depuis la source primaire puis le basculement de l'alias ([collections et aliases](https://typesense.org/docs/latest/api/collections.html), [synchronisation](https://typesense.org/docs/guide/syncing-data-into-typesense.html)).

Ses scoped search keys peuvent embarquer un `filter_by` cryptographiquement non surchargeable, ce qui est élégant pour un ACL simple. Quivr devrait néanmoins continuer à proxyfier les recherches et imposer ses propres filtres : une liste d'utilisateurs/groupes dans chaque document peut devenir lourde et difficile à invalider à grande échelle.

### Disqualifiant Agency

Typesense n'est pas shardé entre les nœuds d'un cluster : **chaque nœud conserve une réplique exacte du dataset entier**. Le clustering augmente la disponibilité et le débit de lecture, pas la capacité corpus ([organisation des collections](https://typesense.org/docs/guide/organizing-collections.html), [HA](https://typesense.org/docs/guide/high-availability.html)). Les index sont en mémoire ; la documentation estime le texte indexé à environ 2–3× sa taille et un vecteur à 7 octets par dimension et par document ([dimensionnement](https://typesense.org/docs/guide/system-requirements.html)).

Cela peut être excellent pour un index de veille borné, mais chaque nœud HA doit pouvoir tenir tout le corpus actif et tous les vecteurs. Le stockage ancien n'a pas de tier froid interrogeable intégré ; les snapshots self-hosted sont des sauvegardes restaurées après arrêt/recréation, puis les index RAM sont reconstruits ([backup/restore](https://typesense.org/docs/guide/backups.html)).

**Sweet spot :** moteur de recherche applicatif très agréable, corpus chaud borné, autocomplétion/facettes/typo tolerance, quelques dizaines de millions de petits records si la RAM le permet.

**Verdict :** magnifique option de produit ou démonstrateur, mais pas le backend canonique « ultra scalable » de Quivr tant que la capacité dataset reste verticale. Le proposer comme mode local en plus d'un autre moteur créerait du travail d'adapter pour un bénéfice limité par rapport au mode PostgreSQL.

## 3. ParadeDB / pg_search : l'idée la plus séduisante, mais pas le socle OSS Agency aujourd'hui

ParadeDB place un index Tantivy directement dans PostgreSQL. Les écritures d'index participent à la transaction et au WAL ; les requêtes restent en SQL, acceptent JOIN, filtres, agrégations et recherche texte/vectorielle. Depuis la version 0.25, l'index supporte nativement le vectoriel et le combine avec le texte ([introduction officielle](https://www.paradedb.com/docs/welcome/introduction), [texte et vecteurs](https://www.paradedb.com/docs/documentation/full-text/overview)). C'est exactement la promesse DX recherchée : pas d'ETL, pas de cohérence éventuelle entre PostgreSQL et un search engine.

Mais deux points sont bloquants :

1. ParadeDB Community est **AGPL-3.0** ([licence du dépôt](https://github.com/paradedb/paradedb)). Ce n'est pas automatiquement incompatible avec Quivr, mais c'est un choix juridique et de distribution plus contraignant que PostgreSQL/Apache/BSD, à valider explicitement.
2. La documentation officielle réserve à **ParadeDB Enterprise** la réplication physique, la haute disponibilité et les read replicas multi-nœuds ([section Production Readiness](https://www.paradedb.com/docs/welcome/introduction#production-readiness)). La même page présente le sharding Citus comme trajectoire horizontale, ce qui ajoute une autre brique et ne constitue pas la distribution OSS simple recherchée.

Le fait que `pg_search` doive être chargé via `shared_preload_libraries` réduit aussi la portabilité vers les offres PostgreSQL managées qui ne distribuent pas l'extension ([initialisation de l'extension](https://github.com/paradedb/paradedb/blob/main/pg_search/src/lib.rs)).

**Sweet spot :** produit mono-instance ou offre qui accepte l'AGPL/Enterprise, données fortement relationnelles, beaucoup de mises à jour et de JOIN, désir absolu de rester en SQL.

**Verdict :** meilleur candidat à réévaluer dans 12 mois. Aujourd'hui, il transforme une économie de DX en dépendance de licence/édition précisément sur HA et réplication. Ne pas en faire une dépendance obligatoire de l'OSS Quivr.

## 4. Weaviate : le meilleur compromis distribué et developer-friendly

Weaviate est BSD-3-Clause, démarre avec un Docker Compose et possède des clients officiels Python, TypeScript/JavaScript, Go et Java ([licence](https://github.com/weaviate/weaviate/blob/main/LICENSE), [quickstart local](https://docs.weaviate.io/weaviate/quickstart/local)). Son modèle combine objets, plusieurs vecteurs, BM25F, hybrid search et filtres structurés. L'hybride exécute BM25 et vectoriel en parallèle puis fusionne leurs scores ; le poids est réglable ([hybride](https://docs.weaviate.io/weaviate/concepts/search/hybrid-search)).

Pour Quivr, sa propriété la plus importante est le **pré-filtrage vectoriel** : les contraintes sont construites avant la recherche ANN, avec ACORN par défaut depuis 1.34 et bascule possible vers du brute force pour les filtres très sélectifs ([filtered vector search](https://docs.weaviate.io/weaviate/concepts/filtering)). C'est plus rassurant que le post-filter HNSW de pgvector pour corpus, organisation, temporalité et visibilité.

Le scale est réel : une collection est divisée en shards, chaque shard contient son object store, ses index inversés et vectoriels ; réplication et sharding se combinent pour capacité et HA ([horizontal scaling](https://docs.weaviate.io/weaviate/concepts/cluster), [réplication](https://docs.weaviate.io/weaviate/concepts/replication-architecture)). Les métadonnées cluster utilisent Raft ; les données sont répliquées sans leader avec cohérence configurable `ONE`, `QUORUM` ou `ALL`. Cette disponibilité a un coût : selon le niveau choisi, des répliques peuvent temporairement diverger.

Le moteur propose des modules de reranking, mais Quivr devrait garder ses plugins de ranking hors du serveur : les modules Weaviate sont des intégrations propres au moteur, activées au déploiement, et les utiliser comme API publique couplerait le système de plugins Quivr à une base particulière ([modules](https://docs.weaviate.io/weaviate/modules), [reranking](https://docs.weaviate.io/weaviate/concepts/reranking)).

### Limites et risques

- Weaviate reste une seconde base : outbox, projection idempotente, lag, tombstones et rebuild sont obligatoires.
- Le texte a BM25F, tokenizers et boosting, mais la programmabilité retrieval/ranking est inférieure à Vespa. Il faut mesurer phrases, noms propres, langues, corrections et fraîcheur sur les vraies dépêches.
- Pour une collection single-tenant, le nombre de shards uniques est fixé à la création. Il faut donc tester la trajectoire de reshard/migration au lieu de présumer qu'ajouter un nœud suffit toujours ([modèle de shards](https://docs.weaviate.io/weaviate/concepts/cluster#shards)).
- Le tier froid n'est pas temporel et générique : l'offload s'applique à un tenant entier, uniquement vers AWS S3 dans la documentation actuelle, et le tenant n'est plus interrogeable avant rechargement ([tenant offloading](https://docs.weaviate.io/deploy/configuration/tenant-offloading), [états](https://docs.weaviate.io/weaviate/manage-collections/tenant-states)). Cela ne remplace pas notre lifecycle `watch/archive`.

**Sweet spot :** API moderne, RAG multimodal, filtres ANN sérieux, scale horizontal et HA OSS, équipe qui préfère un système dédié mais abordable à Vespa.

**Verdict :** premier candidat distribué à benchmarker. C'est le choix le plus cohérent si la priorité est de conserver une DX proche d'une vector database moderne sans sacrifier le scale.

## 5. Vespa : le plus puissant, avec une taxe de concepts

Vespa est Apache-2.0 et réunit dans le même moteur champs structurés, texte, tenseurs/vecteurs, boolean filters, mises à jour partielles et ANN ([dépôt](https://github.com/vespa-engine/vespa), [overview](https://docs.vespa.ai/en/learn/overview.html)). Son avantage décisif n'est pas un benchmark générique : c'est son modèle de retrieval/ranking.

Une requête peut unir `weakAnd`, BM25 et plusieurs nearest-neighbor retrievers, puis appliquer first phase, second phase et global phase. Les expressions peuvent intégrer fraîcheur, attributs, tenseurs, ONNX, XGBoost ou LightGBM, avec des budgets de candidats explicites ([ranking](https://docs.vespa.ai/en/basics/ranking.html), [phased ranking](https://docs.vespa.ai/en/ranking/phased-ranking.html)). Pour une veille où récence, autorité de source, lexical exact, sémantique, diversité et reranker éditorial comptent ensemble, aucun autre candidat de cette note n'offre ce niveau de contrôle natif.

Le scale horizontal et l'HA sont aussi structurels : les content clusters shardent et répliquent, redistribuent les données en ligne, et les groupes permettent de scaler séparément le débit de requêtes. Les writes sont acquittés après WAL et application visible ; put/update/remove sont natifs ([opérations et scale](https://docs.vespa.ai/en/basics/operations.html), [élasticité](https://docs.vespa.ai/en/content/elasticity.html), [feed](https://docs.vespa.ai/en/performance/sizing-feeding.html)). Le Kubernetes Operator officiel automatise davantage les opérations de production self-hosted que le mode self-managed brut.

### Taxe de DX

Localement, il faut le CLI, un conteneur demandant au moins 4 Go de RAM, et une application package avec `services.xml`, fichiers `.sd`, rank profiles et éventuellement Java/Maven pour les composants ([déploiement local](https://docs.vespa.ai/en/basics/deploy-an-application-local-java.html), [application packages](https://docs.vespa.ai/en/basics/applications.html)). Les changements d'indexing peuvent demander une reindexation explicitement activée en self-managed ([reindexing](https://docs.vespa.ai/en/operations/reindexing.html)). Le DSL YQL, les tensors et les rank expressions sont puissants mais spécifiques.

Il n'y a pas, dans les sources examinées, un équivalent simple au searchable snapshot objet pour une archive froide immédiatement interrogeable. PostgreSQL/S3 restent donc sources de rebuild et le hot/archive doit probablement être séparé en content clusters ou projections.

**Sweet spot :** corpus massif, mises à jour continues, ranking sophistiqué qui est lui-même le produit, équipe capable d'industrialiser une application package et son exploitation.

**Verdict :** meilleur choix technique long terme si les tests montrent que le retrieval Agency exige plusieurs étages de ranking et que Weaviate pousse trop de logique dans Quivr. Ce n'est pas le meilleur moteur pour obtenir une DX top _sans travail_. Il peut le devenir si Quivr génère et valide l'application package depuis un `SearchBlueprint` plus simple, mais ce générateur est un vrai produit à budgéter.

## 6. Quickwit : excellent moteur d'archive temporelle, mauvais moteur principal Quivr

Quickwit est Apache-2.0, se lance en un binaire ou conteneur, parle une partie des API Elasticsearch/OpenSearch, et sépare searchers/indexers/control plane/janitor. Ses index résident dans du stockage objet, les searchers sont stateless, et le janitor gère GC, delete tasks et rétention par split temporel ([dépôt et licence](https://github.com/quickwit-oss/quickwit), [architecture](https://quickwit.io/docs/overview/architecture), [retention](https://quickwit.io/docs/configuration/index-config#retention-policy)). C'est une architecture très élégante pour logs, événements append-only et archives temporelles peu modifiées.

Mais Quickwit **ne supporte pas la recherche vectorielle** ; la demande officielle correspondante est une feature request fermée sans implémentation ([issue vector search](https://github.com/quickwit-oss/quickwit/issues/5675)). Les suppressions sont des tâches asynchrones par requête, la rétention supprime des splits entiers, et les changements de mapping ne réindexent pas l'existant et peuvent temporairement valider des documents avec l'ancien mapping ([Delete API](https://quickwit.io/docs/reference/rest-api#delete-api), [mapping updates](https://quickwit.io/docs/reference/updating-mapper)).

**Sweet spot :** projection lexicale secondaire d'une archive append-only à très faible coût objet, logs d'audit ou observabilité.

**Verdict :** disqualifié comme recherche principale multimodale/hybride. L'utiliser uniquement pour le froid introduirait un deuxième langage de recherche et une fusion inter-moteurs ; à envisager seulement si l'économie d'archive devient mesurée et majeure.

## 7. LanceDB : excellente bibliothèque embarquée, FTS encore trop expérimental

LanceDB OSS est Apache-2.0 et s'embarque directement dans Python, TypeScript, Rust ou Java. Les données Lance sont versionnées, les updates/deletes produisent de nouvelles versions ou deletion indexes, et l'API couvre vector, FTS, filtres et hybrid ([dépôt](https://github.com/lancedb/lancedb), [SDK](https://lancedb.github.io/lancedb/), [lecture/écriture Lance](https://lancedb.github.io/lance/introduction/read_and_write.html)). Pour un plugin local, une application desktop ou un prototype multimodal, la DX est remarquable.

Cependant, la référence Python qualifie encore la création d'index FTS de **hautement expérimentale et susceptible de changer** ([API FTS](https://lancedb.github.io/lancedb/python/python/#lancedb.table.Table.create_fts_index)). L'OSS est d'abord une bibliothèque embarquée, pas un serveur distribué HA avec contrôle d'accès et orchestration comparable à Weaviate/Vespa. Le GC des anciennes versions exige aussi de coordonner les writers ; la documentation prévient qu'un nettoyage agressif pendant une autre transaction peut corrompre le dataset ([optimize/cleanup](https://lancedb.github.io/lancedb/js/interfaces/OptimizeOptions/)).

**Sweet spot :** local/embedded, expérimentation, index par utilisateur ou batch analytique sur object storage.

**Verdict :** non pour le service partagé Agency. Son format peut devenir intéressant pour des datasets ML dérivés, mais pas comme API de recherche centrale actuelle.

## Comparaison par disqualifiant

| Candidat | Sweet spot | Disqualifiant actuel pour le socle Quivr/Agency |
|---|---|---|
| PostgreSQL + pgvector | Zéro service supplémentaire, transactions, ACL/RLS, MVP | Ranking texte limité, ANN filtré délicat, scale corpus non natif |
| Typesense | DX produit exceptionnelle, typo/facettes, corpus chaud borné | Chaque nœud contient tout le corpus et les index RAM |
| ParadeDB | BM25/vector/SQL transactionnels sans ETL | AGPL ; HA/réplication physique/read replicas annoncés en Enterprise |
| Weaviate | Meilleur compromis API, hybrid filtré et scale OSS | Deuxième base ; texte/ranking moins programmable ; cold tier limité |
| Vespa | Retrieval/ranking avancé et scale massif | Courbe d'apprentissage et exploitation/application package spécifiques |
| Quickwit | Archive textuelle temporelle sur S3 | Pas de vectoriel ; mutations et mapping orientés append-only |
| LanceDB | Embedded multimodal très agréable | Pas de service distribué HA OSS comparable ; FTS expérimental |

## Architecture recommandée

```text
                  ┌─────────────────────────────────────┐
Connecteurs/API ─▶│ PostgreSQL + S3 : vérité canonique │
                  │ transaction record/version/outbox  │
                  └──────────────┬──────────────────────┘
                                 │ projection idempotente
                                 ▼
                 ┌───────────────────────────────────────┐
                 │ SearchProjection                     │
                 │ v1 = PostgreSQL FTS + pgvector       │
                 │ v2 = Weaviate OU Vespa, après bench  │
                 └─────────────────┬─────────────────────┘
                                   │
             Search API ─ Query AST + ACL obligatoires ─┘
```

### Frontière publique, pas lowest-common-denominator

Le contrat interne ne doit exposer ni SQL, ni GraphQL Weaviate, ni YQL Vespa. Il doit exprimer les besoins du produit :

```text
SearchRequest
  scope: organization/corpus/visibility principal
  query: text + optional vectors
  filters: typed boolean tree
  time_range
  candidate_plan: lexical | semantic | hybrid
  ranking_profile
  page_cursor

SearchHit
  record_version_id / part_id
  score + score_components
  highlights
  matched_projection_generation
```

Les profils de ranking sont des ressources Quivr versionnées. L'adapter PostgreSQL peut en supporter un sous-ensemble ; le moteur distribué expose davantage de capacités via feature flags. Il ne faut pas réduire Vespa à ce que sait faire PostgreSQL : l'API peut déclarer les capacités et refuser proprement un profil indisponible.

### Migration sans big bang

1. Chaque projection porte `projection_generation`, `record_version_id` et le modèle/schema version.
2. L'outbox permet de rejouer toutes les versions vers un nouvel adapter.
3. Un backfill remplit la génération candidate depuis PostgreSQL/S3 pendant que les événements live sont appliqués aux deux générations.
4. Un shadow mode compare rappel, NDCG/precision sur un jeu jugé, p95, débit d'update, consommation et taux d'erreurs.
5. Le read switch se fait par configuration, puis l'ancienne projection est conservée pendant une fenêtre de rollback avant GC.

La source de vérité n'est jamais migrée : seul un index reconstruisible change.

## Benchmark décisionnel Weaviate vs Vespa

Le benchmark doit utiliser un corpus synthétique de taille réaliste et un échantillon Agency autorisé, avec : texte français/anglais/arabe, noms propres, corrections, mêmes photos dans plusieurs records, ACL de cardinalités variées, deux vecteurs par part et backfill concurrent.

Mesurer au minimum :

- `publication -> lexical searchable` et `publication -> hybrid searchable` ;
- p50/p95/p99 de requêtes exactes, phrases, filtres temporels et hybrides ;
- rappel sous filtres ACL à 0,01 %, 1 %, 10 % et 50 % de sélectivité ;
- débit d'upsert/delete et délai de propagation d'une correction ;
- recherche pendant panne d'un nœud et pendant ajout d'un nœud ;
- rebuild complet et rattrapage du live ;
- qualité jugée : Recall@100, nDCG@10/MRR, fraîcheur et diversité ;
- coût RAM/CPU/disque pour 1× et 4× le nominal ;
- temps ingénieur pour schéma, migration, diagnostic et ranking personnalisé.

**Règle de décision proposée :** choisir Weaviate s'il tient les SLO/qualité avec le ranking contenu dans Quivr, car sa DX et son exploitation initiale sont plus accessibles. Choisir Vespa si Weaviate oblige à sortir plusieurs étages de candidate generation/ranking du moteur, si les filtres + hybrid ne tiennent pas le p95, ou si Vespa apporte un gain de qualité significatif avec un coût d'exploitation accepté.

## Décisions à ne pas prendre sans benchmark

- Ne pas choisir Typesense seulement parce que son API est agréable : son modèle de réplication ne devient pas shardé avec plus de nœuds.
- Ne pas choisir ParadeDB seulement parce qu'il supprime le CDC : vérifier juridiquement l'AGPL et commercialement la frontière Community/Enterprise.
- Ne pas choisir Vespa seulement parce qu'il « scale à des milliards » : chiffrer la taxe DX et l'exploitation self-hosted.
- Ne pas choisir Weaviate seulement parce qu'il est vector-native : évaluer sérieusement le lexical journalistique.
- Ne pas ajouter Quickwit comme archive avant d'avoir prouvé que PostgreSQL/S3 + la politique d'index chaud ne suffisent pas.
- Ne pas promettre plusieurs backends officiels à la communauté. Le contrat de projection protège Quivr ; il n'oblige pas l'équipe à maintenir chaque adapter imaginable.
