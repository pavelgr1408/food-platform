// loyalty-service: акции и скидки. Управление акциями — персонал (права проверяет auth-service),
// просмотр действующих — любой клиент. Акции публикуются в promotions.discount (outbox); расчёт
// скидки выполняет orders-service по собственной реплике. Эталон слоёв — services/auth.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/idempotency"
	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/pkg/logging"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/authz"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/config"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/httpapi"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/service"
	"github.com/pavelgr1408/food-platform/services/loyalty/migrations"
)

func main() {
	cfg, err := config.Load()
	log := logging.New(logging.Options{Service: "loyalty", Env: cfg.Env, Level: cfg.LogLevel})
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

	pool, err := pgxutil.Open(ctx, cfg.DatabaseURL, pgxutil.Options{AppName: "loyalty-service"})
	if err != nil {
		return err
	}
	defer pool.Close()

	// Миграции: сервиса + outbox/inbox + ключи идемпотентности. В k8s — отдельной Job: `--migrate-only`.
	if err := pgxutil.Migrate(ctx, pool, log,
		pgxutil.Source{Module: "loyalty", FS: migrations.FS},
		kafka.Migrations(), idempotency.Migrations()); err != nil {
		return err
	}
	if pgxutil.MigrateOnly(os.Args[1:]) {
		log.Info("migrations applied, exiting (--migrate-only)")
		return nil
	}

	auth, err := authclient.New(cfg.Auth, log)
	if err != nil {
		return err
	}
	// Права на управление акциями: в jwks-режиме решает auth-service (актуальные данные БД), в dev (токена нет) — claims.
	var authorizer authz.Authorizer = authz.Claims{}
	if cfg.Auth.Mode == "jwks" {
		authorizer = authz.NewHTTP(cfg.AuthURL, httpx.NewClient(httpx.ClientConfig{Timeout: cfg.AuthTimeout}))
	} else {
		log.Warn("rights are checked by token claims (AUTH_MODE=dev); not for production")
	}

	reg := metrics.New("loyalty")
	m := service.NewMetrics()
	reg.MustRegister("loyalty", m.Collectors()...)
	checks := []httpx.Check{{Name: "db", Fn: pgxutil.Ping(pool)}}

	var wg sync.WaitGroup
	var waker service.Waker
	// Outbox relay → Kafka (включается, если задан KAFKA_BROKERS; иначе события копятся в outbox).
	if len(cfg.KafkaBrokers) > 0 {
		prod, err := kafka.NewProducer(cfg.KafkaBrokers, "loyalty-service")
		if err != nil {
			return err
		}
		defer prod.Close()
		reg.MustRegister("loyalty", kafka.OutboxCollectors(pool)...)
		checks = append(checks, httpx.Check{Name: "kafka", Fn: prod.Ping})
		relay := &kafka.Relay{Pool: pool, Pub: prod, Log: log}
		waker = relay
		wg.Add(1)
		go func() { defer wg.Done(); _ = relay.Run(ctx) }()
	}

	idem := idempotency.NewStore(pool)
	wg.Add(1)
	go func() { defer wg.Done(); idem.Run(ctx, log) }()

	svc := service.New(service.Deps{Pool: pool, Authz: authorizer, Waker: waker, Metrics: m})
	handler := httpapi.New(httpapi.Deps{Log: log, Metrics: reg, Auth: auth, Checks: checks, Promotions: svc, Idempotency: idem})
	srv := httpx.NewServer(":"+cfg.HTTPPort, handler)
	err = httpx.Run(ctx, srv, log, 10*time.Second)
	stop()
	wg.Wait()
	return err
}
