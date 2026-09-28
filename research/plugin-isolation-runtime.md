# Runtime de plugins isolés pour un backend Quivr distribué

_Recherche effectuée le 3 septembre 2026, à partir de spécifications et documentations officielles actuelles._

## Conclusion

Quivr ne devrait construire ni un orchestrateur de processus, ni un mini-Kubernetes, ni un runtime serverless propriétaire. La trajectoire la plus robuste est :

1. définir un **contrat de plugin indépendant du runtime** ;
2. exécuter d'abord chaque plugin distant comme un service ou worker OCI standard, géré par Kubernetes ;
3. utiliser **KEDA** pour adapter les workers asynchrones au backlog du broker ;
4. garder au moins une réplique des plugins synchrones sur le chemin du retrieval ;
5. offrir ensuite un runner **WebAssembly** séparé pour les petites extensions compatibles avec ses contraintes ;
6. sélectionner `runc`, **gVisor** ou **Kata Containers** via `RuntimeClass` selon le niveau de confiance ;
7. ne considérer **Firecracker**, **Knative** et **SpinKube** que derrière des intégrations existantes et pour des besoins précis.

Le choix du runtime ne doit jamais modifier les événements, capacités ou réponses exposés au moteur. Un même plugin logique peut donc être fourni sous forme de conteneur, composant Wasm ou endpoint déjà opéré.

La recommandation concrète pour le premier système est : **Kubernetes Deployment + Service pour le synchrone, Deployment consommateur + KEDA pour l'asynchrone, Protobuf/gRPC comme contrat RPC, CloudEvents comme enveloppe d'événement, AsyncAPI comme documentation des canaux**. Ce socle réutilise des contrôleurs existants et n'exige aucun orchestrateur maison.

## Contraintes à ne pas confondre

« Sandboxé » recouvre plusieurs propriétés différentes :

- limiter CPU et mémoire ;
- empêcher l'accès au système de fichiers, aux secrets ou au réseau ;
- contenir une compromission du code invité ;
- isoler deux organisations ou plugins hostiles ;
- arrêter un calcul infini ;
- déployer et remplacer une version sans interrompre le moteur.

Aucune brique ne fournit seule toutes ces garanties. Les cgroups limitent des ressources mais ne remplacent pas une frontière de sécurité. WebAssembly isole la mémoire et médiatise les capacités, mais son runtime intégré partage encore le processus hôte. Un conteneur OCI est portable, mais son isolation standard repose toujours sur le noyau Linux de l'hôte. Une microVM offre une frontière plus forte, mais augmente le coût opérationnel et ne remplace pas les règles de réseau, d'identité et de secrets.

## Comparaison des options d'exécution

