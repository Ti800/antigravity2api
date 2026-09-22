package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/Ti800/antigravity2api/internal/auth"
	"github.com/Ti800/antigravity2api/internal/config"
	"github.com/Ti800/antigravity2api/internal/server"
	"github.com/Ti800/antigravity2api/internal/upstream"
)

var Version = "dev"

func main() {
	portFlag := flag.Int("port", 0, "listen port")
	configFlag := flag.String("config", "", "config file path")
	authDirFlag := flag.String("auth-dir", "", "directory of account json files")
	versionFlag := flag.Bool("version", false, "print version")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("antigravity2api %s\n", Version)
		os.Exit(0)
	}

	path := *configFlag
	if path == "" {
		path = os.Getenv("ANTIGRAVITY2API_CONFIG")
	}
	if path == "" {
		path = config.Find()
	}
	cfg, err := config.Load(path)
	if err != nil && path != "" {
		log.Printf("config: %v (using defaults)", err)
	}
	if *portFlag != 0 {
		cfg.Port = *portFlag
	}
	if *authDirFlag != "" {
		cfg.AuthDir = *authDirFlag
	}
	if cfg.AuthDir == "" {
		cfg.AuthDir = "auth"
	}
	applyRuntime(cfg)

	up := upstream.New(time.Duration(cfg.RequestTimeoutSec) * time.Second)
	store, err := auth.Load(cfg.AuthDir, up.HTTP)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}

	app := server.New(cfg, store, up, Version)
	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	fmt.Printf("antigravity2api %s\n", Version)
	fmt.Printf("  listening: http://%s:%d\n", cfg.Host, cfg.Port)
	fmt.Printf("  accounts:  %d\n", store.Count())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()
	<-ctx.Done()

	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
}

// applyRuntime keeps the process small enough that iOS is less eager to reclaim
// it. These are soft caps: one in-flight response can still exceed them.
func applyRuntime(cfg config.Config) {
	if cfg.GOMAXPROCS > 0 {
		runtime.GOMAXPROCS(cfg.GOMAXPROCS)
	}
	if cfg.GCPercent > 0 {
		debug.SetGCPercent(cfg.GCPercent)
	}
	if cfg.MemoryLimitMB > 0 {
		debug.SetMemoryLimit(int64(cfg.MemoryLimitMB) << 20)
	}
}
