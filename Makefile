# cli-registry
#
# Build/test/fixtures. Local commands must run through the mise Go 1.25
# toolchain (system `go` on this machine is 1.22.2 and must not be used).

GO        ?= mise exec -- go
GOFMT     ?= mise exec -- gofmt

# Umpire JSON-Schema profile conformance fixtures (vendored under conformance/).
# Provenance: umpire-spec release tarball (authoritative upstream pin).
UMPIRE_SPEC_VERSION = v1.1.0-rc.2
UMPIRE_SPEC_SHA256  = 560184f267e4224f00dd5bed8a6a66bcafdb906153c80699c0f074ec30144240
# Fixed digest over the vendored conformance fixture data (index + fixtures +
# failures; excludes local metadata like this README):
#   cat conformance/index.json conformance/fixtures/* conformance/failures/* | sha256sum
FIXTURE_DIGEST      = 5071576e907e549a96c04ef9c53f1efa335b753a0258a42eecb12b490ad3bd02

.PHONY: test vet build fmtcheck tidycheck fixtures-verify verify

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

build:
	$(GO) build ./...

fmtcheck:
	@test -z "$$($(GOFMT) -l .)"

tidycheck:
	@$(GO) mod tidy
	@test -z "$$(git status --porcelain go.mod go.sum)"

fixtures-verify:
	@actual=$$(cat conformance/index.json conformance/fixtures/* conformance/failures/* | sha256sum | awk '{print $$1}'); \
	if [ "$$actual" != "$(FIXTURE_DIGEST)" ]; then \
		echo "FIXTURE DIGEST MISMATCH: got $$actual, want $(FIXTURE_DIGEST)" >&2; exit 1; \
	fi; \
	echo "conformance fixtures ok ($${UMPIRE_SPEC_VERSION:-$(UMPIRE_SPEC_VERSION)})"

verify: test vet build fmtcheck tidycheck fixtures-verify