| Option | Frontière de sécurité réelle | Portabilité / compatibilité | Coût et démarrage | Réseau | Lifecycle et maturité | Place recommandée |
|---|---|---|---|---|---|---|
| Processus natif séparé | Meilleure qu'une bibliothèque in-process pour les crashes, mais faible sans utilisateur, namespaces, cgroups et seccomp dédiés. | Excellente pour tout exécutable de l'OS ; packaging et dépendances à gérer. | Faible coût fixe et démarrage rapide. | Appels locaux ou réseau ; règles d'egress à construire. | `systemd` ou un superviseur fonctionne sur un nœud, mais il faut reconstruire scheduling et rolling update à l'échelle. | Développement local seulement ; ne pas en faire l'orchestrateur distribué. |
| Conteneur OCI Kubernetes (`runc`) | Limites CPU/mémoire via cgroups et permissions Kubernetes ; le noyau hôte reste partagé. | Maximale : Python, JVM, FFmpeg, bibliothèques natives, GPU, etc. | Plus lourd que Wasm mais prévisible ; coût réglable par requests/limits. | NetworkPolicy et identité de workload disponibles. | Très mature ; Deployments, Services, probes, rollouts et arrêt gracieux existent déjà. | **Défaut du MVP et de la production** pour plugins approuvés. |
| Conteneur OCI sous gVisor | `runsc` interpose un noyau applicatif en userspace ; le code invité n'appelle pas directement le noyau hôte. Ne couvre pas les side channels CPU ni une mauvaise configuration donnant trop de capacités. | OCI/Kubernetes, mais compatibilité Linux réduite et overhead supérieur pour les applications riches en syscalls. | Empreinte flexible, sans RAM de VM préallouée ; coût syscall à mesurer. | Réseau de conteneur standard, avec restrictions Kubernetes en plus. | Intégration Kubernetes existante et usage en production ; changement par `RuntimeClass`. | Bon tier par défaut pour du code tiers lorsque la compatibilité est vérifiée. |
| Kata Containers | Chaque pod/conteneur tourne dans une VM légère avec isolation matérielle, derrière des interfaces OCI et CRI. | Bonne compatibilité conteneur, mais nécessite virtualisation et nœuds/runtime configurés. | RAM et boot d'un guest supérieurs à gVisor/Wasm ; benchmark indispensable. | Réseau de pod plus frontière VM ; politiques réseau toujours nécessaires. | S'intègre à Kubernetes plutôt que d'exiger un scheduler Quivr. | Plugins non fiables ou tenants hostiles exigeant une frontière VM. |
| Firecracker brut | MicroVM KVM, petit modèle de périphériques, seccomp et `jailer` ; forte isolation si l'hôte est correctement durci. | Guest Linux et artefacts à préparer ; ce n'est pas à lui seul un runtime de plugins Kubernetes. | Spécification officielle : VMM ≤5 MiB d'overhead, user space invité ≤125 ms après `InstanceStart`, hors orchestration, image pull et préparation du guest. | TAP/vsock à intégrer et limiter. | Brique VMM mature chez AWS, mais scheduling, image lifecycle et réseau restent à la charge de l'intégrateur. | **Ne pas intégrer directement** : utiliser seulement via une plateforme ou un runtime Kubernetes mature qui le prend en charge. |
| Wasmtime / WASI Component Model | Mémoire Wasm isolée, imports explicites et filesystem WASI par capacités. Le runtime reste du code dans le processus hôte : un bug du runtime ou une host function trop puissante agrandit la frontière de confiance. | Très bonne au niveau WIT/composants, mais les toolchains et bibliothèques disponibles varient selon les langages ; dépendances natives arbitraires difficiles. | Faible empreinte et démarrage rapide ; mémoire, stack et temps peuvent être bornés. | Aucun réseau sans capacité fournie ; idéal pour une allowlist fine. | Wasmtime est actif et orienté sécurité ; WASI 0.2 est stable, WASI 0.3 vient seulement d'introduire l'async natif en juin 2026. | Excellent **second tier** pour transformations pures, règles, filtres et enrichissements légers. L'héberger dans un service runner séparé. |
| Extism | Même sandbox Wasm ; manifeste avec limites mémoire, hosts et chemins autorisés. Host functions à auditer. | Très bon SDK/PDK multi-langage et ABI simple buffer-in/buffer-out ; ce n'est pas le Component Model/WIT complet. | Léger et embarquable ; copies de buffers entre host et guest à prendre en compte. | Hosts autorisés dans le manifeste ; réseau par capacité. | Produit ciblé « plugin system », plus simple à prototyper qu'une plateforme distribuée ; n'apporte ni scheduling ni rolling update. | Bon candidat pour prototyper le runner Wasm, sans en faire le contrat public irréversible de Quivr. |
| Spin / SpinKube | Hérite de Wasmtime/WASI et de l'isolation Kubernetes ; permissions déclarées dans le manifeste. | Applications événementielles Wasm, packaging OCI ; moins général qu'un conteneur pour les stacks multimédia/ML. | Spin annonce des cold starts milliseconde ; le shim SpinKube précompile et met en cache. Le démarrage d'un pod Kubernetes reste distinct. | Modèle HTTP et capacités Spin. | Spin et SpinKube sont encore des projets CNCF Sandbox ; SpinKube ajoute opérateur, CRD et shim containerd. | Piste d'expérimentation, pas dépendance obligatoire du cœur en première version. |

