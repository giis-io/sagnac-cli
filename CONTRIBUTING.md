# Contribuer à sagnac

Merci de proposer une correction ou une amélioration. Ce document dit comment
préparer une demande de fusion (pull request) qui pourra être relue.

Une vulnérabilité ne se signale pas par un ticket ni par une demande de fusion
publique : suivez `SECURITY.md` (securite@sagnac.fr ou signalement privé
GitHub).

## Avant d'écrire du code

Pour un changement de comportement, ouvrez d'abord un ticket qui décrit le
problème : la commande, ce que vous attendiez, ce que vous avez obtenu. Une
correction de faute ou de test peut venir directement en demande de fusion.

Ce que la CLI lit dans un kit de lab et ce qu'elle rend est fixé par
`docs/contrat.md`. Un changement qui touche ce contrat (clés de `meta.yaml`,
champs du résultat, codes de sortie) ne peut pas être accepté seul : la
plateforme doit suivre. Décrivez-le dans un ticket.

## Langue

- Documentation, messages affichés et messages de commit : en français.
- Code, commentaires du code, noms de tests : en anglais.
- Mots-clés techniques (commandes, options, codes d'erreur, clés JSON et YAML) :
  en anglais, partout.

Messages de commit : `type: message`, avec `type` parmi `feat`, `fix`,
`docs`, `test`, `refactor`, `ci`, `chore`. Un commit fait une seule chose.

## Lancer les tests

Il faut Go 1.26.10 (fixé dans `go.mod`), `make`, `git` et un shell POSIX ;
`python3` ou `perl` et `curl` permettent à quelques tests de ne pas être sautés.

```
go vet ./...
GOOS=linux go vet ./...
GOOS=darwin go vet ./...
go test -race -count=1 ./...
make vuln
scripts/tests.sh
```

`go test` ne demande rien d'extérieur au dépôt : les tests de conformité
tournent sur les kits figés de `testdata/`. Ces fichiers sont produits par la
plateforme ; ne les modifiez pas à la main (`testdata/README.md`).

La CI relance ces commandes sur chaque demande de fusion
(`.github/workflows/pr.yml`). Pour une première contribution, un mainteneur
approuve le lancement des workflows avant qu'ils tournent.

## Publier une version

Réservé aux mainteneurs. Une version naît d'une étiquette posée sur `main` ;
le fichier `VERSION` doit déjà porter le même numéro `MAJ.MIN.CORR`.

- `vMAJ.MIN.CORR` : la version. La page est remplie en brouillon puis publiée.
- `vMAJ.MIN.CORR-rc.N` : une répétition. Mêmes étapes, mais la page reste un
  brouillon visible des seuls mainteneurs (pré-version) ; on la supprime à la
  main après contrôle.

1. Fusionner la demande de fusion qui met `VERSION` à jour.
2. Poser et pousser l'étiquette : `git tag v0.1.0-rc.1 && git push origin v0.1.0-rc.1`
   (répétition) ou `git tag v0.1.0 && git push origin v0.1.0`.
3. Depuis le poste du fondateur, dans un clone du dépôt académie, avec le jeton
   Hetzner du projet `sagnac-usine` et un jeton GitHub à grain fin limité à
   `giis-io/sagnac-cli` (« Administration » et « Actions » en écriture), créé
   juste avant et révoqué juste après :

   ```
   SAGNAC_DEPOT=giis-io/sagnac-cli infra/usine/bin/runner-ephemere.sh executer release release.yml v0.1.0
   ```

   Le script tire une étiquette unique `sagnac-release-<date>-<aléa>`, lance
   `.github/workflows/release.yml` sur l'étiquette avec `-f runner=<étiquette>`,
   attend l'approbation de l'environnement `release` (onglet Actions du dépôt,
   « Review deployments »), crée alors une machine jetable enregistrée pour ce
   seul job, puis la détruit.
4. Le job unique `release`, sur la machine jetable seulement, construit les
   quatre binaires (`darwin` et `linux`, `arm64` et `amd64`), les signe et les
   fait notariser pour macOS si les secrets `APPLE_*` existent, produit les
   archives `sagnac_<version>_<système>_<architecture>.tar.gz`, une SBOM
   CycloneDX 1.7 par archive (`sagnac_<version>_<système>_<architecture>.cdx.json`,
   `cyclonedx-gomod` épinglé par empreinte), `install.sh` et `SHA256SUMS`, qui
   liste aussi les SBOM, signe `SHA256SUMS` avec cosign sans clé
   (`SHA256SUMS.sigstore.json`) et vérifie cette signature. Si le dépôt est
   public, il ajoute une attestation de provenance pour chaque archive et pour
   `install.sh`, et une attestation de SBOM pour chaque archive. Il crée la page
   en brouillon, y dépose les 11 fichiers (10 pour une répétition non signée
   d'un dépôt privé), en vérifie le nombre contre celui de `dist/`, puis la
   publie (version) ou la laisse en brouillon (répétition).

Pour essayer `install.sh` sur une répétition (brouillon, dépôt privé), on
télécharge ses fichiers avec un compte de l'organisation puis on installe
depuis ce dossier :

```
gh release download v0.1.0-rc.1 -R giis-io/sagnac-cli -D rc1
SAGNAC_RELEASE_DIR=rc1 SAGNAC_VERSION=0.1.0-rc.1 sh rc1/install.sh
```

Prérequis sur `giis-io/sagnac-cli` :

- l'environnement `release`, avec un reviewer requis et les étiquettes `v*`
  pour seule règle de déploiement ;
- pour la notarisation (facultative ; sans elle la version sort sans) : les
  secrets d'environnement `APPLE_DEVELOPER_ID_P12` (certificat « Developer ID
  Application » de GIIS, `.p12` en base64), `APPLE_DEVELOPER_ID_P12_PASSWORD`,
  `APPLE_NOTARY_KEY` (clé d'API App Store Connect, contenu du `.p8`),
  `APPLE_NOTARY_KEY_ID` et `APPLE_NOTARY_ISSUER_ID`.

Seul `release.yml` peut écrire dans le dépôt, demander un jeton OIDC et lire
des secrets (les seuls `APPLE_*`), et seulement sur le runner jetable :
`scripts/verifier-workflows.sh` refuse ces droits ailleurs, impose aux contrôles
d'une demande de fusion le runner hébergé par GitHub `ubuntu-24.04` (une
machine neuve par job), refuse tout runner auto-hébergé de GIIS hors du runner
jetable de publication, et refuse à `release.yml` tout autre déclencheur que le lancement
manuel et tout autre job que `release`. Pour vérifier une version à la main,
voir « Installer » dans `README.md`.

## Certificat d'origine (DCO)

Chaque commit porte une ligne `Signed-off-by` à votre nom, avec l'adresse de
l'auteur du commit. Elle certifie que vous avez le droit de proposer ce code
sous la licence du projet (Apache 2.0, fichier `LICENSE`), selon le Developer
Certificate of Origin 1.1 ci-dessous. Il n'y a pas d'accord de contribution à
signer en plus.

```
git commit -s -m "fix: ..."
```

`-s` ajoute la ligne à partir de `user.name` et `user.email` de git. Pour un
commit déjà fait : `git commit --amend -s --no-edit` ; pour toute une branche :
`git rebase --signoff main`. La CI refuse une demande de fusion dont un commit
n'est pas signé par son auteur (`scripts/verifier-dco.sh`, que vous pouvez
lancer vous-même : `scripts/verifier-dco.sh main HEAD`).

