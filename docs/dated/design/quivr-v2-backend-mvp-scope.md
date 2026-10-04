# Feature Spec: Quivr V2 Backend MVP

Date: 2026-09-03 (last revised 2026-09-28)

Status: historical design record, frozen on 2026-09-29. Current behaviour is defined by the code, the contracts and the living documentation.

## Summary

Le projet construit un backend open source d’ingestion et de retrieval multimodal pour Quivr. Il permet d’ingérer en continu du texte, des images, de l’audio et de la vidéo, de rendre rapidement les contenus recherchables, puis de les enrichir progressivement au moyen de plugins.

Le premier usage métier est la veille d’une organisation d’information. Cette organisation construit son propre démonstrateur et consomme les APIs de Quivr V2. Ses notions propres sont apportées par des plugins privés plutôt qu’intégrées au cœur générique.

Le MVP vise environ 90 % de la valeur produit : une chaîne complète, multimodale et extensible, agréable à développer et exploitable sur des volumes significatifs. Le scale extrême et le durcissement d’infrastructure ne sont pas des prérequis à la première livraison.

Le MVP n’est pas un prototype jetable. Il constitue le socle du produit : les contrats publics, le modèle canonique et les invariants de traitement sont conçus pour durer. En revanche, la topologie de production, les optimisations de stockage et les mécanismes de scale ne sont ajoutés qu’après mesure.

### Principes de cadrage

1. **Valeur avant exhaustivité** : une chaîne complète de veille vaut davantage qu’un catalogue incomplet de briques techniques.
2. **Disponibilité progressive** : un contenu utile apparaît rapidement, puis gagne en richesse.
3. **Cœur générique, verticales par plugins** : aucun concept propre à un client n’est requis pour utiliser le backend.
4. **Vérité durable, projections remplaçables** : changer de moteur ou de modèle ne doit pas remettre en cause les contenus.
5. **Une responsabilité par composant** : Temporal orchestre, PostgreSQL catalogue, S3 conserve et Weaviate sert le retrieval.
6. **DevX comme contrainte d’architecture** : les détails d’infrastructure restent derrière les APIs et SDK Quivr.
7. **Complexité déclenchée par la preuve** : aucune brique distribuée supplémentaire n’entre dans le socle sans besoin observé.

## Problem

Quivr doit ingérer des contenus hétérogènes provenant de nombreuses sources, préserver leur provenance, les traiter de manière fiable et les rendre disponibles pour la recherche et la veille sans attendre la fin de tous les enrichissements.

Une architecture spécifique à un premier client limiterait la réutilisation du produit. À l’inverse, une plateforme entièrement abstraite ou distribuée dès le départ ralentirait la livraison et augmenterait fortement son coût de maintenance.

Le projet doit donc trouver un équilibre : fournir un noyau générique stable et quelques contrats de plugins puissants, tout en s’appuyant sur des technologies existantes et une topologie initiale simple.

Les difficultés structurantes sont les suivantes :

- les sources n’utilisent ni les mêmes formats, ni les mêmes identifiants, ni les mêmes rythmes ;
- un record peut évoluer, être corrigé ou retiré après sa première ingestion ;
- un média lourd peut demander plusieurs minutes de traitement alors que ses métadonnées sont déjà utiles ;
- les enrichissements et modèles changent plus vite que les contenus sources ;
- les auteurs de plugins doivent pouvoir étendre le système sans dépendre de ses tables ou moteurs internes ;
- le dispositif de veille exige fraîcheur et continuité alors que les backfills et rebuilds consomment beaucoup de ressources ;
- la profondeur historique et les politiques de rétention ne doivent pas imposer de garder chaque projection coûteuse active ;
- la première livraison doit rester maintenable par une petite équipe.

## Goals

- Livrer rapidement un backend utile à un dispositif de veille d’actualité.
- Ingérer du texte, des images, de l’audio et de la vidéo avec une disponibilité progressive.
- Rendre le contenu minimalement recherchable sans attendre les enrichissements lourds.
- Fournir une recherche lexicale, vectorielle, hybride et cross-modale.
- Permettre à des plugins d’ajouter connecteurs, normalisation, enrichissements, retrieval, règles de veille et deliveries.
- Garantir identité stable, idempotence, versions immuables, provenance et retrait des contenus.
- Proposer une excellente expérience locale pour les développeurs et auteurs de plugins.
- Utiliser uniquement des briques obligatoires sous licences permissives.
- Conserver les données canoniques indépendamment des projections de recherche.
- Préserver une trajectoire de passage à l’échelle sans implémenter prématurément l’infrastructure maximale.

### Mesures de succès du MVP

- Un intégrateur peut démarrer le backend localement, ingérer un corpus et rechercher son contenu sans connaître l’architecture interne.
- Un auteur peut créer un plugin externe, le tester, l’activer et observer ses résultats avec les SDK documentés.
- Une dépêche textuelle devient recherchable rapidement même si ses enrichissements multimodaux sont encore en cours.
- Une image ou un segment vidéo peut être retrouvé par une requête textuelle avec sa provenance.
- Une recherche sauvegardée peut produire un match et une delivery sans doublon logique.
- Une correction, un retrait ou une nouvelle génération de plugin peut être traité sans arrêter toute la plateforme.
- Une projection détruite peut être reconstruite depuis les données canoniques.
- Le démonstrateur du premier client peut utiliser uniquement les APIs publiques pour son intégration.

## Non-Goals

