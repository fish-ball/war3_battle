// w3bridge-server 是魔兽争霸3互联网联机工具的中继服务端，部署在有公网 IP 的服务器上。
//
// 用法示例：
//
//	w3bridge-server.exe -listen :51234
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	_ "war3_battle/internal/console" // 控制台 UTF-8 初始化
	"war3_battle/internal/server"
)

func main() {
	listenAddr := flag.String("listen", ":51234", "监听地址，例如 :51234")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "魔兽争霸3互联网联机中继服务端\n\n用法: %s [选项]\n\n选项:\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	log.SetFlags(log.Ltime)
	if err := server.New(*listenAddr).Run(); err != nil {
		log.Fatalf("[服务端] %v", err)
	}
}
