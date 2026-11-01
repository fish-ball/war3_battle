#!/usr/bin/env bash
# war3_battle 中继服务端安装脚本（Ubuntu，安装为 systemd 服务）
# 用法: sudo ./install.sh [服务端二进制路径]
# 不带参数时按脚本同目录、上级 dist 目录的顺序查找 w3bridge-server-linux-amd64
set -euo pipefail

SERVICE_NAME="w3bridge"
INSTALL_PATH="/usr/local/bin/w3bridge-server"
UNIT_PATH="/etc/systemd/system/${SERVICE_NAME}.service"
DEFAULT_ENV_PATH="/etc/default/${SERVICE_NAME}"

if [ "$(id -u)" -ne 0 ]; then
    echo "请用 root 权限运行: sudo $0"
    exit 1
fi

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
if [ -n "${1:-}" ]; then
    BIN_PATH="$1"
elif [ -f "${SCRIPT_DIR}/w3bridge-server-linux-amd64" ]; then
    BIN_PATH="${SCRIPT_DIR}/w3bridge-server-linux-amd64"
else
    BIN_PATH="${SCRIPT_DIR}/../dist/w3bridge-server-linux-amd64"
fi

if [ ! -f "$BIN_PATH" ]; then
    echo "找不到服务端二进制: $BIN_PATH"
    echo "用法: sudo $0 [服务端二进制路径]"
    exit 1
fi

echo "安装二进制: $BIN_PATH -> $INSTALL_PATH"
install -m 755 "$BIN_PATH" "$INSTALL_PATH"

# 监听地址配置文件（已存在则保留用户的修改）
if [ ! -f "$DEFAULT_ENV_PATH" ]; then
    cat > "$DEFAULT_ENV_PATH" <<'EOF'
# war3_battle 服务端监听地址
LISTEN=:51234
EOF
    echo "已生成配置: $DEFAULT_ENV_PATH（默认监听 :51234）"
fi

cat > "$UNIT_PATH" <<EOF
[Unit]
Description=war3_battle relay server (Warcraft III internet LAN tool)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
DynamicUser=true
Environment=LISTEN=:51234
EnvironmentFile=-${DEFAULT_ENV_PATH}
ExecStart=${INSTALL_PATH} -listen \${LISTEN}
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now "${SERVICE_NAME}.service"

echo "安装完成。常用命令:"
echo "  查看状态: systemctl status ${SERVICE_NAME}"
echo "  查看日志: journalctl -u ${SERVICE_NAME} -f"
echo "  修改端口: 编辑 ${DEFAULT_ENV_PATH} 后执行 systemctl restart ${SERVICE_NAME}"
echo "  卸载服务: sudo ./uninstall.sh"
