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
`preconditions` (chacune `code`, `description`), `checks`.

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
  `SAGNAC_LIB`. Tout ce qu'il a lancé est tué à sa fin.
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
- **Plafond.** 60 s par passage, 240 s pour un lab GenAI ; au-delà, les
  contrôles restants rendent `ORACLE_TOTAL_TIMEOUT`.
- **Graine.** `SAGNAC_SEED` vient du serveur quand l'élève est connecté (une
  graine par passage), sinon d'un tirage local. Les valeurs que l'élève doit
  produire en dérivent.
- **Réseau.** Aucun appel sortant pendant `check`, sauf si le lab déclare
  `network_during_check: true` : la CLI ne coupe alors rien. Sinon, elle le
  coupe :
  `sandbox-exec` sous macOS (localhost et sockets Unix permis), `unshare -rn`
  sous Linux. Sans méthode disponible, elle l'écrit sur la sortie d'erreur puis
  lance les contrôles sans coupure.

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
| `platform` | `os`, `architecture`, `container_engine`, `engine_version` |

Le JSON ne contient aucun texte localisé ni aucune chaîne libre lue sur le
poste : seules les valeurs typées de `meta.yaml` sortent. Il est identique
d'une langue à l'autre, horodatage et durées mis à part.

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

## 6. Messages

Le message d'un code est lu dans `<lab>/checks/messages/<langue>/<CODE>.txt`,
puis dans `_cadre/messages/<langue>/` s'il existe, avec repli sur le français
à chaque niveau. `{observed}` et `{expected}` y sont remplacés par les valeurs
typées. Sans fichier, la CLI affiche le code.
