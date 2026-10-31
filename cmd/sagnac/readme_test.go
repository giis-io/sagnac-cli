package main

import (
	"regexp"
	"strings"
	"testing"
)

// The installation instructions never pipe a download into a shell and never
// ask for sudo: the README may say so in a sentence, but no command block does.
func TestReadmeCommandsNeverPipeToAShell(t *testing.T) {
	pipeToShell := regexp.MustCompile(`\|\s*(sudo\s+)?(ba|z|da)?sh\b`)
	inBlock, blocks := false, 0
	for i, line := range strings.Split(readRepoFile(t, "README.md"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inBlock = !inBlock
			if inBlock {
				blocks++
			}
			continue
		}
		if inBlock && (pipeToShell.MatchString(line) || regexp.MustCompile(`\bsudo\b`).MatchString(line)) {
			t.Errorf("README.md:%d: a command block pipes to a shell or uses sudo: %q", i+1, line)
		}
	}
	if blocks == 0 {
		t.Error("no command block found: the check is vacuous")
	}
}

func TestReadmeGivesTheVerification(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	for _, want := range []string{
		"releases/latest/download/install.sh && sh install.sh", "cosign verify-blob", "SHA256SUMS.sigstore.json",
		"--certificate-oidc-issuer https://token.actions.githubusercontent.com", "sha256sum -c -",
		".github/workflows/release.yml@refs/tags/v$VERSION",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md lacks %q", want)
		}
	}
}

// The identity the README tells to check is the one of the workflow file,
// and the archives the formula downloads are the ones the workflow builds.
func TestReleaseWorkflowMatchesTheInstructions(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/release.yml")
	formula := readRepoFile(t, "packaging/homebrew/sagnac.rb.in")
	if !strings.Contains(workflow, "name=sagnac_${VERSION_NAME}_${os}_${arch}") ||
		!strings.Contains(workflow, "darwin/arm64 darwin/amd64 linux/arm64 linux/amd64") {
		t.Error("release.yml no longer names its archives sagnac_<version>_<os>_<arch>")
	}
	for _, target := range []string{"darwin_arm64", "darwin_amd64", "linux_arm64", "linux_amd64"} {
		if !strings.Contains(formula, "sagnac_@VERSION@_"+target+".tar.gz") {
			t.Errorf("the formula does not download the %s archive", target)
		}
	}
	for _, want := range []string{"cosign sign-blob", "SHA256SUMS.sigstore.json", "id-token: write", "cp ../scripts/install.sh install.sh",
		"cyclonedx-gomod", "*.cdx.json install.sh > SHA256SUMS", "sbom-path:"} {
		if !strings.Contains(workflow, want) {
			t.Errorf("release.yml lacks %q", want)
		}
	}
}
