#!/bin/sh
# Tests of verifier-dco.sh and verifier-workflows.sh, in temporary repositories.
# Usage: scripts/tests.sh   (0 all pass, 1 a test fails)
set -u
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failed=0

expect() { # expect CODE DESCRIPTION COMMAND...
	want=$1 what=$2
	shift 2
	"$@" >"$tmp/out" 2>&1
	got=$?
	if [ "$got" -ne "$want" ]; then
		echo "FAIL $what: code $got, want $want"
		sed 's/^/    /' "$tmp/out"
		failed=1
	else
		echo "ok   $what"
	fi
}

# --- verifier-dco.sh ---------------------------------------------------------
repo=$tmp/repo
git init -q -b main "$repo"
g() { git -C "$repo" -c user.name="Awa Diop" -c user.email=awa@example.org -c commit.gpgsign=false "$@"; }
in_repo() { (cd "$repo" && "$@"); }
g commit -q --allow-empty -m "base"
base=$(g rev-parse HEAD)
g commit -q --allow-empty -s -m "signed"
signed=$(g rev-parse HEAD)
expect 0 "signed commit" in_repo "$here/verifier-dco.sh" "$base" "$signed"
expect 0 "empty range" in_repo "$here/verifier-dco.sh" "$signed" "$signed"
g commit -q --allow-empty -m "not signed"
expect 1 "commit without Signed-off-by" in_repo "$here/verifier-dco.sh" "$base" HEAD
grep -q "not signed" "$tmp/out" || { echo "FAIL the unsigned commit is not named"; failed=1; }
g reset -q --hard "$signed"
g commit -q --allow-empty -m "other signer

Signed-off-by: Someone Else <else@example.org>"
expect 1 "Signed-off-by of another person" in_repo "$here/verifier-dco.sh" "$base" HEAD
g reset -q --hard "$signed"
g commit -q --allow-empty -m "sign-off in the body only

text Signed-off-by: Awa Diop <awa@example.org> inside a line"
expect 1 "sign-off outside the trailers" in_repo "$here/verifier-dco.sh" "$base" HEAD
g reset -q --hard "$signed"
g commit -q --allow-empty -m "case of the address

Signed-off-by: Awa Diop <AWA@example.org>"
expect 0 "address compared without case" in_repo "$here/verifier-dco.sh" "$base" HEAD
expect 64 "missing argument" in_repo "$here/verifier-dco.sh" "$base"
expect 64 "unknown revision" in_repo "$here/verifier-dco.sh" "$base" 0000000000000000000000000000000000000000

# --- verifier-workflows.sh ---------------------------------------------------
wf=$tmp/wf/.github/workflows
mkdir -p "$wf"
good='name: pr
on: pull_request
permissions:
  contents: read
jobs:
  t:
    runs-on: [self-hosted]
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: ./.github/actions/local
      - run: go test ./...'
printf '%s\n' "$good" >"$wf/pr.yml"
expect 0 "clean workflow" "$here/verifier-workflows.sh" "$tmp/wf"
expect 0 "this repository" "$here/verifier-workflows.sh" "$here/.."
trigger=pull_request_target
for bad in "on: $trigger" "      - uses: actions/checkout@v7" "      - uses: actions/checkout@3d3c42e" \
	"  id-token: write" "on: workflow_run" "  contents: write" "permissions: write-all" "      pull-requests: write # comment" '        env: {T: "${{ secrets.TOKEN }}"}' "      - uses: docker://alpine:3"; do
	printf '%s\n%s\n' "$good" "$bad" >"$wf/pr.yml"
	expect 1 "refused: $bad" "$here/verifier-workflows.sh" "$tmp/wf"
done
printf '%s\n' "$good" | grep -v '^permissions:' | grep -v 'contents: read' >"$wf/pr.yml"
expect 1 "refused: no top-level permissions" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n' "$good" >"$wf/pr.yml"
printf 'permissions: {}\non: %s\n' "$trigger" >"$wf/other.yaml"
expect 1 "refused in a .yaml file" "$here/verifier-workflows.sh" "$tmp/wf"

# --- release.yml: the one workflow allowed to write and to sign --------------
rel='name: release
on:
  workflow_dispatch:
    inputs:
      runner:
        required: true
permissions:
  contents: read
jobs:
  release:
    name: release
    if: ${{ startsWith(inputs.runner, '"'"'sagnac-release-'"'"') }}
    runs-on: ${{ inputs.runner }}
    environment: release
    permissions:
      contents: write
      id-token: write
      attestations: write
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - env:
          P12: ${{ secrets.APPLE_DEVELOPER_ID_P12 }}
        run: echo publish'
