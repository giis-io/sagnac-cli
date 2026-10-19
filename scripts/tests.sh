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

exit $failed