- Construire le démonstrateur ou les interfaces utilisateur du client.
- Concevoir un moteur généraliste de transactions métier ou d’ETL.
- Fournir une marketplace de plugins dans le MVP.
- Considérer les plugins installés comme du code hostile.
- Imposer signatures, micro-VMs, egress policies ou audit exhaustif.
- Implémenter quotas, billing ou fonctions SaaS.
- Ajouter Kafka ou NATS avant qu’un besoin mesuré de pub/sub indépendant ne l’exige.
- Maintenir plusieurs moteurs vectoriels officiellement supportés.
- Garantir dès le MVP le scale horizontal maximal sur plus de 100 millions d’unités.
- Garder toutes les générations d’embeddings actives indéfiniment.
- Garantir une recherche sémantique instantanée sur tout contenu placé en stockage froid.
- Fournir immédiatement tous les connecteurs, modèles ou formats imaginables.
- Promettre une livraison exactement une fois à une API externe ; les effets externes restent idempotents et au moins une fois.
- Exposer PostgreSQL, Temporal, S3 ou Weaviate comme partie du contrat public.
- Exécuter du code de plugin arbitraire dans le processus principal.
- Construire un data lake analytique, une plateforme de stream processing ou un knowledge graph généraliste.
- Optimiser chaque opération pour le stockage froid dans la première tranche.

## Users And Use Cases

**Développeur intégrateur**

- Ingère des contenus via HTTP ou un SDK.
- Suit leur passage de l’état accepté à l’état recherchable.
- Interroge les contenus et consomme un flux de changements.
- Intègre les résultats dans un démonstrateur ou un produit tiers.

**Auteur de plugin**

- Crée un connecteur, normalizer, enricher, retriever, reranker, subscription ou delivery.
- Développe et teste le plugin localement sans connaître Temporal ou Weaviate.
- Déclare ses capacités, dépendances, configuration et besoins dans un manifeste versionné.
- Publie un artefact OCI ou connecte une API distante.

**Opérateur d’une installation**

- Installe, configure, active, met à jour et retire des plugins auxquels il fait confiance.
- Suit les ingestions, erreurs, backfills, rebuilds et opérations de rétention.
- Choisit les politiques de stockage et de durcissement adaptées à son environnement.

**Organisation cliente et son démonstrateur**

- Ingèrent des dépêches et contenus multimodaux au fil de l’eau.
- Recherchent les versions courantes et les archives autorisées.
- Sauvegardent des recherches et reçoivent des matches ou alertes.
- Ajoutent des comportements propres à l’organisation au moyen de plugins privés.

### Répartition des responsabilités

| Acteur | Responsabilités dans le MVP | Hors de sa responsabilité |
| --- | --- | --- |
| Cœur Quivr | Contrats, identité, orchestration, états, retrieval, lifecycle et APIs | Sémantique éditoriale propre à un client |
| Plugins génériques | Formats, modèles, enrichissements et canaux réutilisables | Contournement des invariants du cœur |
| Plugins du client | NewsML-G2, mappings éditoriaux et comportements propres au dispositif | Modification ou fork du cœur Quivr |
| Démonstrateur du client | Expérience utilisateur et consommation des APIs | Orchestration et stockage internes |
| Installateur | Choix et confiance accordée aux plugins, secrets et déploiement | Stabilité des contrats publiés par Quivr |
| Opérateur | Capacité, sauvegardes, mises à jour et diagnostic d’infrastructure | Développement des fonctionnalités du démonstrateur |

## Proposed Behavior

### Ingestion

1. Le client transmet un petit contenu inline ou envoie un blob lourd directement vers un stockage S3-compatible.
2. Il soumet une enveloppe d’ingestion contenant identité externe, corpus, type, checksum et références de blobs.
3. PostgreSQL commite atomiquement l’input rejouable, l’Ingestion Receipt et l’intent outbox qui démarrera le traitement.
4. Le backend retourne immédiatement le Receipt, puis le dispatcher démarre un workflow Temporal déterministe.
5. Le workflow normalise le contenu, persiste sa version et construit une projection minimale.
6. Le record devient recherchable.
7. Les enrichissements optionnels se poursuivent dans des workflows séparés et mettent à jour la projection.

Les répétitions d’une même requête sont idempotentes. Une nouvelle version ne modifie jamais une version antérieure en place.

#### Chemins d’entrée

| Chemin | Usage | Comportement |
| --- | --- | --- |
| Contenu inline | Texte et petits manifests | Une requête crée le reçu et référence le payload validé. |
| Upload direct | Images, audio, vidéo et archives | Le client obtient une session, charge vers S3, puis confirme le blob par checksum. |
| Batch manifest | Imports et migrations | Chaque entrée possède son propre résultat ; le batch n’est pas une transaction globale. |
| Connector plugin | Polling ou flux métier | Le connecteur produit les mêmes commandes d’ingestion que l’API publique. |

Le point de commit visible par le client est l’acceptation PostgreSQL du travail, pas le démarrage effectif de Temporal ni la fin de l’indexation. Un reçu expose au minimum `receipt_id`, `record_key`, son état `pending` ou `resolved`, son outcome une fois résolu, les éventuelles erreurs et des liens vers le Record Version et sa disponibilité. Une ingestion ordinaire n’est pas une Operation administrative. Si la connexion HTTP tombe après ce commit, le client peut répéter la même requête avec sa clé d’idempotence.

Le chemin critique minimal reste volontairement court :

~~~text
acceptation → identité/version → normalisation minimale → catalogue → projection minimale → searchable
                                                        └──────────► enrichissements asynchrones
~~~

Les corrections créent une nouvelle Record Version et déclarent la version remplacée. Les retraits créent un tombstone prioritaire. Une arrivée tardive ne peut ni réactiver une version retirée ni publier une annotation comme si elle était courante.

