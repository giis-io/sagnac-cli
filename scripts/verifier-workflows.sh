#!/bin/sh
# Rules of the workflows of a public repository:
# no trigger that runs with the rights of the base repository after the code
# of a fork (pull_request_target, workflow_run), a top-level permissions
# block, no write permission, no OIDC token, no secret, every action pinned by
# a full commit SHA.
# One workflow is the exception, release.yml: it is started by hand
# (workflow_dispatch with a "runner" input, the label of a disposable release
# runner, see CONTRIBUTING.md) and by nothing else, and may hold the two
# permissions a signed release needs, contents: write (publish the release),
# id-token: write (the OIDC token of the cosign signature and of the
# attestations) and attestations: write, as job permissions, in its one job,
# named "release", that waits for the protected environment "release" and runs
# on ${{ inputs.runner }}; its only secrets are the APPLE_* secrets of that
# environment (notarization), and it uses no unpinned action.
# Every other workflow runs the code of a pull request, possibly from a fork:
# each of its jobs runs on the GitHub-hosted runner ubuntu-24.04 (a fresh VM
# per job) and on nothing else. No self-hosted runner of GIIS (giis-usine-*,
# self-hosted) takes a job of this public repository, and release.yml uses no
# GitHub-hosted runner.
# Usage: scripts/verifier-workflows.sh [REPOSITORY]   (0 clean, 1 refused)
set -eu
root=${1:-.}
dir=$root/.github/workflows
if [ ! -d "$dir" ]; then
	echo "aucun workflow dans $dir"
	exit 0
