# Stockage et recherche pour le backend Quivr

_Recherche effectuée le 3 septembre 2026. Les constats portent sur les documentations et licences officielles disponibles à cette date. Les appréciations de simplicité et de risque sont des inférences architecturales, pas des benchmarks éditeurs._

## Décision courte

Pour le premier backend robuste, la meilleure combinaison n'est pas la plus spécialisée :

1. **PostgreSQL** comme source de vérité transactionnelle du catalogue, des identités, versions, droits, politiques de rétention, états et lineage ;
2. **Apache Kafka en mode KRaft** comme journal opérationnel rejouable, avec les gros contenus transmis par référence et non dans les messages ;
3. **OpenSearch** comme unique projection de recherche lexicale, vectorielle et filtrée ;
4. **une API S3-compatible** comme source de vérité des blobs bruts et dérivés lourds, avec AWS S3 ou Ceph RGW selon l'environnement ;
5. **ni vector store séparé ni Iceberg au départ**.

Ce choix minimise le nombre d'états distribués à synchroniser tout en gardant tous les index reconstruisibles. Les millions de contenus et les centaines d'utilisateurs attendus ne constituent pas, à eux seuls, une preuve qu'il faut Milvus, Qdrant, Pulsar ou Vespa.

La trajectoire de scale conserve PostgreSQL, Kafka et S3. Elle remplace OpenSearch par **Vespa** uniquement si des essais représentatifs montrent qu'un ranking multi-étapes ou la combinaison forte ingestion + recherche ne tient pas les SLO avec OpenSearch. Elle ajoute **Parquet + Apache Iceberg derrière un REST Catalog** seulement lorsque les relectures analytiques et backfills complets deviennent une charge récurrente. Changer simultanément broker, moteur de recherche et format d'archive serait un risque inutile.

## Principes qui commandent le choix

- L'acceptation d'une ingestion signifie que l'identité, le reçu et le blob — ou sa référence immuable vérifiée — sont durables. Kafka transporte ensuite le travail ; il n'est ni le catalogue ni l'archive définitive.
- PostgreSQL et le stockage objet sont les sources de vérité. OpenSearch, Vespa, Qdrant ou Milvus sont des **projections supprimables et reconstruisibles**.
- Une enveloppe d'événement contient `organization_id`, `corpus_id`, `record_id`, `record_version_id`, le type et des références de blobs. Elle ne transporte normalement ni vidéo ni image complète.
- La livraison est au moins une fois. Les identifiants stables, contraintes d'unicité, offsets/checkpoints et écritures idempotentes rendent le rejeu sûr.
- La sécurité métier reste dans l'API Quivr. Un index ne reçoit jamais une requête utilisateur sans filtre d'organisation, de corpus et de visibilité calculé par le cœur.
- Le journal conserve une fenêtre de replay opérationnelle. L'historique long appartient au stockage objet ; payer un tiering de broker n'est justifié que si les consommateurs doivent relire directement un backlog ancien.
- Aucun résultat de benchmark éditeur ne permet de classer honnêtement ces produits pour le workload Quivr. Les données, mappings, filtres de droits, distributions de tailles et modèles d'embedding doivent être testés ensemble.

## PostgreSQL : le bon rôle, et sa limite

