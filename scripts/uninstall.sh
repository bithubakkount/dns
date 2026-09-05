#!/bin/sh
set -eu
brew services stop localdns 2>/dev/null || true
echo "Restore DNS with:"
echo "sudo networksetup -setdnsservers "Wi-Fi" Empty"
