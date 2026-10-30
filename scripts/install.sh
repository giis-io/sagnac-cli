#!/bin/sh
# Installation of sagnac, without sudo, in ~/.local/bin:
#
#   curl -fsSLO https://github.com/giis-io/sagnac-cli/releases/latest/download/install.sh && sh install.sh
#
# The script is downloaded first, so it can be read before it runs. It picks
# the archive of this machine (darwin or linux, arm64 or amd64), checks it
# against SHA256SUMS, checks the cosign signature of SHA256SUMS when cosign is
# installed, then copies the binary. It never asks for administrator rights.
#
# Environment:
#   SAGNAC_VERSION      version to install (0.1.0); default: the latest release
#   SAGNAC_INSTALL_DIR  destination directory; default: $HOME/.local/bin
#   SAGNAC_RELEASE_DIR  directory that already holds the files of a release
#                       (archives, SHA256SUMS, SHA256SUMS.sigstore.json):
#                       nothing is downloaded. SAGNAC_VERSION is then required.
#
# Exit codes: 0 installed; 1 failure (nothing installed); 2 unsupported system.
set -eu

repo=giis-io/sagnac-cli
identity_prefix="https://github.com/$repo/.github/workflows/release.yml@refs/tags/v"
issuer=https://token.actions.githubusercontent.com

say() { printf 'sagnac : %s\n' "$*"; }
fail() {
	printf 'sagnac : %s\n' "$*" >&2
	exit 1
}

platform() {
	case $(uname -s) in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*)
		printf 'sagnac : système non pris en charge : %s (macOS ou Linux)\n' "$(uname -s)" >&2
		exit 2
		;;
	esac
	case $(uname -m) in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*)
		printf 'sagnac : architecture non prise en charge : %s (amd64 ou arm64)\n' "$(uname -m)" >&2
		exit 2
		;;
	esac
	# Rosetta: an arm64 Mac running this shell as x86_64 gets the native binary.
	if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null || echo 0)" = 1 ]; then
		arch=arm64
	fi
}

fetch() { # fetch URL FILE
	curl -fsSL --proto '=https' --tlsv1.2 --retry 3 -o "$2" "$1" || fail "téléchargement impossible : $1"
}

latest_version() {
	url=$(curl -fsSL --proto '=https' --tlsv1.2 -o /dev/null -w '%{url_effective}' \
		"https://github.com/$repo/releases/latest") || fail "dernière version introuvable"
	v=${url##*/tag/v}
	[ "$v" != "$url" ] || fail "dernière version introuvable ($url)"
	printf '%s\n' "$v"
}

sha256() { # sha256 FILE: prints the hex digest
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum -- "$1" | cut -d ' ' -f 1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 -- "$1" | cut -d ' ' -f 1
	else
		fail "ni sha256sum ni shasum : impossible de vérifier l'archive"
	fi
}

main() {
	platform
	version=${SAGNAC_VERSION:-}
	version=${version#v}
	dest=${SAGNAC_INSTALL_DIR:-${HOME:?}/.local/bin}
	work=$(mktemp -d)
	trap 'rm -rf "$work"' EXIT INT TERM

	if [ -n "${SAGNAC_RELEASE_DIR:-}" ]; then
		[ -n "$version" ] || fail "SAGNAC_RELEASE_DIR demande SAGNAC_VERSION"
	elif [ -z "$version" ]; then
		version=$(latest_version)
	fi
	case $version in
	[0-9]*.[0-9]*.[0-9]*) ;;
	*) fail "version invalide : $version" ;;
	esac
	archive=sagnac_${version}_${os}_${arch}.tar.gz
	say "installation de la version $version ($os/$arch) dans $dest"

	for f in "$archive" SHA256SUMS SHA256SUMS.sigstore.json; do
		if [ -n "${SAGNAC_RELEASE_DIR:-}" ]; then
			# A rehearsal of the private repository has no signature (release.yml).
			[ "$f" = SHA256SUMS.sigstore.json ] && [ ! -f "$SAGNAC_RELEASE_DIR/$f" ] && continue
			[ -f "$SAGNAC_RELEASE_DIR/$f" ] || fail "fichier absent : $SAGNAC_RELEASE_DIR/$f"
			cp "$SAGNAC_RELEASE_DIR/$f" "$work/$f"
		else
			fetch "https://github.com/$repo/releases/download/v$version/$f" "$work/$f"
		fi
	done

	if [ ! -f "$work/SHA256SUMS.sigstore.json" ]; then
		say "répétition sans signature : seule la somme SHA-256 est vérifiée"
	elif command -v cosign >/dev/null 2>&1; then
		cosign verify-blob --bundle "$work/SHA256SUMS.sigstore.json" \
			--certificate-identity "$identity_prefix$version" \
			--certificate-oidc-issuer "$issuer" "$work/SHA256SUMS" >/dev/null 2>&1 ||
			fail "la signature de SHA256SUMS n'est pas celle de GIIS : rien n'est installé"
		say "signature de SHA256SUMS vérifiée (cosign)"
	else
		say "cosign absent : seule la somme SHA-256 est vérifiée (voir « Installer » dans le README)"
	fi

	want=$(awk -v f="$archive" '$2 == f || $2 == "*" f { print $1 }' "$work/SHA256SUMS")
	[ -n "$want" ] || fail "$archive absent de SHA256SUMS"
	[ "$(sha256 "$work/$archive")" = "$want" ] || fail "somme SHA-256 incorrecte pour $archive : rien n'est installé"
	say "somme SHA-256 vérifiée"

	tar -xzf "$work/$archive" -C "$work" sagnac
	mkdir -p "$dest"
	# Copy then rename: a running sagnac is never overwritten in place.
	cp "$work/sagnac" "$dest/.sagnac.new"
	chmod 0755 "$dest/.sagnac.new"
	mv -f "$dest/.sagnac.new" "$dest/sagnac"
	say "installé : $dest/sagnac"
	"$dest/sagnac" version || true

	case ":${PATH:-}:" in
	*":$dest:"*) ;;
	*) say "ajoutez $dest à votre PATH, par exemple : echo 'export PATH=\"$dest:\$PATH\"' >> ~/.profile" ;;
	esac
}

main "$@"
