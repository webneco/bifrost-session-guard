# Bifrost session guard
#
#   make test      unit tests (no gateway needed)
#   make all       build the plugin-capable gateway and the plugin
#   make e2e       end-to-end tests against the real gateway
#   make demo      the before/after demo, no API keys needed
#   make e2e-builtin   the same end-to-end tests against a gateway with the guard compiled in

export GOWORK := off

.PHONY: all gateway plugin gateway-builtin test bench e2e e2e-builtin demo sync clean

all: gateway plugin

gateway: ## Build a plugin-capable Bifrost gateway from Bifrost's source (.work/bin/bifrost-http)
	@scripts/build-gateway.sh

plugin: ## Build build/session-guard.so and check it against the gateway
	@scripts/build-plugin.sh

test: ## Unit tests with the race detector
	go test -race ./...

bench: ## Cost of one decision
	go test -run '^$$' -bench . -benchmem ./guard/

e2e: gateway plugin ## End-to-end tests: real gateway, mock provider, scripted agents
	node --test --test-concurrency=1 test/e2e.test.mjs

gateway-builtin: ## Build a gateway with the session guard compiled in (.work/bin/bifrost-http-builtin)
	@scripts/build-builtin-gateway.sh

e2e-builtin: gateway-builtin ## End-to-end tests against the gateway with the guard compiled in
	GATEWAY=$(CURDIR)/.work/bin/bifrost-http-builtin PLUGIN_SO=builtin node --test --test-concurrency=1 test/e2e.test.mjs

demo: gateway plugin ## Before/after demo with a looping agent
	@demo/run.sh

sync: ## Re-pin dependencies to the Bifrost version in BIFROST_VERSION
	@scripts/sync-deps.sh

clean:
	rm -rf build .work/bin demo/data
