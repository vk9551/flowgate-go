package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vk9551/flowgate-io/internal/api"
	"github.com/vk9551/flowgate-io/internal/config"
	"github.com/vk9551/flowgate-io/internal/dispatcher"
	"github.com/vk9551/flowgate-io/internal/scheduler"
	"github.com/vk9551/flowgate-io/internal/store"
)

func main() {
	// Config path priority: CLI arg > FLOWGATE_CONFIG_PATH env > default.
	cfgPath := "flowgate.yaml"
	if v := os.Getenv("FLOWGATE_CONFIG_PATH"); v != "" {
		cfgPath = v
	}
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("flowgate: load config: %v", err)
	}
	log.Printf("flowgate: config loaded (version=%s, priorities=%d)", cfg.Version, len(cfg.Priorities))

	st, err := openStore(cfg)
	if err != nil {
		log.Fatalf("flowgate: open store: %v", err)
	}
	defer st.Close()

	// API server (holds the live config pointer).
	apiSrv := api.NewServer(cfgPath, cfg, st)

	// Dispatcher reads the live config through the server's getter so that
	// a hot-reload propagates to future dispatch calls.
	disp := dispatcher.New(apiSrv.GetConfig)

	// Scheduler: on fire → dispatch → delete from store.
	sched := scheduler.New(st, func(e *store.ScheduledEvent) {
		if err := disp.Dispatch(e, "send_now"); err != nil {
			log.Printf("dispatcher: event %s: %v", e.ID, err)
		}
	}, 0 /* default 5s interval */)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sched.Start(ctx); err != nil {
		log.Fatalf("flowgate: start scheduler: %v", err)
	}
	log.Println("flowgate: scheduler started")

	apiSrv.SetScheduler(sched)

	port := cfg.Server.Port
	if port == 0 {
		port = 7700
	}
	addr := fmt.Sprintf(":%d", port)

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      apiSrv.Handler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("flowgate: listening on %s", addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("flowgate: server error: %v", err)
		}
	}()

	// Wait for SIGINT or SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("flowgate: shutting down...")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()

	if err := httpSrv.Shutdown(shutCtx); err != nil {
		log.Printf("flowgate: HTTP shutdown error: %v", err)
	}

	cancel() // signal scheduler to stop
	sched.Stop()

	log.Println("flowgate: stopped")
}

func openStore(cfg *config.Config) (store.Store, error) {
	dsn := cfg.Storage.DSN
	if dsn == "" {
		dsn = "flowgate.db"
	}
	switch cfg.Storage.Backend {
	case "sqlite", "":
		return store.OpenSQLite(dsn)
	default:
		return nil, fmt.Errorf("unsupported storage backend %q", cfg.Storage.Backend)
	}
}
