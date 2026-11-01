// Package protocol 定义客户端与服务端之间的帧格式与控制消息。
//
// 帧格式：[类型 1 字节][载荷长度 4 字节大端][载荷]。
// 控制消息载荷为 JSON，游戏数据流载荷为二进制：[流 ID 4 字节大端][数据]。
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// 帧类型
const (
	FrameHello        byte = 0x01 // C->S 登录：Hello
	FrameHelloAck     byte = 0x02 // S->C 登录结果：HelloAck
	FrameMemberJoin   byte = 0x03 // S->C 成员加入：MemberJoin
	FrameMemberLeave  byte = 0x04 // S->C 成员离开：MemberLeave
	FrameGameAdd      byte = 0x05 // C->S / S->C 游戏发布或更新：GameAnnounce
	FrameGameDel      byte = 0x06 // C->S / S->C 游戏下架：GameRemove
	FrameStreamOpen   byte = 0x07 // C->S / S->C 打开转发流：StreamOpen
	FrameStreamResult byte = 0x08 // C->S / S->C 转发流结果：StreamResult
	FrameStreamData   byte = 0x09 // 双向 流数据：[流 ID][数据]
	FrameStreamClose  byte = 0x0A // 双向 关闭流：[流 ID]
	FramePing         byte = 0x0B // C->S 心跳
	FramePong         byte = 0x0C // S->C 心跳应答
)

// maxFrameSize 单帧载荷上限（1MB）
const maxFrameSize = 1 << 20

// ErrClosed 连接已关闭
var ErrClosed = errors.New("protocol: 连接已关闭")

// Hello 客户端登录请求
type Hello struct {
	Name string `json:"name"`
	Room string `json:"room"`
	Pass string `json:"pass"`
}

// Member 房间成员
type Member struct {
	ID   uint32 `json:"id"`
	Name string `json:"name"`
}

// HelloAck 登录结果，Error 非空表示登录失败
type HelloAck struct {
	ClientID uint32         `json:"client_id"`
	Members  []Member       `json:"members"`
	Games    []GameAnnounce `json:"games"`
	Error    string         `json:"error"`
}

// MemberJoin 成员加入房间
type MemberJoin struct {
	Member
}

// MemberLeave 成员离开房间
type MemberLeave struct {
	ID uint32 `json:"id"`
}

// GameAnnounce 游戏发布（GameKey 由客户端生成，同一客户端唯一）
type GameAnnounce struct {
	GameKey  uint32 `json:"game_key"`
	HostID   uint32 `json:"host_id"`
	HostName string `json:"host_name"`
	Raw      []byte `json:"raw"` // 原始 0x30 游戏信息包（JSON 自动 base64）
}

// GameRemove 游戏下架
type GameRemove struct {
	GameKey uint32 `json:"game_key"`
}

// StreamOpen 打开转发流。
// 加入方 -> 服务端：填 ReqID 与 GameKey；服务端 -> 主机方：填 GlobalID 与 GameKey。
type StreamOpen struct {
	ReqID    uint32 `json:"req_id"`
	GlobalID uint32 `json:"global_id"`
	GameKey  uint32 `json:"game_key"`
}

// StreamResult 转发流结果。
// 主机方 -> 服务端：填 GlobalID；服务端 -> 加入方：填 ReqID 与 GlobalID。
type StreamResult struct {
	ReqID    uint32 `json:"req_id"`
	GlobalID uint32 `json:"global_id"`
	OK       bool   `json:"ok"`
	Error    string `json:"error"`
}

// Conn 帧连接。内部带发送队列，保证并发写帧不交错。
type Conn struct {
	raw    net.Conn
	send   chan []byte
	done   chan struct{}
	once   sync.Once
	closed error
}

// NewConn 包装一条 TCP 连接
func NewConn(raw net.Conn) *Conn {
	c := &Conn{
		raw:  raw,
		send: make(chan []byte, 256),
		done: make(chan struct{}),
	}
	go c.writeLoop()
	return c
}

func (c *Conn) writeLoop() {
	for {
		select {
		case buf := <-c.send:
			if err := writeAll(c.raw, buf); err != nil {
				c.closeWith(err)
				return
			}
		case <-c.done:
			return
		}
	}
}

// WriteFrame 发送一帧，连接关闭时返回 ErrClosed
func (c *Conn) WriteFrame(typ byte, payload []byte) error {
	buf := encodeFrame(typ, payload)
	select {
	case c.send <- buf:
		return nil
	case <-c.done:
		if c.closed != nil {
			return c.closed
		}
		return ErrClosed
	}
}

// WriteJSON 以 JSON 载荷发送一帧
func (c *Conn) WriteJSON(typ byte, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("protocol: JSON 编码失败: %w", err)
	}
	return c.WriteFrame(typ, payload)
}

// ReadFrame 读取一帧，连接关闭或出错时返回错误
func (c *Conn) ReadFrame() (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(c.raw, hdr[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(hdr[1:])
	if length > maxFrameSize {
		return 0, nil, fmt.Errorf("protocol: 帧长度 %d 超过上限", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.raw, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

// SetReadDeadline 设置底层读超时
func (c *Conn) SetReadDeadline(t time.Time) error {
	return c.raw.SetReadDeadline(t)
}

// Close 关闭连接（可重复调用）
func (c *Conn) Close() error {
	c.closeWith(ErrClosed)
	return nil
}

func (c *Conn) closeWith(err error) {
	c.once.Do(func() {
		c.closed = err
		close(c.done)
		c.raw.Close()
	})
}

func encodeFrame(typ byte, payload []byte) []byte {
	buf := make([]byte, 5+len(payload))
	buf[0] = typ
	binary.BigEndian.PutUint32(buf[1:], uint32(len(payload)))
	copy(buf[5:], payload)
	return buf
}

func writeAll(w io.Writer, buf []byte) error {
	for len(buf) > 0 {
		n, err := w.Write(buf)
		if err != nil {
			return err
		}
		buf = buf[n:]
	}
	return nil
}

// EncodeStream 编码流帧载荷：[流 ID 4 字节大端][数据]
func EncodeStream(id uint32, data []byte) []byte {
	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(buf, id)
	copy(buf[4:], data)
	return buf
}

// DecodeStream 解码流帧载荷
func DecodeStream(payload []byte) (uint32, []byte, error) {
	if len(payload) < 4 {
		return 0, nil, errors.New("protocol: 流帧载荷过短")
	}
	return binary.BigEndian.Uint32(payload), payload[4:], nil
}
