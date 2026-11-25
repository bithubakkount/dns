#!/bin/sh
set -eu
SERVICE="${1:-Wi-Fi}"
brew services stop localdns 2>/dev/null || true
echo "Restore DNS with:"
echo "sudo networksetup -setdnsservers \"$SERVICE\" Empty"
