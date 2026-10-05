#!/usr/bin/env bash
# 交叉编译 nodepanel 的面板端与节点端到 dist/
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT=$(pwd)
OUT=${OUT:-$ROOT/dist}
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}

# 默认：Linux 全平台 + macOS/Windows 的面板端（节点端仅 Linux，读 /proc）
# 自定义: TARGETS="linux/amd64 darwin/arm64" ./scripts/build.sh
TARGETS=${TARGETS:-"linux/amd64 linux/arm64 linux/arm/v7 linux/386 darwin/amd64 darwin/arm64 windows/amd64"}
LDFLAGS="-s -w -X github.com/geekermd/nodepanel/internal/shared.Version=$VERSION"

mkdir -p "$OUT"
echo "==> nodepanel $VERSION"
echo "==> 输出目录: $OUT"

for t in $TARGETS; do
  os=${t%%/*}
  rest=${t#*/}
  arch=${rest%%/*}
  variant=""
  if [[ "$rest" == */* ]]; then variant=${rest##*/}; fi
  # 节点端只支持 Linux（读取 /proc），面板端各平台都能编译
  bins="nodemgr-panel"
  if [[ "$os" == "linux" ]]; then bins="nodemgr-panel nodemgr-agent"; fi

  for bin in $bins; do
    ext=""
    [[ "$os" == "windows" ]] && ext=".exe"
    # v7 -> _armv7，避免出现 armvv7
    suffix=""
    [[ -n "$variant" ]] && suffix="_${arch}${variant}"
    [[ -z "$suffix" ]] && suffix="_${arch}"
    name="${bin}_${VERSION}_${os}${suffix}${ext}"
    echo "--> $name"
    if [[ -n "$variant" ]]; then
      CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOARM=${variant#v} \
        go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/$name" "./cmd/${bin#nodemgr-}"
    else
      CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
        go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/$name" "./cmd/${bin#nodemgr-}"
    fi
  done
done

# 顺手给本机架构生成不带版本号的短名字，方便直接运行
host_os=$(go env GOOS); host_arch=$(go env GOARCH)
for bin in nodemgr-panel nodemgr-agent; do
  [[ "$host_os" != "linux" && "$bin" == "nodemgr-agent" ]] && continue
  CGO_ENABLED=0 GOOS=$host_os GOARCH=$host_arch go build -trimpath -ldflags "$LDFLAGS" \
    -o "$OUT/$bin" "./cmd/${bin#nodemgr-}"
done

echo
echo "==> 完成，产物："
ls -lh "$OUT" | sed 's/^/    /'
echo
echo "提示：节点端大小约 10 MB（已 -s -w 去符号表），可用 upx 进一步压缩。"
