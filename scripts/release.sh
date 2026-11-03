#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd -- "$root"

fail() {
    echo "release: $*" >&2
    exit 1
}

version=${VERSION:-dev}
if [[ ! $version =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    fail 'VERSION 必须为语义化版本号 MAJOR.MINOR.PATCH，例如 1.2.3'
fi
[[ ! -e dist/$version && ! -L dist/$version ]] || fail "dist/$version 已存在，请先移走原产物；不会覆盖已有发行产物"

GO=${GO:-go}
NPM=${NPM:-npm}
MAKE=${MAKE:-make}
export GO NPM
# Ignore ambient cross-compilation/workspace flags; release Linux binaries for both supported architectures.
expected_go=$(<.go-version)
export GOTOOLCHAIN="go$expected_go" GOWORK=off GOFLAGS=-mod=readonly
export GOOS=linux GOARCH=amd64 GOAMD64=v1 GOARM64=v8.0 CGO_ENABLED=0
[[ $("$GO" env GOVERSION) == "go$expected_go" ]] || fail "需要 Go $expected_go"
[[ $("$GO" env GOHOSTOS GOHOSTARCH) == $'linux\namd64' ]] || fail '发行构建只支持在 linux/amd64 上执行'
expected_node=$(node -p 'require("./web/package.json").engines.node')
expected_npm=$(node -p 'require("./web/package.json").engines.npm')
[[ $(node -p 'process.versions.node') == "$expected_node" ]] || fail "需要 Node $expected_node"
[[ $(<web/.node-version) == "$expected_node" ]] || fail 'web/.node-version 与 package.json 不一致'
[[ $("$NPM" --version) == "$expected_npm" ]] || fail "需要 npm $expected_npm"

"$MAKE" fmt-check vet test test-quality
# Separate invocations also preserve this ordering under make -j.
"$MAKE" web-install
"$MAKE" web-check
"$MAKE" web-build
"$MAKE" test-ui

mkdir -p dist
stage=$(mktemp -d "$root/dist/.release-${version}.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT
install -d -m 0755 "$stage/output"
for arch in amd64 arm64; do
    name="dmemd_${version}_linux_${arch}"
    # Ship the executable itself: no archive wrapper, local config, database or credentials.
    GOARCH="$arch" "$GO" build -trimpath -buildvcs=false -tags webui \
        -ldflags "-s -w -buildid= -X main.version=$version" \
        -o "$stage/output/$name" ./cmd/dmemd
    chmod 0755 "$stage/output/$name"
done
(
    cd -- "$stage/output"
    sha256sum dmemd_"$version"_linux_* > SHA256SUMS
)

# Verify both binaries, then run the native binary against isolated temp data.
DMEMD_RELEASE_DIR="$stage/output" DMEMD_RELEASE_VERSION="$version" \
    "$GO" test -count=1 -v ./tests/release
mv -T -- "$stage/output" "$root/dist/$version"
echo "发行产物：dist/$version/dmemd_${version}_linux_{amd64,arm64} 和 dist/$version/SHA256SUMS"