### Multimodal Processing

- Le contenu brut reste disponible selon sa politique de rétention.
- Un normalizer produit un Record Version Manifest générique et validé.
- Le texte normalisé est distinct de ses segmentations destinées au retrieval.
- Une image peut produire métadonnées, thumbnails, OCR, description et embeddings.
- Une vidéo peut produire proxy, transcription, plans, keyframes, segments temporels et embeddings.
- Les médias deviennent disponibles progressivement.
- Chaque dérivation conserve ses inputs, checksums, plugin, modèle, paramètres et génération.

#### Niveaux de disponibilité

| Concern | Promesse | Exemples de données disponibles |
| --- | --- | --- |
| Acceptation durable | Travail durablement pris en charge | Receipt `pending`, input rejouable et intent de traitement |
| Version `materialized` | Version canonique persistée | Manifest, métadonnées, texte normalisé ou description minimale |
| Version `retrieval_ready` | Baseline minimale publiée | Champs lexicaux, filtres, extrait ou vignette, droits projetés |
| Dérivations optionnelles | Enrichissements indépendants | OCR, transcription, captions, keyframes, embeddings, relations |

Il n’existe pas d’état global terminal `enriched` : chaque dérivation expose son propre état. Un Record peut donc rester recherchable avec une transcription en cours et un embedding image en erreur retryable.

#### Politique par modalité

| Modalité | Chemin minimal | Enrichissements ciblés dans le MVP |
| --- | --- | --- |
| Texte | Normalisation, langue, segmentation et index lexical | Embedding, entités ou résumé par plugin |
| Image | Métadonnées, validation, vignette et texte fourni | OCR, caption et embedding image/texte |
| Audio | Métadonnées et proxy si nécessaire | Transcription horodatée, segmentation et embeddings |
| Vidéo | Métadonnées, proxy et manifeste temporel | Audio/transcription, plans, keyframes, captions et embeddings de segments |

Le MVP n’indexe pas chaque frame. Il indexe des unités sémantiques bornées — document, page, plan, keyframe ou segment temporel — configurées par profil de traitement.

### Retrieval

- Le backend propose les modes lexical, semantic et hybrid, avec hybrid comme défaut.
- Une requête peut être textuelle ou référencer un média.
- Le retrieval opère sur des parts ou segments, puis remonte vers le record et son contexte.
- Plusieurs vector spaces peuvent coexister pour un même contenu.
- Le pipeline peut réécrire la requête, générer des candidats, fusionner, filtrer, reranker, enrichir et hydrater.
- Les contrôles d’accès restent dans le cœur, avant et après les contributions pluginifiables.
- Le résultat contient les parties correspondantes, extraits, timecodes, provenance et raisons principales du match.
- Les scores bruts du moteur ne constituent pas un contrat public stable.

#### Pipeline de requête

~~~text
requête publique
   → validation + scope d’accès
   → Query Rewriters
   → Candidate Retrievers (lexical / dense / cross-modal)
   → fusion et déduplication
   → filtre d’accès cœur
   → Rerankers optionnels
   → Result Enrichers
   → hydratation canonique + filtre final
   → réponse paginée
~~~

Les plugins peuvent contribuer aux étapes déclarées, mais ne peuvent pas court-circuiter les filtres d’accès, l’hydratation canonique ni les limites techniques de la requête.

Trois profils initiaux rendent les compromis explicites :

- `fast` privilégie lexical, filtres et faible latence ;
- `balanced` combine lexical et dense, puis applique un reranking borné ;
- `deep` autorise davantage de candidats et d’enrichissements, avec une latence supérieure.

Les profils sont des configurations versionnées, pas des APIs distinctes. Les résultats indiquent leur fraîcheur, les modalités effectivement interrogées et les enrichissements encore absents. Un document fraîchement ingéré reste éligible lexicalement tant que son embedding n’est pas prêt.

### Continuous Retrieval

- Une Saved Query conserve une requête versionnée.
- Une Subscription évalue cette requête sur les nouvelles versions éligibles.
- Un Match représente une correspondance durable.
- Une Delivery représente séparément la tentative d’effet externe.
- Les notifications sont idempotentes par subscription et record version.
- Une correction pertinente crée un nouveau match lié au précédent ; un retrait peut produire une mise à jour dédiée.

L’évaluation est pilotée par les changements, pas par un rescan permanent du corpus :

~~~text
Record Version searchable → Change Event → subscriptions candidates
                    → évaluation bornée → Match durable → Delivery(s)
~~~

Une Subscription conserve son scope, son profil de retrieval, sa requête sauvegardée et sa politique de delivery. Le Match capture la version de ces éléments afin qu’une alerte passée reste explicable après modification de la requête. Une delivery en échec est retentée indépendamment de l’évaluation ; elle ne recrée pas le Match.

### Plugin Lifecycle

- Un Plugin Package contient un manifeste, ses schémas et les références de ses artefacts.
- Un plugin peut fournir plusieurs contributions de types différents.
- Les dépendances ciblent des capabilities versionnées plutôt que des plugins nommés.
- Une activation résout et fige un Pipeline Plan lié à une Plugin Generation.
- Une nouvelle génération devient candidate, est validée, puis reçoit les nouveaux travaux.
- L’ancienne génération termine ses travaux en cours avant d’être désactivée.
- L’activation s’applique par défaut aux nouveaux contenus ; un backfill temporel est explicite.
- La désinstallation ne purge jamais automatiquement les résultats produits.

#### Contributions du MVP

