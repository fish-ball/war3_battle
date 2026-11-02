package client

import (
	"log"
	"net"
	"sync"

	"war3_battle/internal/protocol"
)

// stream 一条转发流：服务端全局流 ID 与本机 TCP 连接（游戏侧）的对应关系
type stream struct {
	globalID uint32
	local    net.Conn
	writeMu  sync.Mutex
	closed   bool
}

// writeLocal 把服务端下行数据写入本地游戏连接
func (s *stream) writeLocal(data []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
		return net.ErrClosed
	}
	return writeConn(s.local, data)
}

// handleJoin 加入方：本机游戏连接到本地代理端口后，经服务端建立到主机方的转发流
func (c *Client) handleJoin(rg *remoteGame, local net.Conn) {
	reqID := c.takeReqID()
	if err := c.conn.WriteJSON(protocol.FrameStreamOpen, protocol.StreamOpen{
		ReqID:   reqID,
		GameKey: rg.gameKey,
	}); err != nil {
		log.Printf("[客户端] 发送转发流请求失败: %v", err)
		local.Close()
		return
	}

	res, err := c.waitForStreamResult(reqID)
	if err != nil {
		log.Printf("[客户端] 等待转发流结果失败: %v", err)
		local.Close()
		return
	}
	if !res.OK {
		log.Printf("[客户端] 加入远程游戏失败: %s", res.Error)
		local.Close()
		return
	}

	st := &stream{globalID: res.GlobalID, local: local}
	c.mu.Lock()
	c.streams[res.GlobalID] = st
	c.mu.Unlock()

	info := rg.getInfo()
	log.Printf("[客户端] 已连上远程游戏: %s (流 %d)", info.Name, res.GlobalID)
	go c.pumpLocalToServer(st)
}

// handleHostStream 主机方：收到加入请求后连接本机游戏并建立反向转发流
func (c *Client) handleHostStream(open protocol.StreamOpen) {
	c.mu.Lock()
	var target *localGame
	for _, g := range c.localGames {
		if g.gameKey == open.GameKey {
			target = g
			break
		}
	}
	var gameName string
	var gamePort uint16
	var gameIP net.IP
	if target != nil {
		gameName = target.info.Name
		gamePort = target.info.TCPPort
		gameIP = target.addr.IP
	}
	c.mu.Unlock()

	if target == nil {
		c.conn.WriteJSON(protocol.FrameStreamResult, protocol.StreamResult{
			GlobalID: open.GlobalID, OK: false, Error: "本机游戏已关闭",
		})
		return
	}

	// 回连发现游戏时的来源地址：与游戏同机时是回环地址，网关模式下是游戏机的局域网地址
	addr := &net.TCPAddr{IP: gameIP, Port: int(gamePort)}
	local, err := net.DialTCP("tcp", nil, addr)
	if err != nil {
		log.Printf("[客户端] 连接游戏 %s 失败: %v", addr, err)
		c.conn.WriteJSON(protocol.FrameStreamResult, protocol.StreamResult{
			GlobalID: open.GlobalID, OK: false, Error: "连接本机游戏失败: " + err.Error(),
		})
		return
	}

	if err := c.conn.WriteJSON(protocol.FrameStreamResult, protocol.StreamResult{
		GlobalID: open.GlobalID, OK: true,
	}); err != nil {
		local.Close()
		return
	}

	st := &stream{globalID: open.GlobalID, local: local}
	c.mu.Lock()
	c.streams[open.GlobalID] = st
	c.mu.Unlock()

	log.Printf("[客户端] 有人加入游戏: %s (流 %d)", gameName, open.GlobalID)
	go c.pumpLocalToServer(st)
}

// pumpLocalToServer 把本地游戏连接的数据上行到服务端
func (c *Client) pumpLocalToServer(st *stream) {
	buf := make([]byte, 32*1024)
	for {
		n, err := st.local.Read(buf)
		if n > 0 {
			if werr := c.conn.WriteFrame(protocol.FrameStreamData, protocol.EncodeStream(st.globalID, buf[:n])); werr != nil {
				log.Printf("[客户端] 流 %d 上行数据失败: %v", st.globalID, werr)
				break
			}
		}
		if err != nil {
			break
		}
	}
	c.closeStream(st, true)
}

// closeStream 关闭一条转发流，notify 为 true 时通知服务端
func (c *Client) closeStream(st *stream, notify bool) {
	c.mu.Lock()
	_, ok := c.streams[st.globalID]
	if ok {
		delete(c.streams, st.globalID)
	}
	c.mu.Unlock()
	if !ok {
		return
	}

	st.writeMu.Lock()
	st.closed = true
	st.writeMu.Unlock()
	st.local.Close()

	if notify {
		if err := c.conn.WriteFrame(protocol.FrameStreamClose, protocol.EncodeStream(st.globalID, nil)); err != nil {
			log.Printf("[客户端] 发送流关闭通知失败: %v", err)
			return
		}
	}
	log.Printf("[客户端] 转发流 %d 已关闭", st.globalID)
}
