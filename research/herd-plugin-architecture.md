# Herdr : modèle de plugins et enseignements pour l’ingestion de Quivr V2

_Recherche effectuée le 3 septembre 2026. Analyse du dépôt officiel Herdr au commit [`548d4c02d0a6ee199d8e65fb5fcc521fa70187d4`](https://github.com/GroepOnline/herdr/tree/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4). Toutes les affirmations techniques ci-dessous reposent sur la documentation ou le code source officiels._

## Conclusion courte

Dans le contexte « DeepSeek Harness, Herd ou Pi », le projet pertinent est très probablement **[Herdr](https://github.com/GroepOnline/herdr)**, avec un `r` final. Herdr est un gestionnaire de workspaces terminal pour agents de code, prend notamment Pi en charge, et possède une surface de plugins explicitement documentée. Le projet **[Herd de NickGuAI](https://github.com/NickGuAI/Herd)** est un autre produit : un méta-harness web qui orchestre des flottes, des missions, de la mémoire et des approbations au-dessus de plusieurs CLIs. Son README ne présente pas une API publique de plugins comparable. **[pi-herd](https://github.com/ribbons-digital/pi-herd)** est pour sa part un orchestrateur Pi qui s’intègre à Herdr ; ce n’est pas le runtime de plugins lui-même.

L’idée forte de Herdr n’est pas de créer une classe par catégorie métier. Il définit **un seul format de package**, puis plusieurs **types d’entrypoints déclaratifs** dans son manifeste : `startup`, `actions`, `events`, `panes` et `link_handlers`, plus des commandes de `build`. Un même plugin peut en exposer plusieurs. L’implémentation reste un exécutable arbitraire lancé hors processus. ([documentation Plugins, lignes 6–34](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/website/src/content/docs/plugins.mdx#L6-L34), [exemple de manifeste, lignes 55–100](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/website/src/content/docs/plugins.mdx#L55-L100))

Pour Quivr V2, cela suggère de remplacer la taxonomie rigide proposée jusque-là (`SourcePlugin`, `ProcessorPlugin`, `PolicyPlugin`, `SubscriberPlugin`) par deux niveaux distincts :

1. un **package plugin** unique, décrit par un manifeste ;
2. des **capacités et handlers typés** fournis par ce package, chacun avec ses propres garanties.

En revanche, Herdr ne doit pas être copié sur la durabilité : ses hooks d’événements sont des commandes opportunistes, sans file durable, offset, retry, DLQ ni replay. C’est acceptable pour une UI terminal ; cela ne l’est pas pour des alertes éditoriales et une ingestion d’actualité continue.

## Identification : les projets homonymes

### Candidat retenu : GroepOnline/Herdr

Herdr se définit comme un gestionnaire natif de workspaces terminal doté d’un serveur de session, d’une CLI/socket API et de plugins. Les processus continuent lorsque le client se détache. Sa liste officielle d’agents supportés inclut Pi, dont l’état est obtenu via des hooks de cycle de vie lorsqu’ils sont installés ([README, lignes 21–43](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/README.md#L21-L43), [documentation Agents, lignes 63–104](https://herdr.dev/docs/agents/)).

C’est le seul candidat trouvé qui réunisse les trois indices de la demande : harness d’agents, relation explicite avec Pi et architecture publique de plugins/hooks.

### Candidats écartés

| Projet | Nature réelle | Pourquoi ce n’est probablement pas la référence |
|---|---|---|
| [NickGuAI/Herd](https://github.com/NickGuAI/Herd) | Méta-harness de flottes : missions, workers, mémoire, approbations et routage vers Codex/Claude/Gemini/OpenCode. | Sa surface documentée est celle d’un control plane et de packages de commanders, pas d’un runtime public d’entrypoints/plugins analogue à DeepSeek ou Pi. ([README officiel](https://github.com/NickGuAI/Herd/blob/fbda6618c7b53524faddbed070e4fbc14970e412/README.md)) |
| [ribbons-digital/pi-herd](https://github.com/ribbons-digital/pi-herd) | Orchestration de rôles Pi dans des panes et worktrees Herdr. | Il consomme les extensions Pi et les plugins Herdr ; ce n’est pas le noyau qui définit leur cycle de vie. ([README officiel](https://github.com/ribbons-digital/pi-herd)) |
| [andrea-tomassi/pi-open-agents](https://github.com/andrea-tomassi/pi-open-agents) | Plugin Pi de gestion d’agents et sous-agents. | Il illustre l’écosystème de packages Pi et recommande Herdr pour la coordination, mais ne définit pas le modèle de plugins de Herdr. ([README officiel](https://github.com/andrea-tomassi/pi-open-agents/blob/main/README.md)) |

## Ce que Herdr a réellement construit

### 1. Un format de package, plusieurs entrypoints

Le contrat est un fichier `herdr-plugin.toml`. Les champs racine obligatoires sont `id`, `name`, `version` et `min_herdr_version`. Le manifeste peut ensuite déclarer :

- des commandes de `build` exécutées à l’installation ;
- des hooks `startup` ;
- des `actions` invoquées par palette, raccourci, CLI ou socket ;
- des hooks `events` ;
- des `panes` terminal ;
- des `link_handlers` qui routent certains liens vers une action.

Il n’existe donc pas de classe globale « notification plugin » ou « UI plugin ». La **catégorie est portée par chaque contribution** du package. Un plugin peut combiner plusieurs contributions. L’enregistrement dynamique d’actions pendant l’exécution n’est pas supporté en v1 : les entrypoints sont déclarés à l’avance dans le manifeste ([documentation, lignes 18–34](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/website/src/content/docs/plugins.mdx#L18-L34), [schéma Rust des entrypoints](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/api/schema/plugins.rs#L199-L270)).

### 2. Une frontière hors processus, sans SDK obligatoire

Herdr lance des commandes `argv` hors processus. Un plugin peut être écrit en Bash, JavaScript, Python, Rust, Go, Lua ou tout autre langage exécutable. Le host injecte le contexte, le chemin de la socket, les répertoires de configuration et d’état, puis le plugin rappelle Herdr via sa CLI ou sa socket. Il n’y a pas de SDK dédié ; la CLI complète constitue l’API du plugin ([documentation, lignes 6–30 et 120–168](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/website/src/content/docs/plugins.mdx#L6-L30)).

Cette isolation est une **frontière de panne et de langage**, mais pas une frontière de sécurité. Le code tourne comme l’utilisateur, reçoit son environnement et peut appeler toute la CLI. Herdr dit explicitement qu’il ne sandboxe ni ne vérifie les plugins ([Trust and security, lignes 36–53](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/website/src/content/docs/plugins.mdx#L36-L53)).

### 3. Dépendances et compatibilité minimales

Le manifeste possède deux contrôles de compatibilité :

- `min_herdr_version`, vérifié lors de l’installation ou du link ;
- `platforms`, déclarable globalement ou par entrypoint.

Les dépendances applicatives appartiennent au plugin. Les commandes de build peuvent préparer son code ; si le build échoue, l’installation est annulée. Herdr n’installe pas les toolchains manquantes. Il n’existe ni graphe de capacités fournies/requises, ni résolution entre plugins comparable à `inject` dans Cordis/DeepSeek ([documentation, lignes 102–122 et 219–232](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/website/src/content/docs/plugins.mdx#L102-L122)).

### 4. Installation et reload sans arrêt global

Un plugin GitHub est cloné dans un répertoire temporaire, prévisualisé, construit, reparsé pour vérifier que le build n’a pas changé le manifeste, puis déplacé dans un checkout géré et enregistré. Le remplacement garde un backup et effectue un rollback si l’enregistrement échoue ([implémentation de `plugin install`](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/cli/plugin.rs#L154-L260)).

L’installation, le link, l’activation et la désactivation sont globaux à l’utilisateur et visibles dans les sessions sans redémarrage du serveur. Le registre est verrouillé, écrit via un fichier temporaire puis renommé, et les manifestes sont relus depuis le disque avant les événements et les invocations ([documentation, lignes 170–212](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/website/src/content/docs/plugins.mdx#L170-L212), [registre persistant](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/persist/plugin_registry.rs#L19-L132), [rafraîchissement runtime](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/app/api/plugins/mod.rs#L38-L54)).

Nuance importante : les hooks `startup` ne sont pas exécutés lorsqu’un plugin est lié ou activé. Ils s’exécutent après restauration d’une session ou lors d’un live handoff de serveur. Ce sont des commandes one-shot asynchrones, pas des daemons supervisés ; leur échec n’arrête pas Herdr ([documentation Startup hooks, lignes 234–250](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/website/src/content/docs/plugins.mdx#L234-L250)).

### 5. Travail en vol : l’activation change le futur, pas le passé

Une action ou un hook lance un nouveau processus sur un thread séparé, puis attend sa terminaison. Désactiver ou unlinker le plugin empêche les **nouveaux** déclenchements, mais le code de désactivation ne conserve pas de handle pour annuler les commandes déjà lancées. Le code d’unlink est même explicite pour les panes : leurs enregistrements sont retirés mais les panes continuent de tourner ([runtime des commandes, lignes 82–180](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/app/api/plugins/runtime.rs#L82-L180), [unlink, lignes 293–315](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/app/api/plugins/mod.rs#L293-L315), [enable/disable, lignes 903–934](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/app/api/plugins/mod.rs#L903-L934)).

Il n’y a donc **pas de stop-the-world** quand un plugin est ajouté. Herdr charge les nouveaux manifestes pour les prochaines invocations. Le travail déjà démarré continue avec l’exécutable et l’environnement capturés au lancement.

Attention : lors d’une réinstallation, le checkout précédent est déplacé puis supprimé après remplacement. Le runtime ne versionne pas explicitement le root utilisé par les commandes en vol. Herdr ne constitue donc pas une preuve qu’un rolling update de traitements longs est sûr ; il illustre seulement un reload de workflows courts.

### 6. Événements et erreurs : fire-and-forget borné

Avant de lancer des hooks, Herdr recharge le registre, sélectionne les plugins actifs dont le manifeste correspond à l’événement, puis démarre chaque commande. Les événements à fort volume, comme les changements de sortie de pane, sont volontairement exclus de la liste publique de hooks ([sélection des hooks et lancement](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/app/api/plugins/runtime.rs#L218-L266), [liste des événements exposés](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/api/schema/events.rs#L294-L338)).

Le runtime limite à 32 les commandes simultanées. Au-delà, le déclenchement échoue immédiatement et est seulement journalisé. Les sorties sont plafonnées à 64 Kio et les logs à 200 entrées en mémoire. Un exit non nul ou une erreur de spawn marque la commande en échec ; une chaîne d’actions ne poursuit ses étapes suivantes qu’après succès ([limites du runtime](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/app/api/plugins/runtime.rs#L11-L13), [refus à saturation](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/app/api/plugins/runtime.rs#L82-L102), [traitement de terminaison](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/app/api.rs#L186-L224)).

Il n’existe dans cette surface ni queue durable, ni accusé par événement, ni retry, ni DLQ, ni offset de consommateur, ni replay. Le plugin possède lui-même son état et sa base éventuelle ; Herdr ne fournit pas de storage API de plugin ([Storage, lignes 358–361](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/website/src/content/docs/plugins.mdx#L358-L361)).

## Réponse à Q18 : catégories de plugins ou catégories d’entrypoints ?

Herdr donne un contre-exemple utile à la proposition de quatre interfaces mutuellement exclusives. Il ne catégorise pas le **plugin entier** ; il catégorise ce que le plugin **contribue au host**.

Pour Quivr V2, une transposition plus fidèle et plus flexible serait :

```yaml
id: news.editorial-alerts
version: 1.2.0
requires:
  core_api: ">=1.0 <2"
  capabilities:
    - signal.metadata.read
    - alert.delivery.write

contributes:
  subscriptions:
    - event: signal.searchable.v1
      handler: ./bin/evaluate-alert
      delivery: durable
      blocking: false
  commands:
    - id: test-rule
      handler: ./bin/test-rule
  enrichers:
    - id: editorial-document-type
      input: signal.normalized.v1
      output: enrichment.document-type.v1
```

Le **package** est l’unité d’installation, de version, de permission et de déploiement. Les **contributions** sont les unités sémantiques : connector, decoder/parser, enricher, policy, projector/indexer, retriever, subscriber, command ou éventuelle UI. Un package peut fournir plusieurs contributions, par exemple un connecteur et son parseur, ou un classifieur et une commande de test.

Il faut cependant aller plus loin que Herdr : chaque contribution doit déclarer si elle est bloquante, son budget de temps, son modèle de livraison, ses entrées/sorties versionnées et les permissions minimales. `SubscriberPlugin` n’est donc pas une classe de package ; `subscription` est un type de contribution avec des garanties propres.

## Réponse à Q20 : faut-il arrêter tout le système quand on ajoute un plugin ?

**Non, pas par défaut.** Herdr ne le fait pas, et pour une ingestion temps réel un arrêt global créerait précisément le couplage que le système de plugins doit éviter.

Le comportement recommandé est un déploiement en générations :

```text
1. fetch/build/scan/validate dans une zone de staging
2. enregistrer le plugin désactivé
3. vérifier compatibilité, permissions et healthcheck
4. choisir sa politique de départ : from-now | replay(offset/date) | backfill corpus
5. activer atomiquement une nouvelle génération de configuration
6. router les nouveaux signaux vers cette génération
7. laisser finir le travail en vol de l’ancienne génération
8. retirer l’ancienne génération quand son compteur atteint zéro
```

Chaque unité de travail doit garder le `pipeline_generation` et les versions de contributions avec lesquelles elle a commencé. Ainsi, un signal n’est pas traité à moitié par l’ancien graphe et à moitié par le nouveau.

Pour un plugin d’alerte ajouté à chaud, le défaut prudent est `from-now`. Un replay historique doit être une action explicite, avec fenêtre et simulation, car rejouer un million de signaux pourrait envoyer un million d’alertes. Pour un nouvel indexeur ou enrichisseur, un backfill parallèle à priorité basse est cohérent et ne doit pas ralentir la voie temps réel.

Une pause reste possible, mais son rayon doit être minimal : pause du consumer concerné, d’une source, d’un tenant ou d’une partition pendant un basculement incompatible. Un **arrêt global** ne se justifie que pour une migration d’invariant du noyau ou de schéma canonique impossible à rendre rétrocompatible — ce n’est alors plus l’installation normale d’un plugin.

## Ce qu’il faut reprendre et ce qu’il faut renforcer

| Mécanisme Herdr | À reprendre pour Quivr V2 | À renforcer pour une veille d’actualité |
|---|---|---|
| Un package manifesté avec plusieurs entrypoints | Séparer unité d’installation et types de contributions. | Ajouter schémas I/O, garanties de livraison, permissions, budgets, ressources et compatibilité des capacités. |
| Commandes hors processus et langage libre | Garder un protocole neutre ; permettre workers/conteneurs de langages différents. | Isolation réelle par identité de workload, réseau, secrets, CPU/mémoire et accès au contenu. |
| Install en staging puis enregistrement | Valider avant activation et rendre le basculement atomique. | Conserver simultanément ancienne et nouvelle versions jusqu’au drain ; signer les artifacts et définir rollback. |
| Enable/disable à chaud | Les futurs événements utilisent immédiatement la nouvelle config. | Snapshot de génération par travail, drain, cancellation explicite et règles pour le backlog. |
| Hooks déclaratifs | Une alerte peut s’abonner à un événement métier sans modifier l’ingestion. | Broker durable, offsets par abonnement, retry, DLQ, backpressure et idempotence. |
| `min_herdr_version` et plateformes | Vérifier la compatibilité avant montage. | SemVer des contrats, schémas versionnés, coexistence de versions et tests de conformité. |
| Config/state séparés du checkout | Préserver l’état lors d’une mise à jour du code. | Propriété claire des migrations, stockage durable géré et sauvegardé, rétention et suppression. |
| Échec d’un hook non fatal au host | Un subscriber d’alerte ne bloque pas l’apparition du signal. | SLO par plugin, quarantaine/circuit breaker, visibilité opérationnelle et rattrapage garanti. |

## Questions restantes à trancher

1. Les auteurs tiers publient-ils seulement des conteneurs/workers, ou aussi du code chargé dans des workers Quivr approuvés ?
2. Quelles contributions sont autorisées sur le chemin bloquant : policies seulement, parsers aussi, enrichers jamais ?
3. Quel est le comportement par défaut à l’activation : `from-now` selon la catégorie, ou choix toujours obligatoire ?
4. Quelle unité reçoit une génération immuable : publication brute, signal normalisé, job ou événement de bus ?
5. Une mise à jour peut-elle faire coexister deux versions d’une même capability pour différents tenants/sources ?
6. Qui gère les migrations de l’état privé d’un plugin et comment le rollback est-il prouvé ?
7. Quelles permissions sont réellement médiées par le host, plutôt que simplement déclarées dans le manifeste ?

## Sources primaires principales

- [Dépôt officiel GroepOnline/Herdr](https://github.com/GroepOnline/herdr)
- [Documentation officielle des plugins, version analysée](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/website/src/content/docs/plugins.mdx)
- [Runtime des commandes et hooks](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/app/api/plugins/runtime.rs)
- [Schéma du manifeste](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/api/schema/plugins.rs)
- [Installation et remplacement](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/cli/plugin.rs)
- [Registre persistant](https://github.com/GroepOnline/herdr/blob/548d4c02d0a6ee199d8e65fb5fcc521fa70187d4/src/persist/plugin_registry.rs)
- [NickGuAI/Herd, candidat homonyme](https://github.com/NickGuAI/Herd)
- [pi-herd, intégration Pi × Herdr](https://github.com/ribbons-digital/pi-herd)