| Contribution | Responsabilité |
| --- | --- |
| `connector` | Découvrir ou recevoir des contenus et émettre des commandes d’ingestion |
| `normalizer` | Convertir un format source vers le manifest canonique |
| `validator` | Appliquer des règles de validité métier ou technique |
| `enricher` | Produire annotations, relations, dérivés ou embeddings |
| `policy` | Proposer une classification ou une décision non sécuritaire |
| `projector` | Produire des champs supplémentaires pour une projection déclarée |
| `query_rewriter` | Enrichir ou transformer une requête sans changer son scope d’accès |
| `retriever` | Générer un ensemble borné de candidats |
| `reranker` | Réordonner des candidats déjà autorisés |
| `subscription` | Ajouter une stratégie d’évaluation continue |
| `delivery` | Produire un effet externe idempotent |

Chaque contribution déclare un contrat d’entrée/sortie, une version, ses capacités requises, sa politique de retry et si elle est obligatoire ou optionnelle. Le moteur valide cette compatibilité avant activation.

#### Activation sans arrêt global

~~~text
installed → candidate → validated → active → draining → inactive
                         └────────► rejected
~~~

L’activation compile un plan immuable associant chaque étape à un digest de plugin et une génération. Les nouveaux workflows utilisent le nouveau plan ; les travaux déjà attribués conservent l’ancien. Un rollback redirige les nouveaux travaux vers le dernier plan sain. Aucun hot reload de code n’a lieu dans le processus cœur.

Le mode `from-now` est le défaut. Un backfill explicite indique une fenêtre temporelle, un scope, une priorité et éventuellement un dry-run. Il utilise les mêmes invariants d’idempotence que le temps réel, mais des files logiques moins prioritaires afin de ne pas dégrader la veille.

### Lifecycle And Retention

- Une Retention Policy gouverne séparément indexation, disponibilité des blobs, archivage et purge.
- Un Legal Hold bloque les actions destructrices.
- Un tombstone retire rapidement le contenu des projections sans effacer immédiatement son identité.
- Le garbage collection fonctionne par marquage, vérification, délai de grâce puis balayage.
- Le stockage objet réalise les transitions physiques disponibles ; Quivr conserve des états logiques indépendants du fournisseur.
- Une Archive Projection peut offrir une recherche moins coûteuse ou moins rapide sur les données historiques.

Les politiques peuvent être définies au niveau installation, organization, corpus ou record. La règle effective est calculée et rendue visible avant toute opération destructive : le Legal Hold gagne toujours, puis la règle explicite la plus spécifique, dans les limites minimales imposées par l’installation.

La rétention distingue quatre décisions : rester dans la projection active, conserver les artefacts chauds, déplacer les blobs vers un tier froid et purger physiquement. Désindexer n’implique donc pas supprimer. Une restauration depuis le froid est une Operation asynchrone observable.

## UX / API / System Details

### API publique

- REST documenté par OpenAPI.
- Upload direct des blobs lourds par session et URL présignée.
- Ingestion unitaire ou batch par manifest.
- Ressources de suivi pour Ingestion Receipt et Operation.
- Recherche avec filtres, profil, scope et curseur opaque.
- SSE et polling partageant un Change Cursor durable.
- Webhooks au moins une fois, signés et dédupliqués par event ID.
- Erreurs structurées avec codes stables et indication retryable.

L’API reste en version v0 pendant la construction. Les ruptures sont permises mais documentées. La version v1 est gelée environ six mois avant la première mise en production cliente ou au début de l’intégration externe soutenue, selon le premier événement.

#### Ressources et familles d’endpoints

| Ressource | Surface indicative | Sémantique |
| --- | --- | --- |
| Blobs | `/v0/uploads`, `/v0/blobs` | Créer, confirmer et inspecter un upload |
| Records | `/v0/records`, `/v0/records/{id}/versions` | Ingérer, corriger, retirer et lire le canonique |
| Receipts | `/v0/ingestion-receipts/{id}` | Suivre l’acceptation et la matérialisation |
| Search | `/v0/search` | Interroger par modèle de requête stable |
| Saved Queries | `/v0/saved-queries` | Versionner une intention de recherche |
| Subscriptions | `/v0/subscriptions` | Définir évaluation et destinations |
| Changes | `/v0/changes` | Polling ou SSE depuis un curseur commun |
| Operations | `/v0/operations/{id}` | Suivre backfill, rebuild, restore et purge |
| Plugins admin | `/v0/admin/plugins` | Inspecter, valider et activer une génération |

Les noms définitifs seront figés avec l’OpenAPI, mais ces séparations sont contractuelles. Une ressource métier durable ne doit pas être confondue avec une Operation temporaire. Toute opération longue retourne `202 Accepted`, un identifiant et une représentation consultable contenant progression approximative, compteurs, erreurs bornées et état terminal.

SSE et polling lisent le même journal compact de changements. Un curseur expiré produit une erreur structurée et un lien vers une procédure de resynchronisation. Un webhook transporte `event_id`, type, version de schéma, timestamp et référence de ressource ; le destinataire déduplique par `event_id`.

La surface publique couvre ingestion, lecture, retrieval et veille. La surface administrative couvre plugins, policies, rebuilds et diagnostic. Elle reste derrière les mêmes mécanismes d’authentification standards mais avec des scopes distincts.

### Expérience développeur

~~~text
quivr up
quivr plugin init
quivr plugin dev
quivr plugin test
quivr plugin inspect
quivr plugin build
quivr plugin publish
~~~

- Le démarrage local utilise la même API et les mêmes contrats qu’une installation distribuée.
- Les SDK clients et plugins sont proposés en Python et TypeScript.
- Le SDK masque workflows, task queues, index et mécanismes de retry.
- Les tests locaux peuvent simuler timeout, retry, erreur partielle et nouvelle génération.

