# Kits figés des tests de conformité

Ces fichiers sont produits par la plateforme, pas écrits à la main. Ne les
modifiez pas : un test qui ne passe plus signale un écart entre la CLI et le
contrat (`docs/contrat.md`), pas une fixture à corriger.

## `hello-v3/`

Le lab `hello`, version 3, contrat v2.

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
