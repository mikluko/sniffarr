#!/bin/sh
# Enables the service and starts it, or restarts it on an upgrade, where
# systemd runs.
set -e
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload
	systemctl enable sniffarr.service >/dev/null 2>&1 || true
	systemctl restart sniffarr.service || true
fi
