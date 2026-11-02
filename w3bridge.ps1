# w3bridge.ps1 — 魔兽争霸3互联网联机客户端（PowerShell 实现）
# 与 w3bridge.exe 功能一致，共用同一服务端：
#   1. 发现本局域网的游戏并发布到服务端房间；
#   2. 把房间里的游戏广播到本机网络（改写游戏端口指向本地代理）；
#   3. 本机游戏加入远程游戏时，经服务端中继到主机方。
# 用法: powershell -File w3bridge.ps1
#   所有参数均与图形界面版一致取默认值（服务端 war3.aiunit.com:51234、房间 666、
#   玩家名自动取当前 Windows 用户名），需要修改时手动指定，例如:
#   powershell -File w3bridge.ps1 -Server 1.2.3.4:51234 -Room 666 -Name 老崔

[CmdletBinding()]
param(
    [string]$Server = "war3.aiunit.com:51234",
    [string]$Room = "666",
    [string]$Pass = "",
    [string]$Name = $env:USERNAME,
    [int]$GamePort = 6112,
    [int]$ProxyBase = 26112
)

$ErrorActionPreference = "Stop"

# ---------------- 基础工具 ----------------

# 游戏内字符串为 GBK 编码；PowerShell 7 需要先注册代码页提供器，5.1 自带支持
try {
    [System.Text.Encoding]::RegisterProvider([System.Text.CodePagesEncodingProvider]::Instance)
} catch [System.Management.Automation.RuntimeException] {
    # PowerShell 5.1 无此类型，忽略
}
$script:Gbk = [System.Text.Encoding]::GetEncoding(936)

function Write-Log([string]$Message) {
    Write-Host ("{0} [客户端] {1}" -f (Get-Date -Format "HH:mm:ss"), $Message)
}

# 帧协议头为大端；W3GS 包内部字段为小端（BitConverter 在 x86 上即小端）
function ConvertTo-BE32([uint32]$Value) {
    $b = [BitConverter]::GetBytes($Value)
    [Array]::Reverse($b)
    return $b
}

# ---------------- W3GS 协议 ----------------

# 自适配查询的产品与版本范围（产品：冰封王座/混乱之治；版本：1.05-1.50）
$script:SearchProducts = @(
    [byte[]](0x50, 0x58, 0x33, 0x57),  # W3XP 冰封王座
    [byte[]](0x33, 0x52, 0x41, 0x57)   # WAR3 混乱之治
)
$script:MinSearchVersion = 5
$script:MaxSearchVersion = 50

# 生成 0x2F 局域网游戏查询包
function New-SearchGame([byte[]]$Product, [uint32]$Version) {
    $pkt = New-Object byte[] 16
    $pkt[0] = 0xF7
    $pkt[1] = 0x2F
    [BitConverter]::GetBytes([uint16]16).CopyTo($pkt, 2)
    $Product.CopyTo($pkt, 4)
    [BitConverter]::GetBytes($Version).CopyTo($pkt, 8)
    return $pkt
}

# 生成 0x32 游戏刷新包（同步本地列表人数）
function New-RefreshGame([uint32]$GameID, [uint32]$Players, [uint32]$Slots) {
    $pkt = New-Object byte[] 16
    $pkt[0] = 0xF7
    $pkt[1] = 0x32
    [BitConverter]::GetBytes([uint16]16).CopyTo($pkt, 2)
    [BitConverter]::GetBytes($GameID).CopyTo($pkt, 4)
    [BitConverter]::GetBytes($Players).CopyTo($pkt, 8)
    [BitConverter]::GetBytes($Slots).CopyTo($pkt, 12)
    return $pkt
}

# 生成 0x33 游戏取消包
function New-CancelGame([uint32]$GameID) {
    $pkt = New-Object byte[] 8
    $pkt[0] = 0xF7
    $pkt[1] = 0x33
    [BitConverter]::GetBytes([uint16]8).CopyTo($pkt, 2)
    [BitConverter]::GetBytes($GameID).CopyTo($pkt, 4)
    return $pkt
}