rm -f "$wf/other.yaml"
printf '%s\n' "$good" >"$wf/pr.yml"
printf '%s\n' "$rel" >"$wf/release.yml"
expect 0 "release.yml may write, sign, attest and read the APPLE_* secrets" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n' "$rel" | sed 's/contents: write/packages: write/' >"$wf/release.yml"
expect 1 "release.yml: another write permission" "$here/verifier-workflows.sh" "$tmp/wf"
for bad in "  pull_request:" "  push:" "  schedule:" "  release:"; do
	printf '%s\n' "$rel" | sed "s/^  workflow_dispatch:/$bad\\
  workflow_dispatch:/" >"$wf/release.yml"
	expect 1 "release.yml refused: trigger $bad" "$here/verifier-workflows.sh" "$tmp/wf"
done
for bad in "      - run: echo \${{ secrets.TOKEN }}" "      - run: echo \${{ secrets.GITHUB_TOKEN }}" \
	"      - run: echo \${{ toJSON(secrets) }}" "      - run: echo \${{ secrets['APPLE_X'] }}"; do
	printf '%s\n%s\n' "$rel" "$bad" >"$wf/release.yml"
	expect 1 "release.yml refused: $bad" "$here/verifier-workflows.sh" "$tmp/wf"
done
printf '%s\n' "$rel" | sed 's/    runs-on: ${{ inputs.runner }}/    runs-on:\n      group: giis-usine-lourd/' >"$wf/release.yml"
expect 1 "release.yml: the writing job on the shared runners" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n' "$rel" | sed 's/    runs-on: ${{ inputs.runner }}/    runs-on: ubuntu-latest/' >"$wf/release.yml"
expect 1 "release.yml: the writing job not on the disposable runner" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n' "$rel" | sed "s/startsWith(inputs.runner, 'sagnac-release-')/true/" >"$wf/release.yml"
expect 1 "release.yml: no guard on the runner label" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n' "$rel" | grep -v 'workflow_dispatch:' >"$wf/release.yml"
expect 1 "release.yml: not started by hand" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n' "$rel" | sed 's/^permissions:/permissions:\n  id-token: write/' >"$wf/release.yml"
expect 0 "release.yml: a global write with the one job on the disposable runner" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n' "$rel" | grep -v 'environment: release' >"$wf/release.yml"
expect 1 "release.yml: no protected environment" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n' "$rel" | sed 's/    runs-on:/    runs-on: ubuntu-24.04\n    x:/' >"$wf/release.yml"
expect 1 "release.yml: GitHub-hosted runner" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n' "$rel" | sed 's/^  release:/  publish:/' >"$wf/release.yml"
expect 1 "release.yml: the job is not named release" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n' "$rel" | sed 's/^    name: release/    name: publication/' >"$wf/release.yml"
expect 1 "release.yml: the job name is not release" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n  build:\n    runs-on:\n      group: giis-usine-lourd\n    steps:\n      - run: make\n' "$rel" >"$wf/release.yml"
expect 1 "release.yml: a second job" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n' "$rel" >"$wf/release.yml"
printf '%s\n' "$good" | sed 's/runs-on: \[self-hosted\]/runs-on: macos-14/' >"$wf/pr.yml"
expect 1 "pr.yml: GitHub-hosted runner" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n  id-token: write\n' "$good" >"$wf/pr.yml"
expect 1 "id-token stays forbidden in pr.yml" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n  contents: write\n' "$good" >"$wf/pr.yml"
expect 1 "contents: write stays forbidden in pr.yml" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n        env: {P: "${{ secrets.APPLE_DEVELOPER_ID_P12 }}"}\n' "$good" >"$wf/pr.yml"
expect 1 "the APPLE_* secrets stay forbidden in pr.yml" "$here/verifier-workflows.sh" "$tmp/wf"
rm -f "$wf/release.yml" "$wf/pr.yml"

# --- formule-homebrew.sh ------------------------------------------------------
f=$here/formule-homebrew.sh
a=$(printf 'a%.0s' $(seq 1 64))
b=$(printf 'b%.0s' $(seq 1 64))
c=$(printf 'c%.0s' $(seq 1 64))
d=$(printf 'd%.0s' $(seq 1 64))
sums=$tmp/SHA256SUMS
printf '%s  sagnac_1.2.3_darwin_arm64.tar.gz\n%s  sagnac_1.2.3_darwin_amd64.tar.gz\n%s  sagnac_1.2.3_linux_arm64.tar.gz\n%s *sagnac_1.2.3_linux_amd64.tar.gz\n' "$a" "$b" "$c" "$d" >"$sums"
expect 0 "formula from the checksums" sh -c "'$f' 1.2.3 '$sums' >'$tmp/sagnac.rb'"
formula_has() { grep -q -- "$1" "$tmp/sagnac.rb" || { echo "FAIL formula lacks: $1"; failed=1; }; }
formula_has 'version "1.2.3"'
formula_has "releases/download/v1.2.3/sagnac_1.2.3_darwin_arm64.tar.gz"
formula_has "sha256 \"$a\""
formula_has "sha256 \"$b\""
formula_has "sha256 \"$c\""
formula_has "sha256 \"$d\""
if grep -q '@[A-Z_]*@' "$tmp/sagnac.rb"; then echo "FAIL a placeholder is left in the formula"; failed=1; fi
grep -v 'linux_arm64' "$sums" >"$tmp/short"
expect 1 "formula refused: a checksum is missing" "$f" 1.2.3 "$tmp/short"
sed "s/$a/not-a-checksum/" "$sums" >"$tmp/bad"
expect 1 "formula refused: a malformed checksum" "$f" 1.2.3 "$tmp/bad"
{ cat "$sums"; printf '%s  sagnac_1.2.3_darwin_arm64.tar.gz\n' "$b"; } >"$tmp/twice"
expect 1 "formula refused: a checksum given twice" "$f" 1.2.3 "$tmp/twice"
expect 64 "formula refused: version with a v" "$f" v1.2.3 "$sums"
expect 64 "formula refused: no checksums file" "$f" 1.2.3 "$tmp/none"
expect 64 "formula refused: missing argument" "$f" 1.2.3
expect 1 "formula refused: sums of another version" "$f" 9.9.9 "$sums"


