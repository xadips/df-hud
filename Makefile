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
WINDOWS_VM_COMPOSE := windows-vm/compose.yml
WINDOWS_VM_SSH_DIR := windows-vm/.ssh
WINDOWS_VM_USER ?= dfhud

# Stamped into -version and the User-Agent, so a running daemon can be told from a
# working tree. The base stays a dev string - nothing produces release numbers yet -
# and the commit is what makes it answerable.
REV     := $(shell git describe --always --dirty 2>/dev/null || echo unknown)
VERSION ?= 0.1.0-dev+$(REV)

.PHONY: all build test check test-windows package-linux package-windows smoke-windows windows-vm-key windows-vm-up windows-vm-down windows-vm-logs install uninstall enable disable restart logs status clean

all: build

build:
	go build -buildvcs=false -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/df-hud

# Everything CI would run, in the order that fails fastest.
check:
	gofmt -l .
	go vet ./...
	go test ./... -race
	go build -buildvcs=false -tags nolayershell -o /dev/null ./cmd/df-hud

test:
	go test ./...

test-windows:
	WINEDEBUG=-all GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
		go test -tags nolayershell -exec "$(WINE)" ./... -count=1

package-linux:
	./build-linux.sh "$(VERSION)"

windows-vm-key:
	@mkdir -p $(WINDOWS_VM_SSH_DIR)
	@if [ ! -f $(WINDOWS_VM_SSH_DIR)/id_ed25519 ]; then \
		ssh-keygen -q -t ed25519 -N "" -C df-hud-windows-vm \
			-f $(WINDOWS_VM_SSH_DIR)/id_ed25519; \
	fi
	@chmod 600 $(WINDOWS_VM_SSH_DIR)/id_ed25519

package-windows: windows-vm-key
	ssh -p 2222 \
		-i $(WINDOWS_VM_SSH_DIR)/id_ed25519 \
		-o BatchMode=yes \
		-o StrictHostKeyChecking=accept-new \
		-o UserKnownHostsFile=$(CURDIR)/$(WINDOWS_VM_SSH_DIR)/known_hosts \
		$(WINDOWS_VM_USER)@127.0.0.1 \
		powershell.exe -NoProfile -ExecutionPolicy Bypass \
		-File C:\\OEM\\build-df-hud.ps1 \
		-Version "$(VERSION)" -NonInteractive
	@test -f dist/df-hud-windows-amd64-native.zip

smoke-windows:
	cd dist/df-hud-windows-amd64 && \
		WINEDEBUG=-all $(WINE) ./df-hud.exe -version && \
		WINEDEBUG=-all $(WINE) ./df-hud.exe -check-config

windows-vm-up:
	$(DOCKER) compose -f $(WINDOWS_VM_COMPOSE) up -d
	@echo "Windows console: http://127.0.0.1:8006/"

windows-vm-down:
	$(DOCKER) compose -f $(WINDOWS_VM_COMPOSE) down

windows-vm-logs:
	$(DOCKER) compose -f $(WINDOWS_VM_COMPOSE) logs -f

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
