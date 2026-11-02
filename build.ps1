# 构建脚本：编译客户端（Windows）与服务端（Windows / Linux VPS）
# 输出到 dist 目录

$ErrorActionPreference = "Stop"

New-Item -ItemType Directory -Force -Path dist | Out-Null

# 客户端（Windows）
go build -o dist\w3bridge.exe .\cmd\w3bridge

# 客户端图形界面版（Windows）
go build -ldflags="-H windowsgui" -o dist\w3bridge-gui.exe .\cmd\w3bridge-gui

# 服务端（Windows）
go build -o dist\w3bridge-server.exe .\cmd\w3bridge-server

# 服务端（Linux VPS，amd64）与客户端（Linux，网关模式用）
# 网关模式：客户端跑在游戏机同局域网的树莓派/NAS/软路由等设备上，游戏机零安装
$env:CGO_ENABLED = "0"
$env:GOOS = "linux"
go build -o dist\w3bridge-server-linux-amd64 .\cmd\w3bridge-server
go build -o dist\w3bridge-linux-amd64 .\cmd\w3bridge
$env:GOARCH = "arm64"
go build -o dist\w3bridge-linux-arm64 .\cmd\w3bridge
$env:GOARCH = "arm"
$env:GOARM = "7"
go build -o dist\w3bridge-linux-armv7 .\cmd\w3bridge

# 恢复 Windows 构建环境
$env:GOOS = "windows"
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
Remove-Item Env:GOARM -ErrorAction SilentlyContinue

# PowerShell 版客户端直接分发脚本本体与双击启动器
Copy-Item w3bridge.ps1, w3bridge.cmd dist\ -Force

Write-Host "构建完成，产物在 dist 目录"