# --- install.sh, on the files of a release already on disk ----------------------
i=$here/install.sh
rd=$tmp/release
mkdir -p "$rd" "$tmp/stage" "$tmp/nocosign" "$tmp/badcosign"
printf '#!/bin/sh\necho "sagnac v1.2.3"\n' >"$tmp/stage/sagnac"
chmod 0755 "$tmp/stage/sagnac"
for p in darwin_arm64 darwin_amd64 linux_arm64 linux_amd64; do
	tar -C "$tmp/stage" -czf "$rd/sagnac_1.2.3_$p.tar.gz" sagnac
done
(cd "$rd" && for x in *.tar.gz; do
	if command -v sha256sum >/dev/null 2>&1; then sha256sum -- "$x"; else shasum -a 256 -- "$x"; fi
done >SHA256SUMS)
echo '{}' >"$rd/SHA256SUMS.sigstore.json"
printf '#!/bin/sh\nexit 1\n' >"$tmp/badcosign/cosign"
chmod 0755 "$tmp/badcosign/cosign"
safe_path=$tmp/nocosign:/usr/bin:/bin:/usr/sbin:/sbin
inst() { # inst PATH DEST [VERSION]
	env PATH="$1" HOME="$tmp/home" SAGNAC_RELEASE_DIR="$rd" SAGNAC_INSTALL_DIR="$2" SAGNAC_VERSION="${3-1.2.3}" sh "$i"
}
expect 0 "install.sh installs from a release directory" inst "$safe_path" "$tmp/bin1"
[ -x "$tmp/bin1/sagnac" ] || { echo "FAIL install.sh: no executable sagnac"; failed=1; }
grep -q 'somme SHA-256 vérifiée' "$tmp/out" || { echo "FAIL install.sh: checksum not reported"; failed=1; }
expect 0 "install.sh accepts a version with a v" inst "$safe_path" "$tmp/bin2" v1.2.3
expect 1 "install.sh refused: a signature that does not verify" inst "$tmp/badcosign:$safe_path" "$tmp/bin3"
[ ! -e "$tmp/bin3/sagnac" ] || { echo "FAIL install.sh installed after a bad signature"; failed=1; }
mv "$rd/SHA256SUMS.sigstore.json" "$tmp/sig.keep"
expect 0 "install.sh installs a rehearsal without signature" inst "$tmp/badcosign:$safe_path" "$tmp/bin9"
grep -q 'répétition sans signature' "$tmp/out" || { echo "FAIL install.sh: missing signature not reported"; failed=1; }
mv "$tmp/sig.keep" "$rd/SHA256SUMS.sigstore.json"
expect 1 "install.sh refused: a release directory without a version" inst "$safe_path" "$tmp/bin4" ""
cp "$rd/SHA256SUMS" "$tmp/sums.keep"
sed 's/^[0-9a-f]\{8\}/00000000/' "$tmp/sums.keep" >"$rd/SHA256SUMS"
expect 1 "install.sh refused: a wrong checksum" inst "$safe_path" "$tmp/bin5"
[ ! -e "$tmp/bin5/sagnac" ] || { echo "FAIL install.sh installed with a wrong checksum"; failed=1; }
: >"$rd/SHA256SUMS"
expect 1 "install.sh refused: the archive missing from SHA256SUMS" inst "$safe_path" "$tmp/bin6"
cp "$tmp/sums.keep" "$rd/SHA256SUMS"
expect 1 "install.sh refused: a version without its files" inst "$safe_path" "$tmp/bin7" 9.9.9
expect 1 "install.sh refused: an invalid version" inst "$safe_path" "$tmp/bin8" 'x;y'
if grep -n 'sudo' "$i" | grep -v '^[0-9]*:#' >/dev/null; then echo "FAIL install.sh mentions sudo outside a comment"; failed=1; fi

exit $failed
