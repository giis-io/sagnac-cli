#!/bin/sh
# Rules of the workflows of a public repository whose runners are self-hosted:
# no trigger that runs with the rights of the base repository after the code
# of a fork (pull_request_target, workflow_run), a top-level permissions
# block, no write permission, no OIDC token, no secret, every action pinned by
# a full commit SHA.
# One workflow is the exception, release.yml: it runs on the push of a version
# tag only (never a pull request, a manual run or a schedule) and may hold
# the two permissions a signed release needs, contents: write (publish the
# release) and id-token: write (the OIDC token of the cosign signature), as job
# permissions, in a job that waits for the protected environment "release";
# it still uses no secret and no unpinned action.
# No workflow uses a GitHub-hosted runner (ubuntu-*, macos-*, windows-*): the
# CI runs on the factory's runners only.
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
release_writes="contents:write id-token:write"
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
	if grep -En 'runs-on:[[:space:]]*\[?[[:space:]]*"?(ubuntu|macos|windows)-' "$f" >/dev/null; then
		refuse "$name" "runner hébergé par GitHub interdit : la CI tourne sur les runners de l'usine"
	fi
	if [ "$release" -eq 1 ]; then
		if ! grep -Eq '^[[:space:]]+environment:[[:space:]]*release[[:space:]]*$' "$f"; then
			refuse "$name" "le job qui signe doit utiliser l'environnement protégé release"
		fi
		# Nothing but the push of a tag starts a release.
		if grep -En '^[[:space:]]*(pull_request|workflow_dispatch|schedule|issue_comment|release|repository_dispatch):' "$f" >/dev/null; then
			refuse "$name" "seul le push d'une étiquette de version peut lancer une publication"
		fi
		if ! grep -Eq '^[[:space:]]+tags:' "$f" || grep -Eq '^[[:space:]]+branches:' "$f"; then
			refuse "$name" "le déclencheur doit être le push d'une étiquette (tags:), sans branches:"
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
	if [ "$release" -eq 0 ] && grep -n 'id-token' "$f" >/dev/null; then
		refuse "$name" "id-token interdit hors du workflow de publication"
	fi
	if grep -n 'secrets\.' "$f" >/dev/null; then
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
