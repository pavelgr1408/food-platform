package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/example/restaurant-auth-service/internal/config"
	"github.com/example/restaurant-auth-service/internal/httpapi"
	"github.com/example/restaurant-auth-service/internal/service"
	"github.com/example/restaurant-auth-service/internal/store"
	"github.com/example/restaurant-auth-service/internal/token"
	"github.com/example/restaurant-auth-service/migrations"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration error", "error", err)
		os.Exit(1)
	}
	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := migrations.Apply(ctx, db.Pool); err != nil {
		log.Error("database migration failed", "error", err)
		os.Exit(1)
	}
	tokens, err := token.New(cfg.PrivateKeyPath, cfg.PublicKeyPath, cfg.Issuer, cfg.Audience, cfg.KeyID, cfg.AccessTTL)
	if err != nil {
		log.Error("JWT initialization failed", "error", err)
		os.Exit(1)
	}
	svc := service.New(db, tokens, cfg.RefreshTTL)
	server := &http.Server{Addr: ":" + cfg.HTTPPort, Handler: httpapi.New(svc, db, log), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		log.Info("auth service started", "address", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP server failed", "error", err)
			os.Exit(1)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		log.Error("graceful shutdown failed", "error", err)
	}
}
