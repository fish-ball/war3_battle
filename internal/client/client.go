// Package client 实现联机客户端：
// 1. 通过广播 0x2F 查询发现本机局域网的游戏，并发布到服务端房间；
// 2. 把房间内其他机器发布的游戏改写端口后广播到本机网络，让本机游戏的局域网列表直接显示；
// 3. 本机游戏加入远程游戏时，TCP 连接本地代理端口，经服务端中继到主机方，由主机方连本机游戏端口。
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"war3_battle/internal/protocol"
	"war3_battle/internal/w3gs"
)

// readTimeout 与服务端之间的读超时（客户端每 15 秒心跳一次）
const readTimeout = 90 * time.Second

// Config 客户端配置
type Config struct {
	ServerAddr string // 服务端地址，例如 1.2.3.4:51234
	Room       string // 房间名
	Password   string // 房间密码
	Name       string // 玩家名
	GamePort   uint16 // 魔兽局域网端口（游戏选项中的端口，默认 6112）
	ProxyBase  int    // 本地代理端口起始值
}

// Client 联机客户端
type Client struct {
	cfg      Config
	conn     *protocol.Conn
	clientID uint32

	mu            sync.Mutex
	localGames    map[string]*localGame  // key: 发现应答的来源地址
	remoteGames   map[uint32]*remoteGame // key: 游戏发布标识
	streams       map[uint32]*stream     // key: 服务端全局流 ID
	pending       map[uint32]chan protocol.StreamResult
	members       map[uint32]string // 房间成员名，key: 成员 ID
	nextGameSeq   uint32
	nextReqID     uint32
	nextProxyPort int

	done     chan struct{}
	doneOnce sync.Once
}

// New 创建客户端
func New(cfg Config) *Client {
	return &Client{
		cfg:           cfg,
		localGames:    make(map[string]*localGame),
		remoteGames:   make(map[uint32]*remoteGame),
		streams:       make(map[uint32]*stream),
		pending:       make(map[uint32]chan protocol.StreamResult),
		members:       make(map[uint32]string),
		nextProxyPort: cfg.ProxyBase,
		done:          make(chan struct{}),
	}
}

// Run 连接服务端并持续运行，直到 ctx 取消或连接断开
func (c *Client) Run(ctx context.Context) error {
	raw, err := net.Dial("tcp", c.cfg.ServerAddr)
	if err != nil {
		return fmt.Errorf("连接服务端 %s 失败: %w", c.cfg.ServerAddr, err)
	}
	c.conn = protocol.NewConn(raw)
	defer c.shutdown()

	// 登录房间
	if err := c.conn.WriteJSON(protocol.FrameHello, protocol.Hello{
		Name: c.cfg.Name, Room: c.cfg.Room, Pass: c.cfg.Password,
	}); err != nil {
		return fmt.Errorf("发送登录请求失败: %w", err)
	}
	c.conn.SetReadDeadline(time.Now().Add(readTimeout))
	typ, payload, err := c.conn.ReadFrame()
	if err != nil {
		return fmt.Errorf("等待登录响应失败: %w", err)
	}
	if typ != protocol.FrameHelloAck {
		return errors.New("登录响应帧类型异常")
	}
	var ack protocol.HelloAck
	if err := json.Unmarshal(payload, &ack); err != nil {
		return fmt.Errorf("解析登录响应失败: %w", err)
	}
	if ack.Error != "" {
		return fmt.Errorf("登录失败: %s", ack.Error)
	}
	c.clientID = ack.ClientID

	log.Printf("[客户端] 已登录：房间 %s，玩家 %s，服务端 %s", c.cfg.Room, c.cfg.Name, c.cfg.ServerAddr)
	for _, m := range ack.Members {
		log.Printf("[客户端] 房间成员: %s", m.Name)
		c.mu.Lock()
		c.members[m.ID] = m.Name
		c.mu.Unlock()
	}

	// 发布初始房间游戏列表
	for _, g := range ack.Games {
		c.addRemoteGame(g)
	}

	go c.runDiscovery(ctx)
	go c.pingLoop(ctx)
	go func() {
		<-ctx.Done()
		c.conn.Close()
	}()

	// 事件循环
	for {
		c.conn.SetReadDeadline(time.Now().Add(readTimeout))
		typ, payload, err := c.conn.ReadFrame()
		if err != nil {
			c.closeDone()
			if ctx.Err() != nil {
				log.Print("[客户端] 退出")
				return nil
			}
			return fmt.Errorf("与服务端连接断开: %w", err)
		}
		if err := c.dispatch(typ, payload); err != nil {
			log.Printf("[客户端] 处理帧 0x%02X 失败: %v", typ, err)
		}
	}
}

