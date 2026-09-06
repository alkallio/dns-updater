# Version comes from the nearest git tag, so a built binary always names a
# release. Override with `make VERSION=... build` when building outside git.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo unknown)
LDFLAGS := -X main.version=$(VERSION)

INSTANCE ?= $(HOME)/local-services/dns-updater

.PHONY: build test install version

build:
	go build -ldflags "$(LDFLAGS)" -o dns-updater .

test:
	gofmt -l .
	go vet ./...
	go test ./...

version:
	@echo $(VERSION)

# Replaces the instance binary atomically, because the old one may be running.
install: build
	mv -f dns-updater $(INSTANCE)/dns-updater
	@echo "Installed $(VERSION) to $(INSTANCE)."
	@echo "Run: systemctl --user restart dns-updater.service"
