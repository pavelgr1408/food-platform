// notification-service: push-уведомления клиентам по orders.status. Регистрация токенов устройств (REST),
// консьюмер orders.status → очередь уведомлений в БД, отдельный процесс отправки через APNs/FCM/HMS
// (в dev — моки) с повторами. Сбой push не влияет на заказ.
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
	"github.com/pavelgr1408/food-platform/services/notification/internal/config"
	"github.com/pavelgr1408/food-platform/services/notification/internal/httpapi"
	"github.com/pavelgr1408/food-platform/services/notification/internal/push"
	"github.com/pavelgr1408/food-platform/services/notification/internal/service"
	"github.com/pavelgr1408/food-platform/services/notification/internal/store"
	"github.com/pavelgr1408/food-platform/services/notification/migrations"
)

func main() {
	cfg, err := config.Load()
	log := logging.New(logging.Options{Service: "notification", Env: cfg.Env, Level: cfg.LogLevel})
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

	pool, err := pgxutil.Open(ctx, cfg.DatabaseURL, pgxutil.Options{AppName: "notification-service"})
	if err != nil {
		return err
	}
	defer pool.Close()

	// Миграции: сервиса + inbox (processed_events). Сервис ничего не публикует в Kafka, поэтому
	// outbox-таблицы ему не нужны, но модуль kafka общий. В k8s — отдельной Job: `--migrate-only`.
	if err := pgxutil.Migrate(ctx, pool, log,
		pgxutil.Source{Module: "notification", FS: migrations.FS},
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
	reg := metrics.New("notification")
	m := service.NewMetrics()
	reg.MustRegister("notification", m.Collectors()...)
	reg.MustRegister("notification", statsCollectors(pool)...)
	checks := []httpx.Check{{Name: "db", Fn: pgxutil.Ping(pool)}}

	svc := service.New(service.Deps{Pool: pool, Sender: push.NewRouter(cfg.Push), Log: log, Metrics: m, Config: cfg.Service})

	var wg sync.WaitGroup
	if len(cfg.KafkaBrokers) > 0 {
		// Отдельная группа и собственный DLQ (orders.status.dlq): сбой обработки уведомления не блокирует
		// остальных потребителей orders.status (payment, delivery) — у каждого своя группа.
		consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{Brokers: cfg.KafkaBrokers, Group: "notification-service",
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
	} else {
		log.Warn("KAFKA_BROKERS is empty: orders.status is not consumed, no notifications will be queued")
	}
	wg.Add(1)
	go func() { defer wg.Done(); svc.RunSender(ctx, cfg.SendInterval) }()

	handler := httpapi.New(httpapi.Deps{Log: log, Metrics: reg, Auth: auth, Checks: checks, Tokens: svc})
	srv := httpx.NewServer(":"+cfg.HTTPPort, handler)
	err = httpx.Run(ctx, srv, log, 10*time.Second)
	stop()
	wg.Wait()
	return err
}

// statsCollectors — алерты: очередь неотправленных уведомлений и возраст самого старого.
func statsCollectors(pool store.DBTX) []prometheus.Collector {
	st := store.New()
	q := func() (int64, float64) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		n, age, err := st.Stats(ctx, pool, time.Now())
		if err != nil {
			return -1, -1
		}
		return n, age
	}
	return []prometheus.Collector{
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "notification_pending",
			Help: "Уведомления, ожидающие отправки."}, func() float64 { n, _ := q(); return float64(n) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "notification_pending_oldest_age_seconds",
			Help: "Возраст самого старого неотправленного уведомления."}, func() float64 { _, a := q(); return a }),
	}
}