fi
refused=0
refuse() {
	echo "$1 : $2" >&2
	refused=1
}
trigger=pull_request''_target
# Write permissions allowed in a workflow, as "scope:write"; none, except
# in the release workflow (see above).
release_writes="contents:write id-token:write attestations:write"
for f in "$dir"/*.yml "$dir"/*.yaml; do
	[ -f "$f" ] || continue
	name=${f#"$root"/}
	allowed_writes=""
	release=0
	case $name in
	.github/workflows/release.yml | .github/workflows/release.yaml)
		allowed_writes=$release_writes
		release=1
		;;
	esac
	if grep -Ev '^[[:space:]]*#' "$f" | grep -Eq 'giis-usine|self-hosted'; then
		refuse "$name" "runner auto-hébergé de GIIS interdit dans ce dépôt public"
	fi
	if [ "$release" -eq 1 ]; then
		if grep -En 'runs-on:[[:space:]]*\[?[[:space:]]*"?(ubuntu|macos|windows)-' "$f" >/dev/null; then
			refuse "$name" "runner hébergé par GitHub interdit pour la publication (runner jetable)"
		fi
	elif ! grep -Eq '^[[:space:]]*runs-on:' "$f" ||
		grep -E '^[[:space:]]*runs-on:' "$f" | grep -Evq '^[[:space:]]*runs-on:[[:space:]]*ubuntu-24\.04[[:space:]]*$'; then
		refuse "$name" "chaque job d'un workflow de PR tourne sur le runner hébergé ubuntu-24.04, rien d'autre"
	fi
	if [ "$release" -eq 1 ]; then
		if ! grep -Eq '^[[:space:]]+environment:[[:space:]]*release[[:space:]]*$' "$f"; then
			refuse "$name" "le job qui signe doit utiliser l'environnement protégé release"
		fi
		# Nothing but a manual run, with the label of the disposable runner.
		# (triggers are read before "jobs:", where the job named release lives)
		if sed '/^jobs:/,$d' "$f" | grep -En '^[[:space:]]*(push|pull_request|schedule|issue_comment|release|repository_dispatch):' >/dev/null; then
			refuse "$name" "seul un lancement manuel (workflow_dispatch) peut lancer une publication"
		fi
		if ! grep -Eq '^[[:space:]]*workflow_dispatch:' "$f" || ! grep -Eq '^[[:space:]]+runner:' "$f"; then
			refuse "$name" "workflow_dispatch avec l'entrée runner obligatoire"
		fi
		if ! grep -Eq 'runs-on:[[:space:]]*\$\{\{[[:space:]]*inputs\.runner[[:space:]]*\}\}' "$f" ||
			! grep -q "startsWith(inputs.runner, 'sagnac-release-')" "$f"; then
			refuse "$name" "le job de publication tourne sur le runner jetable (inputs.runner, étiquette sagnac-release-)"
		fi
		# A disposable runner takes one job; runner-ephemere.sh follows the job named release.
		jobs=$(awk '/^jobs:/ { j = 1; next } j && /^[^[:space:]#]/ { j = 0 } j && /^  [A-Za-z0-9_-]+:[[:space:]]*$/ { n++ } END { print n + 0 }' "$f")
		if [ "$jobs" -ne 1 ] || ! grep -Eq '^  release:[[:space:]]*$' "$f" ||
			grep -E '^    name:' "$f" | grep -Evq '^    name:[[:space:]]*"?release"?[[:space:]]*$'; then
			refuse "$name" "un seul job, nommé release (runner jetable à un job, suivi par runner-ephemere.sh)"
		fi
	fi
	if grep -n "$trigger" "$f" >/dev/null; then
		refuse "$name" "déclencheur $trigger interdit (code d'un fork avec les droits du dépôt)"
	fi
	if grep -n 'workflow_run' "$f" >/dev/null; then
		refuse "$name" "déclencheur workflow_run interdit (droits du dépôt après une PR de fork)"
	fi
	if ! grep -q '^permissions:' "$f"; then
		refuse "$name" "bloc permissions: de premier niveau obligatoire"
	fi
	if grep -n 'write-all' "$f" >/dev/null; then
		refuse "$name" "permissions write-all interdites"
	fi
	grep -E '^[[:space:]]*[a-z-]+:[[:space:]]*write[[:space:]]*(#.*)?$' "$f" | sed 's/[[:space:]]//g; s/#.*//' | while IFS= read -r w; do
		case " $allowed_writes " in
		*" $w "*) ;;
		*)
			echo "$name : permission en écriture interdite : $w" >&2
			exit 1
			;;
		esac
	done || refused=1
	# A job that writes or asks for an OIDC token must not use the shared
	# runners; permissions before "jobs:" apply to every job.
	if ! awk '
		/^[[:space:]]*#/ { next }
		/^jobs:/ { injobs = 1; next }
		injobs && /^  [A-Za-z0-9_-]+:[[:space:]]*$/ { job++; shared[job] = 0; writes[job] = 0; next }
		/giis-usine-lourd/ { if (injobs) shared[job] = 1; else sharedall = 1 }
		/^[[:space:]]*(contents|id-token|packages|actions|pull-requests|issues|checks|deployments|statuses|security-events|attestations):[[:space:]]*write/ {
			if (injobs) writes[job] = 1; else writeall = 1
		}
		/write-all/ { if (injobs) writes[job] = 1; else writeall = 1 }
		END {
			bad = 0
			for (j = 1; j <= job; j++) if ((shared[j] || sharedall) && (writes[j] || writeall)) bad = 1
			exit bad
		}' "$f"; then
		refuse "$name" "un job avec droit d'écriture ou jeton OIDC ne tourne pas sur giis-usine-lourd (runner jetable de release)"
	fi
	if [ "$release" -eq 0 ] && grep -n 'id-token' "$f" >/dev/null; then
		refuse "$name" "id-token interdit hors du workflow de publication"
	fi
	if [ "$release" -eq 1 ]; then
		if grep -o 'secrets\.[A-Za-z0-9_]*' "$f" | grep -Evq '^secrets\.APPLE_[A-Z0-9_]+$' ||
			grep -Eq 'secrets(\[|\.\*|[[:space:]]*\)|[[:space:]]*\}\})' "$f"; then
			refuse "$name" "seuls les secrets APPLE_* de l'environnement release (notarisation) sont permis"
		fi
	elif grep -n 'secrets\.' "$f" >/dev/null; then
		refuse "$name" "aucun secret dans un workflow de PR"
	fi
	grep -n 'uses:' "$f" | while IFS= read -r line; do
		ref=$(printf '%s\n' "$line" | sed -n 's/.*uses:[[:space:]]*["'\'']*\([^"'\''[:space:]#]*\).*/\1/p')
		case $ref in
		./*) ;;
		*@*)
			sha=${ref##*@}
			if ! printf '%s\n' "$sha" | grep -Eq '^[0-9a-f]{40}$'; then
				echo "$name : action non épinglée par un SHA complet : $ref" >&2
				exit 1
			fi
			;;
		*)
			echo "$name : action refusée : $ref" >&2
			exit 1
			;;
		esac
	done || refused=1
done
if [ "$refused" -ne 0 ]; then
	exit 1
fi
echo "workflows conformes"
