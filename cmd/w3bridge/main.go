// w3bridge 是魔兽争霸3互联网联机客户端。
//
// 用法示例：
//
//	w3bridge.exe -server 1.2.3.4:51234 -room 666 -name 老崔
//	w3bridge.exe -server 1.2.3.4:51234 -room 666 -name 勐哥 -pass abc
//
// 启动后工具会自动完成两件事：
// 1. 发布本机局域网里开的魔兽游戏到房间（谁开图谁运行即可）；
// 2. 把房间里的游戏广播到本机网络，魔兽“局域网”列表里直接能看到并加入。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"

	"war3_battle/internal/client"
	_ "war3_battle/internal/console" // 控制台 UTF-8 初始化
	"war3_battle/internal/w3gs"
)

func main() {
	var (
		serverAddr = flag.String("server", "", "服务端地址，例如 1.2.3.4:51234")
		room       = flag.String("room", "666", "房间名，三人约好一致即可")
		password   = flag.String("pass", "", "房间密码，可留空")
		name       = flag.String("name", "", "玩家名（显示在房间里）")
		product    = flag.String("product", "tft", "游戏类型：tft（冰封王座）或 roc（混乱之治）")
		gamePort   = flag.Uint("game-port", 6112, "魔兽局域网端口（游戏选项里的端口）")
		proxyBase  = flag.Int("proxy-base", 26112, "本地代理端口起始值")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "魔兽争霸3互联网联机客户端\n\n用法: %s -server 服务端地址:端口 -name 玩家名 [选项]\n\n选项:\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if *serverAddr == "" || *name == "" {
		flag.Usage()
		os.Exit(2)
	}

	var prod [4]byte
	switch *product {
	case "tft":
		prod = w3gs.ProductTFT
	case "roc":
		prod = w3gs.ProductROC
	default:
		fmt.Fprintln(os.Stderr, "product 只能是 tft 或 roc")
		os.Exit(2)
	}

	log.SetFlags(log.Ltime)
	log.Print("[客户端] 魔兽争霸3互联网联机工具启动")
	log.Printf("[客户端] 配置：服务端 %s，房间 %s，玩家 %s，端口 %d",
		*serverAddr, *room, *name, *gamePort)
	log.Print("[客户端] 提示：魔兽选项里“局域网 TCP/IP 游戏”的端口需与 -game-port 一致")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	c := client.New(client.Config{
		ServerAddr: *serverAddr,
		Room:       *room,
		Password:   *password,
		Name:       *name,
		Product:    prod,
		GamePort:   uint16(*gamePort),
		ProxyBase:  *proxyBase,
	})
	if err := c.Run(ctx); err != nil {
		log.Fatalf("[客户端] %v", err)
	}
}
