#!/bin/sh
set -eu
SERVICE="${1:-Wi-Fi}"
echo "Setting DNS for macOS network service: $SERVICE"
sudo networksetup -setdnsservers "$SERVICE" 127.0.0.1
echo "Verify: scutil --dns"
echo "Restore: sudo networksetup -setdnsservers "$SERVICE" Empty"
