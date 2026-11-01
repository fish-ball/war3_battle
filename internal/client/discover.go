package client

import (
	"context"
	"log"
	"net"
	"time"

	"war3_battle/internal/w3gs"
)

// discoveryInterval 本机局域网游戏查询周期
const discoveryInterval = 3 * time.Second

// localGameExpire 发现结果过期时间（超过该时间未再应答则认为游戏关闭）
const localGameExpire = 9 * time.Second

// localGame 本机局域网发现的游戏
type localGame struct {
	gameKey  uint32
	info     *w3gs.GameInfo
	lastSeen time.Time

	// 已发布快照，用于检测游戏信息变化
	registered  bool
	sentPlayers uint32
	sentSlots   uint32
	sentName    string
	sentPort    uint16
}

// runDiscovery 周期性广播 0x2F 查询本机局域网的游戏，并把发现的游戏同步到服务端
func (c *Client) runDiscovery(ctx context.Context) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		log.Printf("[客户端] 创建局域网查询套接字失败: %v", err)
		return
	}
	defer conn.Close()

	go c.discoveryReadLoop(ctx, conn)

	ticker := time.NewTicker(discoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pkt := w3gs.BuildSearchGame(c.cfg.Product, c.cfg.Version)
			// 广播查询局域网内的游戏，同时查询本机回环（游戏与工具同机时）
			if _, err := conn.WriteToUDP(pkt, &net.UDPAddr{IP: net.IPv4bcast, Port: int(c.cfg.GamePort)}); err != nil {
				log.Printf("[客户端] 广播局域网查询失败: %v", err)
			}
			if _, err := conn.WriteToUDP(pkt, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(c.cfg.GamePort)}); err != nil {
				log.Printf("[客户端] 发送本机查询失败: %v", err)
			}
			c.syncLocalGames()
		}
	}
}

// discoveryReadLoop 接收游戏的 0x30 应答
func (c *Client) discoveryReadLoop(ctx context.Context, conn *net.UDPConn) {
	buf := make([]byte, 2048)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				log.Printf("[客户端] 局域网查询接收失败: %v", err)
				return
			}
		}
		info, err := w3gs.ParseGameInfo(buf[:n])
		if err != nil {
			// 非游戏信息包直接忽略
			continue
		}

		c.mu.Lock()
		g := c.localGames[addr.String()]
		if g == nil {
			g = &localGame{gameKey: c.makeGameKeyLocked()}
			c.localGames[addr.String()] = g
		}
		g.info = info
		g.lastSeen = time.Now()
		c.mu.Unlock()
	}
}

// makeGameKeyLocked 生成游戏发布标识（调用方需持有 c.mu）
func (c *Client) makeGameKeyLocked() uint32 {
	c.nextGameSeq++
	return c.clientID<<16 | c.nextGameSeq
}

// syncLocalGames 把发现的游戏同步给服务端：新增、信息变化时重新发布，过期则下架
func (c *Client) syncLocalGames() {
	now := time.Now()

	type update struct {
		gameKey uint32
		info    *w3gs.GameInfo
		isNew   bool
	}
	var publishes []update
	var removes []update

	c.mu.Lock()
	for key, g := range c.localGames {
		if now.Sub(g.lastSeen) > localGameExpire {
			removes = append(removes, update{gameKey: g.gameKey, info: g.info})
			delete(c.localGames, key)
			continue
		}
		changed := !g.registered ||
			g.sentPlayers != g.info.Players ||
			g.sentSlots != g.info.Slots ||
			g.sentName != g.info.Name ||
			g.sentPort != g.info.TCPPort
		if !changed {
			continue
		}
		isNew := !g.registered
		g.registered = true
		g.sentPlayers = g.info.Players
		g.sentSlots = g.info.Slots
		g.sentName = g.info.Name
		g.sentPort = g.info.TCPPort
		publishes = append(publishes, update{gameKey: g.gameKey, info: g.info, isNew: isNew})
	}
	c.mu.Unlock()

	for _, u := range removes {
		if err := c.sendGameRemove(u.gameKey); err != nil {
			log.Printf("[客户端] 游戏下架通知失败: %v", err)
			continue
		}
		log.Printf("[客户端] 本机游戏已结束: %s", u.info.Name)
	}

	for _, u := range publishes {
		if err := c.sendGameAdd(u.gameKey, u.info); err != nil {
			log.Printf("[客户端] 游戏发布失败: %v", err)
			continue
		}
		if u.isNew {
			log.Printf("[客户端] 发现本机游戏并发布: %s (地图 %s, %d/%d 人, 主机 %s)",
				u.info.Name, u.info.MapName, u.info.DisplayedPlayers(), u.info.Slots, u.info.HostName)
		} else {
			log.Printf("[客户端] 游戏信息已更新: %s (%d/%d 人)",
				u.info.Name, u.info.DisplayedPlayers(), u.info.Slots)
		}
	}
}
