# Event runtime Quivr : avancé, mais avec une excellente DevX

Date: 2026-09-03 (last revised 2026-09-28)

Status: final research note.

_Recherche au 3 septembre 2026. Sources officielles uniquement._

## Conclusion courte

Le volume historique d'un grand corpus d'actualité — des millions de contenus — ne suffit pas à justifier Kafka. Le dimensionnement du bus dépend du **débit d'événements**, du nombre de consommateurs indépendants, de la durée du journal et du besoin de retraiter ce journal, pas du nombre total de `Record` conservés dans S3/PostgreSQL.

Pour Quivr, la meilleure base est probablement **Temporal-first, sans broker obligatoire** : PostgreSQL/S3 restent les sources de vérité, un outbox PostgreSQL déclenche des workflows Temporal idempotents, et Temporal fournit priorités, retries, reprise, visibilité et drainage des générations. C'est le meilleur ratio sophistication/DevX tant que le pipeline est principalement une orchestration de traitements par record.

Si le journal d'événements devient lui-même un contrat produit — nombreux abonnés plugins indépendants, rejeu libre, nouveaux consommateurs qui repartent dans le passé — la cible cohérente devient **NATS JetStream + Temporal**, avec une séparation stricte : JetStream transporte les faits, Temporal orchestre les travaux. Ne pas mettre chaque étape dans les deux systèmes.

Kafka KRaft est une excellente infrastructure de log, mais il ne fournit pas nativement l'orchestration, les priorités de jobs, les retries métier/DLQ ou le drainage d'une génération de plugin. Il devient pertinent après une preuve de charge ou lorsque l'écosystème Kafka est une exigence d'intégration, pas par réflexe architectural.

## Contraintes traduites en capacités

| Besoin Quivr | Capacité réellement nécessaire |
|---|---|
| Acceptation fiable | reçu durable dans PostgreSQL + outbox ; aucun broker ne remplace le catalogue métier |
| Temps réel | réveiller rapidement un traitement, sans attendre les enrichissements lents |
| Priorités | corrections/retraits et chemin minimal avant enrichissements/backfills |
| Retries/DLQ | backoff, nombre d'essais, erreur inspectable, reprise manuelle ou automatique |
| Replay/backfill | repartir d'une date/version de plugin, idéalement depuis le catalogue canonique |
| Plugins/générations | affectation explicite à une génération, pas un consumer group implicite et mutable |
| Drain | les nouveaux travaux vont à N+1 ; N termine ses travaux en cours avant arrêt |
| Gros médias | uniquement `blob_ref`, checksum, taille, type et autorisation temporaire dans les messages |
| Dev local | une commande, UI/CLI utile, peu de RAM, aucune topologie de production à comprendre pour contribuer |
| Production | réplication, scaling horizontal, Helm/operator, métriques/traces, procédures d'upgrade |

## Comparaison des primitives

### Temporal

Temporal est un moteur d'exécution durable, pas un log public. C'est précisément ce qui le rend très adapté au **pipeline** d'ingestion : Workflow = cycle de traitement d'un record, Activity = appel d'un plugin ou d'un indexeur, Task Queue = classe de capacité ou génération.