# 复制 0x30 包并改写包尾 2 字节的 TCP 游戏端口
function Update-GamePort([byte[]]$Raw, [uint16]$Port) {
    $pkt = [byte[]]$Raw.Clone()
    [BitConverter]::GetBytes($Port).CopyTo($pkt, $pkt.Length - 2)
    return $pkt
}

# 读取 0 结尾字符串，返回文本与占用字节数（含结尾 0）
function Read-CString([byte[]]$Data, [int]$Offset) {
    $end = $Offset
    while ($end -lt $Data.Length -and $Data[$end] -ne 0) { $end++ }
    $len = $end - $Offset
    $bytes = New-Object byte[] $len
    if ($len -gt 0) { [Array]::Copy($Data, $Offset, $bytes, 0, $len) }
    return @{ Text = $script:Gbk.GetString($bytes); Consumed = $len + 1 }
}

# 解码 0x30 包中的编码串（每 8 字节首字节为控制字节，对应位为 0 的字节减 1）
function Decode-EncodedString([byte[]]$Data, [int]$Offset) {
    $end = $Offset
    while ($end -lt $Data.Length -and $Data[$end] -ne 0) { $end++ }
    $out = New-Object System.Collections.Generic.List[byte]
    $mask = [byte]0
    for ($i = $Offset; $i -lt $end; $i++) {
        $rel = $i - $Offset
        if ($rel % 8 -eq 0) { $mask = $Data[$i]; continue }
        if (($mask -band (1 -shl ($rel % 8))) -eq 0) { $out.Add([byte]($Data[$i] - 1)) } else { $out.Add($Data[$i]) }
    }
    return , $out.ToArray()
}

# 解析 0x30 游戏信息包
function ConvertFrom-GameInfo([byte[]]$Pkt) {
    if ($Pkt.Length -lt 44 -or $Pkt[0] -ne 0xF7 -or $Pkt[1] -ne 0x30) { return $null }

    $info = New-Object PSObject -Property @{
        Product  = "W3XP"
        Version  = [BitConverter]::ToUInt32($Pkt, 8)
        GameID   = [BitConverter]::ToUInt32($Pkt, 12)
        Name     = ""
        MapName  = ""
        HostName = ""
        Slots    = [BitConverter]::ToUInt32($Pkt, $Pkt.Length - 22)
        Players  = [BitConverter]::ToUInt32($Pkt, $Pkt.Length - 14)
        NonComputerSlots = [BitConverter]::ToUInt32($Pkt, $Pkt.Length - 10)
        TCPPort  = [BitConverter]::ToUInt16($Pkt, $Pkt.Length - 2)
        Raw      = [byte[]]$Pkt.Clone()
    }
    # 产品标识：线上为 ASCII 反序
    if ($Pkt[4] -eq 0x33 -and $Pkt[5] -eq 0x52) { $info.Product = "WAR3" }

    $name = Read-CString $Pkt 20
    $info.Name = $name.Text
    $encodedStart = 20 + $name.Consumed + 1
    if ($encodedStart -ge $Pkt.Length) { return $null }

    $decoded = Decode-EncodedString $Pkt $encodedStart
    if ($decoded.Length -lt 13) { return $null }
    $map = Read-CString $decoded 13
    $info.MapName = ($map.Text -split "\\")[-1]
    $hostName = Read-CString $decoded (13 + $map.Consumed)
    $info.HostName = $hostName.Text
    return $info
}

# ---------------- 帧协议（与 Go 版服务端一致） ----------------

$FrameHello        = 0x01
$FrameHelloAck     = 0x02
$FrameMemberJoin   = 0x03
$FrameMemberLeave  = 0x04
$FrameGameAdd      = 0x05
$FrameGameDel      = 0x06
$FrameStreamOpen   = 0x07
$FrameStreamResult = 0x08
$FrameStreamData   = 0x09
$FrameStreamClose  = 0x0A
$FramePing         = 0x0B

$script:ServerClient = $null
$script:ServerStream = $null
$script:RecvQueue = New-Object System.Collections.Generic.List[byte]

# 联机状态表
$script:LocalGames = @{}   # 游戏身份 -> 本局域网发现的游戏
$script:RemoteGames = @{}  # gameKey -> 房间里的远程游戏
$script:Streams = @{}      # 全局流ID -> 本地 TCP 连接
$script:Pending = @{}      # 请求ID -> 等待建立转发流的本地 TCP 连接
$script:NextGameSeq = 0
$script:NextReqID = 0
$script:NextProxyPort = $ProxyBase

