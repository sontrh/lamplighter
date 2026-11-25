SHELL := /bin/bash
.PHONY: tools bot test lint fmt smoke dev

# Go 1.25.9 or newer. Override if it is not on PATH: make GO=/path/to/go ...
GO ?= go

# Address namespace the realms are published under.
NS := g1zjumaghw5cgzhyj46eygvd452p6the6n56dppx

# Build gno, gnokey and gnodev at the version the Onyx testnet runs, into .tools/.
tools:
	GO="$(GO)" ./scripts/tools.sh

# Build the executor bot into .tools/bin/lamplighter-bot.
bot:
	mkdir -p .tools/bin
	cd bot && $(GO) build -o ../.tools/bin/lamplighter-bot .

# Unit tests for the three realms and for the bot.
test:
	source env.sh && cd gno.land && gno test ./...
	cd bot && $(GO) test ./...

lint:
	source env.sh && cd gno.land && gno lint ./...
	cd bot && $(GO) vet ./...

fmt:
	source env.sh && cd gno.land && gno fmt -w ./...
	cd bot && gofmt -w .

# End-to-end run with real transactions on a throwaway local chain: the realms,
# the bot, and the bot against hostile jobs. Takes about four minutes.
smoke: bot
	./scripts/smoke.sh

# Local chain with gnoweb at http://127.0.0.1:8899. The ports stay clear of a
# real node listening on 26657.
dev:
	source env.sh && gnodev local -C gno.land \
		-node-rpc-listener 127.0.0.1:36657 -web-listener 127.0.0.1:8899 \
		-empty-blocks=true -add-account lighter=5000000ugnot \
		-add-account $(NS)=10000000000ugnot -deploy-key $(NS) \
		-paths gno.land/r/$(NS)/lamplighter/registry/v2,gno.land/r/$(NS)/lamplighter/taproom/v3,gno.land/r/$(NS)/lamplighter/lastorders/v1