PostgreSQL est sous une licence open source permissive proche de BSD/MIT ([licence officielle](https://www.postgresql.org/about/licence/)). Il apporte transactions, contraintes, relations et row-level security. Lorsqu'une table a RLS activé, les accès ordinaires sont soumis à une politique et l'absence de politique produit un refus par défaut ; les superusers, propriétaires de table et rôles `BYPASSRLS` demandent toutefois une discipline particulière ([RLS](https://www.postgresql.org/docs/current/ddl-rowsecurity.html), [attributs des rôles](https://www.postgresql.org/docs/current/role-attributes.html)).

Il convient donc à :

- l'identité stable de `Record` et les `RecordVersion` immuables ;
- le catalogue de blobs, parts, relations et annotations ;
- les reçus d'ingestion, idempotency keys et outbox ;
- la configuration des connecteurs et plugins ;
- les droits, legal holds, retention classes et tombstones ;
- les états et checkpoints dont la transactionnalité importe.

Les tables append-heavy peuvent être partitionnées par temps, éventuellement sous-partitionnées par corpus. PostgreSQL supporte le range/list/hash partitioning, le pruning, le détachement rapide d'une partition et le placement de partitions froides sur un stockage moins coûteux ; sa propre documentation avertit qu'un mauvais nombre ou une mauvaise clé de partitions dégrade la planification ([partitionnement](https://www.postgresql.org/docs/current/ddl-partitioning.html)). Il faut donc partitionner les tables volumineuses d'événements et versions, pas chaque table par réflexe.

Pour reprise, PostgreSQL fournit base backups, archivage WAL et restauration à un instant donné ; les sauvegardes incrémentales imposent néanmoins de gérer la chaîne de dépendances entre sauvegardes ([PITR et sauvegardes](https://www.postgresql.org/docs/current/continuous-archiving.html)). Les hot standbys servent les lectures, mais le PostgreSQL cœur reste fondamentalement un primaire d'écriture ; il n'offre pas de multi-primary synchrone intégré ([comparaison des réplications](https://www.postgresql.org/docs/current/different-replication-solutions.html)).

**Conclusion :** excellent control plane et catalogue, à garder compact. Ne pas y mettre les médias, les gros textes dérivés répétés, les index ANN ou l'intégralité de la télémétrie. Le scale se traite d'abord par index corrects, partitionnement temporel des tables volumineuses, pooling, répliques de lecture et archivage, pas par un sharding prématuré.

## Journal et files : Kafka, Redpanda ou Pulsar

| Critère | Apache Kafka | Redpanda | Apache Pulsar |
|---|---|---|---|
| Licence | Apache 2.0 ([licence](https://github.com/apache/kafka/blob/trunk/LICENSE)). | Community sous BSL 1.1, donc _source available_ et non OSS au sens strict ; interdiction de fournir un service de streaming/queue, conversion d'une version en Apache 2.0 après quatre ans ([texte BSL](https://github.com/redpanda-data/redpanda/blob/dev/licenses/bsl.md), [modèle de licence](https://docs.redpanda.com/current/get-started/licensing/overview/)). | Apache 2.0 ([projet officiel](https://pulsar.apache.org/)). |
| Exploitation | Intermédiaire. KRaft retire ZooKeeper, mais la documentation recommande des rôles broker/controller séparés pour un environnement critique et au moins trois controllers pour tolérer une panne ([KRaft](https://kafka.apache.org/42/operations/kraft/)). | La promesse d'un binaire sans JVM ni ZooKeeper et la compatibilité Kafka réduisent le nombre de composants ; c'est l'avantage opérationnel principal documenté par le projet ([dépôt officiel](https://github.com/redpanda-data/redpanda)). | La plus complexe des trois : brokers, BookKeeper et metadata store sont des couches distinctes ([architecture](https://pulsar.apache.org/docs/next/concepts-architecture-overview/)). Cette séparation est aussi ce qui permet de scaler compute et stockage indépendamment. |
| Partitionnement et scale | Ordre par partition et réplication par topic-partition. Ajouter des brokers ne déplace pas les données automatiquement ; l'opérateur doit initier et surveiller les réassignations ([concepts](https://kafka.apache.org/documentation/), [extension d'un cluster](https://kafka.apache.org/42/operations/basic-kafka-operations/)). | Partitionnement et API Kafka, avec un runtime plus intégré. Ne pas déduire un avantage de débit sans benchmark Quivr. | Brokers stateless avec équilibrage par bundles et BookKeeper scalable séparément. Les subscriptions `Shared` font file sans ordre ; `Key_Shared` préserve l'affinité de clé ([load balancing](https://pulsar.apache.org/docs/4.2.x/administration-load-balance/), [subscriptions](https://pulsar.apache.org/docs/next/concepts-messaging/)). |
| Multi-tenancy / isolation | ACL par ressource et quotas par principal/client, mais pas de hiérarchie métier native tenant/namespace ([ACL](https://kafka.apache.org/42/security/authorization-and-acls/), [multi-tenancy et quotas](https://kafka.apache.org/42/operations/multi-tenancy/)). Suffisant pour une instance Agency dédiée. | Compatibilité Kafka pour ACLs/quotas de base ; plusieurs contrôles avancés, dont certaines fonctions RBAC/DR/tiering, sont liés à l'édition entreprise. | Tenant puis namespace sont des primitives de capacité, auth, quotas, TTL et isolation ; c'est le meilleur modèle natif pour un grand cluster réellement mutualisé ([multi-tenancy](https://pulsar.apache.org/docs/4.0.x/concepts-multi-tenancy/)). |
| Replay, queue et ordre | Excellent journal à offsets ; ordre seulement dans une partition. Les consumers reprennent depuis leurs offsets ([distribution](https://kafka.apache.org/42/implementation/distribution/)). Les retries, délais et DLQ restent des conventions applicatives. | Même écosystème client Kafka, mais les extensions serveur spécifiques recréent du lock-in. | Subscriptions nommées, ack individuel, redelivery et plusieurs modes queue/pub-sub intégrés ; `Key_Shared` est pertinent pour ordonner par record. |
| Tiered storage | API de remote storage dans Kafka, mais aucune implémentation `RemoteStorageManager` S3/HDFS n'est livrée par Apache Kafka ; les compacted topics ne sont pas supportés dans le tiering documenté ([tiered storage](https://kafka.apache.org/42/operations/tiered-storage/)). | Tiered Storage, topic recovery et whole-cluster restore exigent une licence entreprise ; la migration entre fournisseurs ou buckets n'est pas supportée ([tiering](https://docs.redpanda.com/current/manage/tiered-storage/), [licences](https://docs.redpanda.com/25.3/get-started/licensing/overview/)). | Tiering natif : segments BookKeeper scellés offloadés vers S3/GCS/Azure ou stockage S3-compatible, lisibles ensuite de façon transparente ([tiered storage](https://pulsar.apache.org/docs/4.1.x/tiered-storage-overview/)). |
| Portabilité / lock-in | Très portable, protocole et écosystème larges ; le lock-in porte surtout sur les schémas d'événements et les services managés non standard. | Faible lock-in client si on reste au sous-ensemble Kafka, mais risque licence/fonctions entreprise côté serveur. | Apache et multi-cloud, mais APIs, modèle tenant/namespace, BookKeeper et outils sont spécifiques à Pulsar ; migrer le broker devient un vrai projet. |

### Verdict broker

**Kafka est la référence recommandée**, non parce qu'il est le plus populaire, mais parce que :

- sa licence correspond à un backend redistribuable réellement OSS ;
- Quivr a besoin d'un journal de quelques jours/semaines, pas d'une archive historique dans le broker ;
- l'instance Agency est dédiée, donc l'absence de tenant/namespace natif est peu coûteuse ;
- ses limites de scale sont connues et testables, et les événements restent portables.

Redpanda peut rester un backend compatible optionnel pour un déploiement qui accepte sa licence, mais il ne doit pas définir la distribution OSS de référence. Son tiering payant retire précisément un argument important pour les archives longues.

Pulsar devient rationnel seulement si Quivr opère ultérieurement un **grand cluster partagé** avec isolation par tenant/namespace, très nombreux topics, backlogs longs directement relisibles, scaling indépendant du stockage ou geo-réplication native. Il n'est pas justifié pour éviter quelques opérations Kafka dans une installation Agency dédiée.

## Recherche intégrée : OpenSearch ou Vespa

| Critère | OpenSearch | Vespa |
|---|---|---|
| Licence | Apache 2.0 ([dépôt officiel](https://github.com/opensearch-project/OpenSearch)). | Apache 2.0 ([dépôt officiel](https://github.com/vespa-engine/vespa)). |
| Recherche hybride | BM25, ANN/exact k-NN, vecteurs denses ou sparse, fusion par search pipeline, pré/post filtering et aggregations ([techniques vectorielles et hybrid search](https://docs.opensearch.org/latest/vector-search/vector-search-techniques/index/), [filtres k-NN](https://docs.opensearch.org/latest/vector-search/filter-search-knn/index/)). | `nearestNeighbor`, BM25/weakAnd, filtres booléens, tenseurs et expressions de ranking multi-phases dans un même moteur. La séparation retrieval/ranking est nettement plus programmable ([guide nearest-neighbor](https://docs.vespa.ai/en/querying/nearest-neighbor-search-guide), [tutoriel hybride](https://docs.vespa.ai/en/learn/tutorials/hybrid-search)). |
| Ingestion, update, delete | Index/update/delete NRT. Un update crée une nouvelle version Lucene ; un delete marque le document puis le retire lors d'un merge, donc les corrections fréquentes créent de la dette de segments ([index/update](https://docs.opensearch.org/latest/api-reference/document-apis/index-document/), [delete](https://docs.opensearch.org/latest/api-reference/document-apis/delete-document/)). | Put, partial update et remove en temps réel ; les documents sont distribués par buckets, avec tombstones récents. Le système redistribue les buckets lors d'un changement de topologie ([élasticité](https://docs.vespa.ai/en/content/elasticity.html)). |
| Partitionnement / scaling | Shards et replicas explicites ; allocation par zones, rôles de nœuds, backpressure et séparation possible des workloads index/search ([création et zones](https://docs.opensearch.org/latest/tuning-your-cluster/), [séparation index/search](https://docs.opensearch.org/latest/tuning-your-cluster/separate-index-and-search-workloads/)). Les mauvais choix de shard count coûtent cher. | Buckets et redistribution gérés automatiquement. Ajouter des nœuds augmente la capacité corpus ; répliquer le corpus par groupes augmente le débit de requêtes. La documentation revendique une conception allant à des centaines de nœuds et milliards de documents, ce qui reste à vérifier sur Quivr ([operations](https://docs.vespa.ai/en/basics/operations.html), [dimensionnement](https://docs.vespa.ai/en/performance/sizing-search.html)). |
| Multi-tenancy / sécurité | Security plugin : rôles, permissions index, DLS et FLS. Attention : DLS/FLS protègent les lectures, pas un rôle autorisé à écrire/supprimer ; les écritures doivent donc rester derrière Quivr ([DLS](https://docs.opensearch.org/latest/security/access-control/document-level-security/), [FLS](https://docs.opensearch.org/latest/security/access-control/field-level-security/)). Les « tenants » Dashboards isolent surtout les objets UI, pas les données applicatives. | En self-hosted, les endpoints permettent par défaut des lectures/écritures non authentifiées, les protocoles internes ne sont pas sécurisés par défaut, et l'autorisation applicative se code via filters ; isolation réseau et mTLS sont nécessaires ([sécurisation self-hosted](https://docs.vespa.ai/en/security/securing-your-vespa-installation.html)). L'isolation organisationnelle reste une responsabilité Quivr ou un cluster/content cluster séparé. |
| Snapshots / rebuild | Snapshots incrémentaux vers FS, S3, HDFS ou Azure ; restore versionné. Les searchable snapshots sont read-only et lisent à la demande depuis le repository avec cache, au prix d'une latence plus haute ([snapshots](https://docs.opensearch.org/latest/tuning-your-cluster/availability-and-recovery/snapshots/snapshot-restore/), [searchable snapshots](https://docs.opensearch.org/latest/tuning-your-cluster/availability-and-recovery/snapshots/searchable_snapshot/)). | Vespa Cloud possède des backups gérés. En self-hosted, la voie documentée est surtout l'export/visit et le refeed ; cela renforce l'obligation de garder S3/PostgreSQL comme source de rebuild ([gestion des données](https://docs.vespa.ai/en/operations/data-management.html), [outils self-hosted](https://docs.vespa.ai/en/reference/operations/self-managed/tools.html)). |
| Tiering / archives | Searchable snapshots sur nœuds `warm`, y compris k-NN pour certains moteurs, utiles pour un index d'archive read-only ; les requêtes froides sont plus lentes et consomment des requêtes objet/cache. | Pas d'équivalent self-hosted aussi direct au searchable snapshot S3 dans les sources examinées. Vespa optimise mémoire/disque et distribution mais ne remplace pas un archive store. |
| Exploitation / lock-in | Exploitation intermédiaire et compétences répandues ; mappings, analyzers, shard topology et Query DSL créent du couplage. | Plus forte courbe d'apprentissage : application packages, schemas, YQL, tensor/rank expressions et topology Vespa. Le moteur absorbe en revanche des fonctions qui nécessiteraient ailleurs plusieurs services. L'offre OSS self-hosted automatise moins d'opérations que le Kubernetes Operator ou Vespa Cloud ([matrice opérations](https://docs.vespa.ai/en/basics/operations.html)). |

### Verdict recherche

**OpenSearch est le choix initial.** Il couvre le lexical, les vecteurs, les filtres de droits et les aggregations dans une projection unique, possède une procédure de snapshot/restore OSS claire et un chemin d'archive read-only. Pour 30 requêtes concurrentes et des dizaines de millions de contenus textuels, rien dans les exigences connues ne justifie encore la migration vers un moteur plus spécialisé.

**Vespa est la trajectoire si le retrieval devient le produit différenciant** : plusieurs candidate retrievers, ranking multi-phases, signaux éditoriaux et modèles personnalisés exécutés à fort débit, avec mises à jour continues. Son avantage n'est pas « plus rapide » en général — aucune source primaire comparable ne le démontre pour Quivr — mais une architecture nativement conçue pour exprimer et servir un ranking sophistiqué à grande échelle.

Dans les deux cas, l'API publique Quivr doit traduire un AST de requête interne vers le DSL du moteur. Aucun plugin ne doit émettre directement du Query DSL OpenSearch ou YQL Vespa, sinon la projection cesse d'être remplaçable.

## Faut-il un vector store séparé ? Qdrant ou Milvus

| Critère | Qdrant | Milvus |
|---|---|---|
| Licence | Apache 2.0 ([dépôt](https://github.com/qdrant/qdrant)). | Apache 2.0 ([dépôt](https://github.com/milvus-io/milvus)). |
| Exploitation | Simple en mono-nœud ; le mode distribué ajoute Raft, shards, replicas et load balancer. Le self-hosted demande un stockage bloc POSIX et ne stocke pas ses données actives sur S3 ([déploiement distribué](https://qdrant.tech/documentation/scaling/distributed_deployment/), [contraintes de stockage](https://qdrant.tech/documentation/installation/)). | Nettement plus composé : proxies, coordinator, streaming/query/data nodes, etcd, WAL et object storage. Cette désagrégation autorise un scale horizontal fin mais demande une exploitation Kubernetes solide ([architecture](https://milvus.io/docs/architecture_overview.md)). |
| Hybrid et filtres | Named dense/sparse/multivectors, payload indexes, fusion et requêtes multi-étapes ([hybrid queries](https://qdrant.tech/documentation/search/hybrid-queries/)). Qdrant est devenu capable de BM25/sparse, mais son centre de gravité reste vector-first. | Plusieurs champs denses/sparse, recherche multimodale, fusion/rerank et indexes scalaires ([hybrid multimodal](https://milvus.io/docs/multi-vector-search.md), [scalar indexes](https://milvus.io/docs/scalar_index.md)). |
| Multi-tenancy | Collection dédiée pour isolation forte, payload tenant pour forte cardinalité, user-defined shards pour gros tenants, et stratégie tiered combinant les deux ([multitenancy](https://qdrant.tech/documentation/tutorials/multiple-partitions/)). | Database, collection, partition ou partition key, avec compromis explicites ; RBAC seulement aux niveaux database/collection dans la documentation examinée ([multitenancy](https://milvus.io/docs/v2.5.x/multi_tenancy.md)). |
| Updates/deletes | Upsert/delete de points, WAL et ordering configurables. Le nombre de shards doit être anticipé en self-hosted ; la documentation recommande souvent une nouvelle collection pour resharder hors Cloud ([scaling](https://qdrant.tech/documentation/scaling/distributed_deployment/)). | L'upsert classique est un insert + delete ; les upserts massifs peuvent augmenter fortement la mémoire des data nodes. Le niveau de consistance est configurable de eventual à strong ([upsert](https://milvus.io/docs/v2.6.x/upsert-entities.md), [consistance](https://milvus.io/docs/consistency.md)). |
| Snapshots / tiering | Snapshots par collection et par nœud ; dans un cluster, chaque nœud doit être snapshoté. Les snapshots peuvent aller sur S3-compatible, mais les données actives restent sur bloc local. Payload/vectors ont des modes cached/cold locaux ([snapshots](https://qdrant.tech/documentation/operations/snapshots/), [storage](https://qdrant.tech/documentation/storage/)). | `milvus-backup` copie métadonnées et segments, avec une matrice de compatibilité de versions non symétrique. Le tiered storage objet, disponible depuis 2.6.4, charge les indexes/chunks à la demande et accepte une latence de premier accès supérieure ([backup](https://milvus.io/docs/v2.6.x/milvus_backup_overview.md), [tiered storage](https://milvus.io/docs/tiered-storage-overview.md)). |
| Lock-in | API payload/filter et stratégie de shard spécifiques, mais export/rebuild relativement direct. | Modèle collection/segment/index, dépendances et opérations très spécifiques ; le stockage objet interne n'est pas à traiter comme un lakehouse lisible directement. |

### Verdict vector store

**Ne déployer ni Qdrant ni Milvus dans le MVP.** Avec OpenSearch, un vector store séparé duplique :

- les IDs et versions projetés ;
- les filtres d'organisation, corpus, droits et rétention ;
- le traitement des corrections et tombstones ;
- les snapshots, monitoring et procédures de rebuild ;
- la fusion lexical/vectoriel, qui doit alors revenir dans Quivr.

Qdrant est le meilleur candidat si un besoin futur exige de scaler le vectoriel indépendamment tout en restant relativement simple. Milvus se justifie seulement lorsque le corpus vectoriel et la cadence de rebuild nécessitent réellement une architecture compute/storage désagrégée — typiquement après une preuve de charge, pas sur une estimation. Si le produit a besoin à la fois d'un lexical fort et d'un ranking vectoriel sophistiqué, Vespa est une trajectoire plus cohérente que le couple OpenSearch + Milvus.

## Stockage objet et archive

### Contrat minimal portable

Quivr doit cibler un sous-ensemble S3 testé par contract tests : multipart upload, checksums, range GET, versioning, presigned URLs, tags, lifecycle, notifications si utilisées, et Object Lock si la politique l'exige. « S3-compatible » ne garantit pas une identité de comportement sur toutes ces fonctions : Ceph ne revendique par exemple que la compatibilité avec le modèle de base et publie une matrice par feature ([Ceph RGW S3](https://docs.ceph.com/en/latest/radosgw/s3/)).

Deux backends de production suffisent comme cibles de référence :

- **AWS S3** lorsque le déploiement accepte un service propriétaire : grande simplicité opératoire, lifecycle vers Glacier et Object Lock ;
- **Ceph RGW** pour l'on-premise ou un cloud contrôlé : API S3, versioning, lifecycle, object lock et transition vers des tiers externes, mais coût d'exploitation Ceph élevé ([lifecycle RGW](https://ceph.io/en/news/blog/2025/rgw-deep-dive-2/), [Object Lock RGW](https://docs.ceph.com/en/latest/radosgw/s3/bucketops/)).

MinIO ne doit pas être imposé comme dépendance implicite. Ses pages officielles présentent encore une édition AGPLv3/commerciale, tandis que la distribution communautaire courante est source-only et les binaires éditeur récents relèvent d'une autre licence d'évaluation/commerciale ; cette évolution doit être validée juridiquement et opérationnellement avant packaging ([licence AGPL/commerciale](https://min.io/docs/minio/linux/reference/minio-mc/mc-license-info.html), [distribution communautaire](https://github.com/minio/minio), [licence actuelle des binaires](https://docs.min.io/license/)). Une interface S3 et Ceph comme cible OSS de production réduisent ce risque.

### Lifecycle et legal hold

Le moteur calcule la politique effective ; le stockage exécute les transitions physiques. Une règle S3 peut déplacer les objets vers Standard-IA puis Glacier ou les expirer ([S3 Lifecycle](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lifecycle-mgmt.html)). Object Lock protège une **version** par période ou legal hold et exige le versioning ; une simple suppression peut seulement ajouter un delete marker tandis que la version verrouillée subsiste ([S3 Object Lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)).

Conséquences :

- conserver dans PostgreSQL le `bucket`, `key`, `version_id`, checksum, taille, classe de stockage, état de restauration et date de rétention ;
- ne jamais prendre un delete marker pour la preuve d'une purge physique ;
- dissocier `deindex_at`, `transition_at`, `delete_at` et `legal_hold` ;
- rendre la restauration de Glacier asynchrone ;
- vérifier qu'une politique storage n'efface pas un blob encore référencé par un autre record ou retenu légalement.

### Parquet, Iceberg et catalogues

**Pas d'Iceberg dans le chemin d'ingestion initial.** Iceberg est un format de table pour grands datasets analytiques, pas un catalogue transactionnel de records ni un blob store. L'ajouter implique writer, compaction de petits fichiers, maintenance des manifests/snapshots et catalog service.

Il devient utile si plusieurs moteurs — Spark, Flink, Trino, entraînement ML — doivent relire régulièrement les mêmes données normalisées. Il apporte alors schema/partition evolution, hidden partitioning, snapshots et time travel sur des fichiers ouverts, généralement Parquet ([introduction Iceberg](https://iceberg.apache.org/docs/latest/), [partitionnement](https://iceberg.apache.org/docs/latest/partitioning/), [spécification des snapshots](https://iceberg.apache.org/spec/)).

Le catalogue doit parler le protocole **Iceberg REST Catalog**, conçu pour éviter une implémentation par langage et faciliter le changement de fournisseur ([spécification REST](https://iceberg.apache.org/rest-catalog-spec/)). Apache Polaris est une implémentation Apache 2.0 de ce protocole et peut être retenu plus tard, mais il reste un service supplémentaire, pas une dépendance du MVP ([Apache Polaris](https://polaris.apache.org/)).

L'archive proposée est donc :

```text
blobs canoniques immuables          -> S3-compatible
catalogue / droits / lineage        -> PostgreSQL
projection de recherche             -> OpenSearch ou Vespa
tables analytiques dérivées (plus tard) -> Parquet + Iceberg + REST Catalog
```

Iceberg ne remplace aucune des trois premières lignes.

## Stack 1 — MVP robuste recommandé

```text
Connecteur/API
  -> upload/référence immuable S3
  -> transaction PostgreSQL : identité + version + reçu + outbox
  -> relai outbox vers Kafka
  -> workers idempotents / plugins distants
       -> annotations et lineage PostgreSQL
       -> blobs dérivés S3
       -> projection OpenSearch (lexical + dense/sparse vectors + filtres)

API Retrieval
  -> autorisation Quivr
  -> requête OpenSearch toujours scopée
  -> hydratation PostgreSQL + URL S3 temporaire si nécessaire
```

**Composants :** PostgreSQL, Kafka KRaft, OpenSearch, stockage S3-compatible. Aucun Redis, Qdrant, Milvus, Iceberg ou second moteur de recherche n'est requis par cette décision.

**Déploiement :** le Docker Compose OSS peut simplifier chaque composant pour le développement. La production Agency doit utiliser HA adaptée : PostgreSQL primaire + standby/PITR, Kafka répliqué, OpenSearch multi-nœuds avec replicas/snapshots et object store durable. Le Compose n'est pas une topologie de production.

**Priorités :** séparer les topics/consumer groups `realtime`, `correction-delete`, `enrichment`, `backfill` et `gc` afin qu'un backfill ne consomme pas le budget temps réel. Kafka ne fournit pas une priorité globale ; Quivr l'obtient par lanes, quotas de consumers et pools de workers distincts.

**Archive :** index chaud mutable pour la veille ; rollover temporel. Après la fenêtre chaude, snapshot puis restauration en searchable snapshot read-only si l'archive textuelle doit rester immédiatement interrogeable. Les médias anciens suivent le lifecycle S3 et peuvent exiger une restauration asynchrone.

## Stack 2 — trajectoire scale, déclenchée par preuve

```text
PostgreSQL + Kafka + S3 restent inchangés
                    |
                    +-> Vespa : retrieval/ranking unifié à fort scale
                    |
                    +-> Parquet/Iceberg + REST Catalog : scans analytiques et backfills
```

Cette stack n'est pas « la phase 2 obligatoire ». Elle garde les sources de vérité et remplace seulement les projections concernées :

- **OpenSearch -> Vespa** pour un besoin prouvé de ranking/retrieval complexe ou de scale de serving ;
- **objets bruts -> tables Iceberg dérivées** uniquement pour les workloads analytiques récurrents ;
- **Kafka reste Kafka** tant que son exploitation ne devient pas le problème principal.

Pulsar peut remplacer Kafka dans cette trajectoire uniquement si les exigences deviennent celles d'une plateforme de messaging multi-tenant à long backlog, et non parce que le corpus média grandit. Qdrant peut remplacer la projection vectorielle seule si cette charge se découple franchement du lexical, mais ce fork est moins cohérent que Vespa pour un produit de recherche hybride.

## Critères explicites de bascule

### OpenSearch vers Vespa

Bascule à étudier si, sur le corpus et les requêtes représentatifs :

1. OpenSearch ne respecte pas `p95 < 1 s` à 30 requêtes concurrentes pendant le pic d'ingestion, après une itération bornée de tuning et un test horizontal 1x -> 4x ;
2. le gain de capacité de 1x à 4x reste inférieur à l'objectif de 80 % proportionnel à cause de shard/ranking bottlenecks ;
3. les besoins de plusieurs candidate retrievers, filtres stricts, features éditoriales et ranking multi-phases poussent une part importante du retrieval dans un service applicatif externe ;
4. la dette d'updates/deletes Lucene empêche de tenir la fraîcheur malgré une stratégie d'index temporel correcte ;
5. l'équipe accepte d'opérer le Kubernetes Operator Vespa ou de financer Vespa Cloud et dispose d'un plan de backup/rebuild self-hosted.

Une seule de ces conditions faible ne suffit pas : il faut une mesure reproductible et un prototype de projection identique dans les deux moteurs.

### Ajouter Qdrant

Seulement si le lexical OpenSearch tient ses SLO mais que le vectoriel, avec dimensions/modalités réelles, ne les tient pas même après choix d'algorithme, compression et scaling ; et si l'indépendance de cadence/coût compense la double projection. Tester auparavant si Vespa simplifie davantage l'ensemble.

### Ajouter Milvus

Seulement après démonstration que Qdrant/Vespa/OpenSearch ne peuvent pas tenir la taille ou le rebuild vectoriel cible, et si une équipe sait déjà opérer Kubernetes, etcd, object store, WAL et les composants Milvus. « Des millions de documents » n'est pas ce seuil.

### Kafka vers Pulsar

Au moins deux des contraintes suivantes doivent devenir structurantes :

- mutualisation de nombreuses organisations dans le même cluster avec quotas/isolation par namespace ;
- très grande cardinalité de topics et subscriptions indépendantes ;
- backlog long relu directement depuis object storage ;
- besoin de scaler brokers et stockage séparément sans campagnes de réassignation ;
- geo-réplication et failover de messaging natifs.

La bascule exige ensuite un test de client, sémantique d'ack/ordre, tooling et coût d'exploitation BookKeeper/metadata — pas seulement un benchmark de débit.

### Ajouter Iceberg et un REST Catalog

Lorsque l'un des faits suivants est observé :

- au moins deux moteurs analytiques doivent partager les données normalisées ;
- les replays complets depuis objets bruts sont réguliers et leur parsing domine coût/délai ;
- des scans historiques, entraînements ou audits parcourent fréquemment des années de corpus ;
- schema evolution et partition evolution deviennent nécessaires sans recopier tout le dataset.

Tant que les backfills sont rares et que PostgreSQL sait énumérer les blobs, un manifest de job suffit.

## Tests de décision avant production

1. **Corpus représentatif, pas synthétique uniquement** : tailles de dépêches, PDF, images, vidéos, langues, droits, corrections et suppressions.
2. **Charge combinée** : pic d'ingestion 2x, enrichissements lents et 30 requêtes concurrentes. Mesurer publication -> searchable, p50/p95/p99, consumer lag et taux d'erreur.
3. **Hybrid relevance avec filtres** : BM25 seul, vectoriel seul, fusion ; mesurer rappel/precision sur un jeu jugé, puis répéter avec filtres organisation/corpus/droits très sélectifs.
4. **Mutation** : rafales de nouvelles versions et tombstones ; mesurer fraîcheur, segments supprimés, compaction/merge et espace disque.
5. **Scale 1x -> 4x** : même workload et mêmes SLO, en séparant capacité d'ingestion, capacité corpus et débit de requêtes.
6. **Rebuild intégral** : vider la projection, la reconstruire depuis PostgreSQL + S3 et chronométrer le RTO. Un snapshot ne remplace pas ce test.
7. **Reprise** : panne d'un worker, broker, nœud search et object store temporairement indisponible ; vérifier idempotence, offsets, DLQ et absence de faux `searchable`.
8. **Lifecycle réel** : transition froide, restauration, legal hold, tombstone puis purge physique ; comparer inventaire S3 et catalogue PostgreSQL.

La décision finale de sizing ne doit être prise qu'après ces essais. Les documentations prouvent les mécanismes disponibles ; elles ne prouvent pas le comportement du pipeline Quivr avec son contenu, ses modèles et ses règles de sécurité.