Texte du certificat, tel que publié sur https://developercertificate.org/ :

```
Developer Certificate of Origin
Version 1.1

Copyright (C) 2004, 2006 The Linux Foundation and its contributors.

Everyone is permitted to copy and distribute verbatim copies of this
license document, but changing it is not allowed.


Developer's Certificate of Origin 1.1

By making a contribution to this project, I certify that:

(a) The contribution was created in whole or in part by me and I
    have the right to submit it under the open source license
    indicated in the file; or

(b) The contribution is based upon previous work that, to the best
    of my knowledge, is covered under an appropriate open source
    license and I have the right under that license to submit that
    work with modifications, whether created in whole or in part
    by me, under the same open source license (unless I am
    permitted to submit under a different license), as indicated
    in the file; or

(c) The contribution was provided directly to me by some other
    person who certified (a), (b) or (c) and I have not modified
    it.

(d) I understand and agree that this project and the contribution
    are public and that a record of the contribution (including all
    personal information I submit with it, including my sign-off) is
    maintained indefinitely and may be redistributed consistent with
    this project or the open source license(s) involved.
```

## Relecture

Une demande de fusion est relue quand la CI est verte. Elle peut être refusée
si elle élargit ce que la CLI envoie à la plateforme, ajoute une dépendance
sans raison forte ou change le contrat sans ticket préalable. Chaque refus est
expliqué.
