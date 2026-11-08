// Command dev-routes 打印路由表：Redis 里所有 agentremote:* 键、值与剩余 TTL。
//
// 本地联调时用来看“网关登记上了吗、连接归属写对了吗、断开后清掉了吗”：
//
//	go run ./cmd/dev-routes            # 默认 127.0.0.1:6379
//	go run ./cmd/dev-routes host:6379
package main

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/redis/go-redis/v9"
)

func main() {
	address := "127.0.0.1:6379"
	if len(os.Args) > 1 {
		address = os.Args[1]
	}
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: address})
	defer func() { _ = client.Close() }()

	keys, err := client.Keys(ctx, "agentremote:*").Result()
	if err != nil {
		fmt.Fprintln(os.Stderr, "keys:", err)
		os.Exit(1)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		fmt.Println("(空)")
		return
	}
	for _, key := range keys {
		value, _ := client.Get(ctx, key).Result()
		ttl, _ := client.TTL(ctx, key).Result()
		fmt.Printf("%-45s = %-40s ttl=%s\n", key, value, ttl)
	}
}
