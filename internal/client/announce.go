package client

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"war3_battle/internal/protocol"
	"war3_battle/internal/w3gs"
)

// announceInterval 远程游戏在本机网络上的公告周期
const announceInterval = 3 * time.Second

// remoteGame 房间内其他机器发布的游戏，在本机网络上伪装为局域网游戏
type remoteGame struct {
	gameKey   uint32
	proxyPort int
	udpConn   *net.UDPConn
	tcpLn     net.Listener
	stop      chan struct{}
	stopOnce  sync.Once

	mu   sync.Mutex
	info *w3gs.GameInfo
}

func (rg *remoteGame) setInfo(info *w3gs.GameInfo) {
	rg.mu.Lock()
	rg.info = info
	rg.mu.Unlock()
}

func (rg *remoteGame) getInfo() *w3gs.GameInfo {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	return rg.info
}

// addRemoteGame 登记远程游戏：首次登记时创建本地代理端口并开始公告，重复登记视为信息更新
func (c *Client) addRemoteGame(ann protocol.GameAnnounce) {
	info, err := w3gs.ParseGameInfo(ann.Raw)
	if err != nil {
		log.Printf("[客户端] 解析远程游戏信息失败: %v", err)
		return
	}

	c.mu.Lock()
	rg := c.remoteGames[ann.GameKey]
	if rg != nil {
		rg.setInfo(info)
		c.mu.Unlock()
		return
	}

	rg = &remoteGame{
		gameKey:   ann.GameKey,
		proxyPort: c.nextProxyPort,
		stop:      make(chan struct{}),
		info:      info,
	}
	rg.udpConn, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		c.mu.Unlock()
		log.Printf("[客户端] 创建游戏公告套接字失败: %v", err)
		return
	}
	rg.tcpLn, err = net.Listen("tcp", fmt.Sprintf(":%d", rg.proxyPort))
	if err != nil {
		rg.udpConn.Close()
		c.mu.Unlock()
		log.Printf("[客户端] 监听本地代理端口 %d 失败: %v", rg.proxyPort, err)
		return
	}
	c.nextProxyPort++
	c.remoteGames[ann.GameKey] = rg
	c.mu.Unlock()

	log.Printf("[客户端] 房间新游戏: %s (地图 %s, %d/%d 人, 主机 %s)，本地代理端口 %d",
		info.Name, info.MapName, info.DisplayedPlayers(), info.Slots, ann.HostName, rg.proxyPort)

	go c.announceLoop(rg)
	go c.acceptLoop(rg)
}

// removeRemoteGame 移除远程游戏：通知本机游戏列表删除条目并停止公告
func (c *Client) removeRemoteGame(gameKey uint32) {
	c.mu.Lock()
	rg := c.remoteGames[gameKey]
	if rg == nil {
		c.mu.Unlock()
		return
	}
	delete(c.remoteGames, gameKey)
	c.mu.Unlock()

	info := rg.getInfo()
	rg.stopOnce.Do(func() { close(rg.stop) })
	// 通知本机游戏列表移除该条目
	if _, err := rg.udpConn.WriteToUDP(w3gs.BuildGameCancelled(info.GameID),
		&net.UDPAddr{IP: net.IPv4bcast, Port: int(c.cfg.GamePort)}); err != nil {
		log.Printf("[客户端] 发送游戏取消公告失败: %v", err)
	}
	rg.udpConn.Close()
	rg.tcpLn.Close()
	log.Printf("[客户端] 游戏已下架: %s", info.Name)
}

// announceLoop 周期性广播改写过端口的 0x30 游戏信息，维持本机游戏列表中的条目
func (c *Client) announceLoop(rg *remoteGame) {
	ticker := time.NewTicker(announceInterval)
	defer ticker.Stop()
	dst := &net.UDPAddr{IP: net.IPv4bcast, Port: int(c.cfg.GamePort)}

	for {
		select {
		case <-rg.stop:
			return
		case <-ticker.C:
			info := rg.getInfo()
			// 改写包尾端口为本地代理端口，本机游戏点加入时即会连接代理
			if _, err := rg.udpConn.WriteToUDP(w3gs.PatchGamePort(info.Raw, uint16(rg.proxyPort)), dst); err != nil {
				log.Printf("[客户端] 广播游戏公告失败: %v", err)
			}
			// 0x32 刷新包同步最新人数
			if _, err := rg.udpConn.WriteToUDP(w3gs.BuildRefreshGame(info.GameID, info.DisplayedPlayers(), info.Slots), dst); err != nil {
				log.Printf("[客户端] 广播游戏刷新失败: %v", err)
			}
		}
	}
}

// acceptLoop 接受本机游戏加入远程游戏的 TCP 连接
func (c *Client) acceptLoop(rg *remoteGame) {
	for {
		conn, err := rg.tcpLn.Accept()
		if err != nil {
			return
		}
		go c.handleJoin(rg, conn)
	}
}
