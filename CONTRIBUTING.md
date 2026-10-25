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

Réservé aux mainteneurs. Une version naît d'une étiquette `vMAJ.MIN.CORR` posée
sur `main` ; le fichier `VERSION` doit déjà porter le même numéro.

1. Fusionner la demande de fusion qui met `VERSION` à jour.
2. Poser et pousser l'étiquette : `git tag v0.1.0 && git push origin v0.1.0`.
3. Depuis le poste du fondateur, dans un clone du dépôt académie (même
   procédure que la release de la plateforme), avec un jeton GitHub à grain fin
   limité à `giis-io/sagnac-cli` (« Administration » et « Actions » en
   écriture), créé juste avant et révoqué juste après :

   ```
   SAGNAC_DEPOT=giis-io/sagnac-cli infra/usine/bin/runner-ephemere.sh executer release release.yml v0.1.0
   ```

   Le script tire une étiquette unique `sagnac-release-<date>-<aléa>`, lance
   `.github/workflows/release.yml` sur l'étiquette avec `-f runner=<étiquette>`,
   attend l'approbation de l'environnement `release` (à approuver sur GitHub),
   crée alors une machine jetable enregistrée pour ce seul job, puis la détruit.
4. Le job `build`, en lecture seule, tourne sur le groupe `giis-usine-lourd` et
   produit les archives (`sagnac_<version>_<système>_<architecture>.tar.gz` pour
   `darwin` et `linux`, `arm64` et `amd64`) et `SHA256SUMS`. Le job `publish`,
   sur la machine jetable seulement, signe `SHA256SUMS` avec cosign sans clé
   (`SHA256SUMS.sigstore.json`), vérifie la signature qu'il vient de faire, puis
   crée la page de la version avec ces fichiers et la formule `sagnac.rb`.
5. Copier `sagnac.rb` dans `Formula/sagnac.rb` du dépôt `giis-io/homebrew-tap`,
   puis `brew audit --strict --online giis-io/tap/sagnac`.

Prérequis : l'environnement `release` existe sur `giis-io/sagnac-cli` (reviewer
requis, étiquettes `v*` seulement) et la machine jetable dispose de `gh` et de
`curl`.

Le workflow n'utilise aucun secret. Seul `release.yml` peut écrire dans le dépôt
et demander un jeton OIDC, et seulement sur le runner jetable :
`scripts/verifier-workflows.sh` refuse ces droits ailleurs, refuse à tout job
qui les demande de tourner sur `giis-usine-lourd`, et refuse à `release.yml`
tout autre déclencheur que le lancement manuel. Pour vérifier une version à la
main, voir « Installer » dans `README.md`.

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
