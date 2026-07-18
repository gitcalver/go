# Copyright © 2026 Michael Shields
# SPDX-License-Identifier: MIT

.PHONY: build test test-conformance lint fmt acceptance local-image

COVERAGE_FILE = coverage.out
CONFORMANCE_DIR ?= ../sh
CONFORMANCE_SHA := c89d24c9ac36f0672ecfff9727532e344bfa9af9

build:
	go build -o gitcalver ./cmd/gitcalver

test:
	go test -race -coverprofile=$(COVERAGE_FILE) ./internal/...
	@LC_ALL=C awk 'NR>1{t+=$$2;if($$3>0)c+=$$2} \
	  END{printf "Coverage: %.1f%%\n",(t>0?100*c/t:0); \
	  if(c!=t){print "FAIL: coverage is not 100.0%";exit 1}}' $(COVERAGE_FILE)

test-conformance: build
	@test "$$(git -C "$(CONFORMANCE_DIR)" rev-parse "$(CONFORMANCE_SHA)^{commit}")" = "$(CONFORMANCE_SHA)"
	@tmp="$$(mktemp)"; \
	trap 'rm -f "$$tmp"' EXIT HUP INT TERM; \
	git -C "$(CONFORMANCE_DIR)" show "$(CONFORMANCE_SHA):test/test.sh" >"$$tmp"; \
	GITCALVER="$(CURDIR)/gitcalver" sh "$$tmp"

lint:
	go tool golangci-lint run

fmt:
	go tool gofumpt -w .

acceptance: test-conformance

local-image:
	cd container && go tool ko build --local --platform=linux/$(shell go env GOARCH) -B --tags=latest .
	docker tag ko.local/container:latest gitcalver:latest
