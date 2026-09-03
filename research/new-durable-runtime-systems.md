# Runtimes durables récents pour Quivr : simplicité, plugins et passage à l'échelle

_Recherche effectuée le 3 septembre 2026. Sources primaires uniquement : documentations officielles, dépôts et licences des projets._

## Conclusion

La conclusion la plus utile est de **ne pas valider immédiatement `Temporal + Kafka`**. Cette combinaison est robuste, mais elle paie dès le premier jour le coût de deux plateformes distribuées et de deux modèles mentaux. La customisation par plugins ne suffit pas, à elle seule, à justifier Kafka.

Le candidat récent le plus cohérent avec la priorité « DevX et maintenance » est **Hatchet v1 en mode PostgreSQL-only**. Il réunit dans un même moteur réellement open source : tâches, files, workflows durables, déclenchement par événements, fan-out, retries, replay opérationnel, priorités, fairness, limites de concurrence, workers distants multi-langages et interface d'observation. Son mode embarqué ne demande ni compte ni Docker et son mode production peut commencer avec Hatchet et PostgreSQL uniquement. Hatchet est sous licence MIT et annonce des SDK Python, TypeScript, Go et Ruby ([présentation officielle](https://docs.hatchet.run/v1), [dépôt officiel](https://github.com/hatchet-dev/hatchet)).

