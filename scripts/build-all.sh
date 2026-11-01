#!/usr/bin/env sh
# Cross-platform release builder for HollowTone Server (Linux / macOS).
#
# Produces static binaries in ./dist with a SHA256SUMS.txt, embedding the
# version, commit and build time so a running deployment can be identified via
# GET /version.
#
#   sh scripts/build-all.sh
#   VERSION=v0.2.0 sh scripts/build-all.sh
#   TARGETS="linux/amd64,darwin/arm64" sh scripts/build-all.sh
set -eu

cd "$(dirname "$0")/.."

MODULE="github.com/YLHhhh123/HollowTone-server"
OUT_DIR="${OUT_DIR:-dist}"
TARGETS="${TARGETS:-linux/amd64,linux/arm64,windows/amd64}"

if [ -z "${VERSION:-}" ]; then
    VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
fi
COMMIT="$(git rev-parse HEAD 2>/dev/null || echo none)"
SHORT_COMMIT="$(printf '%s' "$COMMIT" | cut -c1-12)"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "== HollowTone Server build =="
echo "  version : $VERSION"
echo "  commit  : $SHORT_COMMIT"
echo "  built   : $BUILD_TIME"

echo
echo "[1/4] go test ./..."
go test ./... >/dev/null
echo "      ok"

echo "[2/4] go vet ./..."
go vet ./...
echo "      ok"

LDFLAGS="-s -w \
 -X $MODULE/internal/version.Version=$VERSION \
 -X $MODULE/internal/version.Commit=$SHORT_COMMIT \
 -X $MODULE/internal/version.BuildTime=$BUILD_TIME"

mkdir -p "$OUT_DIR"
rm -f "$OUT_DIR"/hollowtone-server* 2>/dev/null || true

echo
echo "[3/4] building targets"
BUILT=""
OLD_IFS="$IFS"
IFS=','
for t in $TARGETS; do
    IFS="$OLD_IFS"
    goos="$(printf '%s' "$t" | cut -d/ -f1 | tr -d ' ')"
    goarch="$(printf '%s' "$t" | cut -d/ -f2 | tr -d ' ')"
    [ -z "$goos" ] && continue
    name="hollowtone-server-$goos-$goarch"
    [ "$goos" = "windows" ] && name="$name.exe"
    echo "      $goos/$goarch -> $OUT_DIR/$name"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
        go build -trimpath -ldflags "$LDFLAGS" -o "$OUT_DIR/$name" ./cmd/hollowtone-server
    BUILT="$BUILT $name"
    IFS=','
done
IFS="$OLD_IFS"

echo
echo "[4/4] SHA256SUMS.txt"
: > "$OUT_DIR/SHA256SUMS.txt"
for n in $BUILT; do
    if command -v sha256sum >/dev/null 2>&1; then
        (cd "$OUT_DIR" && sha256sum "$n") >> "$OUT_DIR/SHA256SUMS.txt"
    else
        h="$(shasum -a 256 "$OUT_DIR/$n" | cut -d' ' -f1)"
        printf '%s  %s\n' "$h" "$n" >> "$OUT_DIR/SHA256SUMS.txt"
    fi
done
cat "$OUT_DIR/SHA256SUMS.txt"

printf '# HollowTone Server %s\n\nBuilt %s from commit %s.\n\nVerify before running:\n\n```bash\nsha256sum -c SHA256SUMS.txt\n```\n\nConfirm which build is live:\n\n```bash\ncurl -s http://127.0.0.1:8080/version\n```\n' \
    "$VERSION" "$BUILD_TIME" "$SHORT_COMMIT" > "$OUT_DIR/README.md"

echo
echo "Done. Artifacts in $OUT_DIR"
ls -lh "$OUT_DIR"