### Conteneurs Kubernetes : le meilleur point de départ

Kubernetes sait déjà maintenir des workloads stateless, remplacer des pods, exposer un endpoint stable et faire un rolling update. Un Deployment monte progressivement le nouveau ReplicaSet et descend l'ancien ; `maxSurge` et `maxUnavailable` bornent l'opération ([Deployments](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/)). Les requests guident le placement et les limites CPU/mémoire sont appliquées par le runtime, généralement via cgroups Linux ([gestion des ressources](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/)).

Cette solution est moins dense que Wasm, mais elle accepte immédiatement les plugins réalistes d'ingestion : Python, bibliothèques OCR, FFmpeg, modèles natifs, SDK propriétaires et accélérateurs. Elle évite surtout de transformer la première livraison en projet de runtime.

Un conteneur standard n'est toutefois pas une frontière suffisante pour du code hostile. La documentation de gVisor rappelle que les primitives Linux seules laissent le workload à un appel système du noyau hôte ; gVisor interpose à la place un noyau applicatif écrit en Go, tout en reconnaissant un coût de compatibilité et d'appels système ([architecture et modèle de sécurité gVisor](https://gvisor.dev/docs/architecture_guide/intro/), [présentation de `runsc`](https://gvisor.dev/docs/)).

Le profil Kubernetes minimal d'un plugin distant devrait comprendre :

- un ServiceAccount dédié sans token Kubernetes monté par défaut ;
- RBAC minimal lorsqu'un accès à l'API est réellement nécessaire ;
- requests/limits CPU et mémoire obligatoires ;
- filesystem racine en lecture seule et volumes explicitement autorisés ;
- exécution non-root, capabilities Linux supprimées et seccomp par défaut ;
- NetworkPolicy `deny-all` puis allowlist d'egress ;
- secrets courts et spécifiques aux capacités déclarées ;
- aucune connexion directe aux tables ou index internes de Quivr.

Kubernetes recommande le moindre privilège pour les ServiceAccounts et permet de désactiver le montage automatique du token ([Service Accounts](https://kubernetes.io/docs/concepts/security/service-accounts/)). `NetworkPolicy` contrôle les flux entre pods et vers l'extérieur, à condition que le CNI choisi l'applique réellement ([modèle réseau Kubernetes](https://kubernetes.io/docs/concepts/services-networking/), [NetworkPolicy](https://kubernetes.io/docs/reference/kubernetes-api/networking/network-policy-v1/)).

### gVisor, Kata et Firecracker : trois niveaux, pas trois concurrents équivalents

**gVisor** conserve l'ergonomie OCI et Kubernetes. Il vise un compromis entre processus et VM : pas de guest Linux complet, ressources flexibles, mais réimplémentation partielle de Linux et overhead par syscall. Il convient si le plugin tiers est compatible et si la densité compte ([introduction sécurité gVisor](https://gvisor.dev/docs/architecture_guide/intro/)).

**Kata Containers** place les workloads dans des VMs légères qui se présentent comme des conteneurs et supporte OCI ainsi que Kubernetes CRI. C'est la meilleure option étudiée lorsque l'isolation matérielle est une exigence et que l'équipe veut encore utiliser les contrôleurs Kubernetes ([architecture Kata Containers](https://katacontainers.io/)). Le coût exact dépend de l'hyperviseur, du guest, du stockage et du matériel ; il faut donc mesurer sur l'infrastructure cible plutôt que recopier un benchmark marketing.

**Firecracker** est un VMM, pas une plateforme de plugins. Il retire les périphériques non nécessaires, utilise KVM, installe des filtres seccomp et fournit un `jailer`. Son document de production demande explicitement un hôte durci, des mises à jour de microcode et noyau, des UID dédiés, des cgroups et des limites opérées par l'utilisateur ([design](https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md), [configuration d'hôte de production](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md)). Ses chiffres de démarrage et mémoire ne comprennent pas un scheduler, une image, le réseau, le guest et le lifecycle qu'il faudrait ajouter ([spécification de performance](https://github.com/firecracker-microvm/firecracker/blob/main/SPECIFICATION.md)). Construire ces couches dans Quivr irait directement contre l'objectif de ne pas réinventer l'orchestration.

## WebAssembly : un tier utile, pas le format unique des plugins

WebAssembly est adapté aux contributions courtes et déterministes : règles, filtres, normalisation légère, calcul de métadonnées, transformation de requête ou scoring. Il est moins adapté à un parseur Python dépendant de bibliothèques C, à FFmpeg ou à un pipeline GPU.

Le Component Model définit des interfaces cross-language en WIT et WASI fournit un jeu commun d'interfaces système. WASI 0.2 est une cible stable ; WASI 0.3, publié le 11 juin 2026, ajoute `async func`, `stream<T>` et `future<T>`. Les runtimes peuvent polyfiller 0.2, mais la documentation officielle signale encore la nécessité de pinner ensemble certaines versions de Wasmtime, `wit-bindgen` et WIT pour 0.3 ([introduction au Component Model](https://component-model.bytecodealliance.org/), [FAQ et statut WASI](https://component-model.bytecodealliance.org/reference/faq.html), [migration 0.2 vers 0.3](https://component-model.bytecodealliance.org/design/migrating-to-p3.html)). En 2026, le choix prudent pour une API publique stable reste donc WIT avec une cible WASI 0.2 étroite, tout en préparant l'async 0.3.

Wasmtime apporte de vraies bornes d'exécution, mais elles doivent être configurées par l'hôte :

- `ResourceLimiter` pour mémoire et tables ;
- taille maximale de stack ;
- **fuel** pour une interruption déterministe mais plus coûteuse ;
- **epochs** pour une interruption plus légère mais non déterministe ;
- timeout externe pour les host calls asynchrones, car fuel et epochs n'interrompent pas à eux seuls une host function bloquée.

La documentation Wasmtime recommande de borner la mémoire même avec les epochs, précise que les epochs résistent à un guest malveillant et explique le compromis fuel/epochs ([configuration et interruption](https://docs.wasmtime.dev/api/wasmtime/struct.Config.html), [exemple d'interruption](https://docs.wasmtime.dev/examples-interrupting-wasm.html)). Son modèle de sécurité isole les mémoires et restreint le filesystem WASI aux capacités accordées ([sécurité Wasmtime](https://docs.wasmtime.dev/security.html)).

Cette isolation ne justifie pas d'exécuter immédiatement du code arbitraire dans le processus principal Quivr. Le runner Wasm devrait lui-même être un service séparé avec :

- un pool d'instances Wasmtime ;
- cache des modules compilés par digest ;
- aucune WASI complète par défaut ;
- seulement des host capabilities Quivr étroites et versionnées ;
- limites CPU, mémoire, taille entrée/sortie, deadline et concurrence par plugin ;
- conteneur Kubernetes durci autour du runtime ;
- redémarrage du runner sans perte de faits durables.

**Extism** peut accélérer cette première implémentation : son manifeste borne les pages mémoire, la taille des réponses HTTP et des variables, et définit les hosts et paths accessibles ([manifeste Extism](https://extism.org/docs/concepts/manifest/)). Son ABI buffer-in/buffer-out et ses PDK rendent l'écriture multi-langage simple ([plugins Extism](https://extism.org/docs/concepts/plug-in/), [mémoire Extism](https://extism.org/docs/concepts/memory/)). Mais Extism est une bibliothèque de plugin embarquée, pas un scheduler distribué ; Quivr doit donc conserver son contrat logique au-dessus, sans exposer directement toutes les conventions Extism comme API moteur.

**Spin** fournit déjà un framework d'applications Wasm événementielles et annonce des cold starts milliseconde ([Spin](https://spinframework.dev/)). **SpinKube** ajoute opérateur, CRDs et shim containerd, package les applications dans un registre OCI et propose HPA ou KEDA ([architecture SpinKube](https://www.spinkube.dev/), [packaging OCI](https://www.spinkube.dev/docs/topics/packaging/), [autoscaling](https://www.spinkube.dev/docs/topics/autoscaling/autoscaling/)). C'est attractif, mais Spin et SpinKube sont CNCF Sandbox : la CNCF décrit ce niveau comme expérimental, susceptible de ruptures et destiné aux innovateurs ([cycle de maturité CNCF](https://contribute.cncf.io/projects/lifecycle/)). Un prototype est pertinent ; une dépendance obligatoire du cœur Quivr ne l'est pas encore.

## Autoscaling : KEDA pour le backlog, Knative pour certains appels HTTP

### KEDA

KEDA est le choix naturel pour les contributions asynchrones : il adapte un Deployment d'après le lag d'un consumer ou crée des Jobs en réponse aux événements. Il sait revenir à zéro lorsqu'aucun message n'attend, puis délègue le scaling `1 → N` au HPA ([scaling des Deployments](https://keda.sh/docs/2.20/concepts/scaling-deployments/)). Un `ScaledJob` crée un Job par événement et convient aux traitements longs isolables ; un Deployment consommateur convient mieux aux messages nombreux et courts, puisqu'il amortit le démarrage et traite plusieurs événements ([scaling des Jobs](https://keda.sh/docs/2.20/concepts/scaling-jobs/)).

KEDA propose des scalers officiels ou communautaires pour Kafka, NATS JetStream, Redis Streams et de nombreuses autres sources ([catalogue des scalers](https://keda.sh/docs/2.20/scalers/)). Pour Kafka, la documentation rappelle que le nombre de consommateurs utiles est borné par les partitions, sauf à accepter des consommateurs inactifs ([scaler Kafka](https://keda.sh/docs/2.20/scalers/apache-kafka/)). KEDA est un projet CNCF Graduated depuis 2023 ([fiche CNCF KEDA](https://www.cncf.io/projects/keda/)).

Recommandation :

- worker asynchrone chaud avec `minReplicaCount >= 1` pour le chemin temps réel prioritaire ;
- scale-to-zero pour backfills, OCR lourd rare, maintenance et plugins peu sollicités ;
- concurrence bornée par plugin et corpus ;
- backlog, ancienneté du plus vieux message et durée de traitement comme métriques ;
- ne pas utiliser un Job par petite dépêche : le coût scheduler/pod serait inutile.

### Knative

Knative Serving ajoute une couche serverless HTTP avec autoscaling à zéro et revisions immuables. Il sait répartir le trafic entre plusieurs Revisions, ce qui est utile pour canary et cutover ([autoscaling](https://knative.dev/docs/serving/autoscaling/), [traffic management](https://knative.dev/docs/serving/traffic-management/)). Knative est CNCF Graduated depuis septembre 2025 ([fiche CNCF Knative](https://www.cncf.io/projects/knative/)).

Il est pertinent pour un grand catalogue de plugins HTTP sporadiques ou lorsque revisions, traffic splitting et scale-to-zero sont déjà souhaités par la plateforme. Il est superflu au MVP si Kubernetes Deployments + KEDA suffisent. Le retrieval synchrone à faible latence ne devrait pas scaler à zéro : Knative expose d'ailleurs des minimums de replicas et un délai de scale-down pour éviter certains cold starts ([bornes de scaling](https://knative.dev/docs/serving/autoscaling/scale-bounds/)).

Knative Eventing ne doit pas remplacer automatiquement le broker durable retenu pour l'ingestion. Les garanties de livraison dépendent de la classe de Broker ; même la documentation prévient que l'`InMemoryChannel` est réservé au développement ([Brokers](https://knative.dev/docs/eventing/brokers/), [avertissement InMemoryChannel](https://knative.dev/docs/getting-started/first-broker/)).

## Transport synchrone : gRPC d'abord, Connect selon les langages

Le protocole de contribution synchrone doit être schema-first, avec unary RPC par défaut et streaming seulement lorsque la taille ou la progressivité le justifie. **gRPC + Protobuf** possède le support linguistique le plus large et des mécanismes standardisés de health checking, retries, flow control, observabilité et graceful shutdown ([langages gRPC](https://grpc.io/docs/languages/), [guides gRPC](https://grpc.io/docs/guides/)). Il reste CNCF Incubating mais est largement adopté ([fiche CNCF gRPC](https://www.cncf.io/projects/grpc/)).

Chaque appel doit fixer une deadline explicite : gRPC n'en applique aucune par défaut et le serveur doit arrêter lui-même le travail créé lorsque l'appel est annulé ([deadlines gRPC](https://grpc.io/docs/guides/deadlines/)). Les retries ne doivent porter que sur des méthodes idempotentes ou munies d'une clé d'idempotence.

**ConnectRPC** utilise les mêmes schémas Protobuf, fonctionne sur HTTP/1.1 ou HTTP/2 et peut parler Connect, gRPC et gRPC-Web selon l'implémentation. Il est plus simple à inspecter avec les outils HTTP et fournit actuellement serveurs/clients Go, Node et Python, ainsi que plusieurs clients mobiles/web ([site Connect](https://connectrpc.com/)). En revanche, le projet n'est encore que CNCF Sandbox ([fiche CNCF Connect RPC](https://www.cncf.io/projects/connect-rpc/)).

Choix recommandé :

- **contrat canonique Protobuf** et interopérabilité gRPC ;
- serveur gRPC si la stack Quivr ou l'écosystème de plugins exige la couverture linguistique maximale ;
- Connect possible lorsque l'implémentation serveur choisie et les langages cibles sont couverts, notamment pour simplifier HTTP et le debug ;
- ne pas exposer deux modèles métier différents : gRPC/Connect ne sont que des transports du même contrat.

L'entrée RPC transporte métadonnées et références de blobs, pas systématiquement des vidéos. Le plugin obtient un jeton court ou une URL présignée uniquement pour les blobs et opérations que sa capability autorise. Cela réduit réseau, mémoire et surface d'accès.

## Transport asynchrone : distinguer enveloppe, documentation et garantie de livraison

**CloudEvents** standardise l'enveloppe et les métadonnées communes d'un événement. Les attributs requis sont `id`, `source`, `specversion` et `type`; la spécification possède des bindings HTTP, Kafka, MQTT, NATS et des formats JSON/Protobuf ([spécification CloudEvents](https://github.com/cloudevents/spec/blob/ce@v1.0.2/cloudevents/spec.md), [formats et bindings publiés](https://github.com/cloudevents/spec)). CloudEvents est CNCF Graduated depuis 2024 ([fiche CNCF CloudEvents](https://www.cncf.io/projects/cloudevents/)).

**AsyncAPI** décrit de façon machine-readable les channels, opérations, messages, schémas et bindings d'une API asynchrone, sans imposer une topologie ni un protocole ([spécification AsyncAPI 3](https://www.asyncapi.com/docs/reference/specification/v3.0.0), [document AsyncAPI](https://www.asyncapi.com/docs/tools/generator/asyncapi-document)).

Ni CloudEvents ni AsyncAPI ne garantissent persistance, ordering, replay, retry ou DLQ. Ces propriétés appartiennent au broker, à la configuration du consumer et au protocole de traitement Quivr. La combinaison saine est :

```text
CloudEvents       = enveloppe interopérable
schéma versionné  = payload métier/capability
AsyncAPI          = catalogue et documentation des canaux
broker            = conservation, partitions, offsets et redelivery
worker Quivr      = idempotence, lease, checkpoint, retry et DLQ
```

Chaque événement remis à un plugin devrait contenir `plugin_id`, `capability`, `contract_version`, `plugin_generation`, `organization_id`, `corpus_id`, `record_id`, `record_version_id`, `attempt` et une clé d'idempotence, en plus des attributs CloudEvents.

## Lifecycle et rolling update sans arrêt global

Kubernetes sait remplacer les pods, mais il ne comprend pas la sémantique « cette tâche appartient à la génération 12 de ce plugin ». Cette petite couche appartient à Quivr ; elle peut rester déclarative sans devenir un orchestrateur.

### Activation d'une génération

1. L'opérateur installe un artefact immuable par digest OCI/Wasm.
2. Le control plane valide manifeste, signature, compatibilité moteur/SDK et permissions.
3. Kubernetes crée la génération candidate avec readiness probe et zéro trafic métier.
4. Le moteur appelle `DescribeCapabilities` et un self-test borné.
5. Si les contrats correspondent, la génération devient `ready`.
6. Le registre de capabilities bascule les nouveaux travaux vers elle.
7. L'ancienne génération passe à `draining` et ne reçoit plus de nouveaux travaux.

### Drain synchrone

Le routeur garde un compteur d'appels en vol par génération. Il retire l'ancienne des endpoints prêts, attend la fin ou la deadline maximale, puis laisse Kubernetes terminer le pod. Les endpoints Kubernetes en terminaison ont `ready=false`, et `PreStop` ainsi que `terminationGracePeriodSeconds` permettent un drainage borné ([terminaison des endpoints](https://kubernetes.io/docs/tutorials/services/pods-and-endpoint-termination-flow/), [lifecycle hooks](https://kubernetes.io/docs/concepts/containers/container-lifecycle-hooks/)). Une deadline RPC reste indispensable : un `PreStop` bloqué finit par être tué à l'expiration de la grace period.

### Drain asynchrone

Le message durable est affecté à une génération au moment du dispatch, pas au moment de sa consommation. Une activation `from now` envoie les nouveaux messages à la nouvelle génération. L'ancienne continue son consumer group jusqu'à ce que ses messages soient acquittés, expirés ou placés en DLQ. Un replay explicite crée de nouveaux travaux ciblant la génération choisie et la fenêtre temporelle demandée.

Cette approche respecte la décision « le travail en vol finit sur l'ancienne génération », même en cas de retry. Elle évite aussi un résultat non reproductible où une tentative initiale utiliserait v1 et son retry v2.

Le control plane n'a pas à créer ou tuer lui-même des processus. Il écrit uniquement l'état désiré et les affectations ; Kubernetes, KEDA et le broker réalisent respectivement pods, scaling et livraison.

## Architecture progressive proposée

### Étape 1 — socle universel, peu risqué

```text
Quivr API / pipeline
  ├─ Capability Registry (contrats + générations + santé)
  ├─ RPC router ── gRPC/Protobuf ── Kubernetes Service ── plugin sync
  └─ durable broker ── consumer group ── plugin async
                                             │
                                           KEDA

Chaque plugin = artefact OCI immuable + manifeste Quivr
Chaque génération = Deployment distinct + ServiceAccount + policies
```

- Plugins officiels/approuvés : conteneurs Kubernetes standards durcis.
- Retrieval : `min replicas >= 1`, deadlines, concurrence, circuit breaker, fail-open/fail-closed déclaré.
- Ingestion/enrichissement : broker durable, at-least-once, idempotence, DLQ, KEDA.
- Rollout : nouvelles générations parallèles, readiness, cutover, drain.
- Développement local OSS : Docker Compose et même protocole RPC ; aucune dépendance Kubernetes dans le SDK du plugin.

### Étape 2 — runner Wasm optionnel

Ajouter un unique type d'adapter `wasm-component` : le control plane planifie l'artefact dans des pods `quivr-wasm-runner`. Le contrat externe demeure identique. À l'intérieur, WIT ou une couche Extism mappe la capability vers Wasmtime.

Ce tier sert d'abord les fonctions pures et courtes. Il doit être validé sur :

- temps d'instanciation chaud/froid et cache de compilation ;
- mémoire réelle par instance et par pool ;
- support des langages prioritaires ;
- taille maximale des payloads ;
- annulation de host calls ;
- absence de fuite entre deux invocations/tenants ;
- comportement à la mise à jour du runtime Wasmtime.

### Étape 3 — profils d'isolation et serverless ciblé

Le manifeste choisit une classe de confiance, pas une technologie précise :

```yaml
runtime:
  artifact: oci://registry/plugin@sha256:...
  isolation: trusted | sandboxed | vm
  execution: async | rpc
  resources:
    cpu: 500m
    memory: 512Mi
  network:
    egress: [api.example.com:443]
```

La politique d'installation traduit ensuite :

- `trusted` → runtime OCI standard durci ;
- `sandboxed` → gVisor si compatible ;
- `vm` → Kata sur un node pool dédié ;
- plugin léger compatible → runner Wasm, indépendamment de ces labels.

Knative peut être ajouté si le nombre de plugins HTTP sporadiques rend son revisioning et son scale-to-zero rentables. SpinKube peut être testé si l'exploitation de nombreux composants WASI devient un besoin dominant. Firecracker brut ne rentre pas dans cette trajectoire.

## Décisions proposées

1. **Le plugin public est un protocole, pas un processus.** Son manifeste et ses contrats ne citent ni Kubernetes, ni Extism, ni Knative.
2. **OCI est le format universel de première livraison.** Il couvre tous les langages et dépendances ; Wasm est une optimisation/capability tier.
3. **Aucun code tiers dans le processus principal.** Même le Wasm tiers tourne dans un runner séparé ; une panne ne doit pas emporter l'API Quivr.
4. **KEDA pour l'asynchrone ; replicas chaudes pour le synchrone.** Scale-to-zero seulement lorsque la cold-start latency est acceptable.
5. **gVisor avant Kata pour la densité**, Kata lorsque le threat model exige réellement une VM. Mesurer compatibilité et overhead sur les workloads réels.
6. **Pas de Firecracker direct.** Le VMM est excellent mais les couches manquantes sont précisément celles que Quivr ne doit pas réinventer.
7. **CloudEvents + AsyncAPI ne sont pas le broker.** Ils standardisent enveloppes et contrats ; les garanties restent explicites dans Quivr et le broker.
8. **Générations immuables et drainées.** Une activation ne redémarre pas le moteur et ne change jamais la génération d'un travail déjà affecté.

## Benchmarks nécessaires avant de figer les profils

Les documentations ne fournissent pas de chiffres comparables pour un pipeline Quivr. Un benchmark court doit mesurer sur l'infrastructure cible :

- cold start artefact présent et artefact absent du nœud ;
- RSS idle et RSS sous charge ;
- débit et p95 pour payloads 4 KiB, 1 MiB et référence de blob ;
- coût CPU d'un plugin syscall-heavy sous `runc` et gVisor ;
- densité maximale de pods gVisor/Kata/Wasm sans contention ;
- temps de scale-from-zero KEDA/Knative, incluant scheduling et image pull ;
- durée d'un rollout avec drain et messages retryés ;
- effet du cache Wasmtime et des limites fuel/epoch ;
- saturation réseau lorsqu'un plugin récupère de gros blobs.

Le résultat doit piloter les valeurs par défaut de requests/limits, concurrence et min replicas. Il ne doit pas modifier la séparation fondamentale entre contrat, exécution et orchestration.