function Send-Frame([byte]$Type, [byte[]]$Payload) {
    if ($null -eq $Payload) { $Payload = [byte[]]@() }
    $hdr = New-Object byte[] 5
    $hdr[0] = $Type
    (ConvertTo-BE32 $Payload.Length).CopyTo($hdr, 1)
    $script:ServerStream.Write($hdr, 0, 5)
    if ($Payload.Length -gt 0) { $script:ServerStream.Write($Payload, 0, $Payload.Length) }
}

function Send-Json([byte]$Type, $Object) {
    $json = ConvertTo-Json -InputObject $Object -Compress -Depth 6
    Send-Frame $Type ([System.Text.Encoding]::UTF8.GetBytes($json))
}

# 读取服务端下行数据并拆帧
function Pump-ServerInput {
    $buf = New-Object byte[] 65536
    while ($script:ServerStream.DataAvailable) {
        $n = $script:ServerStream.Read($buf, 0, $buf.Length)
        if ($n -le 0) { throw "与服务端连接已断开" }
        $seg = New-Object byte[] $n
        [Array]::Copy($buf, $seg, $n)
        $script:RecvQueue.AddRange($seg)
    }
    while ($script:RecvQueue.Count -ge 5) {
        $lenBytes = [byte[]]@($script:RecvQueue[4], $script:RecvQueue[3], $script:RecvQueue[2], $script:RecvQueue[1])
        $plen = [BitConverter]::ToUInt32($lenBytes, 0)
        if ($plen -gt 1048576) { throw "服务端帧长度超限" }
        if ($script:RecvQueue.Count -lt 5 + $plen) { break }
        $type = $script:RecvQueue[0]
        $payload = New-Object byte[] $plen
        $script:RecvQueue.CopyTo(5, $payload, 0, [int]$plen)
        $script:RecvQueue.RemoveRange(0, 5 + [int]$plen)
        Invoke-ServerFrame $type $payload
    }
}

# 处理服务端下行帧
function Invoke-ServerFrame([byte]$Type, [byte[]]$Payload) {
    if ($Type -eq $FrameHelloAck) {
        $script:LoginAck = ConvertFrom-Json ([System.Text.Encoding]::UTF8.GetString($Payload))
    } elseif ($Type -eq $FrameMemberJoin) {
        $m = ConvertFrom-Json ([System.Text.Encoding]::UTF8.GetString($Payload))
        Write-Log ("成员加入: {0}" -f $m.name)
    } elseif ($Type -eq $FrameMemberLeave) {
        $m = ConvertFrom-Json ([System.Text.Encoding]::UTF8.GetString($Payload))
        Write-Log ("成员离开 (ID {0})" -f $m.id)
    } elseif ($Type -eq $FrameGameAdd) {
        $a = ConvertFrom-Json ([System.Text.Encoding]::UTF8.GetString($Payload))
        Add-RemoteGame $a
    } elseif ($Type -eq $FrameGameDel) {
        $d = ConvertFrom-Json ([System.Text.Encoding]::UTF8.GetString($Payload))
        Remove-RemoteGame ([uint32]$d.game_key)
    } elseif ($Type -eq $FrameStreamOpen) {
        $o = ConvertFrom-Json ([System.Text.Encoding]::UTF8.GetString($Payload))
        Open-HostStream $o
    } elseif ($Type -eq $FrameStreamResult) {
        $r = ConvertFrom-Json ([System.Text.Encoding]::UTF8.GetString($Payload))
        $req = [int]$r.req_id
        if ($script:Pending.ContainsKey($req)) {
            $local = $script:Pending[$req]
            $script:Pending.Remove($req)
            if ($r.ok) {
                $script:Streams[[int]$r.global_id] = @{ Client = $local; Stream = $local.GetStream() }
                Write-Log ("已连上远程游戏 (流 {0})" -f $r.global_id)
            } else {
                Write-Log ("加入远程游戏失败: {0}" -f $r.error)
                $local.Close()
            }
        }
    } elseif ($Type -eq $FrameStreamData) {
        $sid = [int](Read-BE32 $Payload 0)
        if ($script:Streams.ContainsKey($sid)) {
            $st = $script:Streams[$sid]
            $data = New-Object byte[] ($Payload.Length - 4)
            [Array]::Copy($Payload, 4, $data, 0, $data.Length)
            $st.Stream.Write($data, 0, $data.Length)
        }
    } elseif ($Type -eq $FrameStreamClose) {
        $sid = [int](Read-BE32 $Payload 0)
        if ($script:Streams.ContainsKey($sid)) {
            $script:Streams[$sid].Client.Close()
            $script:Streams.Remove($sid)
            Write-Log ("转发流 {0} 已关闭" -f $sid)
        }
    }
}

