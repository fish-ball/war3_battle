#!/usr/bin/env bash
# war3_battle 中继服务端卸载脚本（Ubuntu，移除 systemd 服务）
# 用法: sudo ./uninstall.sh
set -euo pipefail

SERVICE_NAME="w3bridge"
INSTALL_PATH="/usr/local/bin/w3bridge-server"
UNIT_PATH="/etc/systemd/system/${SERVICE_NAME}.service"
DEFAULT_ENV_PATH="/etc/default/${SERVICE_NAME}"

if [ "$(id -u)" -ne 0 ]; then
    echo "请用 root 权限运行: sudo $0"
    exit 1
fi

if [ -f "$UNIT_PATH" ]; then
    if ! systemctl disable --now "${SERVICE_NAME}.service"; then
        echo "警告: 停止服务失败，可能服务未在运行，继续清理文件"
    fi
else
    echo "未找到服务定义文件 ${UNIT_PATH}，跳过停止服务"
fi

rm -f "$UNIT_PATH" "$INSTALL_PATH" "$DEFAULT_ENV_PATH"
systemctl daemon-reload

echo "卸载完成，已移除:"
echo "  $UNIT_PATH"
echo "  $INSTALL_PATH"
echo "  $DEFAULT_ENV_PATH"
