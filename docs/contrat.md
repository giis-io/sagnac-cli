# Contrat des labs, côté CLI (v2)

Ce document dit ce que `sagnac` lit dans un kit de lab, comment il lance les
contrôles et ce qu'il rend. Il fait foi pour le code de ce dépôt : les
commentaires qui parlent du « contrat » renvoient ici, section par section.
Les tests de conformité (`testdata/`, voir `testdata/README.md`) vérifient que
la CLI rend, champ par champ, le même résultat que l'exécuteur de référence de
la plateforme sur un kit figé.

Version : contrat v2, résultat `sagnac-result/2`. Les clés et les valeurs
techniques sont en anglais ; les textes affichés à l'élève (messages, titres)
restent en français, langue de référence.

## 1. Le kit

`sagnac pull <lab>` dépose deux dossiers à côté l'un de l'autre :

| Chemin | Contenu |
|---|---|
| `<lab>/meta.yaml` | description du lab et de ses contrôles (section 2) |
| `<lab>/checks/` | scripts des contrôles et `messages/<langue>/<CODE>.txt` |
| `<lab>/starter/` | point de départ de l'espace de travail |
| `<lab>/Makefile` | inclut `../_cadre/lab.mk` (cibles `tools`, `setup`, `clean`) |
| `<lab>/.sagnac/tools.manifest` | présent si le lab déclare des outils |
| `_cadre/lab.mk`, `_cadre/lib/controle.sh`, `_cadre/bin/sagnac-tools` | cadre commun à tous les labs |

L'énoncé, les indices et la solution ne sont jamais dans le kit. L'archive est
un `tar.gz` déterministe ; la CLI n'accepte que des fichiers ordinaires et des
dossiers sous `<lab>/` et `_cadre/` (règles détaillées dans `internal/kit`).

`lib/controle.sh` est cherché dans `<lab>/../_cadre` puis `<lab>/../../_cadre`
(labs rangés par module). Introuvable : kit invalide.

L'espace de travail est `<lab>/workspace/`, créé par `make setup` depuis
`starter/`. La CLI n'y écrit jamais.

## 2. `meta.yaml`

Sous-ensemble de YAML : mappings, listes en bloc ou entre crochets, chaînes
entre guillemets ou nues, entiers, `true` et `false`, commentaires `#`. Pas
d'ancre, pas de chaîne sur plusieurs lignes.

Clés du lab (liste fermée) : `id`, `version`, `title`, `type` (`guide`,
`standalone`, `troubleshooting`), `difficulty`, `target_minutes`,
`objectives`, `prerequisites`, `tools`, `genai`, `gpu`, `docker`,
`network_during_check`, `max_memory_mb`, `certifiable`, `artifacts`,
`preconditions` (chacune `code`, `description`), `checks`, et `cluster`,
facultative (section 7).

Clés d'un contrôle (liste fermée) :

| Clé | Règle |
|---|---|
| `id` | `verify_<état>`, unique dans le lab |
| `step` | étape de l'énoncé ; les contrôles sont listés dans l'ordre des étapes |
| `script` | exécutable sous `checks/` |
| `timeout_s` | 1 à 60 s (240 s si `genai: true`), 10 par défaut |
| `depends_on` | contrôles déclarés avant lui |
| `codes` | codes d'échec, `MAJUSCULES_0-9`, uniques dans le lab |
| `preconditions` | codes de précondition (déclarés en tête) que ce contrôle peut rendre |
| `observed`, `expected` | type de la valeur envoyée : `boolean` ; `integer` avec `min` et `max` ; `enum` avec `values` (`[a-z0-9_]`) ; `duration_ms` |

Les cinq booléens (`genai`, `gpu`, `docker`, `network_during_check`,
`certifiable`) valent `true` ou `false`. Toute autre clé, toute valeur hors de
ces règles et tout `meta.yaml` du contrat v1 sont refusés en les nommant :
`ORACLE_INVALID_KIT`, code de sortie 3. Un fichier à moitié lu n'est jamais
exécuté.