// dispatch 分发服务端下行帧
func (c *Client) dispatch(typ byte, payload []byte) error {
	switch typ {
	case protocol.FrameMemberJoin:
		var m protocol.MemberJoin
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		log.Printf("[客户端] 成员加入: %s", m.Name)
		c.mu.Lock()
		c.members[m.ID] = m.Name
		c.mu.Unlock()
		return nil

	case protocol.FrameMemberLeave:
		var m protocol.MemberLeave
		if err := json.Unmarshal(payload, &m); err != nil {
			return err
		}
		c.mu.Lock()
		name := c.members[m.ID]
		delete(c.members, m.ID)
		c.mu.Unlock()
		if name == "" {
			name = fmt.Sprintf("ID %d", m.ID)
		}
		log.Printf("[客户端] 成员离开: %s", name)
		return nil

	case protocol.FrameGameAdd:
		var ann protocol.GameAnnounce
		if err := json.Unmarshal(payload, &ann); err != nil {
			return err
		}
		c.addRemoteGame(ann)
		return nil

	case protocol.FrameGameDel:
		var del protocol.GameRemove
		if err := json.Unmarshal(payload, &del); err != nil {
			return err
		}
		c.removeRemoteGame(del.GameKey)
		return nil

	case protocol.FrameStreamOpen:
		var open protocol.StreamOpen
		if err := json.Unmarshal(payload, &open); err != nil {
			return err
		}
		go c.handleHostStream(open)
		return nil

	case protocol.FrameStreamResult:
		var res protocol.StreamResult
		if err := json.Unmarshal(payload, &res); err != nil {
			return err
		}
		c.mu.Lock()
		ch := c.pending[res.ReqID]
		c.mu.Unlock()
		if ch != nil {
			ch <- res
		}
		return nil

	case protocol.FrameStreamData:
		id, data, err := protocol.DecodeStream(payload)
		if err != nil {
			return err
		}
		c.mu.Lock()
		st := c.streams[id]
		c.mu.Unlock()
		if st != nil {
			if err := st.writeLocal(data); err != nil {
				log.Printf("[客户端] 流 %d 写入本地连接失败: %v", id, err)
				c.closeStream(st, true)
			}
		}
		return nil

	case protocol.FrameStreamClose:
		id, _, err := protocol.DecodeStream(payload)
		if err != nil {
			return err
		}
		c.mu.Lock()
		st := c.streams[id]
		c.mu.Unlock()
		if st != nil {
			c.closeStream(st, false)
		}
		return nil

	case protocol.FramePong:
		return nil

	default:
		return nil
	}
}

// pingLoop 周期性向服务端发送心跳
func (c *Client) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.conn.WriteJSON(protocol.FramePing, nil); err != nil {
				return
			}
		}
	}
}

// takeReqID 生成本客户端内的转发请求序号
func (c *Client) takeReqID() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextReqID++
	return c.nextReqID
}

// closeDone 释放所有等待中的协程
func (c *Client) closeDone() {
	c.doneOnce.Do(func() { close(c.done) })
}

// shutdown 退出清理：下架本机游戏、移除远程游戏通告、关闭所有流
func (c *Client) shutdown() {
	c.closeDone()

	c.mu.Lock()
	var gameKeys []uint32
	for _, g := range c.localGames {
		if g.registered {
			gameKeys = append(gameKeys, g.gameKey)
		}
	}
	var remotes []*remoteGame
	for _, rg := range c.remoteGames {
		remotes = append(remotes, rg)
	}
	var streams []*stream
	for _, st := range c.streams {
		streams = append(streams, st)
	}
	c.mu.Unlock()

	// 以下均为尽力通知，失败不影响退出
	for _, key := range gameKeys {
		c.conn.WriteJSON(protocol.FrameGameDel, protocol.GameRemove{GameKey: key})
	}
	for _, rg := range remotes {
		c.removeRemoteGame(rg.gameKey)
	}
	for _, st := range streams {
		c.closeStream(st, true)
	}
}

// sendGameAdd 把本机发现的游戏发布到服务端
func (c *Client) sendGameAdd(gameKey uint32, info *w3gs.GameInfo) error {
	return c.conn.WriteJSON(protocol.FrameGameAdd, protocol.GameAnnounce{
		GameKey: gameKey,
		Raw:     info.Raw,
	})
}

// sendGameRemove 把本机游戏从服务端下架
func (c *Client) sendGameRemove(gameKey uint32) error {
	return c.conn.WriteJSON(protocol.FrameGameDel, protocol.GameRemove{GameKey: gameKey})
}

// waitForStreamResult 等待服务端返回转发流建立结果
func (c *Client) waitForStreamResult(reqID uint32) (protocol.StreamResult, error) {
	ch := make(chan protocol.StreamResult, 1)
	c.mu.Lock()
	c.pending[reqID] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, reqID)
		c.mu.Unlock()
	}()

	select {
	case res := <-ch:
		return res, nil
	case <-c.done:
		return protocol.StreamResult{}, errors.New("客户端已退出")
	}
}

// writeConn 向 TCP 连接完整写出数据
func writeConn(conn net.Conn, data []byte) error {
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}