Un projet plugin contient au minimum :

~~~text
my-plugin/
├── quivr-plugin.yaml       # identité, compatibilité, contributions et configuration
├── schemas/                # JSON Schema des entrées, sorties et paramètres
├── src/                    # implémentation Python ou TypeScript
├── tests/                  # contract tests et fixtures
└── Dockerfile              # optionnel en dev, obligatoire pour l’artefact OCI
~~~

Le manifeste sépare clairement la version du package, la plage compatible du moteur, les versions de Plugin API et les capabilities fournies ou requises. `quivr plugin inspect` montre le plan résolu avant activation ; `quivr plugin test` exécute les contract tests sans infrastructure distribuée complète.

### Architecture de référence

~~~text
Clients et démonstrateurs
            │
            ▼
       API Quivr
       │        │
       │        └──────────────► PostgreSQL
       │                         catalogue et état
       ▼
     Temporal ────────────────► Plugin workers
       │                         normalisation et enrichissement
       │
       ├──────────────────────► S3-compatible
       │                         contenus et artefacts
       │
       └──────────────────────► Weaviate
                                 projection de recherche
~~~

### Technologies retenues

- Go pour le cœur cohérent du moteur : API, domaine, transactions PostgreSQL, workflows Temporal et adaptateurs de premier niveau.
- Temporal pour l’exécution durable et les workflows.
- PostgreSQL pour le catalogue transactionnel, les identités, versions, droits, policies et états.
- Stockage S3-compatible pour les contenus, manifests, dérivés lourds et embeddings conservés.
- Weaviate pour la projection lexicale, vectorielle, hybride et cross-modale.
- OCI pour distribuer les plugins.
- Docker Compose pour l’expérience locale et Kubernetes pour les déploiements distribués lorsque nécessaire.

Les SDK et plugins externes restent indépendants du langage du cœur. Python et TypeScript sont les deux surfaces prioritaires ; les traitements multimodaux, modèles et codecs restent dans leurs workers OCI plutôt que dans le binaire Go.

Toutes les briques obligatoires doivent utiliser une licence permissive. Les composants copyleft ou source-available ne font pas partie du socle par défaut.

### Responsabilités des composants

| Composant | Possède | Ne possède pas |
| --- | --- | --- |
| API Quivr | Contrats publics, validation, auth, receipts et queries | Traitements longs et payloads lourds |
| Temporal | Orchestration durable, retries, timers et attribution aux générations | Archive métier et contenu des documents |
| PostgreSQL | Identité, catalogue, versions, droits, policies et journal compact | Recherche multimodale et gros objets |
| S3-compatible | Blobs, manifests détaillés et artefacts reconstruisibles | État transactionnel et ranking |
| Weaviate | Projections de recherche et candidats | Vérité métier et conservation légale |
| Plugin workers | Logique de format, modèle et intégration | Décision finale d’accès et mutations directes du cœur |

### Profils de déploiement

- **Local** : une commande Docker Compose, un worker générique et des services mono-instance.
- **Intégration** : composants séparés, plusieurs workers, stockage persistant et observabilité standard.
- **Distribué** : Kubernetes, pools de workers spécialisés, services répliqués et capacité pilotée par métriques.

Le code applicatif et les contrats sont identiques entre profils. Seule la topologie change. Le MVP documente les deux premiers et vérifie que le troisième reste possible ; il ne livre pas une plateforme Kubernetes exhaustive.

### Priorités et backpressure

Les travaux sont classés au minimum en `realtime`, `interactive`, `delivery`, `enrichment`, `backfill` et `maintenance`. Les ingestions temps réel et retraits ne partagent pas leur capacité réservée avec un rebuild massif. Chaque Activity possède timeout, politique de retry et limite de concurrence. Le système ralentit l’admission ou met le travail en attente au lieu d’accumuler sans borne en mémoire.

## Data And State

### Modèle canonique

- Organization : frontière de propriété et de sécurité.
- Corpus : collection logique et frontière de retrieval.
- Connector Instance : acquisition configurée.
- Record : identité logique stable et typée.
- Record Version : représentation immuable d’un record.
- Part : composant typé et potentiellement multimodal.
- Blob : octets immuables.
- Relation : lien typé entre records ou parts.
- Annotation : fait dérivé et versionné.
- Projection : représentation reconstruisible.

Les invariants communs utilisent des colonnes relationnelles. Les métadonnées propres aux plugins utilisent du JSON namespacé et validé par schéma.

#### Relations essentielles

~~~text
Organization
  └── Corpus
       ├── Connector Instance
       └── Record (identité stable)
            └── Record Version (immuable)
                 ├── Part ─────────► Blob
                 ├── Annotation
                 ├── Relation
                 └── Projection Generation ─► Weaviate objects

Saved Query ─► Subscription ─► Match ─► Delivery
~~~

#### Invariants

- une Record Key désigne au plus un Record dans une organization et son namespace de source ;
- un checksum identique répété pour la même clé ne crée pas une nouvelle version logique ;
- une Record Version et ses manifests publiés sont immuables ;
- chaque dérivation référence exactement ses inputs et sa génération de producteur ;
- les données d’un plugin sont namespacées et ne peuvent écraser celles d’un autre ;
- un objet de projection est toujours traçable vers une Record Version et une Projection Generation ;
- le tombstone et les droits canoniques prévalent sur toute projection en retard ;
- une Delivery ne constitue jamais la preuve unique qu’un Match existe.