`checks/teardown.sh`, s'il existe, est un fichier exécutable (sinon kit
invalide) : il défait ce que le passage a créé hors processus (images,
conteneurs) et tourne après le dernier contrôle (section 3).

## 3. Appel de l'oracle

Chaque `sagnac check` réévalue tous les contrôles, dans l'ordre, sans mémoire
d'un passage à l'autre : aucun verdict n'est mis en cache.

- **Dépendances.** Si un contrôle de `depends_on` n'a pas passé, le contrôle
  n'est pas lancé : statut `skip`, `blocked_by` nomme le premier contrôle en
  cause.
- **Lancement.** Le script tourne dans l'espace de travail, dans sa propre
  session (groupe de processus), avec un environnement réduit : `PATH`,
  `HOME`, `TMPDIR`, `LC_ALL=C`, `XDG_CONFIG_HOME`, et `SAGNAC_LAB`,
  `SAGNAC_CHECK`, `SAGNAC_WORKSPACE`, `SAGNAC_SEED`, `SAGNAC_RESULT`,
  `SAGNAC_LIB`, plus `SAGNAC_CLUSTER` pour un lab à cluster (section 7).
  S'y ajoutent `DOCKER_HOST` s'il est posé et commence par `unix://` (moteur
  choisi par l'élève), `KUBECACHEDIR` et `DOCKER_CONFIG` seulement s'ils sont
  sous le `TMPDIR` de la commande, et, par `GIT_CONFIG_COUNT`,
  `safe.directory=*`, `core.fsmonitor=false` et `core.hooksPath=/dev/null` :
  Git lit les dépôts de l'espace quel que soit leur propriétaire, sans leurs
  crochets. Tout ce qu'il a lancé est tué à sa fin.
- **Ramassage.** Après le dernier contrôle, `checks/teardown.sh` (s'il
  existe) défait ce que le passage a créé hors processus ; pour un lab à
  cluster, `_cadre/lib/cluster-nettoyage.sh` vide aussi l'espace du cluster
  avant le premier contrôle et après le dernier (section 7). Même
  environnement (sans `SAGNAC_CHECK` ni `SAGNAC_RESULT`) et même coupure du
  réseau que les contrôles, 120 s chacun, hors plafond et hors `duration_ms`,
  même après un délai dépassé ou une interruption. Le ramassage de fin ne
  change pas le verdict.
- **Fin du script.** Il se termine par une seule des fonctions de
  `lib/controle.sh`, qui écrit dans `SAGNAC_RESULT` des lignes `code=`,
  `observed=`, `expected=` :

| Fonction | Code du script | Statut | `origin` |
|---|---|---|---|
| `sagnac_reussite [OBS [ATT]]` | 0 | `pass` | — |
| `sagnac_echec CODE [OBS [ATT]]` | 1 | `fail` | `student` |
| `sagnac_plateforme CODE [OBS [ATT]]` | 2 | `error` | `platform` |

- **Erreurs de l'oracle** (statut `error`, `origin: oracle`) : script non
  lancé (`ORACLE_CHECK_NOT_STARTED`), délai du contrôle dépassé
  (`ORACLE_TIMEOUT`), plafond du passage atteint (`ORACLE_TOTAL_TIMEOUT`),
  autre code de script ou valeur hors du type déclaré
  (`ORACLE_INVALID_OUTPUT`), code absent de `codes` ou de `preconditions`
  (`ORACLE_UNDECLARED_CODE`), contrôle tué par une interruption
  (`ORACLE_CHECK_INTERRUPTED`).
- **Plafond.** 60 s par passage sans cluster ni GenAI, 240 s avec l'un des
  deux, 360 s pour un lab à cluster avec GenAI (section 7) ; le délai d'un
  contrôle reste borné par `timeout_s` (section 2). Au-delà, les contrôles
  restants rendent `ORACLE_TOTAL_TIMEOUT`. Le plafond ne compte que les
  contrôles : il démarre après l'attente des services ci-dessous.
- **Attente des services.** Avant le plafond, et avant de demander la session
  au serveur, `check` attend que les services du lab répondent, 180 s au plus.
  Aujourd'hui, le seul service est le cluster que déclare la clé `cluster` de
  `meta.yaml`, et il est prêt quand son API répond à `/readyz` avec
  l'identité « contrôle » (`<cache>/clusters/<nom>/kubeconfig`, jamais
  l'administrateur), comme `sagnac-cluster status`. La sonde est reprise toutes
  les 2 s (`api_prete` du cadre) ; un service qui a répondu n'est plus sondé.
  Un cluster qui n'a jamais été créé (pas de `kubeconfig` dans le cache) n'est
  pas attendu : attendre ne le créerait pas, et les contrôles rendent tout de
  suite la précondition « cluster absent » (`CLUSTER_MISSING`, code 2). Si le
  délai est dépassé, les contrôles s'exécutent quand même et rendent leurs
  propres préconditions, comme sans attente ; l'attente n'est jamais un
  verdict. Ctrl-C pendant l'attente rend 130 sans verdict. L'attente n'entre ni
  dans le plafond ni dans `duration_ms`. Les messages vont sur la sortie
  d'erreur.
- **Graine.** `SAGNAC_SEED` vient du serveur quand l'élève est connecté (une
  graine par passage), sinon d'un tirage local. Les valeurs que l'élève doit
  produire en dérivent.