# 读取 4 字节大端整数
function Read-BE32([byte[]]$Data, [int]$Offset) {
    $b = [byte[]]@($Data[$Offset + 3], $Data[$Offset + 2], $Data[$Offset + 1], $Data[$Offset])
    return [BitConverter]::ToUInt32($b, 0)
}

# 发送流帧：[流ID 4字节大端][数据]
function Send-StreamFrame([byte]$Type, [uint32]$StreamID, [byte[]]$Data) {
    $payload = New-Object byte[] (4 + $Data.Length)
    (ConvertTo-BE32 $StreamID).CopyTo($payload, 0)
    $Data.CopyTo($payload, 4)
    Send-Frame $Type $payload
}

# ---------------- 局域网发现与发布 ----------------

$script:LoginAck = $null
$script:ClientID = [uint32]0

function Get-ProductName([string]$Product) {
    if ($Product -eq "WAR3") { return "混乱之治" }
    return "冰封王座"
}

# 全量探测：游戏只应答与自身产品、版本一致的查询，逐个尝试即自动适配
function Invoke-Discovery {
    $broadcastEP = New-Object System.Net.IPEndPoint ([System.Net.IPAddress]::Broadcast, $GamePort)
    $loopbackEP = New-Object System.Net.IPEndPoint ([System.Net.IPAddress]::Loopback, $GamePort)
    foreach ($product in $script:SearchProducts) {
        for ($v = $script:MinSearchVersion; $v -le $script:MaxSearchVersion; $v++) {
            $pkt = New-SearchGame $product ([uint32]$v)
            [void]$script:DiscoverUdp.Send($pkt, $pkt.Length, $broadcastEP)
            [void]$script:DiscoverUdp.Send($pkt, $pkt.Length, $loopbackEP)
        }
    }

    while ($script:DiscoverUdp.Available -gt 0) {
        $remoteEP = New-Object System.Net.IPEndPoint ([System.Net.IPAddress]::Any, 0)
        $bytes = $script:DiscoverUdp.Receive([ref]$remoteEP)
        $info = ConvertFrom-GameInfo $bytes
        if ($null -eq $info) { continue }
        # 同一游戏可能同时从回环与广播两个地址应答，按游戏身份去重
        $key = "{0}|{1}|{2}" -f $info.GameID, $info.Name, $info.HostName
        if (-not $script:LocalGames.ContainsKey($key)) {
            $script:NextGameSeq++
            $gameKey = [uint32](([uint64]$script:ClientID -shl 16) -bor [uint64]$script:NextGameSeq)
            $script:LocalGames[$key] = @{
                GameKey = $gameKey; Info = $info; Addr = $remoteEP
                LastSeen = Get-Date; Registered = $false
                SentPlayers = [uint32]0; SentSlots = [uint32]0; SentName = ""; SentPort = [uint16]0
            }
        } else {
            $g = $script:LocalGames[$key]
            $g.Info = $info
            $g.Addr = $remoteEP
            $g.LastSeen = Get-Date
        }
    }
}

