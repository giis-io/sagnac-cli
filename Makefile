# Build of the sagnac binary. Publisher data comes from support.yaml; the build
# fails if one value is missing.

SHELL := /bin/sh

yaml_value = $(strip $(shell sed -n 's/^$(1):[[:space:]]*//p' support.yaml | head -n 1 | sed 's/[[:space:]]*$$//'))

BIN              ?= bin/sagnac
GOVULNCHECK      := golang.org/x/vuln/cmd/govulncheck@v1.8.0
# Version: the exact tag of a release (vMAJ.MIN.CORR) when HEAD carries one;
# otherwise the VERSION file followed by build metadata (+commit[-dirty]),
# which the server reads and ignores: sagnac/0.1.0+27f6614-dirty.
RELEASE_TAG      := $(shell git describe --tags --exact-match --match 'v[0-9]*.[0-9]*.[0-9]*' 2>/dev/null)
BUILD_META       := $(shell git describe --always --dirty --exclude '*' 2>/dev/null || echo dev)
VERSION          ?= $(if $(RELEASE_TAG),$(RELEASE_TAG),$(shell cat VERSION)+$(BUILD_META))
END_OF_SUPPORT   := $(call yaml_value,end_of_support)
PUBLISHER        := $(call yaml_value,publisher)
SECURITY_CONTACT := $(call yaml_value,security_contact)
SECURITY_URL     := $(call yaml_value,security_url)

REQUIRED := END_OF_SUPPORT PUBLISHER SECURITY_CONTACT SECURITY_URL
$(foreach v,$(REQUIRED),$(if $($(v)),,$(error support.yaml: missing value for $(v))))

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.endOfSupport=$(END_OF_SUPPORT) \
	-X main.publisher=$(PUBLISHER) \
	-X main.securityContactAddr=$(SECURITY_CONTACT) \
	-X main.securityPolicyURL=$(SECURITY_URL)

.PHONY: build test vuln clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/sagnac

test:
	go test ./...

vuln:
	go run $(GOVULNCHECK) ./...

clean:
	rm -rf bin
