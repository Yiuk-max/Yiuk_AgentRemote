// Command platform-core 是拆分后的业务进程：只有业务，没有客户端连接。
//
// 它监听内网 gRPC（Edge 服务）接收 gateway 送上来的帧，并通过 gateway 的
// Push 服务把出站帧送回去。状态仍然全部在内存，重启即丢弃。
//
// P2 起出站不再只认一个网关地址：先查 Redis 路由，再投给连接所在的网关。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"agentremote/internal/platform/config"
	"agentremote/internal/platform/core"
	"agentremote/internal/platform/edge"
	"agentremote/internal/platform/edge/edgepb"
	"agentremote/internal/platform/logging"
	"agentremote/internal/platform/registry"
	"google.golang.org/grpc"
)

func main() {
	configPath := flag.String("config", "platform.yaml", "path to the platform YAML configuration")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "core config error:", err)
		os.Exit(1)
	}

	closeLog, logger, err := logging.Setup(cfg.LogFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "core log error:", err)
		os.Exit(1)
	}
	defer closeLog()
	slog.SetDefault(logger)

	// 路由表：只记录“谁在哪”。掉了只是寻址失效，不影响授权判断。
	routes := registry.Open(registry.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
		TTL:      cfg.RouteTTL(),
	})
	defer func() { _ = routes.Close() }()

	// core 唯一的出口：连 gateway 的 Push 服务。
	// P2 起先查路由选出网关；查不到就用下面配置里的网关地址。
	service := core.New(cfg, logger, edge.NewRouterLink(routes, cfg.GatewayGRPCAddr))

	listener, err := net.Listen("tcp", cfg.CoreGRPCListen)
	if err != nil {
		logger.Error("could not listen for gateways", "address", cfg.CoreGRPCListen, "error", err)
		os.Exit(1)
	}
	grpcServer := grpc.NewServer() // 注册 Edge 服务，供 gateway 调用
	edgepb.RegisterEdgeServer(grpcServer, edge.NewCoreServer(service))

	if len(cfg.PlayerPassword) < config.RecommendedPlayerPasswordLen {
		logger.Warn("player_password is shorter than recommended; use a longer random secret for public deployments",
			"length", len(cfg.PlayerPassword), "recommended", config.RecommendedPlayerPasswordLen)
	}
	logger.Info("core listening for gateways", "grpc", cfg.CoreGRPCListen,
		"fallback_gateway", cfg.GatewayGRPCAddr, "redis", cfg.RedisAddr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			logger.Error("core gRPC server stopped", "error", err)
			stop()
		}
	}()
	if err := service.Run(ctx); err != nil {
		logger.Error("core service stopped", "error", err)
	}
	grpcServer.Stop()
	logger.Info("core stopped")
}