# 把发现的游戏同步给服务端：新增、变化时重新发布，过期则下架
function Sync-LocalGames {
    $now = Get-Date
    $publish = New-Object System.Collections.ArrayList
    $remove = New-Object System.Collections.ArrayList
    foreach ($key in @($script:LocalGames.Keys)) {
        $g = $script:LocalGames[$key]
        if ((($now - [datetime]$g.LastSeen).TotalSeconds) -gt 9) {
            [void]$remove.Add($g)
            $script:LocalGames.Remove($key)
            continue
        }
        $changed = (-not $g.Registered) -or
            ([uint32]$g.SentPlayers -ne [uint32]$g.Info.Players) -or
            ([uint32]$g.SentSlots -ne [uint32]$g.Info.Slots) -or
            ($g.SentName -ne $g.Info.Name) -or
            ([uint16]$g.SentPort -ne [uint16]$g.Info.TCPPort)
        if ($changed) {
            $isNew = -not $g.Registered
            $g.Registered = $true
            $g.SentPlayers = $g.Info.Players
            $g.SentSlots = $g.Info.Slots
            $g.SentName = $g.Info.Name
            $g.SentPort = $g.Info.TCPPort
            [void]$publish.Add(@{ G = $g; IsNew = $isNew })
        }
    }

    foreach ($g in $remove) {
        Send-Json $FrameGameDel @{ game_key = $g.GameKey }
        Write-Log ("游戏已结束: {0}" -f $g.Info.Name)
    }
    foreach ($p in $publish) {
        $g = $p.G
        Send-Json $FrameGameAdd @{ game_key = $g.GameKey; raw = [Convert]::ToBase64String($g.Info.Raw) }
        $displayed = [uint32]($g.Info.Players + $g.Info.Slots - $g.Info.NonComputerSlots)
        if ($p.IsNew) {
            Write-Log ("发现游戏并发布: {0} ({1} 1.{2}, 地图 {3}, {4}/{5} 人, 主机 {6}, 地址 {7})" -f `
                $g.Info.Name, (Get-ProductName $g.Info.Product), $g.Info.Version, $g.Info.MapName, `
                $displayed, $g.Info.Slots, $g.Info.HostName, $g.Addr)
        } else {
            Write-Log ("游戏信息已更新: {0} ({1}/{2} 人)" -f $g.Info.Name, $displayed, $g.Info.Slots)
        }
    }
}

# ---------------- 远程游戏公告与本地代理 ----------------

# 登记远程游戏：首次登记时创建本地代理端口并开始公告，重复登记视为信息更新
function Add-RemoteGame($Ann) {
    $raw = [Convert]::FromBase64String($Ann.raw)
    $info = ConvertFrom-GameInfo $raw
    $key = [uint32]$Ann.game_key
    if ($null -eq $info) {
        Write-Log "解析远程游戏信息失败"
        return
    }
    if ($script:RemoteGames.ContainsKey($key)) {
        $script:RemoteGames[$key].Info = $info
        return
    }

    $udp = New-Object System.Net.Sockets.UdpClient
    $udp.Client.SetSocketOption([System.Net.Sockets.SocketOptionLevel]::Socket,
        [System.Net.Sockets.SocketOptionName]::Broadcast, $true)
    $port = $script:NextProxyPort
    $script:NextProxyPort++
    $listener = New-Object System.Net.Sockets.TcpListener ([System.Net.IPAddress]::Any, $port)
    $listener.Start()
    $script:RemoteGames[$key] = @{ Info = $info; ProxyPort = $port; Udp = $udp; Listener = $listener }

    $displayed = [uint32]($info.Players + $info.Slots - $info.NonComputerSlots)
    Write-Log ("房间新游戏: {0} ({1} 1.{2}, 地图 {3}, {4}/{5} 人, 主机 {6})，本地代理端口 {7}" -f `
        $info.Name, (Get-ProductName $info.Product), $info.Version, $info.MapName, `
        $displayed, $info.Slots, $Ann.host_name, $port)
}

# 移除远程游戏：通知本机游戏列表删除条目并停止公告
function Remove-RemoteGame([uint32]$Key) {
    if (-not $script:RemoteGames.ContainsKey($Key)) { return }
    $rg = $script:RemoteGames[$Key]
    $script:RemoteGames.Remove($Key)
    $ep = New-Object System.Net.IPEndPoint ([System.Net.IPAddress]::Broadcast, $GamePort)
    $cancel = New-CancelGame $rg.Info.GameID
    [void]$rg.Udp.Send($cancel, $cancel.Length, $ep)
    $rg.Udp.Close()
    $rg.Listener.Stop()
    Write-Log ("游戏已下架: {0}" -f $rg.Info.Name)
}

