# DeepSeek Harness et Cordis : idées transférables à l’ingestion Agency

_Recherche effectuée le 3 septembre 2026. Analyse du dépôt DeepSeek Harness au commit `76fda729799fe9b3848dbe2c211d4b231032b81e`._

## Conclusion courte

La référence visée est presque certainement **DeepSeek Harness**, dont la présentation officielle est « Everything is a plugin », et le papier associé **A Programming Paradigm for Spatiotemporal Composability** (Cordis), publié sur arXiv le 26 août 2026. Le README du dépôt relie explicitement le harness à ce papier, et deux auteurs sur trois sont affiliés à DeepSeek-AI ([annonce officielle](https://www.deepseek.com/harness/en/), [README du dépôt](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/README.md), [papier Cordis](https://arxiv.org/abs/2608.25512)).

Le point le plus intéressant pour Quivr V2 n’est pas « tout rendre interchangeable ». C’est la combinaison de cinq disciplines :

1. des capacités nommées derrière des contrats stables ;
2. des dépendances déclarées par chaque plugin ;
3. des événements typés dont le mode d’exécution fait partie du contrat ;
4. un cycle de vie qui retire automatiquement les inscriptions et ressources d’un plugin ;
5. une distinction explicite entre faits durables et interceptions éphémères.

Ces idées sont transférables. Le runtime Cordis lui-même ne l’est pas automatiquement : DeepSeek Harness est encore en developer preview, son API annonce des changements incompatibles, et son modèle principal est un graphe de plugins TypeScript dans un processus partagé, pas un pipeline distribué de données à très grand volume ([README](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/README.md), [architecture](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/architecture.md)).

## Identification de la référence

### Candidat principal : DeepSeek Harness + papier Cordis

DeepSeek présente officiellement son nouveau harness comme un système où modèles, outils, skills, sessions, sandboxes, stockage, boucles, ordonnancement et UI sont fournis par des plugins. Cordis ne porte pas ces capacités métier : son noyau gère le montage, le démontage et les dépendances des plugins. La composition est choisie par configuration sans modifier le code du harness ([annonce officielle](https://www.deepseek.com/harness/en/)).

Le papier lié par DeepSeek formalise deux propriétés :

- **composabilité temporelle** : retirer un composant doit annuler proprement les effets qu’il a installés ;
- **composabilité spatiale** : un composant déclare ses dépendances et le runtime l’active ou le désactive lorsque celles-ci apparaissent ou disparaissent.

Cordis matérialise cela avec des effets réversibles, des dépendances réactives, un contexte partagé, un loader déclaratif, de la réconciliation de configuration et du hot reload ([papier Cordis, résumé et contributions](https://arxiv.org/abs/2608.25512)).

### Autres papiers récents examinés

| Candidat | Pourquoi il pouvait correspondre | Pourquoi ce n’est probablement pas la référence |
|---|---|---|
| [JIT-Agent: Scaling Harness Intelligence via Just-in-Time Harness Evolution](https://arxiv.org/abs/2608.25593) | Publié le même jour que Cordis ; décrit un harness composable en quatre modules et évalue des modèles DeepSeek. | Le sujet est la génération et l’évolution de harnesses par un modèle, pas le runtime de plugins/hooks présenté par DeepSeek. Le papier n’est pas le papier directement lié depuis le dépôt DeepSeek Harness. |
| [From Model Scaling to System Scaling: Scaling the Harness in Agentic AI](https://arxiv.org/abs/2605.26112) | Défend des architectures de harness persistantes, modulaires, auditables et vérifiables. | Papier conceptuel soumis en mai 2026, antérieur au lancement ; il ne décrit ni Cordis ni le système de plugins du dépôt officiel. |

Le lien explicite entre le produit, Cordis et le papier rend donc le premier candidat nettement plus probable.

## Ce que DeepSeek a réellement construit

### 1. Un arbre de plugins, pas un noyau métier omniscient

Une exécution du harness est un arbre de plugins assemblé au démarrage à partir de profils, bundles et couches de configuration. Une couche peut ajouter, remplacer, désactiver ou reconfigurer une entrée. Les profils interactifs peuvent recharger la configuration à chaud ; certains profils one-shot ne le font volontairement pas lorsque cela invaliderait le travail qu’ils possèdent ([architecture, profils et bundles](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/architecture.md), [composition et HMR](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/cordis-tutorial/06-composition-and-hmr.md)).

Cela implique une nuance importante : « tout est plugin » ne signifie pas « aucune règle centrale ». Le harness publie des contrats centraux — services, événements, journal de session, cycle de vie — auxquels les plugins doivent se conformer.

### 2. Des services nommés et des dépendances explicites

Un plugin fournit une capacité sous une clé stable dans un contexte ; ses consommateurs dépendent de cette clé plutôt que d’importer une implémentation concrète. La déclaration `inject` exprime les dépendances requises. Si un fournisseur manque, le plugin consommateur reste en attente ; si le fournisseur disparaît, ses dépendants sont démontés puis réactivés lorsqu’il revient ([primer Cordis](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/cordis-primer.md), [services et dépendances](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/user/develop/framework/service.md)).

DeepSeek appelle **capability seam** une capacité séparée en trois rôles : définition du contrat, fournisseur de l’implémentation, consommateur. Ce découpage permet de remplacer un fournisseur sans réécrire les consommateurs ([architecture, capability seams](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/architecture.md)).

### 3. Des événements typés avec plusieurs sémantiques

Cordis distingue cinq modes d’événements, et le mode appartient au contrat public :

| Mode | Sémantique Cordis | Usage conceptuel |
|---|---|---|
| `emit` | diffusion synchrone, retours non collectés | observation légère |
| `parallel` | listeners asynchrones exécutés en parallèle et attendus | fan-out attendu |
| `serial` | listeners attendus dans l’ordre, arrêt au premier résultat décisif | politique ordonnée |
| `bail` | version synchrone du court-circuit | décision immédiate |
| `waterfall` | middleware enveloppant, transformant ou bloquant la suite | interception du chemin principal |

Un listener `waterfall` qui ne délègue pas arrête volontairement le pipeline ; un simple observateur doit toujours déléguer. Cela transforme l’ordre, le caractère bloquant et les retours d’un hook en éléments explicites du contrat, plutôt qu’en conventions implicites ([système d’événements](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/user/develop/framework/events.md), [primer](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/cordis-primer.md)).

### 4. Des effets réversibles liés au cycle de vie du plugin

Les inscriptions d’événements, timers, connexions, watchers et autres ressources sont enregistrées comme des effets appartenant au plugin. Chaque acquisition fournit un disposer ; Cordis les exécute au démontage, y compris lors d’un hot reload. Les plugins suivent une machine d’état explicite : attente, chargement, actif, déchargement, terminé ou échec ([cycle de vie et effets](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/cordis-tutorial/02-lifecycle-and-effects.md)).

Le papier reconnaît toutefois que l’inverse correct reste une obligation de l’auteur : le runtime sait appeler le disposer, mais ne peut pas prouver qu’il annule réellement l’effet produit ([papier Cordis, §5.1.1](https://arxiv.org/pdf/2608.25512)).

### 5. Une séparation entre faits durables et contrôle en vol

DeepSeek distingue :

- les événements de session, faits durables ajoutés à un journal append-only et rejouables ;
- les événements `agent/*`, qui observent ou interceptent du travail vivant ;
- les événements de capacité, qui branchent politiques et adaptateurs sur une interface.

Le produit affirme que tout ce que voit le modèle est reconstructible depuis le journal ; reprise, fork, recherche, replay et télémétrie dérivent de ce même flux ([architecture, Events et Session log](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/architecture.md), [présentation officielle](https://www.deepseek.com/harness/en/)).

### 6. Des profils, bundles et overlays

Un profil compose plusieurs bundles et accepte des plugins externes. Des overlays remplacent ou insèrent des lignes de configuration identifiées de façon stable. Les groupes permettent de monter/démonter plusieurs plugins comme une unité, et l’isolation donne à différents sous-arbres des instances différentes d’un même service ([architecture](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/architecture.md), [composition et HMR](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/cordis-tutorial/06-composition-and-hmr.md)).

## Complément du 3 septembre 2026 — plugins hors processus et distants

### Verdict

**DeepSeek Harness ne livre pas aujourd’hui un runtime générique capable de lancer n’importe quel plugin serveur dans un processus ou un conteneur distant.** Le système livré combine plutôt quatre mécanismes distincts qu’il ne faut pas confondre :

1. les plugins Cordis statiques sont chargés dans le processus du harness ;
2. les packages Cordis dynamiques côté Host s’exécutent dans un `node:vm`, toujours dans ce même processus ;
3. un package dynamique peut avoir une seconde moitié exécutée dans la page Web, reliée au Host par le RPC applicatif de DeepSeek ;
4. certains fournisseurs officiels déportent une **capacité précise**, par exemple fichiers et commandes vers E2B, mais ils ne déportent ni le plugin Cordis ni le harness complet.

Le **service broker multi-provider**, son load balancing, le rolling update avec drainage et la transparence interprocessus sont une proposition de la discussion du papier Cordis, pas une implémentation démontrée dans le runtime actuel ([papier, §6.2](https://arxiv.org/pdf/2608.25512#page=71)).

### Ce qui existe dans le dépôt actuel

#### Dynamic plugins : `node:vm`, pas processus isolé

Le runner officiel dit explicitement que les définitions et leur registre restent en mémoire locale, et que la moitié Host s’exécute « in this process » dans `node:vm`. Ce VM masque ou redirige les globals Node et remet au plugin une façade Cordis limitée, mais DeepSeek précise que ce n’est **pas une frontière de sécurité**. Le timeout par défaut de 5 secondes ne borne que l’évaluation synchrone ; un corps asynchrone peut lui échapper ([runner Host](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/packages/extensions/cordis-host-runner/README.md#L10-L54), [limites du runner](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/packages/extensions/cordis-host-runner/README.md#L121-L134), [implémentation du sandbox](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/packages/extensions/cordis-host-runner/src/sandbox.ts#L1-L16)).

Le lifecycle est bien réversible et attendu : `stop` retire les handlers puis attend `fiber.dispose()`. En revanche, la mise à jour dynamique actuelle **rétracte l’ancienne exécution avant de démarrer la nouvelle** ; elle ne maintient pas deux fournisseurs derrière un routeur et ne draine pas les appels en vol ([code de `startFresh`](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/packages/extensions/cordis-host-runner/src/index.ts#L812-L878), [code de `retract`](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/packages/extensions/cordis-host-runner/src/index.ts#L1218-L1231)). Cela diffère du rolling update proposé dans le papier.

#### Host ↔ navigateur : un vrai RPC typé, mais pas un worker plugin générique

Une extension dynamique peut comporter une moitié Browser. Elle est montée dans la page, ne reçoit qu’une surface restreinte, et appelle uniquement sa propre moitié Host via `host.call(method, args)`. Son activation et sa rétraction suivent les identités de run et les effets Cordis ([runner Client](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/packages/extensions/cordis-client-runner/README.md#L25-L71)).

Le transport sous-jacent, Typert/API Gateway, est concret et intéressant : des descripteurs et codecs générés valident entrées et sorties ; les appels unitaires passent par `/api` ; les streams logiques partagent un WebSocket multiplexé ; un carrier in-process évite le réseau quand les deux faces cohabitent ; le retrait d’une contribution supprime ses méthodes et annule ses appels et streams en cours. Le gateway fournit reconnexion et heartbeat, tandis que les curseurs/replays restent la responsabilité du domaine ([API Gateway](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/packages/api/gateway/README.md#L24-L54), [limites de replay](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/packages/api/gateway/README.md#L67-L77)).

Cette infrastructure relie les faces Host et Client choisies par l’application. Les sources officielles ne la présentent pas comme un ordonnanceur de plugins serveur arbitraires, un registre de réplicas ou un broker distribuant une capacité entre plusieurs workers distants.

#### SDK hors processus : le client pilote un harness complet

DeepSeek fournit aussi un SDK TypeScript/Python hors processus : le client lance un runtime complet comme sous-processus et le pilote en JSON-RPC sur stdio. Le plugin serveur expose sessions, prompts et notifications ; ce n’est pas un protocole permettant à un seul plugin distant de rejoindre le graphe de services d’un Host existant ([SDK client](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/packages/sdk/client/README.md#L10-L54), [serveur JSON-RPC](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/packages/sdk/server/README.md#L10-L52)).

#### E2B : déport d’un execution world derrière des capability seams

L’exemple livré le plus proche d’un worker distant est la famille E2B. Un plugin Host conserve le handle d’un sandbox Linux distant ; les adaptateurs `fs` et `subprocess` implémentent les mêmes contrats que leurs variantes locales. Tous partagent un sandbox, créé au chargement et supprimé au timeout ou à l’arrêt. Mais DeepSeek marque ce travail comme POC : état éphémère, aucune plateforme de déploiement configurée, et services Cordis, sessions, logs et LLM restant dans le Host ([owner E2B](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/packages/e2b/e2b/README.md#L10-L94), [limites E2B](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/packages/e2b/e2b/README.md#L121-L131)).

Le coût explicité est la latence réseau au démarrage de chaque commande. Le polling est réglable : l’espacer réduit les requêtes distantes mais retarde la détection de fin. L’adaptateur reconnaît aussi que le SDK retient encore la sortie complète en mémoire Host, ce qui ne respecte pas la borne mémoire habituelle du seam subprocess ([subprocess E2B](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/packages/e2b/subprocess-e2b/README.md#L10-L46), [limites de performance](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/packages/e2b/subprocess-e2b/README.md#L137-L152)).

### Ce que le papier propose seulement

Le papier décrit un **service broker** stable injecté à la fois par les consommateurs et par plusieurs fournisseurs. Le broker pourrait router en round-robin, least-loaded, pondéré par latence ou vers une cible explicite. Un fournisseur s’inscrit par effet réversible ; son retrait l’enlève automatiquement du routing. Pour un rolling update, le nouveau fournisseur devient actif, reçoit progressivement le trafic, puis les anciens sont retirés lorsqu’ils ne portent plus d’appels en vol. Pour traverser les processus, chaque processus aurait son propre contexte Cordis et un composant coordinateur exposerait ses services comme fournisseurs distants derrière RPC ([papier, §6.2](https://arxiv.org/pdf/2608.25512#page=71)).

Le papier impose deux réserves : l’interface distante doit être asynchrone parce qu’un appel RPC ajoute de la latence et peut échouer en vol ; le code non fiable doit être placé derrière une vraie frontière externe — processus sandboxé, runtime séparé, SFI ou conteneur — avec un bridge qui n’expose que les capabilities autorisées ([papier, §6.2–6.3](https://arxiv.org/pdf/2608.25512#page=71)). Le papier ne fournit ni protocole wire, ni algorithme de health-check, ni reprise durable, ni admission/backpressure, ni code de scheduler pour ce broker.

Enfin, il ne publie aucun benchmark du surcoût Cordis ou RPC. Son étude Koishi est une preuve d’existence/adoption, pas une comparaison quantitative ; mesurer l’overhead de l’abstraction reste explicitement un travail futur ([papier, menaces à la validité](https://arxiv.org/pdf/2608.25512#page=70)). Le dépôt DeepSeek avertit en outre que le harness est expérimental, non audité et non production-ready ([Safety](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/SAFETY.md#L5-L23)).

### Conséquence pour Quivr V2

DeepSeek confirme la bonne **forme d’abstraction**, pas une implémentation prête à reprendre. Pour Quivr V2, le chemin cohérent serait un `CapabilityBroker` stable dans le moteur et deux types de providers partageant exactement le même contrat asynchrone : provider local approuvé et provider RPC isolé. Le broker posséderait routing, deadlines, circuit breaker, limites de concurrence, santé, poids de version et drainage. Le worker distant posséderait sandbox CPU/mémoire/réseau, secrets et autoscaling. Le journal d’ingestion posséderait reprise, replay et idempotence : aucune de ces garanties ne doit être attribuée à Cordis ou à un RPC seul.

## Mécanismes transférables à Quivr V2

Cette section ne propose pas encore l’architecture finale. Elle formule les correspondances à tester.

| Mécanisme DeepSeek/Cordis | Transposition possible dans l’ingestion/retrieval | Question ou contrainte à valider |
|---|---|---|
| Capability seam : contrat / fournisseur / consommateur | Séparer, par exemple, le contrat d’un parseur, ses implémentations NewsML-G2/HTML/PDF et les étapes qui consomment du contenu normalisé. Même logique pour enrichissement, indexation, notification ou retrieval. | Quelles capacités doivent être substituables et lesquelles restent des invariants du noyau ? |
| Dépendances déclarées | Un plugin d’alerte pourrait déclarer `signal-events`, `rules`, `delivery` et `audit`, plutôt que recevoir implicitement un accès à toute la plateforme. | Faut-il refuser l’activation ou fonctionner en mode dégradé lorsqu’une dépendance manque ? |
| Modes d’événements explicites | Distinguer une observation asynchrone, une transformation ordonnée, une validation bloquante et un veto de politique. | Quels hooks ont le droit de ralentir ou bloquer une dépêche ? |
| Effets réversibles | Enlever un plugin doit retirer subscriptions, timers, routes, métriques, consommateurs et connexions sans redémarrer les autres composants. | Le rechargement à chaud est-il nécessaire en production ou seulement en développement ? |
| Faits durables vs événements live | Les transitions métier importantes de l’ingestion peuvent être rejouables, tandis que les hooks de politique sur le chemin critique restent éphémères. | Quel événement marque l’engagement durable : brut conservé, signal normalisé ou contenu recherchable ? |
| Profiles / bundles / overlays | Composer un profil Agency avec parseurs, enrichissements, règles et retrievers sélectionnés, sans forker le noyau générique. | Quelle part de la configuration appartient au déploiement, à l’organisation ou à une source ? |
| Isolation de contexte | Restreindre la vue et les capacités disponibles selon le plugin ou le profil. | L’isolation logique suffit-elle pour du code approuvé ? Quel niveau physique pour du code tiers ? |
| Service broker | Plusieurs fournisseurs d’une même capacité peuvent coexister derrière un broker ; le papier étend ce motif au load balancing, au rolling update et aux appels interprocessus asynchrones. | Les fournisseurs doivent-ils être choisis par contenu, tenant, coût, santé ou version ? ([papier Cordis, §6.2](https://arxiv.org/pdf/2608.25512)) |

## Application au cas « déclencher une alerte pour un certain type de document »

Le cas exprimé ne ressemble pas à un nouveau connecteur ou parseur. C’est un **plugin de réaction à un événement métier**. L’enseignement principal de DeepSeek est de choisir l’extension point selon la sémantique recherchée.

Trois variantes doivent rester distinctes :

1. **Observation après engagement durable** : le contenu est déjà conservé/normalisé ; le plugin évalue une règle et déclenche éventuellement une livraison. Une panne du plugin ne doit pas faire disparaître le contenu.
2. **Interception avant admission** : le plugin peut annoter, transformer, mettre en quarantaine ou refuser un contenu avant une transition critique. Il est volontairement sur le chemin principal et nécessite budget de latence, timeout et comportement fail-open/fail-closed.
3. **Enrichissement produisant une nouvelle projection** : le plugin calcule un attribut ou une classification ensuite exploité par les règles d’alerte et le retrieval.

Un unique hook générique `onDocumentIngested` mélangerait ces garanties. Les modes Cordis montrent qu’un système extensible doit encoder au niveau du contrat si un plugin observe, attend, transforme, bloque ou court-circuite.

Un flux hypothétique à tester — pas une décision d’architecture — serait :

```text
publication reçue
  -> conservation / normalisation par le noyau
  -> fait durable SignalSearchable(vN)
       -> plugin de règle : correspondance du type éditorial
       -> capacité de livraison : email, webhook, messagerie, notification
       -> fait durable AlertDeliveryAttempted / Delivered / Failed
```

Ce flux reprend l’idée DeepSeek des faits durables séparés des hooks live, mais ne reprend pas son simple `emit` in-process comme mécanisme de transport.

## Ce qui ne se transpose pas directement

### 1. Un événement Cordis n’est pas un bus durable distribué

Les modes `emit`, `parallel`, `serial`, `bail` et `waterfall` sont des appels entre listeners dans un runtime. Ils ne fournissent pas à eux seuls persistance, partitionnement, backpressure, livraison au moins une fois, reprise sur offset, dead-letter queue ni consommation indépendante. Pour des millions de contenus et un SLA de veille, ces propriétés doivent être traitées séparément ([événements Cordis](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/user/develop/framework/events.md)).

### 2. Un effet externe envoyé n’est pas réellement réversible

Le papier trace explicitement la frontière : l’acquisition d’une ressource peut être annulée, mais l’émission vers l’extérieur — écrire des octets, envoyer un datagramme, déclencher une notification — échappe généralement au rollback. Il faut soit retenir l’émission jusqu’à l’engagement, soit définir une compensation métier ([papier Cordis, §6.1](https://arxiv.org/pdf/2608.25512)).

Pour une alerte, désinstaller le plugin peut retirer son abonnement ; cela ne peut pas « désenvoyer » un message. L’idempotence, l’outbox, les tentatives et l’audit ne découlent donc pas des effets réversibles de Cordis.

### 3. Le code tiers in-process n’est pas isolé

Le papier explique que les déclarations de dépendances offrent une forme de contrôle par capacités, mais qu’un plugin malveillant disposant du runtime hôte peut contourner cette médiation. Le code non fiable exige une frontière externe : autre processus, runtime séparé, sandbox ou conteneur. Le projet DeepSeek lui-même prévient qu’il n’a pas été audité et que des plugins tiers peuvent exposer données et identifiants ([papier Cordis, §6.3](https://arxiv.org/pdf/2608.25512), [Safety](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/SAFETY.md)).

### 4. La compatibilité des contrats reste un problème ouvert

Le papier identifie la dérive d’interface et les collisions de clés entre plugins indépendants. Cordis s’appuie actuellement sur les peer dependencies et le versionnage sémantique, ce qui suppose que les producteurs le respectent et rend difficile la coexistence de plusieurs versions. La compatibilité structurelle et comportementale complète reste un problème ouvert ([papier Cordis, §6.6](https://arxiv.org/pdf/2608.25512)).

Pour Quivr V2, nommer un événement ne suffit donc pas : son schéma, sa version, ses garanties de compatibilité et son comportement en cas de champ inconnu devront faire partie de l’API plugin.

### 5. Le papier ne prouve pas les performances au scale Agency

La validation empirique décrite porte sur Koishi, un écosystème de plugins TypeScript. Les auteurs la présentent comme une preuve d’existence et d’adoption, pas comme une comparaison contrôlée ; la mesure du surcoût de l’abstraction et de la productivité reste un travail futur ([papier Cordis, étude de cas et menaces à la validité](https://arxiv.org/pdf/2608.25512)).

Il n’y a donc pas de preuve dans ces sources que Cordis supporte tel quel un débit Agency, des médias lourds ou des traitements distribués.

### 6. Le hot reload ne migre pas automatiquement l’état interne

Cordis démonte les effets de l’ancienne instance puis remonte la nouvelle. L’état mémoire propre au plugin ne survit pas, sauf s’il a été placé dans une dépendance de durée de vie supérieure ; la migration d’état à chaud est laissée comme travail futur ([papier Cordis, discussion sur le stateful forward migration](https://arxiv.org/pdf/2608.25512)).

## Questions de conception ouvertes pour le prochain round

1. « Quelqu’un » signifie-t-il une équipe Quivr, une équipe Agency, un intégrateur approuvé ou n’importe quel développeur tiers ? Le modèle de confiance change radicalement l’isolation.
2. Un plugin d’alerte peut-il rater une alerte pendant son indisponibilité, ou doit-il reprendre depuis un journal durable ?
3. L’alerte s’évalue-t-elle sur le contenu brut, le signal normalisé, le contenu enrichi, le regroupement événementiel ou plusieurs étapes ?
4. Un plugin peut-il bloquer la publication dans le dispositif de veille ? Si oui, lesquels et avec quel comportement après timeout ?
5. Les plugins sont-ils installés comme bibliothèques approuvées, processus sidecar, workers distants, WebAssembly, webhooks, ou plusieurs classes explicitement distinctes ?
6. Le plugin reçoit-il le contenu complet ou seulement un événement contenant des références et des métadonnées autorisées ?
7. Comment sont versionnés le manifeste, les capacités requises, les schémas d’événements et les permissions ?
8. Quel est le modèle de reprise : offset par plugin, retry borné, quarantaine, DLQ, relecture manuelle ?
9. Comment empêcher un plugin lent ou défectueux d’augmenter la latence de mise à disposition des dépêches ?
10. Quelles sorties externes doivent être exactement-once du point de vue utilisateur, et lesquelles acceptent des doublons dédupliqués par identifiant ?

## Sources primaires principales

- [DeepSeek Harness — annonce officielle](https://www.deepseek.com/harness/en/)
- [Dépôt officiel DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness)
- [Architecture DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/architecture.md)
- [Cordis Primer](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/cordis-primer.md)
- [Services and dependencies](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/user/develop/framework/service.md)
- [Event system](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/user/develop/framework/events.md)
- [Lifecycle and effects](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/cordis-tutorial/02-lifecycle-and-effects.md)
- [Composition and HMR](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/docs/cordis-tutorial/06-composition-and-hmr.md)
- [A Programming Paradigm for Spatiotemporal Composability](https://arxiv.org/abs/2608.25512)
- [DeepSeek Harness Safety](https://github.com/deepseek-ai/deepseek-harness/blob/76fda729799fe9b3848dbe2c211d4b231032b81e/SAFETY.md)
