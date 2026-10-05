#!/usr/bin/env bash
# 本机安装：面板（systemd 服务，开机自启） + 监控本机的节点端
#   sudo bash scripts/install-local.sh '你的管理密码'
# 只安装面板：sudo bash scripts/install-local.sh --panel-only
set -euo pipefail

cd "$(dirname "$0")/.."
REPO=$(pwd)
PW="${1:-}"
INSTALL_DIR=/opt/nodepanel
DATA_DIR=${HOME}/.nodepanel
TARGET_USER=${SUDO_USER:-${USER}}

if [[ $(id -u) -ne 0 ]]; then
  echo "请用 sudo 运行：sudo bash scripts/install-local.sh <管理密码>" >&2
  exit 1
fi
if [[ -z "$PW" ]]; then
  PW=$(head -c 12 /dev/urandom | base64 | tr -d '/+=' | head -c 12)
  echo "未指定密码，已随机生成：$PW"
fi

echo "==> 编译"
make build >/dev/null
mkdir -p "$INSTALL_DIR"
install -m 0755 dist/nodemgr-panel "$INSTALL_DIR/nodemgr-panel"

echo "==> 面板服务"
cat > /etc/systemd/system/nodemgr-panel.service <<EOF
[Unit]
Description=nodepanel 管理面板
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$TARGET_USER
Environment=HOME=/home/$TARGET_USER
ExecStart=$INSTALL_DIR/nodemgr-panel -listen 0.0.0.0:8787 -home $DATA_DIR
Restart=always
RestartSec=3
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now nodemgr-panel
sleep 2
systemctl --no-pager --lines=0 status nodemgr-panel | head -4

echo "==> 设置面板密码"
sudo -u "$TARGET_USER" "$INSTALL_DIR/nodemgr-panel" -home "$DATA_DIR" -set-password "$PW"

if [[ "$PW" != "--panel-only" ]]; then
  echo "==> 安装本机节点端（监控这台电脑自己，端口 8899）"
  install -m 0755 dist/nodemgr-agent "$INSTALL_DIR/nodemgr-agent"
  "$INSTALL_DIR/nodemgr-agent" install -port 8899 -password "$PW" -no-start >/tmp/np-agent-install.log 2>&1 || true
  # install 会把二进制装到 /usr/local/bin，这里直接复用它的 unit 并启动
  systemctl enable --now nodemgr-agent
  sleep 2
  systemctl --no-pager --lines=0 status nodemgr-agent | head -4
  TOKEN=$(grep -o '"admin_tok_hash": "[^"]*"' /etc/nodemgr-agent/config.json | cut -d'"' -f4)
  echo "（令牌以哈希保存，明文只在安装时输出；如需重置：sudo nodemgr-agent -gen-token）"
fi

IP=$(hostname -I 2>/dev/null | awk '{print $1}')
cat <<EOF

========================================
 本机安装完成
   面板:   http://127.0.0.1:8787   （局域网: http://${IP:-<本机IP>}:8787）
   密码:   $PW
   数据:   $DATA_DIR
   服务:   systemctl status|restart nodemgr-panel
           systemctl status|restart nodemgr-agent
========================================
EOF
