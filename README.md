# sagnac

> **Version de développement, pas encore publiée.** Aucune version n'est encore
> distribuée : le code évolue sans préavis. Pour signaler une vulnérabilité, voir
> [SECURITY.md](SECURITY.md).

`sagnac` est la ligne de commande de la formation Sagnac. Elle s'installe sur le
poste de l'élève, lance les vérifications d'un lab et affiche le résultat.

Disponibles : `login`, `logout`, `pull`, `open`, `check`, `clean`, `purge`, `doctor`, `version`, `help`.
Les commandes `submit` et `update` arrivent ensuite.

## Installer

Aucune version n'est encore publiée. À partir de la première, chaque version
est publiée sur la page des versions du dépôt avec ses sommes de contrôle
SHA-256 (`SHA256SUMS`), une signature de ce fichier et la nomenclature
logicielle (SBOM CycloneDX) de chaque archive, `sagnac_<version>_<système>_<architecture>.cdx.json`,
elle aussi listée dans `SHA256SUMS`. Aucune méthode
d'installation de `sagnac` ne passe par `curl ... | sudo bash`, et vous ne
devez en accepter aucune qui le demande : un programme installé avec `sudo`
sans avoir été lu ni vérifié a tous les droits sur votre machine.

### En une ligne (macOS et Linux)

```
curl -fsSLO https://github.com/giis-io/sagnac-cli/releases/latest/download/install.sh && sh install.sh
```

