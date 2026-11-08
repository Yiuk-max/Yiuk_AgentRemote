// Command platform-gateway 是拆分后的接入进程：只管 socket，不管业务。
//
// 客户端连到它，它把每一帧原样转给 core；core 要发的帧通过它的 Push 服务
// 写回对应连接。连接表就在这个进程里，它也是“这条连接还活着吗”的唯一依据。
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
	"agentremote/internal/platform/edge"
	"agentremote/internal/platform/edge/edgepb"
	"agentremote/internal/platform/gateway"
	"agentremote/internal/platform/logging"
	"google.golang.org/grpc"
)

func main() {
	//读取共用的配置文件
	configPath := flag.String("config", "platform.yaml", "path to the platform YAML configuration")
	flag.Parse()
	// 加载配置文件
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gateway config error:", err)
		os.Exit(1)
	}
	// 设置日志记录器
	closeLog, logger, err := logging.Setup(cfg.LogFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gateway log error:", err)
		os.Exit(1)
	}
	defer closeLog()
	slog.SetDefault(logger)

	// 创建网关实例
	gw, err := gateway.New(cfg, logger)
	if err != nil {
		logger.Error("could not start gateway listener", "error", err)
		os.Exit(1)
	}
	// 连接到 core 的 Edge 服务
	coreClient, err := edge.NewCoreClient(cfg.CoreGRPCAddr)
	if err != nil {
		logger.Error("could not connect to core", "address", cfg.CoreGRPCAddr, "error", err)
		os.Exit(1)
	}
	gw.Bind(coreClient) // 绑定 coreClient 到网关，网关收到的每一帧都交给 coreClient.Handle 处理

	listener, err := net.Listen("tcp", cfg.GatewayGRPCListen) // 监听 gRPC 端口，供 core 调用 Push 服务
	if err != nil {
		logger.Error("could not listen for core", "address", cfg.GatewayGRPCListen, "error", err)
		os.Exit(1)
	}
	grpcServer := grpc.NewServer() // 注册 Push 服务
	edgepb.RegisterPushServer(grpcServer, edge.NewPushServer(gw))

	logger.Info("gateway listening", "listen", cfg.Listen, "tls", cfg.TLSCert != "", "insecure", cfg.AllowInsecure,
		"core", cfg.CoreGRPCAddr, "push", cfg.GatewayGRPCListen)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() { // 启动 gRPC 服务循环
		if err := grpcServer.Serve(listener); err != nil {
			logger.Error("gateway gRPC server stopped", "error", err)
			stop()
		}
	}()
	// 启动网关循环
	if err := gw.Run(ctx); err != nil {
		logger.Error("gateway stopped", "error", err)
	}
	grpcServer.Stop()
	logger.Info("gateway stopped")
}
