// Package w3gs 实现魔兽争霸3局域网（W3GS）协议中与联机相关的 UDP 包构造与解析。
// 包格式参考《Warcraft III Game Packet Specs》(Soar Qin) 与 BnetDocs 文档。
//
// 所有 W3GS 包都有 4 字节头：[0xF7 魔数][操作码][2 字节小端长度（含包头）]。
// 局域网发现流程：客户端广播 0x2F 查询包，主机以 0x30 游戏信息包应答，
// 0x30 包尾 2 字节为主机接受 TCP 连接的端口，加入游戏即 TCP 连接该端口。
package w3gs

import (
	"encoding/binary"
	"errors"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	// Magic 所有 W3GS 包首字节魔数
	Magic = 0xF7

	// OpSearchGame 0x2F 局域网游戏查询（广播）
	OpSearchGame = 0x2F
	// OpGameInfo 0x30 游戏信息（应答查询，主机每 5 秒广播一次）
	OpGameInfo = 0x30
	// OpRefreshGame 0x32 游戏人数变化时广播
	OpRefreshGame = 0x32
	// OpGameCancelled 0x33 游戏取消时广播
	OpGameCancelled = 0x33
)

// 产品标识：以小端 DWORD 存储，线上字节为 ASCII 反序
var (
	// ProductTFT 冰封王座（W3XP）
	ProductTFT = [4]byte{'P', 'X', '3', 'W'}
	// ProductROC 混乱之治（WAR3）
	ProductROC = [4]byte{'3', 'R', 'A', 'W'}
)

// 自适配查询的版本号范围（1.15 - 1.33，覆盖经典版各版本）。
// 局域网查询包带游戏版本号，游戏只应答与自身版本一致的查询，
// 因此逐个版本探测即可自动适配，无需用户填写。
const (
	MinSearchVersion uint32 = 15
	MaxSearchVersion uint32 = 33
)

// GameInfo 解析后的 0x30 游戏信息
type GameInfo struct {
	Product          [4]byte // 产品标识（线上字节序）
	Version          uint32  // 游戏版本（1.27 对应 27）
	GameID           uint32  // 主机游戏 ID，随建局自增
	Tick             uint32  // 主机 GetTickCount()
	Name             string  // 游戏名（GBK 解码为 UTF-8）
	MapName          string  // 地图名（取地图路径最后一段）
	HostName         string  // 主机玩家名
	Slots            uint32  // 总槽位数
	Flags            uint32  // 游戏标志
	Players          uint32  // 已进入游戏的玩家数
	NonComputerSlots uint32  // 非电脑槽位数
	TCPPort          uint16  // 主机接受 TCP 连接的端口
	Raw              []byte  // 原始 0x30 包（用于透传给加入方）
}

// DisplayedPlayers 按游戏内显示公式计算人数：已进入玩家 + (总槽位 - 非电脑槽位)
func (g *GameInfo) DisplayedPlayers() uint32 {
	return g.Players + g.Slots - g.NonComputerSlots
}

// ProductName 返回产品名称（W3XP/WAR3）
func (g *GameInfo) ProductName() string {
	return productName(g.Product)
}

func productName(p [4]byte) string {
	// 线上为 ASCII 反序存储
	out := [4]byte{p[3], p[2], p[1], p[0]}
	return string(out[:])
}

// BuildSearchGame 构造 0x2F 局域网游戏查询包（16 字节）
func BuildSearchGame(product [4]byte, version uint32) []byte {
	pkt := make([]byte, 16)
	pkt[0] = Magic
	pkt[1] = OpSearchGame
	binary.LittleEndian.PutUint16(pkt[2:], 16)
	copy(pkt[4:], product[:])
	binary.LittleEndian.PutUint32(pkt[8:], version)
	// 第 12 字节起 4 字节为游戏 ID，广播查询时为 0
	return pkt
}

// BuildRefreshGame 构造 0x32 游戏刷新包（16 字节），用于刷新本地列表中的人数
func BuildRefreshGame(gameID, players, slots uint32) []byte {
	pkt := make([]byte, 16)
	pkt[0] = Magic
	pkt[1] = OpRefreshGame
	binary.LittleEndian.PutUint16(pkt[2:], 16)
	binary.LittleEndian.PutUint32(pkt[4:], gameID)
	binary.LittleEndian.PutUint32(pkt[8:], players)
	binary.LittleEndian.PutUint32(pkt[12:], slots)
	return pkt
}

