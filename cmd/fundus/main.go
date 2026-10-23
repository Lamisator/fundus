// Command fundus runs the Fundus knowledge base server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Lamisator/fundus/internal/config"
	"github.com/Lamisator/fundus/internal/db"
	"github.com/Lamisator/fundus/internal/server"
	"github.com/Lamisator/fundus/internal/store"
)

func main() {
	version := flag.Bool("version", false, "print the version and exit")
	health := flag.String("healthcheck", "", "probe http://<addr>/healthz and exit 0/1 (for Docker HEALTHCHECK)")
	flag.Parse()
	if *version {
		fmt.Println(server.Version)
		return
	}
	if *health != "" {
		os.Exit(probe(*health))
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	srv, err := server.New(cfg, store.New(pool))
	if err != nil {
		return err
	}
	if err := srv.Bootstrap(ctx); err != nil {
		return err
	}
	go srv.Maintenance(ctx)

	hs := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("fundus listening", "addr", cfg.Addr, "version", server.Version, "sso", cfg.OIDC.Enabled())
		errc <- hs.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return hs.Shutdown(shutdown)
}

func probe(addr string) int {
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return 1
	}
	return 0
}
