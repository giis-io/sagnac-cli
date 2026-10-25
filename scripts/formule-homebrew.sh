#!/bin/sh
# Prints the Homebrew formula of a release: the template
# packaging/homebrew/sagnac.rb.in with the version and the SHA-256 of the four
# archives, read from the SHA256SUMS of that release.
# Usage: scripts/formule-homebrew.sh VERSION SHA256SUMS   (VERSION without "v")
# Exit: 0 formula on stdout, 64 wrong use, 1 a checksum is missing or malformed.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
if [ $# -ne 2 ]; then
	echo "usage : formule-homebrew.sh VERSION SHA256SUMS" >&2
	exit 64
fi
version=$1
sums=$2
if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
	echo "version invalide : $version (attendu MAJ.MIN.CORR, sans « v »)" >&2
	exit 64
fi
if [ ! -f "$sums" ]; then
	echo "fichier de sommes introuvable : $sums" >&2
	exit 64
fi
sum_of() { # sum_of OS_ARCH
	file=sagnac_${version}_$1.tar.gz
	# "<64 hex>  <file>" or "<64 hex> *<file>", the format of sha256sum.
	found=$(awk -v f="$file" '($2 == f || $2 == "*" f) { print $1 }' "$sums")
	if [ "$(printf '%s\n' "$found" | grep -c .)" -ne 1 ] || ! printf '%s\n' "$found" | grep -Eq '^[0-9a-f]{64}$'; then
		echo "somme SHA-256 absente, répétée ou mal formée pour $file" >&2
		exit 1
	fi
	printf '%s\n' "$found"
}
darwin_arm64=$(sum_of darwin_arm64)
darwin_amd64=$(sum_of darwin_amd64)
linux_arm64=$(sum_of linux_arm64)
linux_amd64=$(sum_of linux_amd64)
sed -e "s/@VERSION@/$version/g" \
	-e "s/@SHA_DARWIN_ARM64@/$darwin_arm64/" \
	-e "s/@SHA_DARWIN_AMD64@/$darwin_amd64/" \
	-e "s/@SHA_LINUX_ARM64@/$linux_arm64/" \
	-e "s/@SHA_LINUX_AMD64@/$linux_amd64/" \
	"$here/../packaging/homebrew/sagnac.rb.in"
