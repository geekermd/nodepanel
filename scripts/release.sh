#!/usr/bin/env bash
# 打标签 → 全平台编译 → 生成 checksums → 上传 GitHub Release
set -euo pipefail

cd "$(dirname "$0")/.."
VERSION=${1:-}
REPO=${REPO:-geekermd/nodepanel}

if [[ -z "$VERSION" ]]; then
  echo "用法: $0 <版本号>   例如: $0 v1.0.0"
  exit 1
fi
[[ "$VERSION" == v* ]] || VERSION="v$VERSION"

if ! command -v gh >/dev/null 2>&1; then
  echo "需要 GitHub CLI (gh)，并且已 gh auth login" >&2
  exit 1
fi

echo "==> 编译 $VERSION"
VERSION="$VERSION" OUT="$PWD/dist" ./scripts/build.sh

echo "==> 生成校验和"
( cd dist && sha256sum nodemgr-* > SHA256SUMS && cat SHA256SUMS | sed 's/^/    /' )

if git rev-parse "$VERSION" >/dev/null 2>&1; then
  echo "==> 标签 $VERSION 已存在"
else
  echo "==> 创建标签 $VERSION"
  git tag -a "$VERSION" -m "nodepanel $VERSION"
  git push origin "$VERSION"
fi

echo "==> 上传 Release"
gh release create "$VERSION" dist/nodemgr-* dist/SHA256SUMS \
  --repo "$REPO" \
  --title "nodepanel $VERSION" \
  --notes "详见 README。产物：nodemgr-panel（电脑端）、nodemgr-agent（服务器端，仅 Linux）。" \
  || gh release upload "$VERSION" dist/nodemgr-* dist/SHA256SUMS --repo "$REPO" --clobber

echo "==> 完成：https://github.com/$REPO/releases/tag/$VERSION"
