ARCH := $(shell uname -m)
GO_FILES := $(shell find . -name '*.go' -type f)
ACP_VERSION := $(file < ./schema/version)
GOCACHE ?= $(CURDIR)/.gocache
MDSH ?= mdsh

version: schema/meta.json schema/schema.json schema/meta.unstable.json schema/schema.unstable.json schema/q-overrides.json $(shell find cmd/generate -name '*.go' -type f)
	go -C cmd/generate run .
	touch $@
	echo $(ACP_VERSION) > $@

schema/meta.json: schema/version
	curl -o $@ --fail -L https://github.com/agentclientprotocol/agent-client-protocol/releases/download/v$(ACP_VERSION)/meta.json

schema/schema.json: schema/version
	curl -o $@ --fail -L https://github.com/agentclientprotocol/agent-client-protocol/releases/download/v$(ACP_VERSION)/schema.json

schema/meta.unstable.json: schema/version
	@set -e; \
		url=https://github.com/agentclientprotocol/agent-client-protocol/releases/download/v$(ACP_VERSION)/meta.unstable.json; \
		tmp=$@.tmp; \
		status=$$(curl -sS -L -o "$$tmp" -w '%{http_code}' "$$url") || { rm -f "$$tmp"; exit 1; }; \
		if [ "$$status" = "200" ]; then \
			mv "$$tmp" $@; \
		elif [ "$$status" = "404" ]; then \
			rm -f "$$tmp"; \
			printf '%s\n' '{"agentMethods":{},"clientMethods":{},"protocolMethods":{}}' > $@; \
		else \
			rm -f "$$tmp"; \
			echo "failed to download $$url (http $$status)" 1>&2; \
			exit 1; \
		fi

schema/schema.unstable.json: schema/version
	@set -e; \
		url=https://github.com/agentclientprotocol/agent-client-protocol/releases/download/v$(ACP_VERSION)/schema.unstable.json; \
		tmp=$@.tmp; \
		status=$$(curl -sS -L -o "$$tmp" -w '%{http_code}' "$$url") || { rm -f "$$tmp"; exit 1; }; \
		if [ "$$status" = "200" ]; then \
			mv "$$tmp" $@; \
		elif [ "$$status" = "404" ]; then \
			rm -f "$$tmp"; \
			printf '%s\n' '{"$$defs":{}}' > $@; \
		else \
			rm -f "$$tmp"; \
			echo "failed to download $$url (http $$status)" 1>&2; \
			exit 1; \
		fi

.PHONY: fmt
fmt:
	treefmt

# treefmt runs mdsh + mdformat over the markdown, which keeps README.md
# regenerated and in sync (replacing the old mdsh guard). mdsh and mdformat
# disagree on the blank line after an mdsh directive: mdsh strips it, mdformat
# re-adds it. The net result is a no-op (README is a fixpoint of the pair), but
# that intermediate churn trips treefmt's own --fail-on-change. So we format in
# place and let git confirm there's no net drift instead.
.PHONY: check
check:
	treefmt
	git diff --exit-code

.PHONY: test
test: $(GO_FILES)
	GOFLAGS=$(GOFLAGS) GOCACHE=$(GOCACHE) go test ./...
	GOFLAGS=$(GOFLAGS) GOCACHE=$(GOCACHE) go build ./example/...
	go -C cmd/generate test ./...
	$(MAKE) check-generated

.PHONY: generate check-generated
generate:
	go -C cmd/generate run .

check-generated:
	go -C cmd/generate run . -check

.PHONY: clean
clean:
	rm -f schema/meta.json schema/schema.json schema/meta.unstable.json schema/schema.unstable.json version
	mdsh --clean --input README.md
	touch schema/version # Re-fetch the pinned schema on the next make.

.PHONY: update-schema
update-schema:
	@if [ -z "$(VERSION)" ]; then \
		echo "VERSION is required (e.g. 'make update-schema VERSION=0.13.5')" 1>&2; \
		exit 1; \
	fi
	@printf '%s\n' "$(VERSION)" > schema/version
	$(MAKE) version
	$(MAKE) fmt
	GOCACHE=$(GOCACHE) $(MAKE) test
	$(MAKE) check
	@cmp -s schema/version version || (echo "schema/version and version differ; rerun 'make version'" 1>&2; exit 1)
	@echo
	@echo "Schema candidate $(VERSION) is ready. Review the fork overlay and update UPSTREAM.json."

.PHONY: release
release: test
	@echo "SDK checks passed. Commit and push the SDK, then update q's pseudo-version (RELEASING.md)."
	@echo "No tag is required; this target does not commit or publish."
