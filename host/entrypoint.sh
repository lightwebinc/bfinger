#!/bin/sh
# Starts the host. The one thing added to the stock entrypoint: an admin token
# is required by the host and is not something a first run should have to
# invent, so an unset one is replaced by a random one for this run. Nothing
# can use the admin surface until the operator sets their own.
set -eu
if [ -z "${OVERLAY_ADMIN_TOKEN:-}" ]; then
  OVERLAY_ADMIN_TOKEN=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
  export OVERLAY_ADMIN_TOKEN
  echo "finger-host: OVERLAY_ADMIN_TOKEN is unset; using a random one for this run (set it to use /admin)" >&2
fi
exec node /app/dist/index.js "$@"
