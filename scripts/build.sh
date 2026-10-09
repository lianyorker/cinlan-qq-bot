#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BIN="$ROOT/bin"
OUTPUT="$BIN/cinlan-qq-bot"

if ! command -v go >/dev/null 2>&1; then
    echo "go is required to build cinlan-qq-bot" >&2
    exit 1
fi

mkdir -p "$BIN"
if [ "${SKIP_TESTS:-0}" != "1" ]; then
    (cd "$ROOT" && go test ./...)
    (cd "$ROOT" && go vet ./...)
fi

(cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$OUTPUT" ./cmd/cinlan-qq-bot)
chmod 0755 "$OUTPUT"
printf 'Built %s\n' "$OUTPUT"
