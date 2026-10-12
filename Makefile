# SPDX-License-Identifier: GPL-3.0-or-later
GO ?= go
IMAGE ?= s2script-cs2-egg:dev

.PHONY: build test vet egg egg-check clean

build:
	mkdir -p bin
	$(GO) build -trimpath -o bin/s2egg ./cmd/s2egg
	$(GO) build -trimpath -o bin/s2node ./cmd/s2node
	$(GO) build -trimpath -o bin/egggen ./cmd/egggen

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