La recommandation n'est cependant **pas encore de graver Hatchet dans l'architecture**. Il faut faire un bake-off court contre **Temporal + NATS JetStream**, puis retenir Hatchet si son chemin PostgreSQL-only passe les tests de charge, de panne et de croissance de l'historique. Hatchet annonce un test jusqu'à 10 000 tâches/s, mais rappelle lui-même que sa durabilité ajoute un coût ; ce chiffre doit être reproduit avec le fan-out, les payloads et la topologie Quivr ([README Hatchet](https://github.com/hatchet-dev/hatchet/blob/main/README.md)).

Le classement proposé est donc :

1. **Hatchet seul pour le runtime durable et l'eventing opérationnel** — meilleur pari simplicité/DevX ;
2. **Temporal + NATS JetStream** — séparation plus conservatrice, plus de maturité côté workflows et meilleur event fabric, mais deux systèmes ;
3. **Temporal + Kafka** — option de repli si un benchmark ou un besoin de streaming/retention établit que NATS ou Hatchet ne suffisent pas.

Le catalogue PostgreSQL de Quivr et le stockage objet restent dans les trois cas la source de vérité permettant un backfill sur deux ans. Aucun broker ni historique de workflows ne doit devenir l'archive métier.

## Filtre éliminatoire : licence réellement open source

Une brique obligatoire doit être disponible aujourd'hui sous une licence OSI-compatible. Une conversion future vers Apache-2.0 ne suffit pas.

| Système | Licence actuelle de la brique obligatoire | Verdict |
|---|---|---|
| Hatchet | MIT, moteur et version self-hosted annoncés comme complets | **Éligible** |
| Temporal | MIT | **Éligible** |
| Apache Kafka | Apache-2.0 | **Éligible** |
| NATS Server / JetStream | Apache-2.0 | **Éligible** |
| DBOS SDK/runtime | MIT | Éligible seul, mais son control plane Conductor self-hosted est propriétaire |
| Trigger.dev | Apache-2.0 | Éligible juridiquement, écarté pour complexité opérationnelle et orientation TypeScript |
| River Core | MPL-2.0 | Éligible seul, mais plusieurs fonctions requises sont dans River Pro |
| LittleHorse Server | AGPL-3.0 | OSI-compatible, mais impose Kafka et ne simplifie pas la plateforme |
| Restate | BSL 1.1 avec conversion Apache-2.0 après quatre ans | **Éliminé** |
| Inngest Server / CLI | SSPL avec delayed open-source publication | **Éliminé** |

Sources de licence : [Hatchet](https://docs.hatchet.run/v1), [Temporal](https://github.com/temporalio/temporal/blob/main/LICENSE), [Kafka](https://github.com/apache/kafka/blob/trunk/LICENSE), [NATS](https://github.com/nats-io/nats-server), [DBOS Go](https://github.com/dbos-inc/dbos-transact-golang/blob/main/LICENSE), [Trigger.dev](https://github.com/triggerdotdev/trigger.dev/blob/main/LICENSE), [River](https://github.com/riverqueue/river/blob/master/LICENSE), [LittleHorse](https://github.com/littlehorse-enterprises/littlehorse), [Restate](https://github.com/restatedev/restate/blob/main/LICENSE), [Inngest](https://github.com/inngest/inngest).

## Quatre rôles à ne pas confondre

| Rôle | Promesse | Exemples | Ce qu'il ne fait pas seul |
|---|---|---|---|
| **Orchestration / durable execution** | Mémorise où en est un processus, rejoue le code de manière sûre, coordonne timers, appels et compensations. | Temporal, Hatchet durable tasks, DBOS, Restate | Ne fournit pas nécessairement un log diffusable à des consommateurs ajoutés plus tard. |
| **Task queue** | Livre un travail à un worker disponible ; plusieurs workers se partagent la charge. | Hatchet tasks, Temporal Task Queues, River, DBOS Queues | Ne copie pas automatiquement le même fait à chaque plugin. |
| **Pub/sub** | Chaque abonnement logique reçoit une copie du fait. | Kafka consumer groups, NATS durable consumers, événements Hatchet/Inngest | Ne décrit pas à lui seul un processus multi-étapes ni ses compensations. |
| **Event log / stream** | Conserve une suite de faits indépendamment des consommateurs, avec position/replay. | Kafka, NATS JetStream | Ne gère pas naturellement une saga, un timer métier ou la version du code exécutant. |

Temporal documente ses Task Queues comme des files dynamiques pollées par les workers avec de la capacité, persistantes pour les tâches de Workflow et d'Activity ([Task Queues](https://docs.temporal.io/task-queue)). Ce ne sont pas des topics multi-abonnés. Kafka se définit à l'inverse comme une plateforme de streaming avec topics durables multi-producteurs/multi-abonnés et traitement rétrospectif ([introduction Kafka](https://kafka.apache.org/documentation/)).

JetStream ajoute au broker NATS un stockage de messages relisible, des consommateurs durables et plusieurs politiques de rétention. Un serveur local démarre avec `nats-server -js`; en production, un stream R3 et son consumer répliqué survivent à la perte d'un nœud dans un cluster de trois serveurs ([premier stream](https://docs.nats.io/learn/jetstream/your-first-stream), [JetStream en cluster](https://docs.nats.io/learn/topologies/jetstream-in-a-cluster)). JetStream n'est toutefois pas un moteur de workflow.

## Candidat principal : Hatchet

### Pourquoi il correspond particulièrement bien à Quivr

Hatchet v1 combine :

- tâches ordinaires avec retries et timeouts ;
- DAGs pour les pipelines statiques ;
- durable tasks checkpointées pour les pipelines dynamiques ;
- événements pouvant déclencher plusieurs tâches/workflows indépendants ;
- replay des événements par identifiants, clés ou fenêtre temporelle ;
- priorité par run ou événement ;
- limites de concurrence, fairness par clé, rate limits et capacité déclarée en slots par worker ;
- routage vers des workers via labels/affinity ;
- workers Python, TypeScript, Go ou Ruby connectés au moteur par gRPC ;
- UI self-hosted, logs, OpenTelemetry et métriques Prometheus ;
- exécutions et événements durables conservés dans PostgreSQL.

Les sources correspondantes sont [l'introduction v1](https://docs.hatchet.run/v1), [les événements et leur replay](https://docs.hatchet.run/reference/ruby/feature-clients/events), [le contrôle de concurrence](https://github.com/hatchet-dev/hatchet/blob/main/frontend/docs/pages/v1/concurrency.mdx), [les durable tasks](https://github.com/hatchet-dev/hatchet/blob/main/frontend/docs/pages/v1/durable-tasks.mdx) et [la comparaison officielle avec Temporal](https://docs.hatchet.run/v1/from-temporal-to-hatchet).

La DevX locale est aujourd'hui son meilleur différenciateur :

- `Hatchet.from_embedded()` lance le moteur et un PostgreSQL embarqué sans compte, token ni Docker ;
- `hatchet server start --disable-auth` démarre une stack locale avec dashboard ;
- Hatchet Lite offre un seul conteneur ;
- Docker Compose et Kubernetes restent disponibles pour une topologie proche de la production.

Voir [Running Hatchet Locally](https://docs.hatchet.run/v1/running-locally) et [Embedded Hatchet](https://docs.hatchet.run/v1/embedded).

### Projection du modèle Quivr sur Hatchet

```text
Quivr PostgreSQL transaction
  ├─ RecordVersion / lineage / retention / ACL
  └─ outbox: record.accepted.v1
             │
             ▼
       Hatchet event
        ├─ core materialize workflow       priorité temps réel
        ├─ plugin Agency-classifier@2 task    abonnement from-now
        ├─ plugin generic-alert@4 task     abonnement from-now
        └─ search projection task

S3-compatible storage   = blobs et dérivés lourds
Quivr catalogue + S3    = source du backfill historique
Hatchet                  = exécution et replay opérationnels
```

L'outbox Quivr reste nécessaire : créer un record dans le catalogue puis appeler Hatchet sont deux écritures distinctes. L'admission n'est acquittée que lorsque le record, la référence du blob et l'outbox sont durables. Un dispatcher idempotent publie ensuite dans Hatchet.

Un plugin peut fournir plusieurs tâches et workflows. Quivr génère des noms stables mais versionnés, par exemple `plugin.Agency-classifier.v2.classify`. La génération courante reçoit les nouveaux événements ; la précédente reste déployée jusqu'au drain. Hatchet indique explicitement qu'il ne possède pas l'équivalent du patching Temporal pour une rupture dans une durable task : il recommande une nouvelle définition et le drain de l'ancienne. Cela correspond à la décision Quivr « le travail en vol finit sur l'ancienne génération » ([versioning Hatchet](https://docs.hatchet.run/v1/from-temporal-to-hatchet#step-11-versioning-and-determinism)).

### Ce que Hatchet ne remplace pas

Hatchet ne remplace pas :

- le catalogue transactionnel et la provenance Quivr ;
- S3 et la politique hot/warm/cold ;
- le moteur lexical/vectoriel ;
- Kubernetes, gVisor/Kata et les politiques réseau pour isoler le code tiers ;
- un event lake conservé plusieurs années ;
- Kafka Streams/Flink et l'écosystème Kafka Connect si ces besoins apparaissent.

Son event log est un historique opérationnel de tâches et d'événements, pas l'archive Agency. Un plugin installé avec un replay de deux ans doit provoquer un backfill paginé depuis le catalogue/S3 ; il ne doit pas supposer que PostgreSQL Hatchet retient deux ans d'événements.

Hatchet n'a pas non plus de DLQ explicite. Les runs ayant épuisé leurs retries restent persistés et rejouables dans le dashboard/API, ce que la documentation présente comme un équivalent opérationnel, mais pas comme une file DLQ ([bulk retries et note DLQ](https://docs.hatchet.run/v1/error-handling/bulk-retries-and-cancellations)). Le SDK Quivr doit donc définir un état terminal, une vue d'incident et éventuellement une tâche de remédiation, sans prétendre qu'une DLQ native existe.

### Risques à mesurer

1. **Pression PostgreSQL.** Workflow state, task queue, event history et observabilité écrivent dans le même système. La simplicité est excellente, mais le write amplification réel dépend du nombre de contributions par record.
2. **Maturité plus faible que Temporal/Kafka.** Hatchet v1 est récent ; l'équipe doit tester upgrades, sauvegardes, restauration et migration de schéma, pas seulement le happy path.
3. **Versioning moins riche que Temporal.** Pas de `GetVersion`/patching équivalent dans une durable task ; les ruptures passent par nouveau nom + drain.
4. **Permissions plugin.** Les tokens workers donnent accès aux payloads nécessaires aux tâches. Quivr doit envoyer des références minimales et faire respecter les permissions de blob par son propre gateway ; Hatchet n'est pas le capability sandbox.
5. **HA.** Le mode PostgreSQL-only réduit le nombre de briques, mais déplace l'exigence vers un PostgreSQL HA correctement opéré. Plusieurs moteurs peuvent partager une base et continuer le travail lorsqu'un moteur s'arrête, mais ce comportement doit être validé avec la topologie Kubernetes et le chart retenus ([fleet Hatchet embarquée](https://docs.hatchet.run/v1/embedded#run-a-fleet-with-a-shared-database), [charts officiels](https://github.com/hatchet-dev/hatchet-charts)).
6. **Plafond de débit.** L'annonce officielle de 10k tâches/s n'est pas un SLO contractuel. Dix contributions sur une dépêche font dix tâches, pas une.

RabbitMQ est optionnel ; le chart officiel sait utiliser la message queue PostgreSQL. Il faut commencer PostgreSQL-only, puis ajouter RabbitMQ uniquement si le benchmark montre que le dispatcher est le plafond et que l'équipe accepte une brique supplémentaire ([chart Hatchet](https://github.com/hatchet-dev/hatchet-charts)).

## Alternative robuste : Temporal + NATS JetStream

```text
PostgreSQL Quivr + outbox
           │
           ├── Temporal workflow ── mandatory core activities
           │
           └── NATS stream ──────── plugin durable consumers
                                      ├─ generation v1 draining
                                      └─ generation v2 current
```

Cette option sépare proprement les responsabilités :

- **Temporal** orchestre le chemin obligatoire : matérialisation, indexation minimale, opérations longues, compensation et backfills ;
- **NATS JetStream** diffuse les faits engagés à des abonnements de plugins indépendants ;
- **PostgreSQL/S3 Quivr** restent la vérité métier et la profondeur historique.

Temporal est MIT, mature, possède des SDK officiels dans de nombreux langages, démarre localement en une commande avec UI (`temporal server start-dev`) et apporte un versioning de workers nettement plus complet : ramp-up, rollback, workflows pinned, détection du drain et contrôleur Kubernetes ([dépôt Temporal](https://github.com/temporalio/temporal), [Worker Versioning](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning), [déploiement local](https://docs.temporal.io/production-deployment)).

NATS est Apache-2.0, fonctionne comme un seul binaire local et possède des clients dans plus de 40 langages ([dépôt NATS](https://github.com/nats-io/nats-server)). Les subjects hiérarchiques et wildcard sont particulièrement agréables pour un SDK de plugins. Chaque contribution reçoit son durable consumer, peut reprendre son curseur et peut démarrer à une séquence ou une date tant que le fait est encore retenu.

Cette architecture est préférable à `Temporal + Kafka` lorsque :

- les événements sont surtout un fabric opérationnel, pas un data lake ;
- les backfills profonds viennent du catalogue/S3 ;
- l'équipe valorise un binaire et un protocole simples ;
- Kafka Connect, Kafka Streams et une conservation massive par offsets ne sont pas requis.

### Coûts et limites

- Deux control planes, deux historiques et deux procédures de backup/upgrade.
- Temporal ne fournit toujours pas le pub/sub ; NATS ne fournit toujours pas l'orchestration.
- Il faut relier la transaction catalogue à NATS avec un outbox.
- JetStream redélivre les messages non acquittés, mais la politique de retries applicatifs, l'idempotence et la DLQ Quivr restent à construire dans le SDK.
- La production exige au minimum un cluster NATS de trois nœuds avec streams et consumers R3 ; « NATS est un seul binaire » ne signifie pas « un seul pod en production ».
- Les priorités métier sont moins directement unifiées que dans Hatchet : Temporal utilise typiquement plusieurs Task Queues/pools et NATS ses propres consumers.

## Option maximale : Temporal + Kafka

Cette architecture reste la plus conservatrice lorsqu'il faut simultanément un orchestrateur durable très mature et un véritable event log de longue rétention, à débit élevé, avec écosystème de connecteurs et stream processing.

Elle ne doit être choisie que si au moins un besoin mesuré le justifie :

- débit soutenu ou fan-out dépassant les résultats du bake-off Hatchet/NATS ;
- rétention/relecture d'un grand volume d'événements indépendante du catalogue ;
- Kafka Connect ou CDC déjà central dans l'environnement Agency ;
- traitement streaming stateful par Kafka Streams/Flink ;
- équipe opérant déjà Kafka avec une compétence et une plateforme établies.

Kafka facilite bien la customisation **au niveau transport** : ajouter un consumer group n'oblige pas à modifier le producteur. Mais cette liberté déplace du travail vers Quivr : conventions de topics, ACL, partition keys, schémas, retries, DLQ, idempotence, lag, rebalances, versionnement et outils locaux. Elle ne constitue donc pas automatiquement une meilleure DevX plugin.

Si Kafka est retenu, son existence doit rester invisible derrière `quivr.subscribe(...)`. Les auteurs de plugins ne devraient manipuler directement partitions, offsets et groupes qu'en mode avancé.

## Candidats étudiés et écartés

### Restate — meilleur fit technique, licence éliminatoire

Restate aurait été un excellent candidat : binaire unique, exécution durable, services distants HTTP, workflows/virtual objects, retries, état, UI, cluster, Operator Kubernetes et versioning immuable des déploiements avec drain automatique ([services](https://docs.restate.dev/foundations/services), [versioning](https://docs.restate.dev/services/versioning), [Kubernetes](https://docs.restate.dev/server/deploy/kubernetes)). Il sait également consommer Kafka en gérant les consumers et en invoquant durablement les handlers ([intégration Kafka](https://docs.restate.dev/services/invocation/kafka)).

Mais le serveur Restate actuel est sous **BSL 1.1** ; la licence elle-même précise qu'elle n'est pas open source et prévoit seulement une conversion Apache-2.0 quatre ans après la release ([licence Restate](https://github.com/restatedev/restate/blob/main/LICENSE)). Il ne peut donc pas être une dépendance obligatoire du projet OSS Quivr.

### Inngest — excellente DevX événementielle, licence éliminatoire

Inngest offre précisément l'expérience recherchée : fonctions événementielles, fan-out indépendant, retries, flow control multi-tenant, priorités, UI et serveur local monobinaire ([documentation](https://www.inngest.com/docs), [fan-out](https://www.inngest.com/docs/patterns/events/running-functions-in-parallel), [self-hosting](https://www.inngest.com/docs/self-hosting)).

Son serveur et sa CLI sont toutefois sous **SSPL avec publication Apache-2.0 différée** ; seuls les SDK sont Apache-2.0 aujourd'hui ([dépôt et licence Inngest](https://github.com/inngest/inngest)). Il est éliminé par le même filtre que Restate.

### DBOS — runtime minimal remarquable, control plane propriétaire

DBOS est une bibliothèque MIT intégrée à l'application. Elle utilise PostgreSQL comme system database, propose workflows, steps, queues distribuées, priorités, partitionnement, scheduling et versionnement blue/green, et permet à des applications de langages différents de partager une base et de s'appeler ([présentation](https://docs.dbos.dev/why-dbos), [partage cross-language](https://docs.dbos.dev/explanations/sharing-a-system-database), [versioning](https://docs.dbos.dev/golang/tutorials/workflow-tutorial), [Kubernetes](https://docs.dbos.dev/production/hosting-with-kubernetes)). Sa DX et son absence de service dédié sont excellentes.

Le problème pour Quivr est opérationnel : sans Conductor, chaque instance récupère ses propres workflows ; dans un déploiement distribué, la documentation demande de gérer soi-même la détection d'exécuteur mort et la réaffectation. Le composant qui automatise recovery distribué, observation et administration — DBOS Conductor — est propriétaire et demande une licence en self-hosted production ([workflow recovery](https://docs.dbos.dev/production/workflow-recovery), [licence Conductor](https://docs.dbos.dev/production/hosting-conductor)). Le choisir imposerait donc de reconstruire une partie du control plane que Quivr cherche à éviter. DBOS ne fournit pas non plus un pub/sub/event log générique.

### Trigger.dev — OSS, mais trop de plateforme à opérer

Trigger.dev est Apache-2.0 et possède une bonne expérience TypeScript, retries, queues, versioning, priorités, UI et runners longs. Mais son installation Kubernetes de production demande des services externes PostgreSQL, Redis, ClickHouse et object storage, plus registry/supervisor ; le minimum annoncé est 6 vCPU et 12 Go de RAM. La version self-hosted ne possède ni checkpoints ni autoscaling disponibles dans le cloud ([guide Kubernetes](https://trigger.dev/docs/self-hosting/kubernetes), [comparaison self-hosted](https://trigger.dev/docs/self-hosting/overview), [licence](https://github.com/triggerdotdev/trigger.dev/blob/main/LICENSE)). Le SDK principal est JavaScript/TypeScript. C'est l'inverse de la simplicité d'exploitation et du runtime multi-langage recherchés.

### River — excellente queue Go/PostgreSQL, pas la plateforme de plugins

River Core est une job queue Go/PostgreSQL MPL-2.0 avec enqueue transactionnel, priorités, retries, queues isolées et UI. Mais l'exécution reste Go ; les autres langages peuvent seulement insérer des jobs. Workflows, DLQ, resumable jobs, global concurrency et rétention par queue sont des fonctions River Pro ([dépôt River](https://github.com/riverqueue/river), [comparaison Core/Pro](https://riverqueue.com/)). Il ne répond pas au runtime distant multi-langage ni au fan-out/replay exigé.

### LittleHorse — cohérent mais ne retire pas Kafka

LittleHorse est un moteur de workflow multi-langage fondé sur Kafka, avec tâches distantes, événements corrélés, dashboard et image standalone locale. Son serveur est AGPL-3.0 et ses SDK Apache-2.0, donc il satisfait juridiquement le filtre OSI ([dépôt LittleHorse](https://github.com/littlehorse-enterprises/littlehorse)). En revanche, il ajoute son propre kernel et son DSL au-dessus de Kafka ; il ne simplifie pas davantage que Hatchet et son écosystème est nettement plus petit. Il mérite une réévaluation seulement si Kafka est déjà une contrainte ferme et si ses workflows compilés apportent une valeur démontrée.

## Bake-off recommandé avant décision

Construire le même slice vertical avec **Hatchet PostgreSQL-only** et **Temporal + NATS JetStream**. Ne pas comparer des « hello world », mais les invariants Quivr :

1. admission transactionnelle par outbox d'une `RecordVersion` ;
2. pipeline minimal `materialize → lexical projection → searchable` ;
3. fan-out vers 1, 5 puis 20 contributions de plugins ;
4. worker Python OCR lent, worker TypeScript d'alerte, endpoint/plugin indisponible ;
5. priorités `correction > realtime > alert > enrichment > backfill > GC` sous surcharge ;
6. crash du worker au milieu d'un effet externe puis redelivery idempotente ;
7. crash/restart du moteur et failover PostgreSQL ;
8. activation génération v2, nouveaux travaux sur v2, drain et retry des travaux v1 sur v1 ;
9. plugin ajouté `from-now`, puis backfill ciblé depuis le catalogue sur une fenêtre simulant deux ans ;
10. purge et rétention de l'historique d'exécution sans perdre le catalogue métier.

Mesures minimales :

- débit d'admission et de tâches à 1×, 2× et 4× le pic estimé ;
- p50/p95/p99 `accepted → searchable` ;
- write IOPS, WAL, CPU, connexions et croissance disque PostgreSQL ;
- latence du plus vieux message/run par classe de priorité ;
- durée de reprise après perte d'un worker, d'un moteur et du primaire PostgreSQL ;
- nombre d'opérations manuelles pour upgrade, backup, restore et incident ;
- lignes de code et concepts exposés à l'auteur d'un plugin.

### Gate de décision

Retenir **Hatchet seul** si :

- il tient 2× le pic réaliste avec au moins 40 % de marge DB ;
- une panne moteur n'entraîne ni perte ni duplication non idempotente ;
- les générations et priorités sont exprimables sans accès aux tables Hatchet ;
- backup/restore et upgrade sont acceptables pour l'équipe ;
- l'historique opérationnel peut être purgé sans casser les backfills Quivr.

Retenir **Temporal + NATS JetStream** si Hatchet échoue sur débit/HA/versioning, ou si l'indépendance des abonnés et le replay par curseur deviennent des exigences fortes.

Ne retenir **Temporal + Kafka** que si NATS échoue à son tour sur un besoin quantifié ou si Kafka est déjà un standard opéré chez Agency. La décision doit venir d'un seuil mesuré, pas du prestige de la stack.

## Recommandation de conception indépendante du choix

Le SDK Quivr doit masquer le runtime :

```python
plugin.subscribe(
    event="record.searchable.v1",
    handler=alert,
    delivery="durable",
    priority="realtime",
    generation="2.3.1",
)
```

Ce contrat se compile vers un event trigger Hatchet, un consumer JetStream ou un consumer Kafka. Les types publics ne doivent contenir ni `topic`, ni `partition`, ni `task_queue`, ni classe SDK d'un fournisseur.

Enfin, aucune des briques étudiées ne décide seule si une alerte, un index ou un effet externe est « exactly once ». Quivr doit conserver identifiants stables, idempotency keys, lineage et outbox ; l'exécution distribuée reste at-least-once aux frontières externes.
