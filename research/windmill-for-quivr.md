# Windmill comme runtime d'ingestion et de plugins pour Quivr

_Recherche effectuée le 3 septembre 2026. Sources primaires uniquement : documentation officielle, dépôt et licences du projet._

## Verdict

**Windmill est techniquement séduisant mais éliminé comme brique obligatoire de Quivr.** Son serveur et son frontend sont sous AGPLv3, tandis que certaines fonctions Enterprise sont propriétaires. La contrainte retenue pour Quivr exclut tout copyleft et toute licence source-available : seules les licences permissives comme MIT, Apache-2.0, BSD ou PostgreSQL sont admises pour le chemin obligatoire.

La situation est même plus restrictive que le simple mot « AGPL » ne le laisse entendre :

- le `LICENSE` du dépôt précise que `backend/` et `frontend/` sont AGPLv3, sauf fragments Enterprise sous licence propriétaire ;
- les clients Python, Deno, Go et PowerShell ainsi que les spécifications OpenAPI/OpenFlow sont Apache-2.0 ;
- l'image **Community Edition** distribuée contient aussi du code propriétaire non public et son droit d'usage interdit notamment de la modifier, de la wrapper, de la revendre ou de la servir comme service sans accord explicite.

Sources : [licence racine du dépôt](https://github.com/windmill-labs/windmill/blob/main/LICENSE), [licence AGPL du backend](https://github.com/windmill-labs/windmill/blob/main/backend/LICENSE), [licence Apache-2.0 du client Python](https://github.com/windmill-labs/windmill/blob/main/python-client/LICENSE).

Windmill ne peut donc être ni le runtime embarqué de la distribution OSS Quivr, ni une dépendance nécessaire pour atteindre les performances visées. Il pourrait seulement rester :

1. une intégration optionnelle opérée séparément par un utilisateur qui en accepte la licence ;
2. une source d'inspiration pour la DevX ;
3. une source de standards réutilisables sous Apache-2.0, notamment OpenFlow et les API clientes.

## Ce que Windmill propose techniquement

### Architecture et file PostgreSQL

Windmill concentre son état dans PostgreSQL : définition des scripts et flows, ressources, schedules, historique d'exécution et file de jobs. Les serveurs HTTP et workers sont stateless ; les workers tirent les jobs de la file et prennent atomiquement leur exécution. La documentation ETL indique une queue basée sur `UPDATE ... SKIP LOCKED` et annonce jusqu'à 5 000 requêtes/s sur un PostgreSQL ordinaire dans ses benchmarks. La documentation de scaling publie aussi 981 jobs/s avec 100 workers virtuels et des tâches de 100 ms, mais ce test utilisait un bulk insert spécifique, un unique hôte de 4 vCPU, 1 000 connexions PostgreSQL et le mode Dedicated Worker. Ce chiffre est donc un signal de faisabilité, pas un SLO transposable directement à Quivr.

Sources : [self-host et architecture](https://www.windmill.dev/docs/advanced/self_host), [data processing et queue PostgreSQL](https://www.windmill.dev/docs/core_concepts/data_pipelines), [benchmark de scaling](https://www.windmill.dev/docs/misc/benchmarks/competitors/results/scaling).

Cette topologie a une excellente DevX locale : Docker Compose démarre essentiellement PostgreSQL, un serveur et des workers. En production, le chart Helm permet de répliquer serveurs et workers. Elle a toutefois deux implications pour Quivr :

- tous les workers standards ouvrent une connexion directe à PostgreSQL ;
- queue, checkpoints, logs et control plane se disputent la même base.

Les « Agent Workers », qui évitent l'accès direct à la base et passent par une API HTTP/JWT, sont une fonction Cloud/Enterprise. C'est précisément la forme la plus adaptée à des plugins clients externes ou peu fiables, mais elle n'est pas disponible dans la brique permissive requise par Quivr. [Documentation Agent Workers](https://www.windmill.dev/docs/core_concepts/agent_workers).

### Flows, workflows as code et durabilité

Windmill offre deux modèles :

- **Flows OpenFlow**, DAG JSON visuels avec séquences, branches parallèles, boucles, sous-flows, sleeps, suspensions et approbations ;
- **Workflows as Code v2**, en TypeScript ou Python, avec `workflow()`, `task()`, `step()`, `sleep()` et checkpoint/replay.

Chaque étape d'un flow devient un job indépendant. Les workflows as code libèrent le worker entre deux checkpoints, stockent les résultats dans PostgreSQL et rejouent l'orchestrateur jusqu'au premier appel non résolu, un modèle proche de Temporal. Les retries constants ou exponentiels, timeouts et handlers d'erreur sont configurables par étape.

Sources : [architecture des flows](https://www.windmill.dev/docs/flows/architecture), [workflows as code](https://www.windmill.dev/docs/core_concepts/workflows_as_code), [retries](https://www.windmill.dev/docs/flows/retries), [gestion des erreurs](https://www.windmill.dev/docs/flows/error_handling), [spécification OpenFlow](https://www.windmill.dev/docs/openflow).

Deux limites sont importantes pour la politique Quivr « une génération en vol termine sur son ancienne version » :

1. si le code d'un workflow as code change entre deux replays, Windmill détecte le changement de hash mais **redémarre l'exécution depuis le début** ; ce n'est pas le pinning et le drain progressif offert par le Worker Versioning de Temporal ;
2. les scripts ont des hashes immuables, mais les flows ne gardent qu'une version active par chemin. La page des runs indique que les scripts peuvent être relancés sur leur version originale ou la dernière, tandis que les flows ne peuvent être relancés que sur leur dernière version. La fonction « restart deployed flows from any node and version » est classée Enterprise.

Sources : [déterminisme et changement de code](https://www.windmill.dev/docs/core_concepts/workflows_as_code#determinism-requirement), [versioning](https://www.windmill.dev/docs/core_concepts/versioning), [draft et déploiement](https://www.windmill.dev/docs/core_concepts/draft_and_deploy), [runs et réexécution](https://www.windmill.dev/docs/core_concepts/monitor_past_and_future_runs), [comparatif de plans](https://www.windmill.dev/pricing).

Le replay Windmill est par conséquent surtout un **replay d'exécution/checkpoint**. Il ne remplace pas le replay d'un journal d'événements comme NATS JetStream et ne remplace pas le backfill Quivr depuis le catalogue/S3.

### Event triggers et extensibilité

Un script ou flow peut être déclenché par HTTP, schedule, email, WebSocket, PostgreSQL, Kafka, NATS, SQS, MQTT ou GCP Pub/Sub. C'est une DevX attractive pour construire une plateforme d'automatisation. Mais pour Quivr, les triggers les plus intéressants pour un fabric de plugins — **Kafka, NATS et SQS** — sont réservés au self-hosted Enterprise. Le trigger Kafka sait gérer offsets initiaux, auto-commit après mise en file, commit manuel et reset vers l'offset le plus ancien ; cette sophistication n'est donc pas utilisable dans la distribution OSS permissive envisagée.

Sources : [catalogue des triggers](https://www.windmill.dev/platform/triggers), [trigger Kafka](https://www.windmill.dev/docs/triggers/kafka_triggers), [tarification et matrice Community/Enterprise](https://www.windmill.dev/pricing).

Windmill ne fournit pas non plus directement le modèle de plugin Quivr validé : package versionné, plusieurs contributions typées, contrats de capacités publics, activation `from-now`, génération ancienne drainée, nouveau plugin backfillé sur une fenêtre choisie. On pourrait projeter ce modèle vers des scripts, tags et triggers Windmill, mais le registry, les manifestes, les permissions de données, la génération et le backfill resteraient à construire côté Quivr.

### Worker groups, priorités et autoscaling

La Community Edition permet de router scripts et flows par **tags**, définis dans `WORKER_TAGS`. Cela suffit à séparer des pools CPU, GPU ou réseau via la configuration du déploiement. En revanche, sont Enterprise :

- gestion des worker groups depuis l'UI et init scripts ;
- autoscaling natif sur backlog/occupation ;
- workers dédiés à un script pour réduire les cold starts ;
- Agent Workers distants sans accès direct à PostgreSQL ;
- priorités de steps et concurrency limits ;
- alertes critiques et métriques de queue.

Sources : [workers et groupes](https://www.windmill.dev/docs/core_concepts/worker_groups), [autoscaling](https://www.windmill.dev/docs/core_concepts/autoscaling), [dedicated workers](https://www.windmill.dev/docs/core_concepts/dedicated_workers), [matrice officielle des plans](https://www.windmill.dev/pricing).

Il serait possible de recréer un autoscaling Community avec Kubernetes HPA/KEDA et une métrique construite à partir de PostgreSQL, et de gérer les tags par Helm. Mais Quivr devrait alors maintenir exactement les fonctions que le choix d'un runtime devait lui éviter. Surtout, dépendre de ces contournements ne change pas la licence AGPL du moteur.

### Haute disponibilité et comportement en panne

Windmill permet plusieurs serveurs derrière un load balancer et plusieurs workers sur la même base. La HA revient principalement à opérer PostgreSQL en haute disponibilité. Après failover, les jobs encore en queue peuvent reprendre. Les jobs qui étaient réellement en cours ne continuent pas : ils apparaissent échoués ou expirés et doivent être relancés. Le failover multi-datacenter documenté consiste à promouvoir le replica PostgreSQL puis rediriger/redémarrer les composants vers la nouvelle primaire ; il ne s'agit pas d'un active-active multi-région autonome.

Source : [High availability and failover](https://www.windmill.dev/docs/advanced/high_availability).

Cette sémantique est acceptable pour des handlers Quivr idempotents, mais moins forte et moins automatique que la reprise d'un Workflow Temporal après perte d'un worker. Elle oblige à tester précisément : lease de job, détection des zombies, effets de bord déjà commis et réexécution après panne.

### Observabilité

La UI affiche les runs, graphes de flows, inputs, résultats et logs live. C'est l'un des meilleurs aspects de la DevX Windmill. Toutefois, le plan self-hosted gratuit limite la rétention des détails de runs à 30 jours et place dans Enterprise :

- audit logs ;
- queue metrics et Prometheus ;
- tracing et logs OpenTelemetry ;
- stockage S3 des service logs ;
- critical alerts ;
- rétention illimitée des runs.

L'export OTLP inclut des compteurs de push/pull de queue, backlog par tag, occupation et durée des workers, pools DB et santé, mais la documentation le marque explicitement Enterprise. Sources : [matrice de prix](https://www.windmill.dev/pricing), [OpenTelemetry](https://www.windmill.dev/docs/misc/guides/otel), [audit logs](https://www.windmill.dev/docs/core_concepts/audit_logs).

Pour une plateforme devant démontrer ses SLO, l'observabilité n'est pas cosmétique. Une solution où métriques de queue, audit long terme et traces sont payantes ne peut pas constituer le chemin OSS complet attendu, même si l'API et l'UI de base restent utilisables.

### Langages et isolation du code

Windmill a une excellente couverture : TypeScript/Bun et Deno, Python, Go, Bash, PowerShell, SQL, PHP, Rust, C#, Java, Ruby, Ansible, R, dbt, REST/GraphQL ; les autres langages peuvent passer par une image OCI. Les dépendances de scripts disposent de lockfiles générés. [Script editor et langages](https://www.windmill.dev/docs/script_editor).

Mais le modèle de sécurité doit être lu attentivement :

- l'isolation est **désactivée par défaut** ;
- l'isolation PID Linux demande une configuration manuelle et peut nécessiter `privileged: true` avec les flags proposés ;
- NSJAIL fournit une isolation plus complète de filesystem/processus/ressources et éventuellement du réseau, mais doit être explicitement activé et présent sur les workers ;
- les workers standards ont accès direct à la base Windmill ;
- les Agent Workers limités à HTTP/JWT seraient mieux adaptés aux plugins distants, mais sont Enterprise ;
- l'exécution d'une image OCI via `# sandbox <image>` est run-to-completion : pas de daemon, healthcheck, `exec`, service gRPC permanent ou gestion native du lifecycle d'un conteneur plugin.

Sources : [sécurité et isolation](https://www.windmill.dev/docs/advanced/security_isolation), [images OCI dans NSJAIL](https://www.windmill.dev/docs/advanced/docker), [Agent Workers](https://www.windmill.dev/docs/core_concepts/agent_workers).

Pour des plugins communautaires potentiellement non fiables, Windmill ne retire donc pas le besoin de Kubernetes, NetworkPolicy et éventuellement gVisor/Kata. Son runner NSJAIL est intéressant pour des transformations courtes, mais il ne constitue pas à lui seul la frontière de sécurité et de ressources retenue pour Quivr.

## DevX locale et maintenance

Windmill est remarquable sur la boucle développeur : Docker Compose, interface visuelle, éditeur, schémas d'entrée inférés, lockfiles, runs inspectables, CLI `wmill sync`, API, VS Code et flows stockés dans un format JSON portable. La documentation propose aussi `wmill init` pour produire des instructions destinées aux agents de code. Sources : [self-host](https://www.windmill.dev/docs/advanced/self_host), [développement local et Git sync](https://www.windmill.dev/docs/advanced/git_sync), [flows OpenFlow](https://www.windmill.dev/docs/openflow).

Ce bénéfice vient toutefois avec un produit complet — frontend, workspaces, utilisateurs, RBAC, app builder, secrets, resources, triggers, éditeur — dont Quivr n'a pas besoin dans son backend. L'intégrer comme sous-système ferait coexister deux control planes, deux modèles d'autorisations et deux APIs publiques. La simplicité perçue pour l'auteur d'un flow pourrait devenir de la complexité pour l'équipe qui maintient Quivr.

La bonne leçon n'est donc pas « adopter Windmill », mais reprendre ses qualités dans le SDK Quivr :

- exécution locale en une commande ;
- déclaration d'inputs typés et UI/CLI générables ;
- timeline par record et par contribution ;
- retries et timeouts visibles sans connaître l'infrastructure ;
- définition sérialisable des pipelines ;
- images OCI optionnelles et dépendances verrouillées.

## Comparaison ciblée

| Critère Quivr | Windmill | Temporal | NATS JetStream | Hatchet |
|---|---|---|---|---|
| Licence du serveur obligatoire | **AGPLv3 + fragments propriétaires dans l'image CE** | MIT | Apache-2.0 | MIT |
| Rôle naturel | Plateforme scripts/flows complète | Exécution durable mature | Messaging/pub-sub durable | Tasks, événements et workflows durables |
| Dev local | Excellent, Compose + UI | Très bon, serveur dev + UI | Excellent, binaire unique | Excellent, PostgreSQL/embedded + UI |
| Workflow durable | Oui, flows + WAC v2 | **Excellent**, cœur du produit | Non, à construire | Oui |
| Pub/sub indépendant | Via triggers externes ; NATS/Kafka Enterprise | Non | **Excellent** | Événements avec fan-out |
| Ancienne génération drainée | Insuffisant nativement ; changement WAC peut repartir de zéro | **Worker Versioning natif** | Le consumer/versioning est à modéliser | Nouveau nom + drain à modéliser |
| Workers plugins distants | Agent Workers Enterprise ; sinon DB directe ou appels HTTP depuis scripts | SDK workers distants | Clients multi-langages | SDK workers distants gRPC |
| Observabilité OSS requise | Plusieurs fonctions essentielles Enterprise | UI et métriques open source | Métriques serveur open source, UI à choisir | Dashboard/observabilité annoncés MIT |
| Autoscaling OSS natif | Non, Enterprise | À faire avec K8s/outils externes | À faire avec K8s/outils externes | À valider dans le bake-off |

Licences : [Windmill](https://github.com/windmill-labs/windmill/blob/main/LICENSE), [Temporal](https://github.com/temporalio/temporal/blob/main/LICENSE), [NATS Server](https://github.com/nats-io/nats-server/blob/main/LICENSE), [Hatchet](https://github.com/hatchet-dev/hatchet/blob/main/LICENSE).

### Lecture de la comparaison

- **Temporal** reste le meilleur candidat si Quivr privilégie la maturité, la reprise durable et les déploiements versionnés. Il ne fournit pas de pub/sub ; NATS pourra être ajouté seulement quand ce besoin est prouvé.
- **NATS JetStream** reste le meilleur fabric événementiel permissif et léger. Il ne doit pas être transformé en moteur de workflow artisanal.
- **Hatchet** reste le duel le plus intéressant face à Temporal pour maximiser la DevX avec un seul control plane PostgreSQL. Sa maturité et son comportement sous charge/panne doivent être mesurés.
- **Windmill** aurait été un candidat convaincant si son moteur complet et ses fonctions de production nécessaires avaient été sous Apache-2.0/MIT. Avec la contrainte actuelle, il est hors compétition avant même le bake-off.

## Recommandation pour Quivr

1. **Ne pas inclure Windmill dans le bake-off du runtime obligatoire.** Sa licence suffit à l'écarter.
2. Maintenir le bake-off **Temporal contre Hatchet**, avec NATS JetStream comme extension événementielle future plutôt que comme orchestrateur.
3. Ajouter aux critères du prototype les qualités Windmill suivantes : timeline exploitable, inputs typés, local setup en une commande, définition de pipeline lisible, gestion visible des retries et lockfiles reproductibles.
4. Étudier **OpenFlow** uniquement comme source d'idées ou éventuel format d'import/export, car la spécification est Apache-2.0 ; ne pas adopter implicitement les semantics Windmill comme Plugin API Quivr.
5. Autoriser ultérieurement un adaptateur optionnel `quivr-windmill` : il pourrait publier des événements Quivr vers une instance Windmill opérée et licenciée séparément par l'utilisateur. L'intégration ne doit apporter aucune garantie obligatoire ni modifier le cœur permissif de Quivr.

En synthèse : **Windmill a peut-être la meilleure démonstration de la DevX désirée, mais pas la licence ni la séparation de responsabilités désirées.** Il faut copier le niveau d'expérience développeur, pas introduire le serveur Windmill dans l'architecture de référence.