La commande télécharge le script `install.sh` puis le lance : vous pouvez le
lire avant (`less install.sh`). Il choisit l'archive de votre machine (`darwin`
ou `linux`, `arm64` ou `amd64`), vérifie sa somme SHA-256 dans `SHA256SUMS`,
vérifie la signature de `SHA256SUMS` si [cosign](https://docs.sigstore.dev/cosign/installation/)
est installé, puis copie `sagnac` dans `~/.local/bin`, sans `sudo`. Il
n'installe rien si une vérification échoue.

- Une version précise : `SAGNAC_VERSION=0.1.0 sh install.sh`.
- Un autre dossier : `SAGNAC_INSTALL_DIR=$HOME/bin sh install.sh`.

`install.sh` figure lui aussi dans `SHA256SUMS`. Une installation par Homebrew
viendra avec une version suivante.

### Téléchargement vérifié

Les commandes ci-dessous n'utilisent pas `sudo` : le binaire va dans
`~/.local/bin`. Choisissez la version, le système (`darwin` ou `linux`) et
l'architecture (`arm64` ou `amd64`, `uname -m`).

```
VERSION=0.1.0
OS=darwin
ARCH=arm64
BASE=https://github.com/giis-io/sagnac-cli/releases/download/v$VERSION
curl -fsSLO "$BASE/sagnac_${VERSION}_${OS}_${ARCH}.tar.gz"
curl -fsSLO "$BASE/SHA256SUMS"
curl -fsSLO "$BASE/SHA256SUMS.sigstore.json"
```

1. Vérifiez que `SHA256SUMS` a été signé par le workflow de publication de ce
   dépôt (signature « keyless » de [cosign](https://docs.sigstore.dev/cosign/installation/),
   sans clé à gérer : l'identité est celle du workflow GitHub Actions) :

   ```
   cosign verify-blob \
     --bundle SHA256SUMS.sigstore.json \
     --certificate-identity "https://github.com/giis-io/sagnac-cli/.github/workflows/release.yml@refs/tags/v$VERSION" \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     SHA256SUMS
   ```

   La réponse attendue est `Verified OK`. Toute autre réponse : n'installez pas.

2. Vérifiez la somme de l'archive (Linux, puis macOS) :

   ```
   grep "  sagnac_${VERSION}_${OS}_${ARCH}.tar.gz\$" SHA256SUMS | sha256sum -c -
   grep "  sagnac_${VERSION}_${OS}_${ARCH}.tar.gz\$" SHA256SUMS | shasum -a 256 -c -
   ```

   La réponse attendue est `...tar.gz: OK` (ou `: Réussi` selon la langue).

3. Installez, sans `sudo` :

   ```
   tar -xzf "sagnac_${VERSION}_${OS}_${ARCH}.tar.gz" sagnac
   install -d ~/.local/bin
   install -m 0755 sagnac ~/.local/bin/sagnac
   sagnac version
   ```

   `~/.local/bin` doit être dans votre `PATH`. Sans `cosign`, l'étape 2 seule
   garantit que l'archive est celle du fichier `SHA256SUMS` téléchargé ; seule
   l'étape 1 garantit que ce fichier vient de GIIS.

### Depuis les sources

On construit `sagnac` soi-même, depuis un clone du dépôt. Il faut Go 1.26.10
(version fixée dans `go.mod`) et `make`.

```
make build
./bin/sagnac version
```

`bin/sagnac` est un fichier autonome : copiez-le dans un dossier de votre `PATH`
(par exemple `~/.local/bin`). Un simple `go build` ne convient pas : il laisse
vides la date de fin de support et les coordonnées de l'éditeur, et `sagnac`
refuse alors de démarrer.

## Kits de labs

Les kits que télécharge `sagnac pull` ne sont pas encore signés. `sagnac`
vérifie l'empreinte SHA-256 annoncée par la plateforme, ce qui protège contre un
téléchargement corrompu mais pas contre une plateforme compromise. La
vérification de signature viendra avant toute publication.

## Construire

`make build` produit un binaire statique (`CGO_ENABLED=0`, `-trimpath`) et lit la
date de fin de support, l'éditeur et le contact sécurité dans `support.yaml`. La
construction échoue si l'une de ces valeurs manque.

La version vient du fichier `VERSION`, suivie du commit (`0.1.0+27f6614`) ; sur
une étiquette de publication (`v0.1.0`), c'est l'étiquette exacte.

## Commandes

| Commande | Effet |
|---|---|
| `sagnac login` | Connecte cet ordinateur à votre compte (code à saisir dans le navigateur). Vide la file des résultats en attente et les copies hors ligne des énoncés laissées par la connexion précédente. |
| `sagnac logout` | Déconnecte cet ordinateur, révoque ses jetons, vide la file des résultats en attente et supprime les copies hors ligne des énoncés. |
| `sagnac pull LAB` | Télécharge le kit d'un lab dans le dossier courant et vérifie son empreinte. Un compte télécharge au plus 30 kits différents sur 24 heures (un kit repris dans la fenêtre ne compte pas) ; au-delà, la commande donne le plafond et l'heure locale de reprise, et s'arrête avec le code 1, sans nouvel essai. Les kits déjà présents restent utilisables. |
| `sagnac open LAB` | Ouvre l'énoncé d'un lab et en garde une copie lisible hors ligne. Si la plateforme limite la lecture des énoncés (trop de demandes, ou trop d'énoncés nouveaux sur 24 heures), la commande donne l'heure locale de reprise et s'arrête avec le code 2, sans nouvel essai ; les copies déjà récupérées restent lisibles. |
| `sagnac check [LAB]` | Vérifie le dossier `workspace/` du lab, affiche le résultat et l'envoie à votre compte comme tentative déclarée. Sans réseau, le résultat attend dans `$XDG_STATE_HOME/sagnac/queue/` (30 jours). `--json` : le résultat en JSON. |
| `sagnac check --show-payload` | Lance les contrôles, puis affiche les deux requêtes que `sagnac check` enverrait à la plateforme (`POST /v1/sessions`, `POST /v1/attempts`), en-têtes et corps compris, sans rien envoyer ni contacter. Le jeton n'est jamais affiché. Voir `docs/contrat.md`, section 4 bis. |
| `sagnac clean [--all] [--dry-run]` | Supprime le cluster du lab courant (tous les clusters avec `--all`) et les fichiers temporaires du cache de Sagnac. Garde les modèles, les outils, les images de base, votre `workspace/` et les résultats en attente. `--dry-run` affiche ce qui serait supprimé. Voir `docs/contrat.md`, section 5 bis. |
| `sagnac purge` | Supprime les résultats en attente d'envoi. |
| `sagnac doctor` | Signale une version plus récente, un avis de sécurité et la fin de support proche. Fonctionne sans compte. |
| `sagnac doctor --share` | Fait le même diagnostic et envoie le rapport à Sagnac (`POST /v1/doctor/reports`, schéma `sagnac-doctor/1`) : version de `sagnac`, système, architecture, et pour chaque point un identifiant, un état et un code. Ni compte, ni jeton, ni chemin, ni nom d'utilisateur ou de poste. Affiche l'identifiant du rapport, à donner au support. Si l'envoi échoue, le rapport reste affiché : code 2 si la plateforme limite les envois ou ne répond pas, 3 si elle refuse le rapport. |
| `sagnac version` | Affiche la version, la date de fin de support, l'éditeur et le contact sécurité. |
| `sagnac help` | Affiche l'aide. |

`--session` (avec `pull`, `open`, `check`) garde la connexion en mémoire le temps
de la commande : rien n'est écrit sur le disque.

Un résultat en attente n'est envoyé que par la connexion qui l'a produit. Quand
la plateforme met fin à la connexion (30 jours sans usage, appareil retiré
depuis le site), la file et les copies hors ligne des énoncés sont supprimées :
relancez `sagnac check` après vous être reconnecté.

Au plus une fois par jour, `sagnac` lit le fichier public `cli/version.json` du site
(`SAGNAC_VERSION_URL` pour un autre serveur). La requête ne porte que la version de la CLI (`User-Agent: sagnac/<version>`, comme toutes les requêtes
vers la plateforme), jamais de jeton. La réponse est gardée dans `$XDG_CACHE_HOME/sagnac/version.json` ; hors
ligne, la commande continue sans message, et `sagnac doctor` dit que la vérification est
impossible. Un avis de sécurité ou une fin de support à moins de six mois s'affichent sur
la sortie d'erreur à chaque lancement, sans changer le code de sortie. L'aide
(`sagnac help`, `-h`, aucune commande) et les commandes inconnues répondent tout
de suite : elles lisent la copie gardée, sans interroger le réseau.

## Ce que `sagnac check` envoie

Connecté, `sagnac check` envoie deux requêtes à la plateforme : `POST /v1/sessions`
(le lab, sa version et l'empreinte de ses contrôles, pour obtenir une graine) puis
`POST /v1/attempts` (le résultat : verdict ; pour chaque contrôle, son identifiant, son
statut, son code d'échec et les valeurs typées qu'il a relevées ; durées, graine, empreinte
des contrôles, système, architecture, moteur de conteneurs et version de la CLI).
Aucun fichier de votre espace de
travail n'y figure. `sagnac check --show-payload` les affiche exactement, sans
rien envoyer.

L'option `--lang` choisit la langue des messages. Sans elle, `sagnac` lit
`LC_ALL`, `LC_MESSAGES` puis `LANG`. Le français est la langue de référence et
le repli pour toute langue absente.

## Codes de sortie

Mêmes codes pour toutes les commandes (`login`, `logout`, `pull`, `open`,
`check`, `clean`, `purge`, `doctor`, `version`, `help`) :

| Code | Sens |
|---|---|
| 0 | réussite |
| 1 | refus : vous ou votre compte devez agir (droit inactif, lab inconnu, connexion à refaire, dossier du lab déjà présent ou cadre différent, plafond de kits sur 24 heures atteint, contrôles de `check` en échec, point à corriger signalé par `doctor`) |
| 2 | l'environnement ne permet pas l'opération maintenant : réseau coupé, proxy, certificat refusé, délai dépassé, trop de demandes (429), serveur occupé ou en erreur (5xx) ; pour `check`, précondition de plateforme absente |
| 3 | erreur de stockage (disque, droits, trousseau) ou erreur imprévue (réponse du serveur hors contrat, kit refusé, erreur interne) |
| 64 | mauvaise utilisation (commande, option ou argument inconnu) |
| 130 | interruption (Ctrl-C, SIGTERM) avant la fin du travail de la commande ; rien n'est laissé à moitié écrit ni à moitié envoyé. Une commande qui a fini rend son propre code |

Le code 2 dit que l'environnement ne permet pas l'opération (réseau, proxy,
certificat, serveur) ; le code 1 demande une action de votre part. Pour
`check`, le code est celui de la vérification : l'échec de l'envoi ne le
change jamais, et une interruption pendant l'envoi, une fois le verdict
affiché, garde ce code.

## Fabricant

GIIS, société française. Coordonnées complètes du fabricant : https://sagnac.fr/mentions-legales
Contact sécurité : securite@sagnac.fr, voir [SECURITY.md](SECURITY.md).

## Licence

Apache-2.0, voir [LICENSE](LICENSE) et [NOTICE](NOTICE).