# 周期性广播改写过端口的 0x30 游戏信息，维持本机游戏列表中的条目
function Invoke-Announce {
    $ep = New-Object System.Net.IPEndPoint ([System.Net.IPAddress]::Broadcast, $GamePort)
    foreach ($key in @($script:RemoteGames.Keys)) {
        $rg = $script:RemoteGames[$key]
        $patched = Update-GamePort $rg.Info.Raw ([uint16]$rg.ProxyPort)
        [void]$rg.Udp.Send($patched, $patched.Length, $ep)
        $displayed = [uint32]($rg.Info.Players + $rg.Info.Slots - $rg.Info.NonComputerSlots)
        $refresh = New-RefreshGame $rg.Info.GameID $displayed $rg.Info.Slots
        [void]$rg.Udp.Send($refresh, $refresh.Length, $ep)
    }
}

# 接受本机游戏加入远程游戏的 TCP 连接
function Accept-Connections {
    foreach ($key in @($script:RemoteGames.Keys)) {
        $rg = $script:RemoteGames[$key]
        while ($rg.Listener.Pending()) {
            $local = $rg.Listener.AcceptTcpClient()
            $script:NextReqID++
            $req = $script:NextReqID
            $script:Pending[$req] = $local
            Send-Json $FrameStreamOpen @{ req_id = $req; game_key = $key }
        }
    }
}

# 主机方：收到加入请求后连接游戏并建立反向转发流
function Open-HostStream($Open) {
    $target = $null
    foreach ($g in $script:LocalGames.Values) {
        if ([uint32]$g.GameKey -eq [uint32]$Open.game_key) { $target = $g; break }
    }
    if ($null -eq $target) {
        Send-Json $FrameStreamResult @{ req_id = 0; global_id = $Open.global_id; ok = $false; error = "本机游戏已关闭" }
        return
    }
    try {
        $local = New-Object System.Net.Sockets.TcpClient
        # 回连发现游戏时的来源地址：与游戏同机时是回环地址，网关模式下是游戏机的局域网地址
        $local.Connect($target.Addr.Address, [int]$target.Info.TCPPort)
        Send-Json $FrameStreamResult @{ req_id = 0; global_id = $Open.global_id; ok = $true; error = "" }
        $script:Streams[[int]$Open.global_id] = @{ Client = $local; Stream = $local.GetStream() }
        Write-Log ("有人加入游戏: {0} (流 {1})" -f $target.Info.Name, $Open.global_id)
    } catch [System.Net.Sockets.SocketException] {
        Write-Log ("连接游戏失败: {0}" -f $_.Exception.Message)
        Send-Json $FrameStreamResult @{ req_id = 0; global_id = $Open.global_id; ok = $false; error = "连接游戏失败: " + $_.Exception.Message }
    }
}

# 把本地游戏连接的数据上行到服务端
function Pump-Streams {
    foreach ($sid in @($script:Streams.Keys)) {
        $st = $script:Streams[$sid]
        try {
            while ($st.Client.Connected -and $st.Client.Available -gt 0) {
                $buf = New-Object byte[] 32768
                $n = $st.Stream.Read($buf, 0, $buf.Length)
                if ($n -le 0) { throw "连接已关闭" }
                $data = New-Object byte[] $n
                [Array]::Copy($buf, $data, $n)
                Send-StreamFrame $FrameStreamData ([uint32]$sid) $data
            }
        } catch [System.Net.Sockets.SocketException] {
            Write-Log ("流 {0} 读取失败: {1}" -f $sid, $_.Exception.Message)
            Send-StreamFrame $FrameStreamClose ([uint32]$sid) ([byte[]]@())
            $st.Client.Close()
            $script:Streams.Remove($sid)
        } catch [System.IO.IOException] {
            Write-Log ("流 {0} 已断开: {1}" -f $sid, $_.Exception.Message)
            Send-StreamFrame $FrameStreamClose ([uint32]$sid) ([byte[]]@())
            $st.Client.Close()
            $script:Streams.Remove($sid)
        }
    }
}