Les identifiants internes utilisent UUIDv7 pour conserver unicité et localité temporelle. Les identifiants externes restent des chaînes namespacées et ne sont jamais supposés globalement uniques. La déduplication de contenu par checksum ne traverse pas une frontière d’organisation ou de sécurité.

### Placement des données

- PostgreSQL conserve les identités, versions, reçus, références de manifests, policies et lineage important.
- S3 conserve les contenus complets, manifests détaillés, parts volumineuses, annotations et embeddings durables.
- Weaviate contient uniquement les champs et vecteurs utiles au retrieval.
- Temporal conserve un historique opérationnel court, pas l’archive métier.

#### Canonique et reconstruisible

| Donnée | Autorité | Reconstruction |
| --- | --- | --- |
| Identité, version, policy, droits | PostgreSQL | Sauvegarde/restauration de la base |
| Contenu brut et manifest publié | S3-compatible | Source externe si disponible, sinon sauvegarde objet |
| Annotation ou embedding durable | S3 + référence PostgreSQL | Recalculable si le producteur reste disponible |
| Objet lexical/vectoriel | Weaviate | Rebuild depuis PostgreSQL et S3 |
| État de workflow récent | Temporal | Non utilisé comme archive métier |
| Change Event client | PostgreSQL | Reconstituable partiellement depuis le canonique |

Une Projection Generation suit `building → validating → active → draining → retired`. Le cutover ne modifie pas les Record Versions. Deux générations peuvent coexister pour comparer qualité, couverture et performance. Une génération retirée devient éligible au garbage collection après délai de grâce.

Les manifests volumineux — par exemple les milliers de segments d’une vidéo — peuvent être découpés en child manifests, NDJSON ou Parquet dans S3. PostgreSQL n’a pas à contenir une ligne par frame ou token.

### États principaux

~~~text
Receipt      pending → created | duplicate | withdrawal_applied | conflict
Version      materialized → building_baseline → retrieval_ready
                                      └───────→ quarantined
Record       without_current → active → withdrawn
Derivation   pending → succeeded | failed
~~~

Ces cycles de vie restent séparés. Une erreur d’enrichissement optionnel n’empêche pas le Record de rester recherchable. Une contribution obligatoire qui ne peut terminer sûrement peut placer la soumission ou la version en quarantaine. Le [modèle canonique détaillé](./quivr-v2-canonical-data-model.md) fixe les relations, transitions et responsabilités correspondantes.

La purge suit les références depuis les versions conservées, Legal Holds, projections actives et opérations en cours. Elle produit d’abord une liste de candidats inspectable, attend un délai de grâce, revalide les références, puis supprime. L’état logique `hot`, `warm`, `cold`, `restoring` ou `purged` reste indépendant des classes propres à AWS, Azure ou un fournisseur S3.

## Permissions And Trust Boundaries

- L’installateur choisit les plugins et leur fait confiance.
- Les plugins s’exécutent hors du processus principal pour limiter les pannes et consommations incontrôlées.
- Le conteneur OCI standard est le runtime par défaut ; un durcissement supplémentaire reste un choix de déploiement.
- Le manifeste déclare les données, secrets, réseau et ressources nécessaires à l’installation.
- La signature des artefacts est configurable ; le digest immuable reste obligatoire.
- Les plugins utilisent les contrats Quivr et ne dépendent pas directement des tables, workflows ou DSL de recherche internes.
- Le backend applique les droits par organisation et corpus avant de retourner les résultats.
- L’audit du MVP reste limité aux changements administratifs, opérations destructrices et erreurs importantes.

Cette frontière est volontairement pragmatique : Quivr fournit l’isolation de processus, les scopes d’API, la séparation des données et des diagnostics utiles, mais ne construit ni sandbox hostile, ni secret broker propriétaire, ni politique réseau universelle. Les tokens et secrets passent par les mécanismes standards du déploiement. Une installation sensible peut ajouter signature obligatoire, network policies ou runtime renforcé sans modifier le Plugin API.

## Edge Cases

- Une ingestion répétée avec la même clé et le même checksum retourne le même résultat logique.
- Une même clé avec un contenu différent crée une nouvelle version.
- Un blob envoyé mais non référencé devient un Purge Candidate après un délai de grâce.
- Un worker arrêté au milieu d’une Activity reprend sans produire deux résultats logiques.
- Un plugin optionnel indisponible ne bloque pas la recherche minimale.
- Une génération en cours de drainage continue de traiter les travaux qui lui appartiennent.
- Un tombstone reçu pendant un enrichissement neutralise les résultats tardifs.
- Une correction ne recalcule que les dérivés dépendant des données modifiées.
- Un média froid retourne immédiatement ses métadonnées et peut déclencher une restauration asynchrone.
- Un changement de modèle d’embedding construit une nouvelle Projection Generation avant cutover.
- Une recherche hybride inclut un contenu lexicalement disponible même si son embedding est encore absent.
- Une erreur partielle de batch n’annule pas les ingestions réussies.
- Un client SSE trop ancien repart d’un snapshot puis reprend depuis un curseur courant.
- Un batch contenant des entrées invalides expose un résultat par entrée et reste relançable sans dupliquer les succès.
- Une capability requise absente empêche l’activation du plan, mais pas le fonctionnement du plan actif.
- Deux plugins fournissant une capability incompatible produisent une erreur de résolution explicite avant activation.
- Une projection en retard ne peut rendre visible un record désormais interdit : le filtre canonique final s’applique toujours.
- Une génération de projection partiellement construite n’est jamais servie comme active.
- La suppression d’un plugin conserve ses annotations historiques ; leur lecture reste possible via leur schéma archivé.
- Un backfill interrompu reprend depuis un checkpoint et ne repasse pas devant la file temps réel.
- Une restauration froide annulée laisse le contenu dans son état antérieur sans projection partielle active.
- Une destination webhook lente n’épuise pas les workers d’ingestion ; ses retries sont isolés.
- Une correction arrivée avant la fin de la version précédente marque les sorties devenues obsolètes au moment de leur publication.

