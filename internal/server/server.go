// Package server 实现联机中继服务端：房间管理、游戏发布列表与 TCP 流转发。
//
// 所有客户端主动连接服务端（便于穿透家庭 NAT），服务端负责：
// 1. 维护房间与成员列表；
// 2. 转发主机方发布的游戏信息（0x30 原始包）给房间内其他成员；
// 3. 在加入方与主机方之间中继游戏 TCP 流。
package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"war3_battle/internal/protocol"
)

// readTimeout 读超时，客户端每 15 秒发一次心跳
const readTimeout = 90 * time.Second

// Server 中继服务端
type Server struct {
	listenAddr string

	mu           sync.Mutex
	rooms        map[string]*room
	streams      map[uint32]*streamEntry
	nextClientID uint32
	nextStreamID uint32
}

type room struct {
	name    string
	pass    string
	clients map[uint32]*clientConn
	games   map[uint32]*gameEntry // gameKey -> 已发布游戏
}

type gameEntry struct {
	key  uint32
	host *clientConn
	ann  protocol.GameAnnounce
}

type streamEntry struct {
	id          uint32
	joiner      *clientConn
	host        *clientConn
	joinerReqID uint32
}

type clientConn struct {
	id   uint32
	name string
	conn *protocol.Conn
	srv  *Server
	room *room
}

// New 创建服务端
func New(listenAddr string) *Server {
	return &Server{
		listenAddr: listenAddr,
		rooms:      make(map[string]*room),
		streams:    make(map[uint32]*streamEntry),
	}
}

// Run 启动监听并持续接受客户端连接
func (s *Server) Run() error {
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return err
	}
	log.Printf("[服务端] 已启动，监听 %s", s.listenAddr)
	for {
		raw, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.serve(raw)
	}
}

// serve 处理单个客户端连接
func (s *Server) serve(raw net.Conn) {
	conn := protocol.NewConn(raw)
	s.mu.Lock()
	s.nextClientID++
	cc := &clientConn{
		id:   s.nextClientID,
		conn: conn,
		srv:  s,
	}
	s.mu.Unlock()
	log.Printf("[服务端] 客户端 %s 上线", raw.RemoteAddr())

	defer func() {
		s.cleanup(cc)
		conn.Close()
		log.Printf("[服务端] 客户端 %s 下线", raw.RemoteAddr())
	}()

	for {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		typ, payload, err := conn.ReadFrame()
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				log.Printf("[服务端] 客户端 %d 读取失败: %v", cc.id, err)
			}
			return
		}
		if err := s.dispatch(cc, typ, payload); err != nil {
			log.Printf("[服务端] 客户端 %d 处理帧 0x%02X 失败: %v", cc.id, typ, err)
			return
		}
	}
}

// dispatch 分发处理一帧
func (s *Server) dispatch(cc *clientConn, typ byte, payload []byte) error {
	switch typ {
	case protocol.FrameHello:
		return s.handleHello(cc, payload)
	case protocol.FrameGameAdd:
		return s.handleGameAdd(cc, payload)
	case protocol.FrameGameDel:
		return s.handleGameDel(cc, payload)
	case protocol.FrameStreamOpen:
		return s.handleStreamOpen(cc, payload)
	case protocol.FrameStreamResult:
		return s.handleStreamResult(cc, payload)
	case protocol.FrameStreamData, protocol.FrameStreamClose:
		return s.handleStreamRelay(cc, typ, payload)
	case protocol.FramePing:
		return cc.conn.WriteFrame(protocol.FramePong, nil)
	default:
		return nil
	}
}

