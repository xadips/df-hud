# df-hud
#
# `make` builds. `make install` puts the binary somewhere stable and installs the
# systemd user unit; `make enable` starts it and starts it at login from then on.
#
# The install target exists mainly so the running df-hud is NOT the binary in this
# directory. Rebuilding under a running process leaves it holding the old inode:
# the log says one version, the file on disk is another, and the difference is
# invisible until it wastes an hour.

BIN     := df-hud
PREFIX  ?= $(HOME)/.local
BINDIR  := $(PREFIX)/bin
UNITDIR ?= $(HOME)/.config/systemd/user
DOCKER  ?= docker
WINE    ?= wine
WINDOWS_BUILDER_IMAGE ?= df-hud-windows-builder

# Stamped into -version and the User-Agent, so a running daemon can be told from a
# working tree. The base stays a dev string - nothing produces release numbers yet -
# and the commit is what makes it answerable.
REV     := $(shell git describe --always --dirty 2>/dev/null || echo unknown)
VERSION ?= 0.1.0-dev+$(REV)

.PHONY: all build test check test-windows package-linux windows-builder package-windows smoke-windows install uninstall enable disable restart logs status clean

all: build

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/df-hud

# Everything CI would run, in the order that fails fastest.
check:
	gofmt -l .
	go vet ./...
	go test ./... -race
	go build -tags nolayershell -o /dev/null ./cmd/df-hud

test:
	go test ./...

test-windows:
	WINEDEBUG=-all GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
		go test -tags nolayershell -exec "$(WINE)" ./... -count=1

package-linux:
	./build-linux.sh "$(VERSION)"

windows-builder:
	$(DOCKER) build -f Dockerfile.windows -t $(WINDOWS_BUILDER_IMAGE) .

package-windows: windows-builder
	$(DOCKER) run --rm \
		-e HOST_UID="$$(id -u)" \
		-e HOST_GID="$$(id -g)" \
		-v "$(CURDIR):/src" \
		-v df-hud-windows-go-mod:/root/go/pkg/mod \
		-v df-hud-windows-go-build:/root/.cache/go-build \
		$(WINDOWS_BUILDER_IMAGE) "$(VERSION)"

smoke-windows:
	cd dist/df-hud-windows-amd64 && \
		WINEDEBUG=-all $(WINE) ./df-hud.exe -version && \
		WINEDEBUG=-all $(WINE) ./df-hud.exe -check-config

install: build
	install -Dm755 $(BIN) $(BINDIR)/$(BIN)
	install -Dm644 contrib/df-hud.service $(UNITDIR)/df-hud.service
	systemctl --user daemon-reload
	@echo
	@echo "installed $(BINDIR)/$(BIN) ($(VERSION))"
	@echo "next: make enable    # start it, and at every login"

# --now so it starts here rather than at the next login, which is the surprise
# otherwise: enable succeeds and nothing appears.
enable:
	systemctl --user enable --now df-hud.service
	@systemctl --user --no-pager status df-hud.service | head -5

disable:
	systemctl --user disable --now df-hud.service

# A restart loses nothing: the run clock and the XP window are persisted, and the
# run is tied to the game's own process so it resumes rather than restarting.
restart: install
	systemctl --user restart df-hud.service

logs:
	journalctl --user -u df-hud.service -f -n 50

status:
	systemctl --user --no-pager status df-hud.service

uninstall: disable
	rm -f $(BINDIR)/$(BIN) $(UNITDIR)/df-hud.service
	systemctl --user daemon-reload

clean:
	rm -f $(BIN)
