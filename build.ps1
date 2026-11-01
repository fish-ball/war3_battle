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

# 服务端（Linux VPS，amd64）
$env:CGO_ENABLED = "0"
$env:GOOS = "linux"
go build -o dist\w3bridge-server-linux-amd64 .\cmd\w3bridge-server

$env:GOOS = "windows"
Write-Host "构建完成，产物在 dist 目录"
