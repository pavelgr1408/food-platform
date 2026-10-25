// delivery-service: заявка на курьера по orders.state, статусы курьерской системы (вебхук + опрос),
// координаты курьера в delivery-Redis, публикация orders.delivery.status.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/pkg/logging"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/pkg/redisx"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/config"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/courier"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/httpapi"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/location"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/restaurant"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/service"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/store"
	"github.com/pavelgr1408/food-platform/services/delivery/migrations"
)

func main() {
	cfg, err := config.Load()
	log := logging.New(logging.Options{Service: "delivery", Env: cfg.Env, Level: cfg.LogLevel})
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

	pool, err := pgxutil.Open(ctx, cfg.DatabaseURL, pgxutil.Options{AppName: "delivery-service"})
	if err != nil {
		return err
	}
	defer pool.Close()

	// Миграции: сервиса + outbox/inbox. В k8s — отдельной Job: `--migrate-only`.
	if err := pgxutil.Migrate(ctx, pool, log,
		pgxutil.Source{Module: "delivery", FS: migrations.FS},
		kafka.Migrations()); err != nil {
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
	reg := metrics.New("delivery")
	m := service.NewMetrics()
	reg.MustRegister("delivery", m.Collectors()...)
	reg.MustRegister("delivery", statsCollectors(pool)...)
	checks := []httpx.Check{{Name: "db", Fn: pgxutil.Ping(pool)}}

	// Координаты курьера: Redis (REDIS_ADDR) или, без него, память процесса (только dev/один под).
	var locs location.Store
	if cfg.RedisAddr != "" {
		rc, err := redisx.New(redisx.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
		if err != nil {
			return err
		}
		defer rc.Close()
		locs = location.Redis{C: rc}
		// Redis не критичен для готовности: без него отказывают только координаты (503 LOCATION_STORE_UNAVAILABLE).
		reg.MustRegister("delivery", redisUp(rc)...)
	} else {
		log.Warn("REDIS_ADDR is empty: courier locations are kept in process memory (single replica only)")
		locs = location.NewMemory()
	}

	// Курьерская система и restaurant-service: вызовы вне транзакций; ретраи — дело reconciler.
	cr := &courier.Client{BaseURL: cfg.CourierURL, APIKey: cfg.CourierAPIKey,
		HTTP: httpx.NewClient(httpx.ClientConfig{Timeout: cfg.CourierTimeout, MaxRetries: -1})}
	rest := &restaurant.Client{BaseURL: cfg.RestaurantURL,
		HTTP: httpx.NewClient(httpx.ClientConfig{Timeout: cfg.CourierTimeout})}

	var wg sync.WaitGroup
	var waker service.Waker
	if len(cfg.KafkaBrokers) > 0 {
		prod, err := kafka.NewProducer(cfg.KafkaBrokers, "delivery-service")
		if err != nil {
			return err
		}
		defer prod.Close()
		reg.MustRegister("delivery", kafka.OutboxCollectors(pool)...)
		checks = append(checks, httpx.Check{Name: "kafka", Fn: prod.Ping})
		relay := &kafka.Relay{Pool: pool, Pub: prod, Log: log}
		waker = relay
		wg.Add(1)
		go func() { defer wg.Done(); _ = relay.Run(ctx) }()
	} else {
		log.Warn("KAFKA_BROKERS is empty: delivery events accumulate in the outbox and orders.state is not consumed")
	}

	svc := service.New(service.Deps{Pool: pool, Courier: cr, Restaurants: rest, Locations: locs, Waker: waker, Log: log, Metrics: m,
		Config: service.Config{SearchTimeout: cfg.SearchTimeout, LocationTTL: cfg.LocationTTL, PollEvery: cfg.PollEvery,
			PollMax: cfg.PollMax, LinkWait: cfg.LinkWait}})

	if len(cfg.KafkaBrokers) > 0 {
		consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{Brokers: cfg.KafkaBrokers, Group: "delivery-service",
			Topics: service.Topics(), Pool: pool, Handler: svc.HandleEvent, Log: log})
		if err != nil {
			return err
		}
		defer consumer.Close()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := consumer.Run(ctx); err != nil && ctx.Err() == nil {
				log.Error("consumer stopped", "error", err)
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); svc.RunReconciler(ctx, cfg.ReconcileInterval) }()

	handler := httpapi.New(httpapi.Deps{Log: log, Metrics: reg, Auth: auth, Checks: checks, Deliveries: svc,
		CourierWebhookSecret: []byte(cfg.CourierWebhookSecret)})
	srv := httpx.NewServer(":"+cfg.HTTPPort, handler)
	err = httpx.Run(ctx, srv, log, 10*time.Second)
	stop()
	wg.Wait()
	return err
}

// statsCollectors — алерты: активные доставки и возраст самой старой в поиске курьера.
func statsCollectors(pool store.DBTX) []prometheus.Collector {
	st := store.New()
	q := func() (int64, float64) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		n, age, err := st.Stats(ctx, pool)
		if err != nil {
			return -1, -1
		}
		return n, age
	}
	return []prometheus.Collector{
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "delivery_active",
			Help: "Активные доставки (не delivered/failed)."}, func() float64 { n, _ := q(); return float64(n) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "delivery_searching_oldest_age_seconds",
			Help: "Возраст самой старой доставки в поиске курьера."}, func() float64 { _, a := q(); return a }),
	}
}

// redisUp — 1, если Redis отвечает на PING.
func redisUp(rc *redisx.Client) []prometheus.Collector {
	return []prometheus.Collector{prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "delivery_redis_up",
		Help: "1, если delivery-Redis отвечает."}, func() float64 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if rc.Ping(ctx) != nil {
			return 0
		}
		return 1
	})}
}
