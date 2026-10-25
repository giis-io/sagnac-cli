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
  push:
    tags: ["v[0-9]+.[0-9]+.[0-9]+"]
permissions:
  contents: read
jobs:
  publish:
    runs-on: ubuntu-24.04
    permissions:
      contents: write
      id-token: write
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - run: echo publish'
rm -f "$wf/other.yaml"
printf '%s\n' "$good" >"$wf/pr.yml"
printf '%s\n' "$rel" >"$wf/release.yml"
expect 0 "release.yml may write contents and request an OIDC token" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n' "$rel" | sed 's/contents: write/packages: write/' >"$wf/release.yml"
expect 1 "release.yml: another write permission" "$here/verifier-workflows.sh" "$tmp/wf"
for bad in "  pull_request:" "  workflow_dispatch:" "  schedule:" "      - run: echo \${{ secrets.TOKEN }}"; do
	printf '%s\n%s\n' "$rel" "$bad" >"$wf/release.yml"
	expect 1 "release.yml refused: $bad" "$here/verifier-workflows.sh" "$tmp/wf"
done
printf '%s\n' "$rel" | sed 's/tags: .*/branches: [main]/' >"$wf/release.yml"
expect 1 "release.yml: a push that is not a tag" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n' "$rel" >"$wf/release.yml"
printf '%s\n  id-token: write\n' "$good" >"$wf/pr.yml"
expect 1 "id-token stays forbidden in pr.yml" "$here/verifier-workflows.sh" "$tmp/wf"
printf '%s\n  contents: write\n' "$good" >"$wf/pr.yml"
expect 1 "contents: write stays forbidden in pr.yml" "$here/verifier-workflows.sh" "$tmp/wf"
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

exit $failed
