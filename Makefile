.PHONY: build test vet lint build-rask-init codesign

RASK_INIT_EMBED := internal/substrate/vz/embedded/rask-init

# build-rask-init cross-compiles cmd/rask-init (the vz guest's PID 1) for
# linux/arm64 and overwrites the placeholder internal/substrate/vz/embedded
# checks into version control, so `go build ./...` on macOS produces a
# self-contained rask binary with no dependency on the source tree being
# available at runtime (see internal/substrate/vz/embedded's package doc).
# Safe to run on any host: it's a pure cross-compile (CGO_ENABLED=0), never
# executed here.
build-rask-init:
	rm -f $(RASK_INIT_EMBED)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o $(RASK_INIT_EMBED) ./cmd/rask-init

build: build-rask-init
	go build ./...
	go build -o rask ./cmd/rask

# codesign ad-hoc signs the rask binary with the
# com.apple.security.virtualization entitlement, required for the vz
# substrate's vm.Start() to succeed at all (see vz.entitlements). A no-op
# (with a warning) on non-macOS hosts.
codesign: build
	@if [ "$$(go env GOOS)" = "darwin" ]; then \
		codesign --entitlements vz.entitlements -f -s - ./rask; \
	else \
		echo "codesign: skipping, not on darwin"; \
	fi

test:
	go test -race -shuffle=on -count=1 ./...

vet:
	go vet ./...

lint: vet
	golangci-lint run ./...
