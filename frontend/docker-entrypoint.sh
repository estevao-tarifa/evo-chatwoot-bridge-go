#!/bin/sh
# Runs from nginx's /docker-entrypoint.d/ before nginx starts.
# Turns BRIDGE_API_BASE_URL into the runtime config the SPA reads.
set -e

: "${BRIDGE_API_BASE_URL:=}"

cat > /usr/share/nginx/html/config.js <<EOF
window.__BRIDGE_CONFIG__ = { apiBaseUrl: "${BRIDGE_API_BASE_URL}" };
EOF
