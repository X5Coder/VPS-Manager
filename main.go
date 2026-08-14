package main

import (
	"embed"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/x5coder/vps-rooms/internal/api"
	"github.com/x5coder/vps-rooms/internal/config"
	"github.com/x5coder/vps-rooms/internal/dockerx"
	"github.com/x5coder/vps-rooms/internal/metrics"
	"github.com/x5coder/vps-rooms/internal/store"
)

//go:embed all:web
var webEmbed embed.FS

func main() {
	cfg := config.Load()
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(cfg.RoomsDir, 0o700); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(cfg.RuntimeDir, 0o700); err != nil {
		log.Fatal(err)
	}
	logDir := filepath.Join(cfg.DataDir, "logs")
	_ = os.MkdirAll(logDir, 0o700)
	if lf, err := os.OpenFile(filepath.Join(logDir, "panel.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, lf))
		defer lf.Close()
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	_ = st.CleanupSessions()

	var docker *dockerx.Client
	if d, err := dockerx.New(); err != nil {
		log.Printf("docker unavailable: %v (panel will still run)", err)
	} else {
		docker = d
		defer docker.Close()
		log.Printf("docker connected")
	}

	hub := metrics.NewHub(cfg.AgentSock)
	hub.Start()

	srv := api.New(cfg, st, docker, hub)
	if err := srv.Gate.EnsureOwnerChatID(cfg.TelegramChatID); err != nil {
		log.Printf("telegram chat lock: %v", err)
	} else if strings.TrimSpace(cfg.TelegramChatID) != "" {
		log.Printf("telegram owner chat id locked in secrets (not exposed)")
	}
	webFS, err := fs.Sub(webEmbed, "web")
	if err != nil {
		log.Fatal(err)
	}

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(http.FS(webFS)),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("VPS Rooms Panel listening on %s", cfg.ListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	// Periodic log trim so AI / UI never ingest unbounded text.
	go func() {
		api.PruneLogsDir(cfg.DataDir, 256*1024)
		t := time.NewTicker(30 * time.Minute)
		defer t.Stop()
		for range t.C {
			api.PruneLogsDir(cfg.DataDir, 256*1024)
		}
	}()

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	log.Printf("shutting down")
	_ = httpSrv.Close()
}
