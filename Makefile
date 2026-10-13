# SPDX-License-Identifier: GPL-3.0-or-later
GO ?= go
IMAGE ?= ghcr.io/s2script/s2script-cs2-egg:prerelease

.PHONY: build test vet egg egg-check clean

build:
	mkdir -p bin
	$(GO) build -trimpath -buildvcs=false -o bin/s2egg ./cmd/s2egg
	$(GO) build -trimpath -buildvcs=false -o bin/s2node ./cmd/s2node
	$(GO) build -trimpath -buildvcs=false -o bin/egggen ./cmd/egggen

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

egg:
	$(GO) run ./cmd/egggen --image $(IMAGE) --catalog-output docs/bundled-plugins.md

egg-check:
	$(GO) run ./cmd/egggen --image $(IMAGE) --catalog-output docs/bundled-plugins.md --check

clean:
	rm -rf bin

# Install tests/schema-requirements.txt in an isolated environment first.
SCHEMA_PYTHON ?= python3
.PHONY: schema-check
schema-check:
	S2_SCHEMA_PYTHON=$(SCHEMA_PYTHON) $(GO) test ./internal/releasecontract ./internal/server ./internal/node ./internal/selfupdate -run "Schema|BotInterface" -count=1

.PHONY: release-check release
release-check: egg-check
	$(GO) test ./internal/releasepack ./tests/integration -count=1
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests/release -v

# OUT must be outside this checkout and must not already exist.
release:
	$(GO) run ./cmd/releasectl pack --tag $(RELEASE_TAG) --output $(OUT) --go $(GO)
