// auth-service: идентификация и доступ — регистрация, вход по телефону, JWT RS256 (+JWKS), refresh,
// introspect и authorize (контракт contracts/openapi/auth.yaml). Эталон слоёв платформы.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/logging"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/auth/internal/config"
	"github.com/pavelgr1408/food-platform/services/auth/internal/httpapi"
	"github.com/pavelgr1408/food-platform/services/auth/internal/seed"
	"github.com/pavelgr1408/food-platform/services/auth/internal/service"
	"github.com/pavelgr1408/food-platform/services/auth/internal/store"
	"github.com/pavelgr1408/food-platform/services/auth/internal/token"
	"github.com/pavelgr1408/food-platform/services/auth/migrations"
)

func main() {
	cfg, err := config.Load()
	log := logging.New(logging.Options{Service: "auth", Env: cfg.Env, Level: cfg.LogLevel})
	if err != nil {
		log.Error("configuration error", "error", err)
		os.Exit(1)
	}
	if err := run(cfg, log); err != nil {
		log.Error("service stopped with error", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxutil.Open(ctx, cfg.DatabaseURL, pgxutil.Options{AppName: "auth-service", MaxConns: 20, MinConns: 2})
	if err != nil {
		return err
	}
	defer pool.Close()

	// Миграции. До S5 у auth была своя таблица schema_migrations(version): усыновляем её в формат по модулям,
	// иначе мигратор повторно применил бы 001/002 к живой БД. В k8s — отдельной Job: `--migrate-only`.
	src := pgxutil.Source{Module: "auth", FS: migrations.FS}
	if err := pgxutil.AdoptLegacy(ctx, pool, log, src); err != nil {
		return err
	}
	if err := pgxutil.Migrate(ctx, pool, log, src); err != nil {
		return err
	}
	if pgxutil.MigrateOnly(os.Args[1:]) {
		log.Info("migrations applied, exiting (--migrate-only)")
		return nil
	}

	tokens, err := token.New(token.Config{PrivatePath: cfg.PrivateKeyPath, PublicPath: cfg.PublicKeyPath, Issuer: cfg.Issuer,
		Audience: cfg.Audience, KeyID: cfg.KeyID, TTL: cfg.AccessTTL, Previous: cfg.PreviousKeys})
	if err != nil {
		return err
	}
	st := store.New(pool)
	svc := service.New(st, tokens, cfg.RefreshTTL, cfg.BcryptCost)

	switch {
	case cfg.SeedOnStart:
		if err := seed.Apply(ctx, st, svc, log); err != nil {
			return err
		}
	case cfg.Env == "prod":
		// Учётки из миграции 002 создаются при любом окружении — в проде их быть не должно.
		if n, err := st.DeleteUsers(ctx, seed.LegacyDevIDs()); err != nil {
			return err
		} else if n > 0 {
			log.Warn("legacy dev users removed", "count", n)
		}
	}

	reg := metrics.New("auth")
	checks := []httpx.Check{{Name: "db", Fn: pgxutil.Ping(pool)}}
	handler := httpapi.New(httpapi.Deps{Log: log, Metrics: reg, Checks: checks, Service: svc})
	srv := httpx.NewServer(":"+cfg.HTTPPort, handler)
	return httpx.Run(ctx, srv, log, 10*time.Second)
}