// BuildGameCancelled 构造 0x33 游戏取消包（8 字节）
func BuildGameCancelled(gameID uint32) []byte {
	pkt := make([]byte, 8)
	pkt[0] = Magic
	pkt[1] = OpGameCancelled
	binary.LittleEndian.PutUint16(pkt[2:], 8)
	binary.LittleEndian.PutUint32(pkt[4:], gameID)
	return pkt
}

// PatchGamePort 复制 0x30 包并改写包尾 2 字节的 TCP 游戏端口。
// 改写后广播到本地网络，本机游戏即会把该端口作为加入游戏的连接端口。
func PatchGamePort(raw []byte, port uint16) []byte {
	pkt := make([]byte, len(raw))
	copy(pkt, raw)
	binary.LittleEndian.PutUint16(pkt[len(pkt)-2:], port)
	return pkt
}

// ParseGameInfo 解析 0x30 游戏信息包
func ParseGameInfo(pkt []byte) (*GameInfo, error) {
	if len(pkt) < 44 {
		return nil, errors.New("w3gs: 0x30 包长度不足")
	}
	if pkt[0] != Magic || pkt[1] != OpGameInfo {
		return nil, errors.New("w3gs: 非 0x30 游戏信息包")
	}

	info := &GameInfo{}
	copy(info.Product[:], pkt[4:8])
	info.Version = binary.LittleEndian.Uint32(pkt[8:12])
	info.GameID = binary.LittleEndian.Uint32(pkt[12:16])
	info.Tick = binary.LittleEndian.Uint32(pkt[16:20])

	// 第 20 字节起为 0 结尾的游戏名，其后 1 字节未知，再后为编码串
	nameBytes, nameUsed := cstring(pkt[20:])
	info.Name = decodeText(nameBytes)
	encodedStart := 20 + nameUsed + 1
	if encodedStart >= len(pkt) {
		return nil, errors.New("w3gs: 0x30 包缺少编码串")
	}

	decoded := decodeEncodedString(pkt[encodedStart:])
	if len(decoded) < 13 {
		return nil, errors.New("w3gs: 0x30 编码串长度不足")
	}
	// 解码串结构：[0:4] 游戏设置 [5:7] 地图宽 [7:9] 地图高 [9:13] 地图校验 [13:] 地图名、主机名
	mapBytes, mapUsed := cstring(decoded[13:])
	info.MapName = decodeText(mapBytes)
	hostBytes, _ := cstring(decoded[13+mapUsed:])
	info.HostName = decodeText(hostBytes)

	// 包尾 22 字节固定字段：[槽位数][标志][已进玩家][非电脑槽位][未知][TCP 端口]
	info.Slots = binary.LittleEndian.Uint32(pkt[len(pkt)-22:])
	info.Flags = binary.LittleEndian.Uint32(pkt[len(pkt)-18:])
	info.Players = binary.LittleEndian.Uint32(pkt[len(pkt)-14:])
	info.NonComputerSlots = binary.LittleEndian.Uint32(pkt[len(pkt)-10:])
	info.TCPPort = binary.LittleEndian.Uint16(pkt[len(pkt)-2:])

	info.Raw = make([]byte, len(pkt))
	copy(info.Raw, pkt)
	return info, nil
}

// cstring 读取 0 结尾字符串，返回内容（不含结尾 0）与占用的总字节数（含结尾 0）
func cstring(data []byte) ([]byte, int) {
	for i, b := range data {
		if b == 0 {
			return data[:i], i + 1
		}
	}
	return data, len(data)
}

// decodeEncodedString 解码 0x30 包中的编码串。
// 编码规则：每 8 字节为一组，首字节为控制字节，其 bit1-7 对应后续 7 字节；
// 对应位为 0 时该字节减 1。串以 0 字节结尾（编码后 0 不可能出现）。
func decodeEncodedString(data []byte) []byte {
	end := len(data)
	for i, b := range data {
		if b == 0 {
			end = i
			break
		}
	}
	data = data[:end]

	out := make([]byte, 0, len(data))
	var mask byte
	for i, b := range data {
		if i%8 == 0 {
			mask = b
			continue
		}
		if mask&(1<<(i%8)) == 0 {
			out = append(out, b-1)
		} else {
			out = append(out, b)
		}
	}
	return out
}

// decodeText 将游戏内字符串（简体中文版为 GBK 编码）转换为 UTF-8
func decodeText(b []byte) string {
	out, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(out)
}
