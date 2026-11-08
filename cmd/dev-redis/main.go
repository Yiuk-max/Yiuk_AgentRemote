// Command dev-redis 是本机开发用的假 Redis：把 miniredis 挂在 127.0.0.1:6379，Ctrl-C 退出。
//
// 只为了本机能起 core + gateway，不持久化、不用于任何真实部署。
// 生产环境请用真正的 Redis（路由可丢，但重启期间多网关寻址会失效）。
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alicebob/miniredis/v2"
)

func main() {
	address := "127.0.0.1:6379"
	if len(os.Args) > 1 {
		address = os.Args[1]
	}
	server := miniredis.NewMiniRedis()
	if err := server.StartAddr(address); err != nil {
		fmt.Fprintln(os.Stderr, "dev-redis:", err)
		os.Exit(1)
	}
	fmt.Println("dev-redis (miniredis) listening on", server.Addr())
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	server.Close()
}
