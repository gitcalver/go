# Copyright © 2026 Michael Shields
# SPDX-License-Identifier: MIT

.PHONY: build test lint fmt acceptance local-image

COVERAGE_FILE = coverage.out

build:
	go build -o gitcalver ./cmd/gitcalver

test:
	go test -race -coverprofile=$(COVERAGE_FILE) ./internal/...
	@LC_ALL=C awk 'NR>1{t+=$$2;if($$3>0)c+=$$2} \
	  END{printf "Coverage: %.1f%%\n",(t>0?100*c/t:0); \
	  if(c!=t){print "FAIL: coverage is not 100.0%";exit 1}}' $(COVERAGE_FILE)

lint:
	go tool golangci-lint run

fmt:
	go tool gofumpt -w .

acceptance: build
	GITCALVER=$(CURDIR)/gitcalver ../sh/test/test.sh

local-image:
	cd container && go tool ko build --local --platform=linux/$(shell go env GOARCH) -B --tags=latest .
	docker tag ko.local/container:latest gitcalver:latest