- **Réseau.** Aucun appel sortant pendant `check`, sauf si le lab déclare
  `network_during_check: true` : la CLI ne coupe alors rien. Sinon, elle le
  coupe :
  `sandbox-exec` sous macOS (localhost et sockets Unix permis), `unshare -rnm`
  puis `unshare -Ur` sous Linux (le contrôle, root d'un espace de noms
  utilisateur enfant, ne peut ni démonter ni remonter). Dans les deux cas, le
  kubeconfig administrateur des clusters
  (`~/.cache/sagnac/clusters/*/admin/`) est illisible, quel que soit le lab :
  refus du profil sous macOS, `tmpfs` sous Linux, dont l'échec arrête le
  contrôle (`ORACLE_CHECK_INTERRUPTED`), jamais de contrôle sans masque. Sans
  méthode disponible, elle l'écrit sur la sortie d'erreur puis lance les
  contrôles sans coupure.

## 4. Résultat `sagnac-result/2`

`sagnac check --json` écrit un objet dont tous les champs sont présents :

| Champ | Contenu |
|---|---|
| `schema` | `sagnac-result/2` |
| `cli` | `sagnac/<version>` |
| `lab` | `id`, `version` |
| `checks_sha256` | empreinte du kit (ci-dessous) |
| `session` | identifiant de la session serveur, ou `local` |
| `seed` | graine du passage |
| `started_at`, `duration_ms` | début (UTC, à la seconde) et durée |
| `status` | `pass`, `fail` ou `error` |
| `exit_code` | code de la section 5 |
| `checks[]` | `id`, `step`, `status` (`pass`, `fail`, `error`, `skip`), `code` (pour `fail` et `error` seulement), `origin` (`student`, `platform`, `oracle`), `observed`, `expected`, `blocked_by`, `duration_ms`, dans l'ordre des étapes |
| `platform` | `os`, `architecture`, `container_engine`, `engine_version` (ci-dessous) |

Le JSON ne contient aucun texte localisé ni aucune chaîne libre lue sur le
poste : seules les valeurs typées de `meta.yaml` sortent. Il est identique
d'une langue à l'autre, horodatage et durées mis à part.

`container_engine` et `engine_version`, pour un lab `docker: true` : `docker`
ou `podman` et la version du serveur lue par `docker version` (32 caractères
au plus, `[0-9][0-9A-Za-z.+~-]*`) ; `null` sinon, ou si le moteur ne répond
pas. `duration_ms` ne compte pas le ramassage (section 3).

