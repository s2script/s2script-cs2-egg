# SPDX-License-Identifier: GPL-3.0-or-later
GO ?= go
IMAGE ?= s2script-cs2-egg:dev

.PHONY: build test vet egg egg-check clean

build:
	mkdir -p bin
	$(GO) build -trimpath -o bin/s2egg ./cmd/s2egg
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