// handleHello 处理登录并加入房间（房间不存在则创建）
func (s *Server) handleHello(cc *clientConn, payload []byte) error {
	var hello protocol.Hello
	if err := json.Unmarshal(payload, &hello); err != nil {
		return err
	}
	if hello.Name == "" || hello.Room == "" {
		return cc.conn.WriteJSON(protocol.FrameHelloAck, protocol.HelloAck{Error: "玩家名与房间名不能为空"})
	}

	s.mu.Lock()
	r := s.rooms[hello.Room]
	if r == nil {
		r = &room{
			name:    hello.Room,
			pass:    hello.Pass,
			clients: make(map[uint32]*clientConn),
			games:   make(map[uint32]*gameEntry),
		}
		s.rooms[hello.Room] = r
	} else if r.pass != hello.Pass {
		s.mu.Unlock()
		return cc.conn.WriteJSON(protocol.FrameHelloAck, protocol.HelloAck{Error: "房间密码错误"})
	}

	cc.name = hello.Name
	cc.room = r
	r.clients[cc.id] = cc

	ack := protocol.HelloAck{ClientID: cc.id}
	for id, other := range r.clients {
		if id == cc.id {
			continue
		}
		ack.Members = append(ack.Members, protocol.Member{ID: id, Name: other.name})
	}
	for _, g := range r.games {
		ack.Games = append(ack.Games, g.ann)
	}
	join := protocol.MemberJoin{Member: protocol.Member{ID: cc.id, Name: cc.name}}
	others := otherClients(r, cc.id)
	memberCount := len(r.clients)
	roomName := r.name
	s.mu.Unlock()

	if err := cc.conn.WriteJSON(protocol.FrameHelloAck, ack); err != nil {
		return err
	}
	log.Printf("[服务端] %s 加入房间 %s（当前 %d 人）", hello.Name, roomName, memberCount)
	for _, other := range others {
		if err := other.conn.WriteJSON(protocol.FrameMemberJoin, join); err != nil {
			log.Printf("[服务端] 通知成员加入失败: %v", err)
		}
	}
	return nil
}

// handleGameAdd 处理游戏发布或更新
func (s *Server) handleGameAdd(cc *clientConn, payload []byte) error {
	var ann protocol.GameAnnounce
	if err := json.Unmarshal(payload, &ann); err != nil {
		return err
	}

	s.mu.Lock()
	if cc.room == nil {
		s.mu.Unlock()
		return errors.New("尚未登录")
	}
	ann.HostID = cc.id
	ann.HostName = cc.name
	cc.room.games[ann.GameKey] = &gameEntry{key: ann.GameKey, host: cc, ann: ann}
	others := otherClients(cc.room, cc.id)
	s.mu.Unlock()

	for _, other := range others {
		if err := other.conn.WriteJSON(protocol.FrameGameAdd, ann); err != nil {
			log.Printf("[服务端] 转发游戏发布失败: %v", err)
		}
	}
	return nil
}

// handleGameDel 处理游戏下架
func (s *Server) handleGameDel(cc *clientConn, payload []byte) error {
	var del protocol.GameRemove
	if err := json.Unmarshal(payload, &del); err != nil {
		return err
	}

	s.mu.Lock()
	if cc.room == nil {
		s.mu.Unlock()
		return errors.New("尚未登录")
	}
	g := cc.room.games[del.GameKey]
	if g == nil || g.host != cc {
		s.mu.Unlock()
		return nil
	}
	delete(cc.room.games, del.GameKey)
	others := otherClients(cc.room, cc.id)
	s.mu.Unlock()

	for _, other := range others {
		if err := other.conn.WriteJSON(protocol.FrameGameDel, del); err != nil {
			log.Printf("[服务端] 转发游戏下架失败: %v", err)
		}
	}
	return nil
}

// handleStreamOpen 加入方请求打开到某游戏的转发流
func (s *Server) handleStreamOpen(cc *clientConn, payload []byte) error {
	var open protocol.StreamOpen
	if err := json.Unmarshal(payload, &open); err != nil {
		return err
	}

	s.mu.Lock()
	if cc.room == nil {
		s.mu.Unlock()
		return errors.New("尚未登录")
	}
	g := cc.room.games[open.GameKey]
	if g == nil {
		s.mu.Unlock()
		return cc.conn.WriteJSON(protocol.FrameStreamResult, protocol.StreamResult{
			ReqID: open.ReqID, OK: false, Error: "游戏已关闭",
		})
	}
	s.nextStreamID++
	gid := s.nextStreamID
	s.streams[gid] = &streamEntry{id: gid, joiner: cc, host: g.host, joinerReqID: open.ReqID}
	host := g.host
	s.mu.Unlock()

	log.Printf("[服务端] 打开转发流 %d: %s -> %s 的游戏 %d", gid, cc.name, host.name, open.GameKey)
	return host.conn.WriteJSON(protocol.FrameStreamOpen, protocol.StreamOpen{
		GlobalID: gid, GameKey: open.GameKey,
	})
}

