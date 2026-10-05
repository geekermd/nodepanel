#!/usr/bin/env bash
# 服务器端一键安装 nodepanel 节点端（nodemgr-agent）
#
#   curl -fsSL https://raw.githubusercontent.com/geekermd/nodepanel/main/scripts/install-agent.sh \
#     | sudo bash -s -- -port 8899 -password 'root密码'
#
# 所有参数都会透传给 `nodemgr-agent install`，常用：
#   -port 8899              监听端口（0 = 只走 cloudflared 内网穿透）
#   -password 'pw'          管理密码（同时作为 SSH 中继密码）
#   -tunnel                 启用 cloudflared 内网穿透
#   -tunnel-mode token      自有域名隧道（需配合 -tunnel-token）
#   -tunnel-token 'eyJ...'  cloudflared token
set -euo pipefail

REPO=${REPO:-geekermd/nodepanel}
VERSION=${VERSION:-latest}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

if [[ $(id -u) -ne 0 ]]; then
  echo "请使用 root 运行：sudo bash -s -- <参数>" >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  armv7l|armv7) ARCH=armv7 ;;
  i386|i686) ARCH=386 ;;
  *) echo "不支持的架构: $(uname -m)" >&2; exit 1 ;;
esac

if [[ "$VERSION" == "latest" ]]; then
  URL="https://github.com/$REPO/releases/latest/download/nodemgr-agent_latest_linux_${ARCH}"
  # latest 下载地址里带的是实际版本号，取一次重定向
  RESOLVED=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")
  TAG=${RESOLVED##*/}
  if [[ -n "$TAG" && "$TAG" != "latest" ]]; then
    URL="https://github.com/$REPO/releases/download/$TAG/nodemgr-agent_${TAG}_linux_${ARCH}"
  fi
else
  URL="https://github.com/$REPO/releases/download/$VERSION/nodemgr-agent_${VERSION}_linux_${ARCH}"
fi

echo "==> 下载 $URL"
if ! curl -fsSL -o "$TMP/nodemgr-agent" "$URL"; then
  echo "下载失败。请确认该 Release 已上传对应架构的二进制，或手动 scp 上传。" >&2
  exit 1
fi
chmod +x "$TMP/nodemgr-agent"

echo "==> 执行安装"
"$TMP/nodemgr-agent" install "$@"
