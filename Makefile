# df-hud
#
# `make` builds the Rust overlay. `make install` puts the binary somewhere
# stable and installs the systemd user unit; `make enable` starts it and starts
# it at login from then on.
#
# The install target exists mainly so the running df-hud is NOT the binary in this
# directory. Rebuilding under a running process leaves it holding the old inode:
# the log says one version, the file on disk is another, and the difference is
# invisible until it wastes an hour.

BIN     := df-hud
PREFIX  ?= $(HOME)/.local
BINDIR  := $(PREFIX)/bin
UNITDIR ?= $(HOME)/.config/systemd/user
CARGO   ?= cargo
RELEASE := target/release/$(BIN)

# Archive names and the `make install` banner. `--version` / User-Agent come
# from Cargo.toml. Tag CI passes VERSION= without the leading v.
VERSION ?= $(or $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//'),unknown)

.PHONY: all build test check package-linux package-windows install uninstall enable disable restart logs status clean

all: build

build:
	$(CARGO) build --release
	cp -f $(RELEASE) $(BIN)

# Product tests.
check:
	$(CARGO) test --locked

test:
	$(CARGO) test --locked

package-linux:
	./build-linux.sh "$(VERSION)"

package-windows:
	sh ./build-windows.sh "$(VERSION)"
	@test -f "dist/df-hud-$(VERSION)-windows-amd64.zip"

install: build
	install -Dm755 $(RELEASE) $(BINDIR)/$(BIN)
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
	$(CARGO) clean
