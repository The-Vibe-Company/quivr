# Meilisearch pour le backend générique Quivr

> Recherche effectuée le 3 septembre 2026. Sources utilisées : documentation, dépôts et publications officielles de Meilisearch uniquement. La version stable visible au moment de la recherche est `v1.53.0`; les capacités distribuées décrites ci-dessous ont été introduites dans l'Enterprise Edition à partir de `v1.37`.

## Conclusion

Meilisearch mérite d'être gardé dans la réflexion. C'est probablement le meilleur candidat parmi les moteurs de recherche spécialisés pour offrir une très bonne expérience locale : un binaire ou un conteneur, une API HTTP simple, un modèle JSON sans schéma préalable, des SDK nombreux, du lexical tolérant aux fautes, des filtres/facettes et de l'hybride vectoriel dans le même produit.

En revanche, **Meilisearch Community Edition ne peut pas être l'unique moteur de référence d'un Quivr qui promet horizontalité et haute disponibilité open source**. Le sharding et la réplication coordonnés sont des fonctions Enterprise sous BUSL; la production exige un contrat commercial. Même dans la version distribuée actuelle, toutes les écritures passent par un leader et Meilisearch indique explicitement que la haute disponibilité des écritures n'est pas encore couverte.

La bonne position architecturale serait donc :

- Meilisearch comme **projection de recherche**, jamais comme source de vérité;
- une interface `SearchBackend` stable dans Quivr afin de ne pas exposer le dialecte Meilisearch aux plugins;
- Meilisearch Community comme excellent profil local, petite/moyenne production ou installation verticale;
- Meilisearch Enterprise/Cloud comme candidat pour un grand déploiement **à benchmarker et contractualiser**, pas comme choix déjà validé;
- une autre implémentation distribuée possible derrière la même interface si la licence, le write HA ou les essais de charge éliminent Meilisearch.

Autrement dit, il ne faut ni écarter Meilisearch à cause de son ancien modèle mono-nœud, ni construire Quivr de façon à dépendre de ses capacités payantes récentes.

## 1. Éditions et licence

Le dépôt principal contient deux régimes de licence :

- les composants Community Edition sont sous MIT;
- les fichiers marqués Enterprise Edition sont sous Business Source License 1.1 adaptée par Meili SAS;
- la BSL permet test, développement et évaluation, mais **l'utilisation en production du code EE requiert une licence commerciale**; chaque version passe sous MIT quatre ans après sa publication.

La documentation d'édition dit que la Community Edition inclut la recherche plein texte et la recherche IA. La documentation actuelle du cluster précise que **sharding et réplication nécessitent Enterprise Edition v1.37 ou supérieure**. La page générale des éditions dit encore que « la seule fonction exclusive » est le sharding, formulation moins précise que les pages opérationnelles dédiées; pour une décision de production, il faut retenir les pages de sharding/réplication et le fichier de licence.

Meilisearch Cloud utilise l'Enterprise Edition. Le sharding et la réplication y sont un add-on Enterprise configuré avec l'équipe Meilisearch; les pages tarifaires ne donnent pas de prix public pour ce niveau.

