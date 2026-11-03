# Kits figés des tests de conformité

Ces fichiers sont produits par la plateforme, pas écrits à la main. Ne les
modifiez pas : un test qui ne passe plus signale un écart entre la CLI et le
contrat (`docs/contrat.md`), pas une fixture à corriger.

## `hello-v5/`

Le lab `hello`, version 5, contrat v2.

`hello` est le lab de démonstration de la formation : dix minutes, gratuit, un
script shell à écrire. Sa solution de référence et six mauvaises
solutions typiques sont publiées ici volontairement : sans elles, les tests ne
pourraient pas vérifier le cas où tout passe ni les échecs précis attendus.
Les fichiers des espaces sont copiés tels quels du lab ; le commentaire « Jamais
visible de l'élève » de la solution (et des mutants qui en dérivent) vaut pour la
formation, pas pour ce dépôt. Aucun autre lab n'est publié de cette façon.

| Fichier | Contenu |
|---|---|
| `kit.tar.gz` | le kit tel que `sagnac pull hello` le reçoit |
| `kit.json` | identifiant, version, schéma du résultat, SHA-256 de l'archive, `checks_sha256` du kit et graine des résultats |
| `states/<état>/workspace/` | un espace de travail : `initial` (point de départ), `solution` (solution de référence), `mutant-<nom>` (une erreur typique d'élève) |
| `states/<état>/expected.json` | code de sortie et échecs attendus (`declared_exit`, `declared_failures`), puis le résultat `sagnac-result/2` de l'exécuteur de référence sur cet espace, avec la graine de `kit.json` |

Le résultat attendu omet ce qui change d'un passage à l'autre : `started_at`,
les `duration_ms`, `cli` et `platform`. Seul le bit d'exécution des fichiers
compte, comme dans git.

## Tests qui les lisent

- `internal/oracle`, `TestConformanceHello` : extrait le kit avec le code de
  `sagnac pull`, vérifie son empreinte, puis lance l'oracle de la CLI sur
  chaque état et compare le résultat champ par champ.
- `cmd/sagnac`, `TestTwoLocalesSameJSON` : `sagnac check --json` sur le point
  de départ, en français puis dans une langue absente, donne le même JSON.

`go test ./...` suffit : rien hors de ce dépôt n'est nécessaire.
`TestCrossCheck` rejoue en plus l'exécuteur de référence lui-même, sur les
labs que désignent `SAGNAC_XCHECK_REFERENCE` et `SAGNAC_XCHECK_LABS` ; il ne
tourne que dans la CI de la plateforme et il est sauté ailleurs.
`TestCrossCheckLoad` se contente de lire chaque lab de `SAGNAC_XCHECK_LABS`
(`meta.yaml`, `checks/`, conf du cluster), sans exécuteur de référence ni
cluster : il dit sur n'importe quel poste si la CLI accepte le kit (pas de
`ORACLE_INVALID_KIT`).

Pour un lab à cluster (`cluster:` dans `meta.yaml`), `TestCrossCheck` ne lance
jamais `make setup`, qui créerait le cluster : l'espace vient de `starter/`
(`_cadre/lib/espace.sh`), puis de la solution et des mutants, et `HOME` est un
dossier du test, si bien qu'aucun des deux exécuteurs ne voit le cache d'un
vrai cluster. Les deux doivent rendre le même résultat dans trois situations :
sans cluster (préconditions du lab, `CLUSTER_MISSING`), cluster tenu par un
autre `check` (`CLUSTER_BUSY` partout) et cluster sale (un `kubectl` de
substitution liste toujours un pod restant : `CLUSTER_DIRTY` partout).

## Tests qui demandent Linux ou un vrai cluster

`TestEnvelopeClusterLinux` et `TestEnvelopeClusterCleanupLinux`
(`internal/oracle`) vérifient la coupure du réseau et le relais de l'API sous
Linux : ils sont sautés si `unshare -rnm unshare -Ur true` est refusé. Ils
n'ont besoin d'aucun cluster (un serveur local tient lieu d'API) ; un
conteneur Ubuntu 24.04 lancé avec `--security-opt seccomp=unconfined
--security-opt apparmor=unconfined --init` suffit, en root comme sans
privilège.

Le passage complet d'un lab à cluster (solution verte, mutants rouges avec
les codes de `mutant.yaml`, même résultat que l'exécuteur de référence)
demande le cluster `sagnac-b1` : il se joue sur une VM jetable de l'usine,
jamais sur un poste ni sur un hôte persistant. Sur la VM (Linux, moteur de
conteneurs, Go, `python3`, `make`), avec le dépôt de la plateforme
`<plateforme>` et ce dépôt :

```
make -C <plateforme>/labs/RN/rn-l6-kubernetes setup   # outils et cluster sagnac-b1 (réseau requis la première fois)
cd <sagnac-cli>
go test -count=1 -run 'Envelope|Relay|Gateway|Cluster' -v ./internal/oracle
L=<plateforme>/labs
SAGNAC_XCHECK_CLUSTER=1 SAGNAC_XCHECK_REFERENCE=$L/_cadre/sagnac_local.py \
SAGNAC_XCHECK_LABS=$L/RN/rn-l6-kubernetes:$L/B1-M2/b1-m2-l2-model-volume:$L/B1-M2/b1-m2-l3-resources:$L/B1-M2/b1-m2-l4-probes:$L/B1-M2/b1-m2-l5-oomkilled \
  go test -count=1 -timeout 60m -run TestCrossCheck -v ./internal/oracle
make -C <plateforme>/labs/RN/rn-l6-kubernetes cluster-delete
```

Avec `SAGNAC_XCHECK_CLUSTER=1`, `TestCrossCheck` traite un lab à cluster
comme les autres : `make setup` (hors ligne, cluster déjà créé), solution et
mutants, coupure du réseau, donc relais de l'API sous Linux.

Les labs du contrat de cluster v3 (`cluster: sagnac-b3`, B3-M3 à M5) se
jouent de la même façon, sur une autre VM jetable ou après
`make cluster-delete` du lab de B1 (un seul cluster Sagnac en marche à la
fois) : `make -C <plateforme>/labs/B3-M3/b3-m3-l1-hardened-pod setup` crée
`sagnac-b3` (API sur `127.0.0.1:6560`), puis `TestCrossCheck` avec
`SAGNAC_XCHECK_CLUSTER=1` sur les labs de `B3-M3`, `B3-M4` et `B3-M5`. Sans
cluster, le cas « dirty v3 » vérifie déjà que les deux exécuteurs prennent
la branche v3 du ramassage.

Les labs de B4 (`cluster: sagnac-b4`, API sur `127.0.0.1:6580`, modèles
montés par `MODELES`) et de B5 (`cluster: sagnac-b5`, `127.0.0.1:6570`,
Kyverno et registre local) se jouent de même, chacun sur son cluster, après
`make setup` d'un de leurs labs.
