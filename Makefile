.PHONY: build test vet lint build-rask-init codesign bundle-payload bundle

RASK_INIT_EMBED := internal/substrate/vz/embedded/rask-init

# TARGET selects bundle-payload/bundle's platform (see cmd/bundle-payload's
# doc comment for the full matrix); K8S_VERSION defaults to
# components.DefaultK8sVersion when unset.
TARGET ?= linux/amd64
K8S_VERSION ?=

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

# bundle-payload downloads and stages internal/components/bundlepayload/
# payload for one platform (TARGET), so a later `make bundle` has something
# real to embed. Network-heavy (dl.k8s.io/GitHub/Alpine, up to ~1GB for
# TARGET=darwin/arm64's guest userland); safe to re-run, it resumes rather
# than re-downloading already-staged blobs (see cmd/bundle-payload).
#
#   make bundle-payload TARGET=linux/amd64
#   make bundle-payload TARGET=darwin/arm64 K8S_VERSION=v1.34.0
bundle-payload:
	go run ./cmd/bundle-payload -target $(TARGET) $(if $(K8S_VERSION),-k8s-version $(K8S_VERSION),)

# bundle builds a self-contained rask binary for TARGET with whatever
# `make bundle-payload TARGET=...` has already staged compiled in
# (-tags bundle activates internal/components/bundlepayload/fs_bundle.go's
# go:embed directive — a no-op, network-fallback build if nothing was
# staged; see that package's doc comment). On Linux this is built with
# CGO_ENABLED=0 for a fully static binary (nothing in the linux
# hostproc substrate needs cgo — see internal/substrate/hostproc's
# `//go:build linux`), so a haro-style bake onto an arbitrary base image
# never has to worry about libc compatibility. Darwin needs the default
# CGO_ENABLED=1 (internal/substrate/vz cgo-links Virtualization.framework)
# and is ad-hoc codesigned afterward, exactly like the plain `codesign`
# target above.
bundle: build-rask-init
	@if [ "$$(go env GOOS)" = "darwin" ]; then \
		go build -tags bundle -o rask ./cmd/rask; \
		codesign --entitlements vz.entitlements -f -s - ./rask; \
	else \
		CGO_ENABLED=0 go build -tags bundle -o rask ./cmd/rask; \
	fi

test:
	go test -race -shuffle=on -count=1 ./...

vet:
	go vet ./...

lint: vet
	golangci-lint run ./...
