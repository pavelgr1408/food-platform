// restaurant-service: каталог ресторанов (UC-FND-01). Read-only, без Kafka: в S1 сервис
// ничего не публикует и не потребляет. Эталон слоёв — services/auth.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // IANA-зоны в бинаре: distroless-образ не содержит /usr/share/zoneinfo

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/logging"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/config"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/httpapi"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/seed"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/service"
	"github.com/pavelgr1408/food-platform/services/restaurant/internal/store"
	"github.com/pavelgr1408/food-platform/services/restaurant/migrations"
)

func main() {
	cfg, err := config.Load()
	log := logging.New(logging.Options{Service: "restaurant", Env: cfg.Env, Level: cfg.LogLevel})
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

	pool, err := pgxutil.Open(ctx, cfg.DatabaseURL, pgxutil.Options{AppName: "restaurant-service"})
	if err != nil {
		return err
	}
	defer pool.Close()

	// Миграции. В k8s — отдельной Job: `--migrate-only`.
	if err := pgxutil.Migrate(ctx, pool, log, pgxutil.Source{Module: "restaurant", FS: migrations.FS}); err != nil {
		return err
	}
	if pgxutil.MigrateOnly(os.Args[1:]) {
		log.Info("migrations applied, exiting (--migrate-only)")
		return nil
	}

	st := store.New(pool)
	if cfg.SeedOnStart {
		rs, err := seed.Restaurants()
		if err != nil {
			return err
		}
		if err := st.Upsert(ctx, rs); err != nil {
			return err
		}
		log.Info("seed applied", "restaurants", len(rs))
	}

	auth, err := authclient.New(cfg.Auth, log)
	if err != nil {
		return err
	}
	reg := metrics.New("restaurant")
	checks := []httpx.Check{{Name: "db", Fn: pgxutil.Ping(pool)}}

	handler := httpapi.New(httpapi.Deps{Log: log, Metrics: reg, Auth: auth, Checks: checks, Catalog: service.New(st)})
	srv := httpx.NewServer(":"+cfg.HTTPPort, handler)
	return httpx.Run(ctx, srv, log, 10*time.Second)
}