## Acceptance Criteria

- [ ] Une installation locale complète démarre par une commande documentée.
- [ ] Un client peut ingérer un texte inline et un média par référence de blob.
- [ ] Toute ingestion acceptée retourne un Ingestion Receipt durable et idempotent.
- [ ] Le record devient recherchable avant la fin de ses enrichissements optionnels.
- [ ] 95 % des records du profil de référence deviennent minimalement recherchables en moins de cinq minutes.
- [ ] Les recherches simples respectent un p95 inférieur à une seconde sous le profil de charge convenu.
- [ ] Une requête hybride peut retourner texte, image ou segment vidéo avec provenance et contexte.
- [ ] Un plugin Python et un plugin TypeScript peuvent être développés, testés et activés sans utiliser directement Temporal ou Weaviate.
- [ ] Une nouvelle Plugin Generation reçoit les nouveaux travaux pendant que l’ancienne termine les siens.
- [ ] Un plugin peut être activé uniquement pour les nouveaux contenus ou avec un backfill temporel explicite.
- [ ] Un plugin optionnel en échec ne rend pas indisponible un record déjà recherchable.
- [ ] Le replay d’une Activity ne crée ni version, annotation, match ni delivery logique en double.
- [ ] Un tombstone retire le contenu des résultats et empêche la publication de résultats tardifs.
- [ ] Une Saved Query peut produire un Match et une Delivery idempotente.
- [ ] SSE reprend après une coupure à partir d’un Change Cursor valide.
- [ ] Une Projection Generation Weaviate peut être reconstruite depuis PostgreSQL et S3.
- [ ] Une ancienne génération d’embedding peut coexister avec la nouvelle pendant sa validation.
- [ ] Une politique de rétention peut désindexer un contenu sans supprimer immédiatement son original.
- [ ] Un backfill ne bloque pas les ingestions temps réel.
- [ ] La perte d’un worker ne fait perdre aucune ingestion déjà reconnue.
- [ ] Une correction crée une version immuable liée à la précédente ; un retrait disparaît des résultats sans attendre la purge physique.
- [ ] Chaque réponse de recherche peut être reliée à un record, une version, une part et une génération de projection.
- [ ] Les profils `fast`, `balanced` et `deep` utilisent le même modèle public de requête.
- [ ] Un batch partiellement invalide peut être rejoué sans dupliquer les entrées déjà acceptées.
- [ ] L’activation échoue avant cutover lorsqu’une capability ou une version compatible manque.
- [ ] Un plan de plugin actif est inspectable avec les digests et versions effectivement résolus.
- [ ] Les backfills utilisent une priorité distincte et ne font pas dépasser le seuil de fraîcheur convenu du temps réel.
- [ ] Un rebuild complet peut être validé puis activé atomiquement sans interrompre les recherches.
- [ ] Une purge propose un dry-run, respecte Legal Hold et délai de grâce, puis revalide ses candidats.
- [ ] Les endpoints asynchrones partagent un modèle Operation cohérent et documenté dans l’OpenAPI.
- [ ] L’installation ne dépend d’aucun composant obligatoire copyleft ou source-available.

## Test Plan

- Unit:
  - validation des manifests et schémas de plugins ;
  - résolution d’identité, idempotence et versionnement ;
  - calcul de lineage, supersession et rétention ;
  - compilation des filtres d’accès et requêtes de retrieval ;
  - transitions des états d’ingestion, plugin et operation.
- Integration:
  - workflows Temporal avec retries, timeouts et workers interrompus ;
  - écritures idempotentes dans PostgreSQL et S3 ;
  - projection, suppression et rebuild Weaviate ;
  - activation, drainage et rollback de générations de plugins ;
  - Saved Queries, Matches, webhooks et reprise SSE ;
  - tombstone reçu pendant un enrichissement.
- E2E / manual:
  - ingestion d’un petit corpus texte, image et vidéo ;
  - plugin NewsML-G2 vers le modèle générique ;
  - recherche lexicale, sémantique, hybride et cross-modale ;
  - alerte produite par une recherche sauvegardée ;
  - backfill sur une fenêtre temporelle choisie ;
  - restauration d’un média marqué froid ;
  - boucle complète de développement d’un plugin externe.
- Regression:
  - corpus de référence versionné avec jugements de pertinence ;
  - tests de compatibilité API et Plugin API pendant chaque phase publiée ;
  - absence de doublons après replay ;
  - absence de résultats tombstonés ou non autorisés ;
  - mesures de latence et de fraîcheur sur un environnement de référence.

Les contract tests des plugins sont publiés avec chaque version de Plugin API. Ils vérifient schémas, idempotence, gestion des timeouts, taille maximale des résultats et compatibilité SemVer. Les SDK Python et TypeScript doivent passer les mêmes scénarios comportementaux.

Le benchmark de référence sépare : débit d’acceptation, temps jusqu’à `searchable`, durée des enrichissements, latence de recherche, qualité de retrieval et backlog par priorité. Les mesures sont prises avec texte seul puis avec un mix représentatif d’images et vidéos afin de ne pas masquer le coût multimodal.

Les tests de récupération couvrent au minimum : arrêt brutal d’un worker, indisponibilité temporaire de Weaviate, retry S3, redémarrage Temporal, projection corrompue et webhook durablement indisponible. Le résultat attendu porte sur les invariants et la reprise, pas seulement sur le retour HTTP.

