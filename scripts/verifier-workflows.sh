#!/bin/sh
# Rules of the workflows of a public repository whose runners are self-hosted:
# no trigger that runs with the rights of the base repository after the code
# of a fork (pull_request_target, workflow_run), a top-level permissions
# block, no write permission, no OIDC token, no secret, every action pinned by
# a full commit SHA.
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
# Write permissions allowed in a workflow, as "scope: write"; none today.
allowed_writes=""
for f in "$dir"/*.yml "$dir"/*.yaml; do
	[ -f "$f" ] || continue
	name=${f#"$root"/}
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
	if grep -n 'id-token' "$f" >/dev/null; then
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