# ---------------- 主程序 ----------------

Write-Log "魔兽争霸3互联网联机工具启动（PowerShell 版）"
Write-Log ("配置：服务端 {0}，房间 {1}，玩家 {2}，端口 {3}" -f $Server, $Room, $Name, $GamePort)

# 创建局域网发现套接字
$script:DiscoverUdp = New-Object System.Net.Sockets.UdpClient
$script:DiscoverUdp.Client.Bind((New-Object System.Net.IPEndPoint ([System.Net.IPAddress]::Any, 0)))
$script:DiscoverUdp.Client.SetSocketOption([System.Net.Sockets.SocketOptionLevel]::Socket,
    [System.Net.Sockets.SocketOptionName]::Broadcast, $true)
# 关闭 UDP 的 ICMP 端口不可达回显（SIO_UDP_CONNRESET），
# 避免探测无人应答的游戏端口时抛出连接重置异常
$script:DiscoverUdp.Client.IOControl([int]-1744830452, [byte[]]@(0, 0, 0, 0), [byte[]]@(0, 0, 0, 0)) | Out-Null

# 连接服务端并登录
$serverPart = $Server -split ":"
if ($serverPart.Count -ne 2) { throw "服务端地址格式应为 IP:端口" }
$script:ServerClient = New-Object System.Net.Sockets.TcpClient
$script:ServerClient.Connect($serverPart[0], [int]$serverPart[1])
$script:ServerStream = $script:ServerClient.GetStream()
Send-Json $FrameHello @{ name = $Name; room = $Room; pass = $Pass }

$deadline = (Get-Date).AddSeconds(10)
while ($null -eq $script:LoginAck) {
    if ((Get-Date) -gt $deadline) { throw "等待登录响应超时" }
    Pump-ServerInput
    Start-Sleep -Milliseconds 50
}
if ($script:LoginAck.error) { throw ("登录失败: {0}" -f $script:LoginAck.error) }
$script:ClientID = [uint32]$script:LoginAck.client_id
Write-Log ("已登录：房间 {0}，玩家 {1}，服务端 {2}" -f $Room, $Name, $Server)
foreach ($m in $script:LoginAck.members) {
    Write-Log ("房间成员: {0}" -f $m.name)
}
foreach ($ga in $script:LoginAck.games) {
    Add-RemoteGame $ga
}

$lastDiscovery = [datetime]::MinValue
$lastPing = Get-Date
try {
    while ($true) {
        Pump-ServerInput
        Pump-Streams
        Accept-Connections

        $now = Get-Date
        if ((($now - $lastDiscovery).TotalSeconds) -ge 3) {
            $lastDiscovery = $now
            try {
                Invoke-Discovery
                Sync-LocalGames
                Invoke-Announce
            } catch [System.Net.Sockets.SocketException] {
                Write-Log ("局域网收发失败: {0}" -f $_.Exception.Message)
            }
        }
        if ((($now - $lastPing).TotalSeconds) -ge 15) {
            $lastPing = $now
            Send-Frame $FramePing ([byte[]]@())
        }
        Start-Sleep -Milliseconds 10
    }
} finally {
    # 退出清理：下架本机游戏、移除远程公告、关闭转发流
    Write-Log "正在退出..."
    foreach ($g in $script:LocalGames.Values) {
        if ($g.Registered) {
            try { Send-Json $FrameGameDel @{ game_key = $g.GameKey } }
            catch [System.Exception] { Write-Log ("下架通知失败: {0}" -f $_.Exception.Message) }
        }
    }
    foreach ($key in @($script:RemoteGames.Keys)) {
        try { Remove-RemoteGame $key }
        catch [System.Exception] { Write-Log ("移除远程游戏失败: {0}" -f $_.Exception.Message) }
    }
    foreach ($sid in @($script:Streams.Keys)) {
        try {
            Send-StreamFrame $FrameStreamClose ([uint32]$sid) ([byte[]]@())
            $script:Streams[$sid].Client.Close()
        } catch [System.Exception] { Write-Log ("关闭转发流失败: {0}" -f $_.Exception.Message) }
    }
    try { $script:ServerClient.Close() } catch [System.Exception] { }
    Write-Log "已停止"
}



