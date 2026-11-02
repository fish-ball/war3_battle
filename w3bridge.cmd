@echo off
rem 魔兽争霸3互联网联机工具（PowerShell 版）双击启动器
rem 参数默认与图形界面版一致；需要修改时直接跟在后面，例如:
rem   w3bridge.cmd -Server 1.2.3.4:51234 -Room 666 -Name 老崔
title 魔兽争霸3互联网联机工具
powershell -NoProfile -File "%~dp0w3bridge.ps1" %*
rem 退出后保留窗口方便查看日志；脚本化调用请直接用 powershell -File w3bridge.ps1
pause