`checks_sha256` : SHA-256 de la suite, pour `meta.yaml` puis chaque fichier de
`checks/` dans l'ordre des chemins (parcours en profondeur, noms triés), de
`chemin NUL` (chemin relatif au lab, `checks/…` avec des `/`), `x` ou `-`
(droit d'exécution), puis SHA-256 du contenu. Sont exclus : `checks/mutants/`,
`checks/RESULTAT.md`, tout dossier `__pycache__`, les fichiers `.DS_Store` et
toute entrée qui n'est pas un fichier ordinaire (lien symbolique, socket…). Le serveur refuse une tentative dont l'empreinte ne correspond à aucun
kit publié.

Le verdict local est formatif : l'élève contrôle sa machine. Une tentative
envoyée par la CLI est enregistrée comme déclarée.

## 4 bis. Envoi du résultat

Connecté, `sagnac check` demande une session (`POST /v1/sessions`) avant les
contrôles, puis envoie le résultat (`POST /v1/attempts`) avec une clé
d'idempotence. Hors connexion, rien n'est envoyé.

| Réponse | Session (avant les contrôles) | Tentative (après) |
|---|---|---|
| réseau coupé, délai dépassé, 5xx | contrôles locaux, résultat mis en file | mis en file |
| 429 `TOO_MANY_REQUESTS` | contrôles locaux, rien d'envoyé ni mis en file ; message qui dit d'attendre le délai de `Retry-After` | mis en file comme un 5xx ; message qui cite le délai de `Retry-After` |
| autre 4xx | contrôles locaux, message selon le code ; rien en file | message selon le code ; rien en file |

La file (`$XDG_STATE_HOME/sagnac/queue/`) garde une tentative 30 jours ; elle
repart au `sagnac check` suivant avec la même clé. Pendant ce renvoi, un 429,
un 5xx ou une panne réseau gardent l'entrée et arrêtent le renvoi ; tout autre
4xx la supprime avec un avertissement. `sagnac logout` et un nouveau
`sagnac login` vident la file.

Le délai de `Retry-After` (secondes) est affiché, jamais attendu : la limite
de la plateforme se compte par heure, et `check` ne bloque pas l'élève pour un
envoi.

### Voir ce qui est envoyé

`sagnac check --show-payload` lance les contrôles ici, avec une graine locale,
puis affiche les deux requêtes qu'un `sagnac check` connecté enverrait
(`POST /v1/sessions`, `POST /v1/attempts`) : méthode, adresse, en-têtes et
corps. Il ne contacte personne : ni session, ni envoi, ni connexion, ni file,
et pas même la lecture quotidienne du fichier de version. Les requêtes sont
construites par le code qui les envoie (`internal/attempt`) : mêmes en-têtes,
même corps, octet pour octet, hors ce que seul le serveur ou l'horloge décide
(`session` et `seed` du résultat, `started_at`, durées). Le jeton n'est jamais
affiché (`Authorization: Bearer <jeton de votre connexion>`), ni la clé
d'idempotence, tirée au moment de l'envoi. `--json` donne `{"requests": [...]}`
sur la sortie standard. `--session` est refusé avec cette option (code 64). Le
code de sortie est celui de l'oracle.

## 5. Codes de sortie

Mêmes codes pour toutes les commandes :

| Code | Sens |
|---|---|
| 0 | réussite ; pour `check`, tous les contrôles passent |
| 1 | refus : l'élève ou son compte doivent agir ; pour `check`, au moins un échec de l'élève |
| 2 | l'environnement ne permet pas l'opération maintenant (réseau, proxy, certificat, délai, 429, 5xx) ; pour `check`, précondition de plateforme absente |
| 3 | erreur de stockage ou imprévue ; pour `check`, erreur de l'oracle ou kit invalide |
| 64 | mauvaise utilisation |
| 130 | interruption avant la fin du travail de la commande ; aucune écriture ni aucun envoi commencés ne sont laissés à moitié ; pour `check`, aucun verdict |

Pour `check`, plusieurs causes se cumulent dans cet ordre : 3, puis 2, puis 1.
L'échec de l'envoi ne change jamais le code de `check`, et une interruption
après l'affichage du verdict garde ce code.

Un 429 sur la session ou la tentative ne change donc pas le code de `check` :
le code dit ce que les contrôles ont trouvé dans l'espace de travail, et un
script qui enchaîne `sagnac check` doit pouvoir s'y fier quel que soit l'état
de la plateforme ; le 429 se lit dans le message. Les autres commandes rendent
2 sur un 429, comme sur un 5xx : l'opération n'a pas eu lieu et ne dépend pas
d'une action de l'élève.
Seule exception : `KIT_DOWNLOAD_LIMIT` (429, plafond de kits différents sur
24 heures) rend 1, car le compte doit attendre la sortie d'un kit de la
fenêtre (`docs/api/erreurs.md` de la plateforme).

## 5 bis. `sagnac clean`

`sagnac clean` supprime le cluster du lab courant (celui que déclare la clé
`cluster` de son `meta.yaml`) et les fichiers laissés à moitié écrits par un
téléchargement ou une copie interrompus (`*.partiel.<pid>` dans le cache,
`.workspace.partiel.<pid>` dans le lab). `--all` vise tous les clusters du cache
et tous les labs du dossier courant ; `--dry-run` affiche ce qui serait supprimé
sans rien supprimer.

- **Même effet que le cadre.** Le cluster est supprimé par le script du kit,
  `_cadre/bin/sagnac-cluster delete <nom>`, le même que `make cluster-delete` :
  la CLI ne refait pas la suppression des conteneurs, du réseau et des volumes
  du cluster. Sans ce script, le cluster est signalé (code 1) et son dossier
  d'état est gardé : le supprimer seul laisserait le cluster sans moyen de
  l'atteindre.
- **Gardé.** Modèles, binaires des outils, images de base (`$SAGNAC_CACHE`, sinon
  `$HOME/.cache/sagnac`, comme le cadre : `XDG_CACHE_HOME` est ignoré), le
  dossier `workspace/` de l'élève et la file des résultats en attente. À la
  différence de `make clean`, `sagnac clean` ne supprime jamais `workspace/`.
- **Chemins.** Le cache est résolu (liens suivis) et doit être absolu ; la
  racine, le dossier personnel et tout dossier qui le contient sont refusés, de
  même qu'un chemin vide ou relatif, et un nom de cluster hors de
  `[a-z0-9][a-z0-9-]*`. Un lien symbolique du cache n'est jamais suivi. Chaque
  fichier temporaire est vérifié un à un avant sa suppression : rien n'est
  supprimé hors du cache de Sagnac ou du dossier du lab.
- **Codes.** 0 réussite ; 1 chemin refusé ou script du kit introuvable ; 2
  suppression du cluster impossible (moteur de conteneurs arrêté) ; 3 erreur
  de stockage ; 64 mauvaise utilisation ; 130 interruption.

## 6. Messages

Le message d'un code est lu dans `<lab>/checks/messages/<langue>/<CODE>.txt`,
puis dans `_cadre/messages/<langue>/` s'il existe, avec repli sur le français
à chaque niveau. `{observed}` et `{expected}` y sont remplacés par les valeurs
typées. Sans fichier, la CLI affiche le code.

## 7. Labs à cluster

Un lab déclare `cluster: <nom>` pour travailler sur un cluster Kubernetes
local, partagé entre les labs d'un parcours (`sagnac-b1` pour B1-M2 à M6).
`make setup` du kit le crée ou le démarre (`_cadre/bin/sagnac-cluster`) ;
`sagnac check` ne le crée jamais.

- **Kit.** `<nom>` (`[a-z0-9][a-z0-9-]{0,31}`) désigne
  `_cadre/clusters/<nom>.conf`, lignes `CLE=valeur` (section 7 bis) dont
  `API_PORT` (1 à 65535) donne le port de l'API sur `127.0.0.1`. Le lab
  déclare aussi `docker: true`, dans `tools` au moins `k3d`, `kubectl`, `k3s`,
  `k3s-airgap` et `k3d-tools`, et les préconditions `CLUSTER_DIRTY` et
  `CLUSTER_BUSY` (avec leurs messages). Sinon : `ORACLE_INVALID_KIT`.
- **Verrou.** Un seul `check` à la fois par cluster : la CLI prend sans
  attendre `flock` sur `~/.cache/sagnac/clusters/<nom>/check.lock` (créé par
  `make setup`). S'il est tenu, elle ne ramasse ni ne lance rien et chaque
  contrôle rend `CLUSTER_BUSY` (`error`, `platform`, code 2). Sans ce
  fichier (cluster jamais installé), le passage continue et les contrôles le
  disent (`CLUSTER_MISSING`).
- **Ramassage d'ouverture.** Si `cluster-nettoyage.sh` échoue ou dépasse son
  délai avant le premier contrôle (objets restés d'un passage interrompu),
  aucun contrôle n'est lancé : chacun rend `CLUSTER_DIRTY` (`error`,
  `platform`, code 2).
- **Coupure du réseau.** Les contrôles parlent au cluster par la seule API
  Kubernetes (`sagnac_kubectl`, identité « contrôle »), jamais par le socket
  du moteur de conteneurs. macOS : l'API est sur `127.0.0.1:<port>`, permis
  par le profil, qui retire pour ce lab les sockets Unix. Linux : l'espace de
  noms réseau du contrôle n'a pas le `127.0.0.1` de l'hôte ; la CLI ouvre
  hors de cet espace un socket Unix dans un dossier privé (0700, sous
  `TMPDIR`, ou `/tmp` si le chemin est trop long) relié au seul port de
  l'API, et relance son propre binaire dans l'espace du contrôle
  (`sagnac __cluster-relay`, commande cachée) qui écoute `127.0.0.1:<port>`,
  renvoie vers ce socket (32 connexions simultanées au plus) et lance le
  contrôle. Rien d'autre ne passe : ni Internet, ni un autre port de l'hôte,
  ni le moteur (`/run` et `/var/run` recouverts d'un `tmpfs`). Le kubeconfig
  reste valide tel quel. Le ramassage passe par la même enveloppe.
- **Plafond.** Un passage d'un lab à cluster a 240 s, 360 s avec GenAI,
  quel que soit le contrat du cluster : ses contrôles démarrent des charges
  sur le cluster avant de juger quoi que ce soit. `timeout_s` de chacun
  reste à 60 s au plus, 240 s avec GenAI (section 2).

## 7 bis. Contrat du cluster (v2, v3)

La version du contrat est portée par le cluster, jamais par `meta.yaml` :
`_cadre/clusters/<nom>.conf` sans ligne `CONTRAT` est un cluster du contrat
v2 (`sagnac-b1`), avec `CONTRAT=3` un cluster du contrat v3 (`sagnac-b3`,
B3-M3 à M5).

- **Lecture de la conf.** Lignes vides et lignes qui commencent par `#`
  ignorées. Seules les clés `API_PORT` et `CONTRAT`, chacune une fois au
  plus, valeur prise telle quelle (sans retrait d'espaces) ; `CONTRAT`
  absent ou `3`. Toute autre ligne ou valeur (`CONTRAT=9`, clé d'un contrat
  plus récent, clé en double, ligne indentée) refuse le kit :
  `ORACLE_INVALID_KIT`, code 3, avant tout contrôle, sans session ni envoi.
  Une CLI qui ne connaît pas le contrat d'un cluster ne juge jamais ses
  labs ; la mettre à jour règle le refus.
- **Contrat v3.** Le code de l'élève ne tourne plus sur le poste pendant
  `check` : les contrôles le placent dans des pods des espaces élèves du
  cluster, sous les seuls droits du RBAC de l'élève. Pour la CLI, rien ne
  change dans l'appel des contrôles, le ramassage (`lib/cluster-nettoyage.sh`
  reconnaît lui-même le v3), le verrou ni la coupure du réseau ; seul le
  port de l'API diffère. Les contrôles d'un lab v3 utilisent `python3` du
  poste (`lib/k8s_v3.py`, code du cadre) ; son absence est une précondition
  du lab, pas une erreur de la CLI.
- **Kit v3.** En plus des fichiers de la section 7, le kit porte
  `_cadre/clusters/v3/` (politique d'audit, garde-fous, socle, gabarits),
  que `_cadre/bin/sagnac-cluster` copie dans
  `~/.cache/sagnac/clusters/<nom>/v3/` à la création du cluster. La CLI les
  dépose tels quels et ne les lit pas.