Sources : [licence racine](https://github.com/meilisearch/meilisearch/blob/main/LICENSE), [licence EE](https://github.com/meilisearch/meilisearch/blob/main/LICENSE-EE), [Community et Enterprise](https://www.meilisearch.com/docs/resources/self_hosting/enterprise_edition), [sharding et réplication](https://www.meilisearch.com/docs/resources/self_hosting/sharding/overview), [tarifs](https://www.meilisearch.com/pricing).

### Conséquence pour Quivr OSS

Quivr peut librement distribuer ou recommander la Community Edition. Mais si le profil de production « standard » exige le cluster Meilisearch coordonné, l'installation complète n'est plus composée uniquement de briques open source exploitables librement en production. Ce n'est pas forcément rédhibitoire pour un déploiement client, mais c'est une décision produit et commerciale explicite.

## 2. Expérience développeur et exploitation

### Local

La DX est excellente : binaire unique, Docker, Homebrew, APT ou compilation depuis les sources. Le démarrage local tient en une commande. L'API est HTTP/JSON et la documentation recense des SDK officiels JavaScript/TypeScript, Python, PHP, Ruby, Go, Rust, Java, .NET, Swift et Dart, ainsi que des intégrations frontend InstantSearch.

Sources : [installation locale](https://www.meilisearch.com/docs/resources/self_hosting/getting_started/install_locally), [intégrations et SDK](https://www.meilisearch.com/integrations), [format des requêtes API](https://www.meilisearch.com/docs/reference/api/requests).

### Production

La Community Edition reste simple à opérer tant qu'un nœud vertical suffit, mais l'opérateur porte lui-même les mises à jour, sauvegardes, supervision, capacité disque, compaction et reprise. Le Cloud apporte sauvegardes automatiques, autoscaling du disque, upgrade assisté et SLA; sharding/réplication font partie des offres Enterprise.

Le stockage utilise le memory mapping : l'index peut être plus grand que la RAM, mais les pages froides seront servies depuis le disque. L'indexation est intensive en mémoire. Par défaut, l'indexeur peut employer jusqu'aux deux tiers de la mémoire disponible et jusqu'à la moitié des cœurs; la documentation avertit qu'un dépassement mémoire peut tuer le processus et que l'indexation peut dégrader la recherche.

Sources : [Cloud vs self-hosting](https://www.meilisearch.com/docs/getting_started/features), [impact RAM et multithreading](https://www.meilisearch.com/docs/resources/self_hosting/performance/ram_multithreading), [configuration](https://www.meilisearch.com/docs/resources/self_hosting/configuration/reference).

## 3. Ingestion, mises à jour et suppressions

Meilisearch offre :

- ajout/remplacement complet par identifiant primaire;
- upsert partiel, les champs absents étant conservés;
- mise à jour des seuls documents existants;
- suppression par identifiant, lot d'identifiants, filtre ou totalité de l'index;
- payloads JSON, NDJSON ou CSV, compressibles;
- limite de payload par défaut de 100 MB, configurable en self-hosted;
- recommandations officielles de lots de 50 000 à 100 000 documents au-delà d'un million de documents, à ajuster selon leur taille.

Toutes les écritures et modifications de réglages sont asynchrones. L'API répond `202` avec un `taskUid`; la réussite réelle doit être observée ensuite. Les tâches sont traitées dans l'ordre et Meilisearch regroupe automatiquement les tâches compatibles en batches pour améliorer le débit. On peut attacher une métadonnée de corrélation et recevoir des webhooks de changement d'état.

Les upserts sont naturellement idempotents si Quivr fournit un identifiant de projection déterministe. Meilisearch ne remplace cependant pas l'outbox, le journal de lineage ou le broker de Quivr : un `202` confirme seulement la mise en file de la tâche Meilisearch, pas la matérialisation réussie de la projection.

Sources : [ajout et mise à jour](https://www.meilisearch.com/docs/reference/api/documents/add-or-update-documents), [gros imports](https://www.meilisearch.com/docs/capabilities/indexing/how_to/import_large_datasets), [suivi des tâches](https://www.meilisearch.com/docs/capabilities/indexing/tasks_and_batches/monitor_tasks), [suppression à grande échelle](https://www.meilisearch.com/docs/capabilities/indexing/how_to/delete_documents_at_scale).

### Écritures continues

Le comportement documenté est cohérent pour un flux régulier, mais pas magique :

- les tâches d'un nœud sont ordonnées et auto-batchées;
- la recherche reste accessible pendant les opérations asynchrones;
- l'indexeur réserve par défaut une partie des cœurs à la recherche;
- augmenter les threads d'indexation peut dégrader la latence des recherches;
- changer beaucoup de réglages déclenche une réindexation complète;
- une compaction laisse la recherche disponible mais **met les nouvelles écritures en attente jusqu'à la fin**;
- la Community Edition n'offre pas de scale-out d'écriture natif;
- dans le cluster Enterprise, toutes les écritures passent par le leader, les autres nœuds les refusent.

Pour Quivr, il faut donc un contrôleur de projection avec mesure de lag, lots adaptatifs, retry idempotent, circuit breaker et priorités. Les corrections/retraits temps réel ne doivent pas être coincés derrière un backfill ou une compaction massive. Un benchmark doit mélanger recherches, ingestion continue, corrections, suppressions et enrichissements — pas seulement charger un dump puis mesurer des requêtes.

Sources : [RAM et concurrence indexation/recherche](https://www.meilisearch.com/docs/resources/self_hosting/performance/ram_multithreading), [compaction](https://www.meilisearch.com/docs/capabilities/indexing/how_to/compact_an_index), [cluster distribué](https://www.meilisearch.com/docs/resources/self_hosting/sharding/overview).

## 4. Taille et passage à l'échelle

Il n'existe pas de plafond documentaire universel documenté : la taille dépend fortement de la longueur des documents, du nombre de champs recherchables/filtrables/triables, des proximités, des facettes, du nombre et de la dimension des vecteurs, du matériel et du profil de requêtes.

Les repères officiels raisonnables sont :

- la documentation présente le produit comme conçu pour des **millions** de documents par index, non pour des milliards;
- elle recommande de découper les longs contenus, les documents de plus de 10 KB étant moins naturels pour le moteur;
- l'outil d'import officiel a été testé avec des datasets de plus de 40 millions de documents;
- Meilisearch publie une démonstration de recherche multimodale sur 100 millions d'images;
- son retour officiel sur le sharding cite un client de plus de 100 millions de documents, pour lequel un nœud ne tenait plus les objectifs et plusieurs shards ont été déterminés par tests de charge.

Ces chiffres sont des preuves de possibilité, pas un dimensionnement pour notre cas. Aucun de ces exemples publics ne documente simultanément taille moyenne des documents, nombre de vecteurs, ACL, débit d'update, topologie, coût et SLO.

Sources : [internes du ranking et domaine de taille](https://www.meilisearch.com/docs/resources/internals/ranking), [importer officiel](https://github.com/meilisearch/meilisearch-importer), [démonstration Flickr 100 M](https://www.meilisearch.com/docs/resources/demos/flickr), [retour officiel sur un cluster >100 M](https://www.meilisearch.com/blog/sharding-replication).

### Sharding, réplication et haute disponibilité

Depuis EE v1.37, un réseau Meilisearch peut répartir les documents d'un même index en shards explicites et assigner un shard à plusieurs remotes pour le répliquer. Les recherches fan-out puis fusionnent les résultats. Les filtres, facettes, hybride/vectoriel, multi-search et tenant tokens sont annoncés compatibles.

Limites importantes au 3 septembre 2026 :

- cette capacité n'est pas disponible librement en production dans la Community Edition;
- la topologie est dirigée par un leader;
- **les écritures, changements de réglages et créations d'index doivent passer par le leader**;
- la publication officielle de lancement indique que la réplication couvre la haute disponibilité en lecture, mais pas encore la disparition du SPOF d'écriture;
- la topologie et le nombre de shards doivent être dimensionnés; ce n'est pas un cluster auto-élastique transparent;
- certaines fonctions récentes ne sont pas encore compatibles avec le sharding, par exemple les joins/hydratations distants indiqués dans les notes de version.

Sources : [vue d'ensemble sharding/réplication](https://www.meilisearch.com/docs/resources/self_hosting/sharding/overview), [configuration de la réplication](https://www.meilisearch.com/docs/resources/self_hosting/sharding/configure_replication), [publication officielle et limite write HA](https://www.meilisearch.com/blog/sharding-replication), [roadmap](https://www.meilisearch.com/roadmap).

### Application à un grand corpus d'actualité

Un corpus de dizaines de millions de contenus texte, photo et vidéo ne correspond pas au même nombre de « documents Meilisearch » :

- une dépêche longue devient plusieurs chunks;
- une photo peut produire une projection metadata/texte, une ou plusieurs représentations vectorielles et des relations;
- une vidéo devient plusieurs segments, transcriptions, thumbnails ou plans;
- les versions/corrections peuvent multiplier les écritures même si seule la version courante reste projetée.

La projection searchable peut donc dépasser largement 100 M entrées, avec plusieurs vecteurs par contenu. **Un unique nœud Community n'est pas une hypothèse acceptable sans benchmark exceptionnel**, et le scale-out Enterprise devient probablement nécessaire si tout l'historique est dans Meilisearch.

Une stratégie plus saine est de garder un index chaud limité par politique, puis un index d'archive séparé ou un autre backend d'archive. Cela réduit le working set, mais Meilisearch ne fournit pas lui-même le tiering : Quivr doit gérer le routage et le lifecycle.

## 5. Recherche lexicale, filtres, facettes et langues

Meilisearch apporte une qualité « application search » immédiatement utile : recherche préfixe, tolérance aux fautes, phrases, exclusions, synonymes, dictionnaire, stop words, surlignage, filtres, tri, facettes et géorecherche. Les champs filtrables, triables, affichés et recherchables sont configurés par index.

Son ranking lexical n'est pas BM25. C'est un tri lexicographique par buckets : chaque règle ne départage que les ex aequo laissés par la précédente (`words`, `typo`, `proximity`, `attributeRank`, `sort`, `wordPosition`, `exactness`). On peut réordonner les règles et introduire un tri ascendant/descendant sur un attribut, mais **pas exécuter une formule arbitraire de score ou un script de ranking**. C'est très lisible et efficace pour des recherches courtes; c'est moins flexible pour un ranking éditorial/retrieval complexe.

Sources : [pipeline de ranking](https://www.meilisearch.com/docs/capabilities/full_text_search/advanced/ranking_pipeline), [limites du ranking face aux formules](https://www.meilisearch.com/docs/resources/internals/ranking), [API de recherche](https://www.meilisearch.com/docs/reference/api/search/search-with-post).

### Multilingue

Le tokenizer Charabia traite explicitement de nombreuses familles et écritures, dont latin, allemand, arabe, persan, hébreu, chinois, japonais, coréen, thaï et khmer. Des `localizedAttributes` peuvent forcer le tokenizer d'un champ et `locales` celui de la requête.

Pour les gros corpus, la recommandation officielle reste **un index par langue**, puis multi-search/fédération. Un index multilingue unique est présenté comme acceptable pour un POC ou moins d'environ un million de documents; au-delà, pertinence et performance peuvent baisser, particulièrement avec des tokenisations hétérogènes. Cette stratégie multiplie les index et complexifie le fan-out, mais elle s'aligne avec le prototype existant.

Sources : [gestion des datasets multilingues](https://www.meilisearch.com/docs/capabilities/indexing/how_to/handle_multilingual_data), [langues et tokenisation](https://www.meilisearch.com/docs/resources/help/language).

## 6. Recherche vectorielle, hybride et multimodale

Meilisearch sait combiner lexical et similarité vectorielle dans la même requête via `semanticRatio` : `0` lexical, `1` sémantique, valeur intermédiaire hybride. Il accepte soit des vectors fournis par Quivr, soit des embedders gérés par Meilisearch. Plusieurs embedders nommés peuvent coexister dans le même index. Les requêtes fédérées peuvent fusionner plusieurs stratégies/index avec des poids.

Pour Quivr, les vectors fournis par la pipeline sont préférables : ils gardent le modèle, sa version, les retries, les quotas et la provenance sous contrôle du moteur/plugin system au lieu de les enfouir dans la projection de recherche.

La binary quantization réduit un vecteur float 32 bits à un bit par dimension, mais elle est irréversible sur l'embedder concerné et dégrade la précision. La documentation la recommande surtout au-delà d'un million de documents avec des dimensions élevées. Meilisearch documente également DiskANN pour éviter que tous les vectors résident en RAM.

Sources : [sémantique vs hybride](https://www.meilisearch.com/docs/capabilities/hybrid_search/advanced/semantic_vs_hybrid), [plusieurs embedders](https://www.meilisearch.com/docs/capabilities/hybrid_search/advanced/multiple_embedders), [quantification binaire](https://www.meilisearch.com/docs/capabilities/hybrid_search/advanced/binary_quantization), [fonctionnalités vectorielles](https://www.meilisearch.com/docs/getting_started/features).

### Multimodalité réelle et limites

L'image-to-image et text-to-image existe via des embedders REST et des fragments, mais la documentation marque encore l'API multimodale correspondante comme **expérimentale**. Les images ne sont pas stockées ni décodées comme actifs média par Meilisearch : un document JSON référence une URL ou fournit les données nécessaires à un service d'embedding. Les vidéos sont représentées par thumbnails, transcriptions ou embeddings pré-calculés; Meilisearch n'est ni un pipeline vidéo ni un object store.

La démonstration officielle sur 100 M images est encourageante, mais ne valide ni une grande photothèque avec droits/ACL et updates, ni les segments d'un grand fonds vidéo, ni la combinaison avec des dizaines de millions de chunks textuels. Ces scénarios doivent être benchmarkés avec les embeddings réellement retenus.

Sources : [recherche d'image multimodale expérimentale](https://www.meilisearch.com/docs/capabilities/hybrid_search/how_to/image_search_with_multimodal), [combinaison texte/image](https://www.meilisearch.com/docs/capabilities/multi_search/how_to/combine_text_and_image_search), [démo 100 M images](https://www.meilisearch.com/docs/resources/demos/flickr).

## 7. Multi-tenancy et contrôle d'accès

Les API keys sont limitées par actions, motifs d'index et expiration. Les tenant tokens sont des JWT courts générés par le backend; ils embarquent, pour chaque index, un filtre obligatoire qui est combiné à toute recherche. Cela permet un document-level isolation simple dans un index partagé et fonctionne avec le sharding.

Points d'attention :

- les tenant tokens protègent les endpoints de recherche, pas les opérations d'administration ou d'indexation;
- les règles d'un tenant token ne contiennent qu'un filtre par index; la décision d'autorisation et la génération sûre du filtre restent chez Quivr;
- les API keys ont des scopes d'action et d'index, mais pas un modèle RBAC métier complet;
- la séparation de champs sensibles doit être imposée par la projection et `displayedAttributes`; le filtre de tenant n'est pas une politique de field-level redaction;
- Quivr ne doit donc pas exposer directement Meilisearch au démonstrateur d’un client : son API doit appliquer les droits, puis transmettre un filtre non contournable ou utiliser un tenant token à durée courte.

La documentation propose aussi un RBAC basé sur des foreign filters et un index d'accès. C'est utile, mais plus récent et ne dispense pas Quivr de maintenir les droits dans sa source de vérité; Meilisearch n'assure pas l'intégrité référentielle de ces relations.

Sources : [sécurité et tenant tokens](https://www.meilisearch.com/docs/capabilities/security/overview), [payload du tenant token](https://www.meilisearch.com/docs/capabilities/security/advanced/tenant_token_payload), [API keys](https://www.meilisearch.com/docs/capabilities/security/how_to/manage_api_keys), [RBAC avec joins](https://www.meilisearch.com/docs/capabilities/security/advanced/rbac_with_joins), [absence d'intégrité référentielle des relations](https://www.meilisearch.com/docs/capabilities/indexing/joins/define_index_relationships).

## 8. Sauvegarde, réindexation et cycle de vie

### Sauvegardes

Deux mécanismes existent :

- snapshot : copie exacte rapide à restaurer, compatible seulement avec la même version;
- dump : export portable entre versions, plus compact mais dont l'import réindexe tout et peut être très long.

En self-hosted, les snapshots peuvent être planifiés et stockés hors serveur, y compris vers S3 selon la configuration actuelle. Le Cloud gère ses sauvegardes; les snapshots self-hosted ne sont pas une API Cloud.

Sources : [sauvegardes](https://www.meilisearch.com/docs/resources/self_hosting/data_backup/overview), [snapshots](https://www.meilisearch.com/docs/resources/self_hosting/data_backup/snapshots), [dumps](https://www.meilisearch.com/docs/resources/self_hosting/data_backup/dumps).

### Réindexation sans coupure

Un changement de réglages comme `searchableAttributes`, stop words ou plusieurs paramètres structurels déclenche une réindexation complète. La recherche peut continuer pendant les tâches asynchrones, mais les coûts CPU/RAM et le retard d'écriture doivent être surveillés.

Pour un rebuild contrôlé, on peut construire `index_v2`, puis effectuer un **index swap atomique** avec `index_v1`; les clients continuent d'utiliser le même alias logique sans coupure. Meilisearch ne prend toutefois pas en charge le rattrapage applicatif entre le début du rebuild et le swap. Quivr doit rejouer son journal ou dual-write les deux générations, vérifier un watermark, geler brièvement le cutover puis swapper.

Sources : [index swap atomique](https://www.meilisearch.com/docs/resources/internals/indexes), [réindexation des searchable attributes](https://www.meilisearch.com/docs/capabilities/full_text_search/how_to/configure_searchable_attributes), [bonnes pratiques de configuration](https://www.meilisearch.com/docs/getting_started/good_practices).

### Hot / warm / cold et rétention

Meilisearch ne propose pas un équivalent natif d'Index Lifecycle Management avec déplacement automatique hot/warm/cold ou recherche directe sur un tier Glacier. Il fournit : suppression par filtre/date, index séparés, snapshots/dumps et compaction.

Après de nombreuses mises à jour/suppressions, les espaces obsolètes peuvent rester dans les structures internes. En Cloud, la compaction est automatisée; en self-hosted, l'opérateur doit la déclencher. Elle demande temporairement environ la taille de l'index en espace libre, laisse les recherches disponibles, mais reporte les nouvelles tâches d'indexation.

Quivr doit donc rester propriétaire de la politique de rétention : désindexer/supprimer dans Meilisearch, conserver la source canonique et les blobs dans les tiers objet, éventuellement maintenir un ou plusieurs index d'archive, puis reconstruire les projections au besoin.

Sources : [suppression et lifecycle applicatif](https://www.meilisearch.com/docs/capabilities/indexing/how_to/delete_documents_at_scale), [compaction](https://www.meilisearch.com/docs/capabilities/indexing/how_to/compact_an_index).

## 9. Ce que le prototype `multimodal-rag` exploite déjà

Le prototype démontre plusieurs qualités pertinentes de Meilisearch :

- un service unique `getmeili/meilisearch:v1.16` dans Docker Compose, avec volume persistant;
- des indexes séparés par langue et modalité;
- filtres/facettes sur les métadonnées de presse;
- vecteurs textuels fournis par la pipeline et rangés dans `_vectors`;
- multi-search fédérée sur les indexes de langues;
- choix keyword/semantic/hybrid avec `semanticRatio`;
- découpage du texte avant vectorisation.

Références locales : [`docker-compose.yml`](../multimodal-rag/backend/multimodal_rag_api/docker-compose.yml), [`client.py`](../multimodal-rag/backend/multimodal_rag_api/src/multimodal_rag_api/api/services/meilisearch/client.py), [`activities.py`](../multimodal-rag/backend/multimodal_rag_api/src/multimodal_rag_api/temporal_worker/activities.py), [`search_helpers.py`](../multimodal-rag/backend/multimodal_rag_api/src/multimodal_rag_api/api/services/search_helpers.py).

Ce prototype ne valide pas encore une exploitation de production :

- un seul nœud Community, aucune réplication ou stratégie de reprise;
- master key en clair et mode development;
- cache de projection en mémoire dans le worker;
- lots minuscules par défaut (100), alors que les recommandations Meilisearch pour gros corpus sont nettement supérieures;
- attente synchrone de chaque tâche d'indexation;
- réglages d'index renvoyés avant les lots, sans génération d'index ni stratégie de migration;
- aucune projection canonique versionnée, watermark ou dual-write;
- image embedding encore en TODO et aucune vidéo;
- pas de rétention, compaction, snapshot, purge en cascade, métriques de lag ou test de charge.

Il faut conserver la preuve de DX et les patterns `primary key stable + userProvided vectors + multi-search`, mais pas l'architecture opérationnelle.

## 10. Évaluation synthétique pour Quivr

| Critère | Community | Enterprise self-hosted / Cloud | Lecture Quivr |
|---|---|---|---|
| Installation locale | Excellente | Plus lourde/contractuelle | Très bon défaut Compose |
| API et SDK | Excellents | Identiques | Très bonne DX |
| Lexical + filtres + facettes | Oui | Oui | Fort |
| Hybride/vectoriel | Oui | Oui | Fort, vectors fournis recommandés |
| Multimodal | Oui mais parties expérimentales | Oui | Prometteur, benchmark requis |
| Multilingue | Oui; plusieurs index conseillés à grande échelle | Oui | Bon mais fan-out à prévoir |
| Ranking arbitraire/plugin | Non | Dynamic rules/personalisation selon offre, mais pas moteur de script générique | Limite pour retrieval avancé |
| Sharding coordonné | Non | Oui, EE | Verrou licence/édition |
| Réplication/HA lecture | Non native | Oui, EE | Verrou licence/édition |
| HA écriture | Non | Pas encore; leader unique | Risque important en production |
| Hot/warm/cold natif | Non | Non documenté comme ILM | À gérer par Quivr |
| Sauvegarde | Snapshots/dumps | Cloud automatisé / EE selon offre | Correct si projection reconstruisible |
| Rebuild sans coupure | Index swap atomique | Idem | Quivr doit gérer dual-write/replay |
| ACL métier | Tenant tokens + filtres | Plus fonctions Cloud/Enterprise | Garder enforcement dans Quivr |
| Très gros corpus | Vertical/manual federation | Sharding récent | Aucun go sans benchmark |

## 11. Recommandation de décision

### Ce que l'on peut décider maintenant

1. **Ne pas figer OpenSearch par défaut.** Meilisearch est suffisamment sérieux pour être candidat principal à la projection de recherche.
2. **Ne pas figer Meilisearch comme invariant du moteur.** Les contrats publics Quivr doivent parler de recherche hybride, filtres, facets, pagination/cursors et ranking, pas d'index UID, `semanticRatio` ou tenant token Meilisearch.
3. **Adopter Meilisearch Community dans le profil local** pour préserver une DX excellente, sous réserve qu'un backend encore plus compact ne soit pas retenu après prototype.
4. **Traiter le déploiement client comme un choix séparé** : Meilisearch EE/Cloud peut être pertinent, mais seulement après benchmark et clarification commerciale sur licence, on-prem, SLA, support, sauvegarde et roadmap write HA.
5. **Garder les données canoniques hors de Meilisearch** afin de pouvoir reconstruire, migrer, tierer et changer de backend sans perdre le corpus.

### Gate de validation proposé

Avant de choisir Meilisearch pour un grand déploiement, exécuter un benchmark reproductible avec au moins :

- distribution réelle des tailles de chunks et métadonnées;
- projection chaude représentative, puis extrapolation archive;
- plusieurs langues et filtres d'autorisation à forte cardinalité;
- un et plusieurs vectors par document avec les dimensions retenues;
- ingestion continue, corrections et suppressions pendant les recherches;
- quelques dizaines de recherches concurrentes, quelques centaines d’utilisateurs simulés et pointes 2×;
- p95 de recherche simple inférieur à 1 s;
- 95 % du contenu minimalement searchable en moins de 5 min, cible interne texte de 60 s;
- perte du leader, perte d'un remote, reprise, upgrade, snapshot/restore et rebuild complet;
- backfill, compaction et rétention en parallèle sans bloquer la voie temps réel;
- coût complet Community vertical vs EE self-hosted vs Cloud.

### Verdict

**Meilisearch est un excellent choix de DX et un candidat crédible de production, mais pas encore un choix validé pour un grand déploiement.** Le point bloquant n'est plus la recherche hybride : elle est riche. Le vrai point de décision est de savoir si Quivr accepte que son profil distribué/HA repose sur une édition commerciale récente avec leader d'écriture unique, ou s'il veut une trajectoire distribuée entièrement open source dès le départ.
