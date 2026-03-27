#!/bin/bash
set -e

echo "=== pVPN Install ==="
echo ""

# Check for Go
if ! command -v go &>/dev/null; then
    echo "Error: Go is required but not installed."
    echo "Install Go from https://go.dev/dl/ or your package manager."
    exit 1
fi

# Build and install via Makefile
echo "Building and installing..."
sudo make install

# Reload systemd
echo "Reloading systemd..."
sudo systemctl daemon-reload

# Enable and start daemon
echo "Enabling pvpnd service..."
sudo systemctl enable pvpnd
sudo systemctl start pvpnd

echo ""
echo "=== Installation Complete ==="
echo ""
echo "  pvpnd   -> daemon (running as systemd service)"
echo "  pvpn    -> TUI client (run as your user)"
echo "  pvpnctl -> CLI client (run as your user)"
echo ""
echo "Usage:"
echo "  pvpn                    Open TUI"
echo "  pvpnctl status          Check VPN status"
echo "  pvpnctl connect fastest Connect to fastest server"
echo "  pvpnctl disconnect      Disconnect"
echo ""
echo "Uninstall: ./uninstall.sh or make uninstall"
