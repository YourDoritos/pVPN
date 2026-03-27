PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin
SYSTEMD_DIR ?= /etc/systemd/system
REAL_USER ?= $(or $(SUDO_USER),$(USER))

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build clean install uninstall

all: build

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o pvpnd  ./cmd/pvpnd
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o pvpn   ./cmd/pvpn
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o pvpnctl ./cmd/pvpnctl

clean:
	rm -f pvpn pvpnd pvpnctl

install: build
	install -Dm755 pvpnd   $(DESTDIR)$(BINDIR)/pvpnd
	install -Dm755 pvpn    $(DESTDIR)$(BINDIR)/pvpn
	install -Dm755 pvpnctl $(DESTDIR)$(BINDIR)/pvpnctl
	sed 's|^ExecStart=.*|ExecStart=$(BINDIR)/pvpnd|;/^Environment=SUDO_USER=/d;/^\[Service\]/a Environment=SUDO_USER=$(REAL_USER)' \
		dist/pvpnd.service | install -Dm644 /dev/stdin $(DESTDIR)$(SYSTEMD_DIR)/pvpnd.service

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/pvpn
	rm -f $(DESTDIR)$(BINDIR)/pvpnd
	rm -f $(DESTDIR)$(BINDIR)/pvpnctl
	rm -f $(DESTDIR)$(SYSTEMD_DIR)/pvpnd.service
	-systemctl stop pvpnd 2>/dev/null || true
	-systemctl disable pvpnd 2>/dev/null || true
