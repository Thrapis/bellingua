#!/usr/bin/env sh
# Builds the web UI (static export) and embeds it into the bellingua binary.
set -e
cd "$(dirname "$0")/.."
(cd web && { [ -d node_modules ] || npm ci; } && npm run build)
find internal/ui/dist -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
cp -R web/out/. internal/ui/dist/
ext=""; [ "$(go env GOOS)" = "windows" ] && ext=".exe"
go build -trimpath -ldflags "-s -w" -o "bellingua$ext" ./cmd/bellingua
echo "built bellingua$ext"
