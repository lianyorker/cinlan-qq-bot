#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ENV_FILE="$ROOT/.env"
EXAMPLE_FILE="$ROOT/.env.example"
EXECUTABLE="$ROOT/bin/cinlan-qq-bot"

if [ ! -f "$ENV_FILE" ]; then
    cp "$EXAMPLE_FILE" "$ENV_FILE"
    echo "Created .env from .env.example. Configure QQ_PLATFORM, QQ_GROUP_ALLOWLIST, AGENT_API_URL, and SESSION_ENCRYPTION_KEY, then run scripts/start.sh again." >&2
    exit 2
fi

# The deployment .env is trusted local configuration. Export it without
# changing the caller's shell after this process exits.
set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

if [ ! -x "$EXECUTABLE" ]; then
    "$ROOT/scripts/build.sh"
fi

cd "$ROOT"
exec "$EXECUTABLE"
