#!/bin/bash
set -e

echo "=== pVPN Uninstall ==="
echo ""

# Stop and disable service
echo "Stopping pvpnd service..."
sudo systemctl stop pvpnd 2>/dev/null || true
sudo systemctl disable pvpnd 2>/dev/null || true

# Remove via Makefile
echo "Removing installed files..."
sudo make uninstall

# Reload systemd
sudo systemctl daemon-reload

echo ""
echo "=== Uninstall Complete ==="
echo ""
echo "Note: Config files in ~/.config/pvpn/ and session data in"
echo "~/.local/share/pvpn/ were NOT removed. Delete them manually"
echo "if you want a complete removal."
