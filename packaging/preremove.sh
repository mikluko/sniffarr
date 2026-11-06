#!/bin/sh
# Stops and disables the service when the package is removed, not when it is
# upgraded.
set -e
if [ "$1" = remove ] && [ -d /run/systemd/system ]; then
	systemctl disable --now sniffarr.service >/dev/null 2>&1 || true
fi
