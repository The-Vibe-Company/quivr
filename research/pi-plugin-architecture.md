# Pi : extensions, Chord et enseignements pour l’ingestion Agency

_Recherche effectuée le 3 septembre 2026. Sources primaires examinées au commit Pi `e44d75c20a51142abc056c243b13c1d7bb4be687` (`@earendil-works/pi-coding-agent` 0.84.4)._

Le dépôt canonique actuel est [`earendil-works/pi`](https://github.com/earendil-works/pi). L’ancienne URL `badlogic/pi-mono`, encore présente dans certains permaliens historiques et résultats de recherche, redirige vers ce dépôt.

## Conclusion courte

Pi ne confirme pas l’idée de quatre classes rigides comme `SourcePlugin`, `ProcessorPlugin`, `PolicyPlugin` et `SubscriberPlugin`. Son API stable utilise au contraire **un seul type d’extension**, une fonction TypeScript qui reçoit `ExtensionAPI`, puis enregistre librement des outils, commandes, handlers d’événements, renderers ou fournisseurs. Cette unification était intentionnelle : Pi a fusionné ses anciens « hooks » et « custom tools » afin de donner un modèle mental unique et de permettre le partage d’état dans la closure d’une extension ([documentation des extensions](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/docs/extensions.md#L154-L181), [proposition officielle de fusion](https://github.com/badlogic/pi-mono/issues/454)).

Le dépôt contient aussi une architecture plus récente et plus intéressante pour Quivr V2 : **Chord**, un runtime autonome de composition par plugins, actuellement utilisé dans le nouveau chemin client/serveur **expérimental** de Pi. Chord conserve un type unique `Facet`, mais fait émerger les rôles par les services typés que la facet fournit, consomme ou observe. Il valide le graphe complet de dépendances avant activation, démarre les fournisseurs avant les consommateurs et détruit les ressources dans l’ordre inverse ([README de Chord](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/README.md#L1-L39), [modèle et assemblage](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/PLANNING.md#L101-L162)).

La réponse directe à Q20 est donc : **non, ajouter un plugin ne devrait pas arrêter tout le système d’ingestion**. Pi stable attend que sa session locale soit au repos, puis remplace brièvement tout son runtime d’extensions. Mais Chord fait mieux pour une application serveur : il prépare une génération candidate pendant que l’ancienne sert encore, la valide, puis bascule les services. Pour Agency, une alerte branchée sur un événement durable ne nécessite même pas de couper l’ingestion : on ajoute un consommateur avec son propre curseur. Seul un changement structurel du chemin synchrone peut demander une courte mise en quiescence, limitée aux workers ou partitions concernés.

## Deux systèmes distincts dans le dépôt Pi

### 1. Le système d’extensions actuel

L’extension Pi publique est un module TypeScript dont l’export par défaut est une factory synchrone ou asynchrone :

```ts
type ExtensionFactory = (pi: ExtensionAPI) => void | Promise<void>;
```

Il n’existe pas de sous-types `ToolExtension`, `PolicyExtension` ou `SubscriberExtension`. La même factory peut :

- s’abonner aux événements de session, d’agent, de modèle ou d’outil avec `pi.on(...)` ;
- enregistrer un outil, une commande, un raccourci ou un flag ;
- intervenir avant un appel d’outil, modifier un résultat ou le contexte du modèle ;
- enregistrer un fournisseur de modèles ;
- communiquer avec d’autres extensions via `pi.events` ;
- persister des entrées dans le journal de session.

L’interface réelle et ses overloads sont visibles dans [les types `ExtensionAPI`](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/src/core/extensions/types.ts#L1241-L1505). La documentation décrit explicitement les outils, interceptions, commandes, UI, persistance et intégrations externes comme des **capacités d’une extension**, pas comme des catégories d’extensions ([capacités documentées](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/docs/extensions.md#L3-L29)).

Cette décision est transférable : il est plus souple d’avoir un manifeste `Plugin` unique et des **contributions typées** que de forcer chaque package dans une seule catégorie. Un plugin Agency peut raisonnablement fournir à la fois un parseur NewsML-G2, une métrique et une commande de diagnostic. Ce sont ses contributions — et surtout leur sémantique bloquante ou non bloquante — qui doivent être catégorisées.

### 2. Chord et le chemin expérimental de Pi

Chord définit une unité de composition minimale :

```ts
interface Plugin {
  readonly id: string;
  setup(environment: PluginEnvironment): void;
}
```

Le terme concret est `Facet`. Une feature peut livrer plusieurs facets exécutées dans des environnements différents, par exemple une facet de session et une facet de présentation/TUI. Pendant `setup`, une facet déclare synchroniquement ce qu’elle fournit, utilise ou observe ; le travail asynchrone est différé à l’activation. Le host construit ainsi le graphe réel sans demander à l’auteur de maintenir en parallèle un manifeste `requires/provides` ([vocabulaire et plugin shape](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/PLANNING.md#L39-L55), [règles de setup](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/PLANNING.md#L101-L148)).

Les concepts structurants sont :

- **service token** : identifiant stable et typé d’une capacité ;
- **singleton provider** : une implémentation, plusieurs consommateurs ;
- **keyed provider** : une collection d’instances dynamiques adressées par clé et génération ;
- **stable facade** : le consommateur conserve la même référence tandis que l’implémentation située derrière est remplacée ;
- **replicated state** : état autoritatif projeté vers des consommateurs locaux ou distants ;
- **Context** : annulation et valeurs immuables par invocation, sans imposer l’identité ou la télémétrie à la couche générique.

Ces mécanismes sont décrits dans [le README Chord](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/README.md#L16-L65). Dans Pi, ils sont aujourd’hui branchés dans `packages/coding-agent/src/experimental/` : le worker de session assemble les facets intégrées et celles des packages, puis expose des services comme `AgentController`, `Transcript`, `Models` et `SessionPlugins` ([état de l’intégration expérimentale](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/src/experimental/services/README.md#L1-L25)).

## API, hooks et ordre d’exécution

### Pi stable : hooks séquentiels dans un même processus

Pi expose un large cycle de vie : démarrage et arrêt de session, début/fin d’agent et de tour, messages, sélection de modèle, appels/résultats d’outils, entrée utilisateur, découverte de ressources, compaction et changement de session. Certains hooks sont des notifications ; d’autres peuvent modifier une valeur ou interrompre une action. Le diagramme officiel est dans [la documentation du cycle de vie](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/docs/extensions.md#L273-L349).

Les handlers d’un événement sont parcourus par ordre de chargement et attendus séquentiellement. Les transformations comme `message_end` et `tool_result` sont chaînées : le résultat d’un handler devient l’entrée du suivant. `tool_call` court-circuite au premier blocage ([runner générique](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/src/core/extensions/runner.ts#L851-L924), [interception des tools](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/src/core/extensions/runner.ts#L982-L1003)).

Le bus `pi.events` est simplement un `EventEmitter` local. Il n’a ni journal durable, ni replay, ni curseur, ni backpressure. Les erreurs de listener sont journalisées et l’émetteur n’attend pas leur achèvement ([implémentation du bus](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/src/core/event-bus.ts#L1-L32)). C’est adapté à une UI locale, mais pas au transport de faits d’ingestion critiques.

### Chord : services plutôt qu’un catalogue central de catégories

Dans Chord, une facet fait des appels comme `env.provide()`, `env.provideMany()`, `env.use()` et `env.observe()`. Le host déduit les dépendances, rejette les fournisseurs manquants ou dupliqués, les incompatibilités singleton/keyed et les cycles, puis active dans l’ordre topologique ([assemblage](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/PLANNING.md#L134-L162)).

Pour Quivr V2, cela suggère un modèle de ce type :

```text
Plugin unique
  ├─ fournit une capacité : SourceConnector, Parser, Enricher, Projection, Delivery…
  ├─ consomme une capacité : RawStore, SignalStore, EventLog, SecretBroker…
  ├─ observe un fait durable : signal.searchable.v1
  └─ intercepte un hook contrôlé : signal.before_publish.v1
```

Le nom `Plugin` reste unique. Les **points d’extension** portent en revanche des garanties différentes : durable ou éphémère, bloquant ou non, ordonné ou parallèle, local ou distant, rejouable ou non.

## État, contexte et services

### Pi stable

L’état en mémoire peut vivre dans la closure d’une extension. Pour survivre au reload et respecter les branches d’une session, Pi recommande de l’enregistrer dans les `details` des résultats d’outils ou avec `pi.appendEntry()`, puis de le reconstruire pendant `session_start` à partir du journal ([gestion d’état](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/docs/extensions.md#L1471-L1486), [reconstruction branch-aware](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/docs/extensions.md#L1879-L1911)).

`ExtensionContext` expose notamment le cwd, un session manager en lecture seule, le modèle, le registry, le signal d’annulation courant, `abort()`, `shutdown()` et l’état idle. Les opérations susceptibles de remplacer le runtime, dont `reload()`, sont limitées au `ExtensionCommandContext`, car leur appel depuis un event handler peut provoquer un deadlock ([types du contexte](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/src/core/extensions/types.ts#L304-L389)).

### Chord

Chord sépare l’état d’invocation et l’état partagé. Son `Context` propage l’annulation et des valeurs locales typées ; l’identité, les permissions et la télémétrie restent des responsabilités de l’application. Son état répliqué publie des snapshots/deltas et repasse en état non prêt lors d’une déconnexion ou d’un remplacement avant réhydratation ([Context et frontière JSON](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/PLANNING.md#L77-L99), [replicated state](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/README.md#L34-L54)).

Pour Agency, un plugin ne devrait pas recevoir directement une connexion Postgres, S3, Kafka ou des secrets globaux. Il devrait consommer des services étroits et versionnés, et recevoir par invocation un contexte comportant identité technique, tenant, droits, trace, deadline et annulation.

## Chargement, rechargement et ajout d’un plugin

### Pi stable : remplacement complet d’un runtime local

Les extensions sont découvertes dans des dossiers globaux ou projet, dans les settings et dans des packages npm/git. Elles sont chargées avec `jiti`, donc du TypeScript peut s’exécuter sans compilation. Le code possède tous les droits du processus ([découverte et sécurité](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/docs/extensions.md#L109-L150), [packages](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/docs/packages.md#L18-L50)).

`/reload` suit ce cycle :

```text
attendre que la session locale soit idle
→ session_shutdown(reason = reload)
→ invalider l’ancien runtime et retirer ses abonnements au bus
→ redécouvrir et recharger toutes les ressources
→ reconstruire l’ExtensionRunner et ses outils
→ session_start(reason = reload)
```

La TUI refuse explicitement le reload pendant une réponse ou une compaction ([garde idle](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/src/modes/interactive/interactive-mode.ts#L5889-L5897)). Le cœur émet ensuite `session_shutdown`, invalide l’ancien runner, recharge et rebâtit le runtime ([implémentation du reload](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/src/core/agent-session.ts#L2818-L2842)). Après `ctx.reload()`, l’ancien call frame existe encore mais ses objets sont périmés ; Pi recommande de traiter le reload comme terminal pour le handler ([contrat documenté](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/docs/extensions.md#L1303-L1327)).

Pi sait par ailleurs enregistrer un **nouvel outil** à chaud depuis une extension déjà chargée : l’outil est visible immédiatement sans reload. Cela ne signifie pas qu’un nouveau module arbitraire est injecté au milieu d’un travail en cours ; l’installation d’une nouvelle extension passe toujours par la découverte/reconstruction ([outils dynamiques](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/docs/extensions.md#L1365-L1377)).

Donc Pi réalise bien une forme de « stop-the-world », mais seulement pour **une session locale**, une fois son travail en cours terminé. Ce n’est pas un modèle à copier tel quel sur une plateforme distribuée recevant continuellement des dépêches.

### Chord : génération candidate puis cutover

Chord charge des bundles content-addressed, vérifie leur SHA-256 et évalue chaque génération séparément hors du cache ESM/CommonJS global. Une génération retirée devient collectable quand ses ressources et références ont été libérées ([bundling et génération](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/README.md#L124-L202)).

Pour un remplacement qui conserve la même forme de services, le host :

1. construit la candidate ;
2. vérifie que ses dépendances et services sont compatibles ;
3. active la candidate pendant que l’ancien fournisseur reste routé ;
4. remplace directement la cible derrière les facades stables ;
5. désactive l’ancienne facet et libère son module.

Une erreur de setup, de validation ou d’activation **avant** le cutover laisse l’ancienne génération active. Après le début du cutover, Chord ne promet pas de rollback : une erreur terminale tue le host pour éviter un graphe partiellement transformé ([sémantique détaillée](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/PLANNING.md#L189-L227), [implémentation](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/src/facets/host.ts#L423-L510)). Pi sérialise aussi les reloads et ne libère l’ancienne génération qu’après une bascule réussie ([worker expérimental](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/src/experimental/services/worker.ts#L73-L106)).

Limite importante : le `FacetHost.reload()` actuellement implémenté n’accepte que des facets déjà actives et dont la forme `requires/provides` reste identique. **Ajouter ou enlever un plugin est une modification structurelle.** Le plan Chord prévoit alors de préparer et valider la génération complète, retirer l’ancien graphe, installer le nouveau puis détruire l’ancien ; cette voie structurelle reste annoncée comme planifiée, pas comme une garantie de l’API actuelle ([contrôle actuel des IDs et de la forme](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/src/facets/host.ts#L423-L445), [remplacement structurel planifié](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/PLANNING.md#L229-L251)).

## Travail en vol et erreurs

### Pi stable

- une extension qui échoue au chargement est écartée ; les autres continuent à être chargées ;
- les erreurs de la plupart des handlers sont remontées comme diagnostics et la boucle continue ;
- une exception dans `tool_call` bloque l’appel par sécurité ;
- une exception dans l’exécution d’un outil est transformée en résultat `isError: true` et rendue au modèle.

Ces politiques sont résumées dans [la documentation des erreurs](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/docs/extensions.md#L2921-L2925). Le loader initialise chaque extension transactionnellement : ses changements en attente sont commit seulement si sa factory termine, sinon les abonnements et changements provisoires sont abandonnés ([initialisation](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/src/core/extensions/loader.ts#L543-L586)).

### Chord

Chaque génération possède ses callbacks, ressources, instances keyed, observations et services. La destruction est idempotente, continue malgré les erreurs et agrège les échecs ; les consommateurs sont désactivés avant leurs fournisseurs ([ownership et cleanup](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/PLANNING.md#L150-L162)).

Mais Chord est explicite sur sa limite : un appel déjà parti dans une ancienne facet **n’est pas drainé**. Un accès ultérieur à un handle révoqué peut échouer comme travail périmé. Le projet envisage un futur host en isolate capable de tuer ce travail ([appels pendant unload](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/PLANNING.md#L253-L255)).

Quivr V2 doit être plus strict pour les traitements durables : chaque job porte la version du plugin et de sa configuration ; soit l’ancienne génération termine avec cette version, soit son lease est révoqué et le job est rejoué idempotemment. Une bascule ne doit jamais produire un résultat dont on ne sait pas quelle génération l’a calculé.

## Isolation et sécurité

Pi stable avertit que les extensions ont tous les droits du processus et peuvent exécuter du code arbitraire. La confiance dans un projet empêche seulement de charger automatiquement sa configuration avant consentement ; elle ne sandboxe pas le code ([sécurité des extensions](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/coding-agent/docs/extensions.md#L109-L114), [permissions du projet](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/README.md#L28-L39)).

Chord réduit les imports externes à une liste déclarée par le bundle et vérifie l’intégrité du code, mais `node:vm` sert ici à créer des générations déchargeables, pas une frontière de sécurité. Les built-ins Node déclarés peuvent être chargés par le host ([bundle loader](https://github.com/badlogic/pi-mono/blob/e44d75c20a51142abc056c243b13c1d7bb4be687/packages/chord/src/node/bundle-loader.ts#L133-L202)).

Pour Agency, deux niveaux sont nécessaires :

- plugins Quivr approuvés, éventuellement in-process pour les parseurs purs et rapides ;
- plugins tiers ou avec effets externes dans un worker/conteneur isolé, sans accès direct aux secrets ni aux stores internes.

Le manifeste doit déclarer permissions, types de données accessibles, événements consommés, services requis, quotas et politique réseau. Le runtime d’exécution doit faire respecter ces limites ; une simple déclaration ne suffit pas.

## Réponse recommandée à Q18

**Adopter une interface de plugin unique, comme Pi, et typer les contributions, comme Chord.** Ne pas créer quatre hiérarchies de plugins exclusives.

Une forme conceptuelle possible :

```ts
definePlugin({
  id: "Agency.breaking-alerts",
  setup(ctx) {
    const signals = ctx.use(DurableSignalEvents);
    const delivery = ctx.use(NotificationDelivery);

    ctx.subscribe(signals, "signal.searchable.v1", {
      mode: "durable",
      blocking: false,
      handler: alertOnBreakingSignal(delivery),
    });
  },
});
```

Les catégories à rendre explicites sont celles des **contrats** :

| Contribution | Sémantique attendue |
|---|---|
| `provide(SourceConnector)` | produit des entrées avec checkpoint et backpressure |
| `provide(Parser)` | transformation déterministe et rejouable |
| `subscribe(DurableSignalEvents)` | consommateur indépendant, curseur et idempotence propres |
| `intercept(BeforePublishPolicy)` | synchrone, timeout court, droit de blocage explicite |
| `provide(ProjectionWriter)` | projection reconstruisible vers un index |
| `provide(Retriever)` | capacité de lecture, droits appliqués avant résultat |

Un plugin peut fournir plusieurs contributions. Le host peut toutefois refuser certaines combinaisons de permissions ou forcer leur exécution dans des runtimes séparés.

## Réponse recommandée à Q20

Il faut distinguer trois cas :

### A. Ajout d’une alerte ou d’un observateur

**Aucun arrêt de l’ingestion.** Le nouveau plugin rejoint le journal durable comme un consumer group, choisit un point de départ (`maintenant`, offset précis ou replay historique), construit son état, puis passe en live. L’acquisition, la conservation et la disponibilité des signaux continuent.

### B. Remplacement d’un plugin sans changer ses contrats

**Bascule générationnelle.** On charge et valide la candidate en parallèle, on arrête d’attribuer de nouveaux jobs à l’ancienne version, on laisse finir ou on replanifie les jobs en vol, puis on change atomiquement le routage. C’est le principe des stable facades et du candidate-first reload de Chord.

### C. Ajout ou suppression sur le chemin synchrone critique

**Quiescence locale, pas arrêt global.** On prépare le graphe complet hors ligne, on valide contrats et migrations, puis on met brièvement en pause l’admission du segment concerné — un tenant, une source, une partition ou un pool de workers. Les publications déjà reçues restent durablement dans le log et forment un backlog rejouable. Les autres partitions continuent.

Un arrêt complet de toute la plateforme ne devrait être réservé qu’à une rupture d’invariant canonique impossible à faire coexister, par exemple une migration non rétrocompatible du format durable. Si l’ajout d’une simple alerte exige l’arrêt de tous les connecteurs, parseurs et indexeurs, le système de plugins a créé un couplage que son existence devait précisément éviter.

## Ce qu’il faut retenir pour Quivr V2

1. **Un seul concept de plugin**, pas une taxonomie de classes rigides.
2. **Des capacités et contributions nommées**, versionnées et assorties de sémantiques d’exécution.
3. **Un graphe validé avant activation**, avec ownership et cleanup automatique des ressources.
4. **Des générations de plugins immuables**, identifiées dans chaque job et chaque résultat.
5. **Candidate-first puis cutover**, pas mutation improvisée d’un runtime actif.
6. **Journal durable pour les faits métier** ; le bus local de hooks ne sert qu’au contrôle en vol.
7. **Quiescence au plus petit domaine de panne**, jamais arrêt global par défaut.
8. **Isolation réelle pour le code tiers** ; ni `jiti` ni `node:vm` ne constituent une sandbox de production.