- Retries, timeouts et reprise après crash sont des concepts natifs ; le dépôt officiel le présente comme une plateforme qui reprend les workflows et retente les opérations échouées. Le serveur est sous licence MIT. [Temporal server](https://github.com/temporalio/temporal), [licence](https://github.com/temporalio/temporal/blob/main/LICENSE)
- Les Task Queues supportent maintenant cinq niveaux de priorité et une fairness pondérée par clé. La documentation cite directement le cas temps réel contre batch. L'ordre est toutefois approximatif à l'échelle globale car il est appliqué par partition. [Priority and Fairness](https://github.com/temporalio/documentation/blob/main/docs/develop/task-queue-priority-fairness.mdx)
- Worker Versioning sait faire du ramping, pinner un workflow sur une version et signaler les états `Draining` puis `Drained`; c'est très proche du lifecycle souhaité pour les générations de plugins. [Worker Versioning](https://github.com/temporalio/documentation/blob/main/docs/production-deployment/worker-deployments/worker-versioning.mdx)
- Le développement local est excellent : `temporal server start-dev` démarre serveur et UI. [README officiel](https://github.com/temporalio/temporal#download-and-start-temporal-server-locally)
- En production, le chart Helm officiel déploie plusieurs services Temporal et demande une persistance externe. Quivr peut réutiliser un cluster PostgreSQL, mais doit isoler les bases/schémas et gérer les migrations Temporal. Ce n'est donc pas « un seul petit conteneur » en production. [Guide self-hosted](https://github.com/temporalio/documentation/blob/main/docs/production-deployment/self-hosted-guide/deployment.mdx), [chart Helm](https://github.com/temporalio/helm-charts)
- Temporal n'a pas besoin d'une DLQ traditionnelle : un workflow/une activity épuisé reste un objet durable inspectable, réessayable ou terminable. C'est souvent une meilleure DevX qu'un message opaque déplacé dans une queue, mais il faut exposer ce modèle via l'API Quivr.
- Temporal n'est pas le bon journal pour diffuser tous les faits à des consommateurs arbitraires. Le backfill fonctionnel doit énumérer les `RecordVersion` dans le catalogue Quivr et démarrer des workflows idempotents ; il ne faut pas détourner l'historique interne Temporal en event store métier.

**Point de vigilance SDK :** un plugin ne doit pas dépendre directement des concepts internes Temporal. Deux runners sont possibles : (1) le SDK Quivr enveloppe un worker/activity Temporal pour les langages officiellement supportés ; (2) une Activity Quivr appelle l'API HTTP/gRPC d'un plugin externe. Dans les deux cas, le contrat public reste Quivr.

### NATS JetStream

NATS offre dans un seul petit serveur le pub/sub, les requêtes et JetStream. Sa simplicité de déploiement et ses sujets hiérarchiques donnent une excellente DevX pour un bus de plugins.

- JetStream stocke les messages et peut les rejouer ; un Consumer durable conserve sa position et fournit une livraison au moins une fois avec acknowledgements. [Vue d'ensemble JetStream](https://github.com/nats-io/nats.docs/blob/master/nats-concepts/jetstream/README.md), [Consumers](https://github.com/nats-io/nats.docs/blob/master/nats-concepts/jetstream/consumers.md)
- Les consumers exposent `MaxDeliver`, une séquence de `BackOff`, un départ par séquence ou date, `ReplayInstant`/`ReplayOriginal`, des filtres de sujets et le contrôle du nombre d'acks en vol. [Configuration des consumers](https://github.com/nats-io/nats.docs/blob/master/nats-concepts/jetstream/consumers.md)
- Après `MaxDeliver`, le message reste dans le stream et une advisory est émise : une vraie DLQ demande donc un petit contrôleur Quivr ou une republication explicite. De plus, `BackOff` s'applique au timeout d'ack ; un `nak` nécessite un délai explicite. Ce n'est pas difficile, mais ce n'est pas « DLQ automatique ».
- JetStream n'expose pas une priorité de message générale comparable à RabbitMQ 4.3 ou Temporal. Il faut modéliser quelques lanes (`critical`, `realtime`, `background`) par sujets/streams/consumers et réserver une concurrence à chacune. C'est plus explicite et évite la famine, mais Quivr porte la policy.
- Une génération de plugin peut avoir son propre Consumer durable. N+1 démarre `from now`, depuis une date, ou depuis une séquence ; N est supprimée lorsque son nombre d'acks en vol tombe à zéro. NATS fournit les compteurs, mais Quivr/Kubernetes porte la machine d'état de drainage.
- Le serveur et ses charts sont Apache-2.0. Il existe une image Docker officielle, un chart Helm officiel, et Surveyor exporte l'état d'un déploiement vers Prometheus. [Licence serveur](https://github.com/nats-io/nats-server/blob/main/LICENSE), [Docker officiel](https://github.com/nats-io/nats-docker), [Helm officiel](https://github.com/nats-io/k8s), [Surveyor](https://github.com/nats-io/nats-surveyor)

**Limite structurante :** JetStream résout très bien transport, fan-out et replay, mais pas un DAG durable multi-étapes avec compensation, pauses, timers, versioning de code et diagnostic par exécution. Construire ces éléments au-dessus de NATS reviendrait progressivement à recréer Temporal/Restate.

### RabbitMQ 4.3 : Quorum Queues + Streams

RabbitMQ est aujourd'hui plus intéressant pour Quivr qu'une image mentale basée sur les anciennes classic queues. La version 4.3 a ajouté aux Quorum Queues des priorités strictes, consumer timeouts et retries retardés.

- Les Quorum Queues sont répliquées par Raft et offrent priorité stricte sur 32 niveaux, poison-message handling, dead-lettering au moins une fois (à configurer), limite de livraison, consumer timeout et retry retardé linéaire. La documentation recommande néanmoins quelques queues de priorité séparées lorsqu'il faut garantir qu'un trafic bas ne soit jamais affamé. [Quorum Queues](https://www.rabbitmq.com/docs/quorum-queues), [priorités](https://www.rabbitmq.com/docs/priority)
- RabbitMQ recommande les Streams pour les backlogs très longs, gros fan-outs et lectures répétables. Les Streams sont des logs persistants rejouables par offset/date ; les Super Streams les partitionnent horizontalement. En revanche, un Stream ne possède ni priorité de messages ni DLX. [Streams et Super Streams](https://www.rabbitmq.com/docs/streams)
- Une architecture Quivr complète doit donc employer **deux structures** : un Stream comme journal de faits, des Quorum Queues comme files de travail prioritaires. Elles vivent dans le même cluster et la même UI, mais dupliquent les références de messages et imposent deux sémantiques aux développeurs.
- Le serveur est MPL-2.0, donc réellement open source. Un conteneur `rabbitmq:4-management` donne un excellent démarrage local et une UI ; l'équipe RabbitMQ maintient un Cluster Operator Kubernetes et les intégrations Prometheus/Grafana. [Licence](https://github.com/rabbitmq/rabbitmq-server/blob/main/LICENSE), [Docker](https://www.rabbitmq.com/docs/download), [Operator](https://www.rabbitmq.com/kubernetes/operator/operator-overview), [monitoring](https://www.rabbitmq.com/docs/prometheus)
- Les Quorum Queues ne sont pas faites pour une forte création/suppression de queues ni pour des backlogs supérieurs à environ cinq millions de messages ; cela interdit une queue éphémère par job, pas une queue durable par plugin/génération raisonnablement bornée. [Cas d'usage et limites](https://www.rabbitmq.com/docs/quorum-queues)

**Verdict :** excellente option « un seul produit de messaging » si la priorité/DLQ traditionnelle est dominante. Moins élégante que Temporal pour le lifecycle de workflows, et moins simple que NATS pour un bus de capacités.

### Apache Kafka en KRaft

Kafka est le meilleur des candidats évalués pour un journal massif partagé entre de nombreux consommateurs indépendants.

- Les topics sont des logs durables partitionnés ; chaque consumer group garde ses offsets et peut les rembobiner pour retraiter les événements. [Design Kafka](https://kafka.apache.org/documentation/#design_consumerposition), [quickstart](https://kafka.apache.org/quickstart/)
- KRaft a supprimé ZooKeeper. Un broker combinant controller et broker est simple en développement, tandis qu'une production sérieuse sépare généralement les rôles. L'image Docker ASF officielle rend le local nettement meilleur qu'autrefois. [KRaft](https://kafka.apache.org/documentation/#kraft), [image et quickstart](https://kafka.apache.org/quickstart/)
- Kafka ne fournit pas de priorité de messages, de retries métier/DLQ standardisés, de workflow ou de drain de génération. Ces comportements se construisent avec plusieurs topics, des conventions et du code, ou avec un orchestrateur séparé.
- Strimzi automatise Kafka/KRaft sur Kubernetes et fournit métriques, upgrades, users/topics et rebalancing, mais ajoute CRDs, operator, node pools et une vraie charge d'exploitation. Strimzi est lui-même Apache-2.0. [Guide Strimzi](https://strimzi.io/docs/operators/latest/deploying), [licence](https://github.com/strimzi/strimzi-kafka-operator)
- Kafka est Apache-2.0. [Licence](https://github.com/apache/kafka/blob/trunk/LICENSE)

**Verdict :** robuste, standard et prévisible, mais mauvais choix par défaut si Quivr doit ensuite réinventer son scheduler et son moteur de jobs. Il ne devient supérieur que lorsque le **log** et son écosystème comptent davantage que la DevX du pipeline.

### Redpanda

Redpanda améliore fortement la DevX Kafka : un binaire sans JVM/ZooKeeper, `rpk container start`, console et compatibilité API Kafka. [Dépôt officiel](https://github.com/redpanda-data/redpanda)

Mais la distribution courante est sous BSL 1.1, avec interdiction de proposer un « Streaming or Queuing Service » aux tiers avant le changement de licence de chaque version. [Licence BSL](https://github.com/redpanda-data/redpanda/blob/dev/licenses/bsl.md)

Pour un logiciel Quivr OSS qui permet à des tiers d'installer et d'étendre une plateforme d'ingestion, cette dépendance obligatoire contredit le critère déjà validé « dépendances centrales réellement open source ». Redpanda peut rester un backend Kafka-compatible **optionnel apporté par l'opérateur**, après validation juridique ; ce ne doit pas être la distribution de référence.

### Restate

Restate a probablement la surface la plus séduisante conceptuellement : fonctions durables, journaux, retries, idempotency, UI/CLI, services HTTP distants, service versioning et Operator Kubernetes qui conserve les anciennes ReplicaSets jusqu'au drainage.

- Une seule image Docker suffit en local et les SDK existent en TypeScript, Java/Kotlin, Python, Go et Rust. [Dépôt officiel](https://github.com/restatedev/restate)
- Les invocations durables reprennent au dernier step enregistré ; retries, rétention des journaux, pause/resume et introspection sont natifs. [Concepts](https://docs.restate.dev/foundations/key-concepts), [configuration](https://docs.restate.dev/services/configuration), [gestion des invocations](https://docs.restate.dev/services/invocation/managing-invocations)
- Le versioning envoie les nouvelles requêtes au dernier déploiement et conserve les anciennes sur leur déploiement d'origine ; l'Operator automatise le drainage. [Versioning](https://docs.restate.dev/services/versioning), [déploiement Kubernetes](https://docs.restate.dev/services/deploy/kubernetes)
- Prometheus, Grafana et OTLP sont documentés. [Métriques](https://docs.restate.dev/server/monitoring/metrics), [traces](https://docs.restate.dev/server/monitoring/tracing)
- Restate ne remplace pas un log public rejouable. Son ingestion Kafka existe, mais nécessite justement Kafka ; le chemin batch Kafka amélioré est encore marqué expérimental dans la documentation 1.6. [Kafka ingress](https://docs.restate.dev/services/invocation/kafka)
- Aucune primitive de priorité de tâches comparable à Temporal/RabbitMQ n'est documentée comme stable. Les virtual queues et limites de concurrence restent en partie décrites dans la roadmap OSS. [Roadmap OSS](https://docs.restate.dev/roadmap/oss)

Surtout, le serveur est en BSL 1.1, pas open source, avec une restriction explicite sur un « Public Restate Platform Service » permettant à des tiers d'enregistrer et invoquer leurs propres services. Un écosystème de plugins Quivr mérite une revue juridique approfondie. [Licence Restate](https://github.com/restatedev/restate/blob/main/LICENSE)

**Verdict :** très bon prototype ou backend optionnel, mais mauvais socle obligatoire pour la distribution OSS Quivr dans le cadre de la politique de licence actuelle.

## Matrice synthétique

Échelle qualitative : `++` natif et agréable, `+` faisable proprement, `±` conventions/code Quivr, `−` mauvais ajustement.

| Candidat | Local | Replay libre | Priorités | Retry / parking | Drain génération | Orchestration | K8s ops | Licence socle OSS |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Temporal | ++ | ± catalogue | ++ | ++ | ++ | ++ | + | ++ MIT |
| NATS JetStream | ++ | ++ | ± lanes | + | ± | − | ++ | ++ Apache-2.0 |
| RabbitMQ 4.3 hybride | ++ | ++ Streams | ++ QQ | ++ QQ | ± | − | ++ | ++ MPL-2.0 |
| Kafka KRaft | + | ++ | − | ± topics | ± | − | + Strimzi | ++ Apache-2.0 |
| Redpanda | ++ | ++ | − | ± topics | ± | − | ++ | − BSL |
| Restate | ++ | ± invocations | − | ++ | ++ | ++ | ++ | − BSL |

## Trois architectures cohérentes au maximum

### A — Temporal-first, sans broker obligatoire — recommandation initiale

```text
API/connecteur
   → transaction PostgreSQL : RecordVersion + ingestion_receipt + outbox
   → outbox dispatcher : StartWorkflow(id déterministe)
   → Temporal Workflow(record_version_id, pipeline_generation)
        → Activity plugin/parser/indexer par référence S3
        → événements produits dans l'outbox PostgreSQL
```

**Pourquoi c'est cohérent**

- Temporal possède l'exécution ; PostgreSQL possède les faits métier ; il n'y a qu'une seule autorité pour chaque responsabilité.
- Le `workflow_id` dérivé de `record_version_id + pipeline_generation` rend le dispatcher idempotent.
- Priorité Temporal : corrections/retraits = 1, temps réel = 2, interactif = 3, enrichissement = 4, backfill/GC = 5. Une fairness key peut représenter organisation/corpus/plugin pour empêcher la monopolisation.
- Une Task Queue ou un endpoint immuable identifie la génération. Worker Versioning ou la registry Quivr route les nouveaux travaux ; les anciens restent pinés et sont drainés.
- L'« équivalent DLQ » est un workflow durable en échec/pause avec son historique, pas un message perdu dans un topic annexe.
- Un backfill sélectionne les versions dans PostgreSQL selon un intervalle puis démarre des workflows avec une génération ciblée. Cela respecte la source de vérité même si la rétention d'un bus a expiré.

**Sweet spot :** pipeline par record, plusieurs étapes, traitements longs, retries fins, besoin d'audit et faible équipe plateforme. C'est le cas Quivr connu aujourd'hui.

**Seuil de bascule vers B :** les événements Quivr deviennent une API durable consommée indépendamment par de nombreux plugins/applications, ou l'on doit raccorder/décorder un consommateur et lui faire relire une fenêtre sans créer un workflow de fan-out central.

**Risque :** utiliser un workflow par micro-événement ou stocker de gros payloads dans l'historique. Les workflows ne transportent que IDs et références S3 ; les activités grossières évitent un historique excessif.

### B — NATS JetStream pour les faits + Temporal pour les commandes longues

```text
PostgreSQL outbox → JetStream `facts.*`
                         ├→ subscriptions plugins simples
                         ├→ projections/read models
                         └→ starter idempotent → Temporal workflows

Temporal → traitements multi-étapes, retries, priorité, versioning/drain
```

**Règle anti-complexité :** un fait est publié une fois dans JetStream. Un workflow Temporal est démarré uniquement lorsqu'il existe une orchestration durable ; chaque étape Temporal n'est pas republiée comme un nouveau job NATS. À l'inverse, NATS ne sert pas à implémenter les retries internes du workflow.

**Sweet spot :** plateforme de plugins où observation/fan-out/replay sont des capacités de premier rang, tout en gardant des pipelines sophistiqués. Dev local encore raisonnable : un conteneur NATS et `temporal server start-dev`; en production, deux control planes restent à opérer.

**Seuil de bascule depuis A :** au moins plusieurs consommateurs indépendants stables d'un même fait, besoin de rejeu par curseur/date sans passer par le catalogue, ou intégrations externes demandant un vrai bus.

**Seuil de bascule vers Kafka :** benchmark montrant que JetStream ne tient pas la combinaison débit × durée de rétention × nombre de consumers, ou exigence ferme de Kafka Connect/Schema Registry/clients Kafka déjà opérés chez les clients. Ce seuil doit être mesuré avec le payload réel de références, pas estimé depuis la taille du corpus.

**Risque :** deux systèmes de retry et deux vues d'état. L'API Quivr doit montrer clairement si un traitement est une subscription JetStream ou un Workflow Temporal et fournir un `trace_id` commun.

### C — RabbitMQ 4.3 hybride, sans moteur de workflow

```text
exchange `facts`
   ├→ Stream/Super Stream : journal rejouable
   └→ Quorum Queues par capability/generation : jobs prioritaires + retries + DLQ

workers Quivr → state machine de pipeline stockée dans PostgreSQL
```

**Pourquoi c'est cohérent :** un seul broker, une seule UI/operator, mais deux structures spécialisées. Les messages ne contiennent que des références ; la duplication de petits envelopes reste acceptable. Les states de pipeline restent explicites dans PostgreSQL.

**Sweet spot :** équipe à l'aise avec AMQP, pipeline relativement plat, priorité stricte/DLQ plus importantes que les workflows multi-étapes, désir d'un seul produit messaging open source.

**Seuil de sortie vers Temporal (A ou B) :** dès que Quivr accumule des tables/cron dédiés aux retries, timers, fan-in, compensations, reprise après pause et migration de workflows. Ce sont les symptômes d'un orchestrateur réinventé.

**Seuil de sortie vers Kafka :** journal partagé à très longue rétention et très grand fan-out, écosystème Kafka requis, ou benchmark qui impose plus de partitions/brokers que l'exploitation RabbitMQ souhaitée.

## Pourquoi Kafka + Temporal n'est pas la recommandation par défaut

Cette combinaison est techniquement excellente : Kafka tient le journal et Temporal les workflows. Elle est aussi la plus coûteuse à opérer : Kafka/Strimzi, Temporal, leurs stockages, deux observabilités et un pont idempotent Kafka→Temporal. Elle devient rationnelle seulement lorsque **Kafka est déjà une contrainte externe** ou lorsqu'un benchmark élimine NATS/RabbitMQ. Avant cela, B donne la même séparation conceptuelle avec une meilleure DevX locale et opérationnelle.

## Décision proposée et preuve à obtenir

1. Prototyper **A — Temporal-first** avec un connecteur texte, un plugin distant, une correction urgente et un backfill d'une fenêtre temporelle.
2. Garder une interface interne `EventPublisher`/`EventSubscription` sans exposer Temporal dans le Plugin API ; son implémentation initiale peut être l'outbox/catalogue.
3. Ajouter NATS seulement lorsque l'on implémente un véritable abonnement durable de plugin. Ne pas l'ajouter « pour plus tard ».
4. Benchmarker avant gel d'architecture avec au moins : débit nominal/4×, 3 classes de priorité, plugin lent, panne de worker, génération N→N+1 en drain, backfill concurrent et références de médias réalistes.

Les critères de réussite ne doivent pas seulement mesurer le débit : p95 `received→searchable`, retard du temps réel pendant un backfill, temps de récupération, absence de doublons visibles, durée du drain et nombre de commandes nécessaires pour diagnostiquer/rejouer un échec.
