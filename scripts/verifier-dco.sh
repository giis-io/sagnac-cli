#!/bin/sh
# DCO check of a pull request: every commit of BASE..HEAD, merges excepted,
# carries a Signed-off-by trailer with the address of its author
# (CONTRIBUTING.md). Run from the repository.
# Usage: scripts/verifier-dco.sh BASE HEAD   (0 signed, 1 missing, 64 usage)
set -eu
if [ $# -ne 2 ]; then
	echo "usage : scripts/verifier-dco.sh BASE HEAD" >&2
	exit 64
fi
for rev in "$1" "$2"; do
	if ! git rev-parse --verify -q "$rev^{commit}" >/dev/null; then
		echo "révision inconnue : $rev" >&2
		exit 64
	fi
done
lower() { tr '[:upper:]' '[:lower:]'; }
missing=0
count=0
for c in $(git rev-list --no-merges "$1..$2"); do
	count=$((count + 1))
	author=$(git log -1 --format=%ae "$c" | lower)
	signers=$(git log -1 --format='%(trailers:key=Signed-off-by,valueonly,unfold)' "$c" |
		sed -n 's/.*<\([^>]*\)>.*/\1/p' | lower)
	if ! printf '%s\n' "$signers" | grep -qxF "$author"; then
		echo "sans Signed-off-by de son auteur ($author) : $(git log -1 --format='%h %s' "$c")" >&2
		missing=1
	fi
done
if [ "$missing" -ne 0 ]; then
	echo "Signez chaque commit avec git commit -s (CONTRIBUTING.md, « Certificat d'origine »)." >&2
	exit 1
fi
echo "DCO : $count commit(s) signé(s)"
