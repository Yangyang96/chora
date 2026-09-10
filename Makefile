.PHONY: check-toolchain publication-check public-test public-e2e test vet verify localweb-race oss-alpha-closure-e2e

GO_PRODUCT_PACKAGES = go list -e ./... | grep -Ev '/spikes/[^/]+/evidence(/|$$)|/contracts/g2-m4/source-baseline-v[456]/delta(/|$$)'
GO_REMAINING_PRODUCT_PACKAGES = $(GO_PRODUCT_PACKAGES) | grep -Ev '/internal/(app|localweb)$$'
GO_PUBLIC_PACKAGES = go list -e ./... | grep -Ev '/(contracts/g2-m4/source-baseline-v[456]/delta|cmd/o4-service-controller|distribution/v1/artifacts|internal/(baselinebundle|sourcebundle)|spikes)(/|$$)'
GO_TEST_TIMEOUT ?= 15m
GO_LOCALWEB_RACE_TIMEOUT ?= 45m
GO_TEST_PACKAGE_PARALLEL ?= 4
GO_RACE_PACKAGE_PARALLEL ?= 3
GO_TEST_HEARTBEAT_SECONDS ?= 30
GO_TAGGED_LOCALWEB_TEST_PATTERN ?= ^(TestVerifiedReviewPublicAPIExposesPatchAcceptAndUnconfiguredSCM|TestE2EVerifierStartupCandidateRecoveryDoesNotUseAmbientDocker|TestVerifiedRejectAndAgentRetryPreservePredecessorAndReverifySuccessor)$$

define RUN_WITH_PROGRESS
	@start="$$(date +%s)"; \
	label='$(1)'; \
	printf '==> %s\n' "$$label"; \
	{ $(2); } & command_pid=$$!; \
	( while kill -0 "$$command_pid" 2>/dev/null; do \
		sleep "$(GO_TEST_HEARTBEAT_SECONDS)"; \
		if kill -0 "$$command_pid" 2>/dev/null; then \
			now="$$(date +%s)"; \
			printf '... %s still running (%ss)\n' "$$label" "$$((now-start))"; \
		fi; \
	done ) & heartbeat_pid=$$!; \
	trap 'kill -INT "$$command_pid" 2>/dev/null || true; kill "$$heartbeat_pid" 2>/dev/null || true; wait "$$command_pid" 2>/dev/null || true; exit 130' INT TERM HUP; \
	wait "$$command_pid"; status=$$?; \
	kill "$$heartbeat_pid" 2>/dev/null || true; \
	wait "$$heartbeat_pid" 2>/dev/null || true; \
	trap - INT TERM HUP; \
	now="$$(date +%s)"; elapsed="$$((now-start))"; \
	if [ "$$status" -eq 0 ]; then \
		printf '<== %s completed (%ss)\n' "$$label" "$$elapsed"; \
	else \
		printf '<== %s failed (exit %s after %ss)\n' "$$label" "$$status" "$$elapsed" >&2; \
	fi; \
	exit "$$status"
endef

check-toolchain:
	@node -e 'const [major, minor] = process.versions.node.split(".").map(Number); if (major < 22 || (major === 22 && minor < 12)) { console.error(`Node.js >=22.12.0 is required; found $${process.version}`); process.exit(1) }'
	@version="$$(npm --version)"; major="$${version%%.*}"; if [ "$$major" -lt 11 ]; then echo "npm >=11 is required; found $$version" >&2; exit 1; fi
	@version="$$(go env GOVERSION)"; version="$${version#go}"; major="$${version%%.*}"; rest="$${version#*.}"; minor="$${rest%%.*}"; if [ "$$major" -lt 1 ] || { [ "$$major" -eq 1 ] && [ "$$minor" -lt 26 ]; }; then echo "Go >=1.26 is required; found go$$version" >&2; exit 1; fi

publication-check:
	npm run publication:test
	npm run publication:check
	npm run publication:export:test
	npm run publication:export:dry-run
	npm run docs:links:test
	npm run docs:links
	npm run dependency-metadata:test
	npm run dependency-metadata:check

public-test: check-toolchain publication-check
	node --test e2e/public-go-tests.test.mjs
	$(call RUN_WITH_PROGRESS,Public Go tests,packages="$$( $(GO_PUBLIC_PACKAGES) )"; test -n "$$packages"; GO_TEST_PACKAGE_PARALLEL=$(GO_TEST_PACKAGE_PARALLEL) GO_TEST_TIMEOUT=$(GO_TEST_TIMEOUT) node e2e/public-go-tests.mjs $$packages)
	npm run disclosure:test
	npm run web:typecheck
	npm run web:test
	npm --workspace web run build -- --outDir "$${CHORA_WEB_BUILD_DIR:-dist}"

test: check-toolchain
	$(MAKE) publication-check
	$(call RUN_WITH_PROGRESS,Go tests: internal/app,go test -p=1 -timeout=$(GO_TEST_TIMEOUT) ./internal/app)
	$(call RUN_WITH_PROGRESS,Go tests: internal/localweb,go test -p=1 -timeout=$(GO_TEST_TIMEOUT) ./internal/localweb)
	$(call RUN_WITH_PROGRESS,Go tests: remaining product packages,packages="$$( $(GO_REMAINING_PRODUCT_PACKAGES) )"; test -n "$$packages"; go test -p=$(GO_TEST_PACKAGE_PARALLEL) -timeout=$(GO_TEST_TIMEOUT) $$packages)
	$(call RUN_WITH_PROGRESS,Go tests: tagged localweb,go test -p=1 -timeout=$(GO_TEST_TIMEOUT) -tags chora_e2e -run='$(GO_TAGGED_LOCALWEB_TEST_PATTERN)' ./internal/localweb)
	node --test internal/agent/pi/resource_check_observer.test.mjs
	npm run disclosure:test
	npm run web:test

vet: check-toolchain
	@packages="$$( $(GO_PRODUCT_PACKAGES) )"; test -n "$$packages"; go vet $$packages

localweb-race:
	go test -race -p=1 -timeout=$(GO_TEST_TIMEOUT) ./internal/localweb

verify: check-toolchain
	$(call RUN_WITH_PROGRESS,Go race tests: internal/app,go test -race -p=1 -timeout=$(GO_TEST_TIMEOUT) ./internal/app)
	$(call RUN_WITH_PROGRESS,Go race tests: internal/localweb,$(MAKE) --no-print-directory GO_TEST_TIMEOUT=$(GO_LOCALWEB_RACE_TIMEOUT) localweb-race)
	$(call RUN_WITH_PROGRESS,Go race tests: remaining product packages,packages="$$( $(GO_REMAINING_PRODUCT_PACKAGES) )"; test -n "$$packages"; go test -race -p=$(GO_RACE_PACKAGE_PARALLEL) -timeout=$(GO_TEST_TIMEOUT) $$packages)
	$(call RUN_WITH_PROGRESS,Go race tests: tagged localweb,go test -race -p=1 -timeout=$(GO_TEST_TIMEOUT) -tags chora_e2e -run='$(GO_TAGGED_LOCALWEB_TEST_PATTERN)' ./internal/localweb)
	node --test internal/agent/pi/resource_check_observer.test.mjs
	npm run disclosure:test
	npm run web:typecheck
	npm run web:test
	npm --workspace web run build -- --outDir "$${CHORA_WEB_BUILD_DIR:-dist}"

oss-alpha-closure-e2e: check-toolchain
	npm run e2e:oss-alpha-closure

public-e2e: check-toolchain
	npm run e2e:public