La qualité est évaluée sur un corpus versionné comprenant requêtes, résultats attendus, droits, corrections et retraits. Un changement de modèle ou de ranking publie un rapport comparatif avant cutover.

## Rollout

1. **Verticale texte utile** : ingestion, reçu, normalisation, projection, recherche et webhook. Sortie lorsque la chaîne est observable, rejouable et démontrable via API.
2. **Premier vrai plugin** : modèle générique, SDK Python, CLI et normalizer externe. Sortie lorsqu’un contributeur peut développer sans accès aux internes.
3. **Veille continue** : Saved Queries, Subscriptions, Matches, deliveries et corrections/retraits. Sortie avec une boucle de veille d’actualité fonctionnelle de bout en bout.
4. **Multimodal progressif** : image, audio et vidéo avec niveaux de disponibilité. Sortie lorsqu’une requête texte retrouve des médias avec contexte et provenance.
5. **Lifecycle des extensions** : SDK TypeScript, résolution de capabilities, activation, drainage, rollback et backfill. Sortie après mise à jour d’un plugin sans arrêt global.
6. **Intégration de la première verticale** : plugin NewsML-G2 et autres mappings prioritaires branchés au démonstrateur via API uniquement. Sortie après validation conjointe des parcours principaux.
7. **Palier de référence** : campagne jusqu’à 10 millions d’unités avec mix multimodal, correction des plafonds et runbook. Sortie lorsque fraîcheur et latence cibles sont reproductibles.
8. **Préparation v1** : compatibilité, migrations, sauvegarde/restauration et déploiement d’intégration stabilisés. Gel environ six mois avant la première production cliente ou au début de l’intégration externe soutenue.
9. **Scale déclenché par preuve** : sharding, pools spécialisés, NATS ou projection d’archive uniquement si les métriques réelles montrent leur nécessité.

## Rollback

- Une release applicative conserve la compatibilité nécessaire avec la version précédente pendant son drainage.
- Une Plugin Generation défaillante est désactivée et la génération précédente reprend les nouveaux travaux.
- Une Projection Generation défaillante est abandonnée sans modifier les données canoniques.
- Une migration PostgreSQL utilise une stratégie expand/contract tant que deux versions coexistent.
- Un modèle d’embedding précédent reste disponible pendant la validation du nouveau.
- Les opérations destructrices disposent d’une simulation et d’un délai de grâce avant purge physique.

## Risks

| Risque | Réduction du risque dans le MVP |
| --- | --- |
| Explosion du nombre de parts vidéo et de vecteurs | Unités sémantiques bornées, profils, budgets de dérivation et mesures par modalité |
| Weaviate insuffisant en coût, ACL ou ingestion | Search Projection encapsulée, benchmark tôt, données canoniques indépendantes ; Qdrant reste une issue de secours |
| Historiques Temporal trop volumineux | Workflow court par version, activités externes, IDs/références seulement, Continue-As-New et rétention opérationnelle |
| Plugin API trop large | Stabiliser d’abord normalizer, enricher, delivery et retrieval essentiels ; garder le reste expérimental si nécessaire |
| Cœur contaminé par les concepts d’un client | Revue explicite de généricité et spécificités client uniquement par manifests/plugins |
| Abstraction trop générique et pénible | Tranches verticales réelles, SDK ergonomique et premier plugin externe très tôt |
| Qualité cross-modale insuffisante | Corpus de référence, profils de modèles remplaçables et validation avant cutover |
| Rétention destructive ou incompréhensible | Policy effective visible, dry-run, Legal Hold, délai de grâce et données canoniques séparées |
| Backfills dégradant la veille | Priorités séparées, concurrence bornée et SLO de fraîcheur comme garde-fou |
| Dérive vers hardening ou scale extrême | Gates de rollout centrés sur la valeur et ajout d’infrastructure uniquement à partir de métriques |

## Open Questions

### Bloquantes avant implémentation de chaque tranche

- Quels connecteurs génériques et verticaux sont prioritaires pour la première verticale ?
- Quel format exact et quel sous-ensemble NewsML-G2 constituent la première fixture de la verticale ?
- Quel corpus versionné sert aux jugements de pertinence et au benchmark initial ?
- Quels quatre ou cinq contribution types sont stables dans le premier Plugin API ?

### À décider par benchmark ou avant intégration

- Quels modèles ouverts servent de profils par défaut pour texte, image, transcription et cross-modalité ?
- Quelle topologie de référence et quel profil de charge valident les critères de fraîcheur et de latence ?
- Quelle profondeur d’archive sémantique est nécessaire au MVP de la première verticale ?
- À quel seuil de coût ou de performance une Archive Projection devient-elle utile ?
- Quels signaux justifieraient réellement NATS, un second moteur ou du sharding PostgreSQL ?

## Handoff

Use this for review:

- Spec name: Quivr V2 Backend MVP.
- Chosen scope: MVP livré par tranches verticales, sans scale extrême anticipé.
- Key decisions: backend générique, Temporal, PostgreSQL, S3-compatible, Weaviate, plugins OCI, APIs REST/SSE/webhooks, multimodalité progressive.
- Highest-risk areas: volume des projections multimodales, qualité du retrieval, design du Plugin API et maintien de la simplicité.
- Acceptance criteria: ingestion durable et idempotente, disponibilité progressive, recherche hybride/cross-modale, plugins versionnés, veille continue et rebuild des projections.
- Open questions: premiers connecteurs, modèles ouverts, corpus de benchmark, archive sémantique et surface stable du Plugin API.
