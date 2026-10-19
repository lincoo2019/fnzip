package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var appVersion = "1.3.1"

func main() {
	var (
		addr     = flag.String("addr", "0.0.0.0:18687", "监听地址")
		dataDir  = flag.String("data-dir", "./data", "数据目录")
		sevenZip = flag.String("7zz", "", "7-Zip 可执行文件路径")
		webDir   = flag.String("web-dir", "", "开发模式下的静态页面目录")
	)
	flag.Parse()

	log.SetFlags(log.LstdFlags)
	state, err := LoadState(*dataDir)
	if err != nil {
		log.Fatalf("加载状态失败: %v", err)
	}
	if *sevenZip != "" {
		state.SetSevenZip(*sevenZip)
	}

	engine := NewEngine(state)
	engine.Start()
	server := NewServer(state, engine, *webDir)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("智能解压已启动: http://%s (版本 %s)", *addr, appVersion)
		log.Printf("数据目录: %s", *dataDir)
		log.Printf("7-Zip: %s", engine.BinPath())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务失败: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	engine.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Printf("智能解压已停止")
}