// handleStreamResult 主机方返回连接本机游戏的结果
func (s *Server) handleStreamResult(cc *clientConn, payload []byte) error {
	var res protocol.StreamResult
	if err := json.Unmarshal(payload, &res); err != nil {
		return err
	}

	s.mu.Lock()
	st := s.streams[res.GlobalID]
	if st == nil || st.host != cc {
		s.mu.Unlock()
		return nil
	}
	if !res.OK {
		delete(s.streams, res.GlobalID)
	}
	joiner := st.joiner
	reqID := st.joinerReqID
	s.mu.Unlock()

	if !res.OK {
		log.Printf("[服务端] 转发流 %d 建立失败: %s", res.GlobalID, res.Error)
	}
	return joiner.conn.WriteJSON(protocol.FrameStreamResult, protocol.StreamResult{
		ReqID: reqID, GlobalID: res.GlobalID, OK: res.OK, Error: res.Error,
	})
}

// handleStreamRelay 在流两端之间转发数据或关闭通知
func (s *Server) handleStreamRelay(cc *clientConn, typ byte, payload []byte) error {
	id, _, err := protocol.DecodeStream(payload)
	if err != nil {
		return err
	}

	s.mu.Lock()
	st := s.streams[id]
	if st == nil {
		s.mu.Unlock()
		return nil
	}
	peer := st.host
	if peer == cc {
		peer = st.joiner
	}
	if typ == protocol.FrameStreamClose {
		delete(s.streams, id)
	}
	s.mu.Unlock()

	if typ == protocol.FrameStreamClose {
		log.Printf("[服务端] 转发流 %d 关闭", id)
	}
	return peer.conn.WriteFrame(typ, payload)
}

// cleanup 客户端断开后的清理：移除成员、下架其游戏、关闭其相关流
func (s *Server) cleanup(cc *clientConn) {
	s.mu.Lock()
	r := cc.room
	if r == nil {
		s.mu.Unlock()
		return
	}
	delete(r.clients, cc.id)
	cc.room = nil

	var removed []uint32
	for key, g := range r.games {
		if g.host == cc {
			delete(r.games, key)
			removed = append(removed, key)
		}
	}

	type streamClose struct {
		peer *clientConn
		id   uint32
	}
	var closes []streamClose
	for id, st := range s.streams {
		if st.joiner == cc || st.host == cc {
			peer := st.host
			if peer == cc {
				peer = st.joiner
			}
			closes = append(closes, streamClose{peer: peer, id: id})
			delete(s.streams, id)
		}
	}
	others := otherClients(r, cc.id)
	empty := len(r.clients) == 0
	roomName := r.name
	if empty {
		delete(s.rooms, roomName)
	}
	s.mu.Unlock()

	for _, other := range others {
		other.conn.WriteJSON(protocol.FrameMemberLeave, protocol.MemberLeave{ID: cc.id})
		for _, key := range removed {
			other.conn.WriteJSON(protocol.FrameGameDel, protocol.GameRemove{GameKey: key})
		}
	}
	for _, sc := range closes {
		log.Printf("[服务端] 转发流 %d 因客户端下线关闭", sc.id)
		sc.peer.conn.WriteFrame(protocol.FrameStreamClose, protocol.EncodeStream(sc.id, nil))
	}
}

// otherClients 返回房间内除指定客户端外的其他客户端
func otherClients(r *room, except uint32) []*clientConn {
	out := make([]*clientConn, 0, len(r.clients))
	for id, c := range r.clients {
		if id != except {
			out = append(out, c)
		}
	}
	return out
}
