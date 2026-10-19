// payment-service: оплата заказов (UC-PAY-01/02, каркас UC-PAY-04). Платёж пишется в БД до вызова
// шлюза, статусы публикуются в orders.payments.status через transactional outbox, вебхуки подписаны
// HMAC, итог «неизвестных» платежей добывает reconciler. Эталон слоёв — services/auth.
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
	"github.com/pavelgr1408/food-platform/pkg/idempotency"
	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/pkg/logging"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/payment/internal/config"
	"github.com/pavelgr1408/food-platform/services/payment/internal/gateway"
	"github.com/pavelgr1408/food-platform/services/payment/internal/httpapi"
	"github.com/pavelgr1408/food-platform/services/payment/internal/service"
	"github.com/pavelgr1408/food-platform/services/payment/internal/store"
	"github.com/pavelgr1408/food-platform/services/payment/migrations"
)

func main() {
	cfg, err := config.Load()
	log := logging.New(logging.Options{Service: "payment", Env: cfg.Env, Level: cfg.LogLevel})
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

	pool, err := pgxutil.Open(ctx, cfg.DatabaseURL, pgxutil.Options{AppName: "payment-service"})
	if err != nil {
		return err
	}
	defer pool.Close()

	// Миграции: сервиса + outbox/inbox + ключи идемпотентности. В k8s — отдельной Job: `--migrate-only`.
	if err := pgxutil.Migrate(ctx, pool, log,
		pgxutil.Source{Module: "payment", FS: migrations.FS},
		kafka.Migrations(), idempotency.Migrations()); err != nil {
		return err
	}
	if pgxutil.MigrateOnly(os.Args[1:]) {
		log.Info("migrations applied, exiting (--migrate-only)")
		return nil
	}

	auth, err := authclient.NewFromMode(cfg.AuthMode, cfg.Env, log)
	if err != nil {
		return err
	}
	reg := metrics.New("payment")
	m := service.NewMetrics()
	reg.MustRegister("payment", m.Collectors()...)
	reg.MustRegister("payment", unknownCollectors(pool)...)
	checks := []httpx.Check{{Name: "db", Fn: pgxutil.Ping(pool)}}

	var wg sync.WaitGroup
	var waker service.Waker
	var consumer *kafka.Consumer
	var svc *service.Service
	// Шлюз: без ретраев на уровне HTTP (вызов из запроса пользователя должен быть быстрым; повтор — дело reconciler,
	// Create идемпотентен по ключу), circuit breaker оберегает от лавины запросов к упавшему шлюзу.
	gw := &gateway.Client{BaseURL: cfg.GatewayURL, APIKey: cfg.GatewayAPIKey,
		HTTP: httpx.NewClient(httpx.ClientConfig{Timeout: cfg.GatewayTimeout, MaxRetries: -1})}

	// Outbox relay → Kafka и консьюмеры orders.state/orders.status. Без KAFKA_BROKERS платежи работают
	// (события копятся в outbox), но заказы о них не узнают, а реплика заказов не наполняется.
	var relay *kafka.Relay
	if len(cfg.KafkaBrokers) > 0 {
		prod, err := kafka.NewProducer(cfg.KafkaBrokers, "payment-service")
		if err != nil {
			return err
		}
		defer prod.Close()
		reg.MustRegister("payment", kafka.OutboxCollectors(pool)...)
		checks = append(checks, httpx.Check{Name: "kafka", Fn: prod.Ping})
		relay = &kafka.Relay{Pool: pool, Pub: prod, Log: log}
		waker = relay
		wg.Add(1)
		go func() { defer wg.Done(); _ = relay.Run(ctx) }()
	} else {
		log.Warn("KAFKA_BROKERS is empty: payment events accumulate in the outbox and orders.state is not consumed")
	}

	svc = service.New(service.Deps{Pool: pool, Gateway: gw, Waker: waker, Log: log, Metrics: m,
		Config: service.Config{GatewayTimeout: cfg.GatewayTimeout, WebhookWait: cfg.WebhookWait, PendingExpiry: cfg.PendingExpiry,
			PollMin: cfg.PollMin, PollMax: cfg.PollMax, DefaultReturnURL: cfg.DefaultReturnURL}})

	if len(cfg.KafkaBrokers) > 0 {
		consumer, err = kafka.NewConsumer(kafka.ConsumerConfig{Brokers: cfg.KafkaBrokers, Group: "payment-service",
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

	idem := idempotency.NewStore(pool)
	wg.Add(1)
	go func() { defer wg.Done(); idem.Run(ctx, log) }()
	wg.Add(1)
	go func() { defer wg.Done(); svc.RunReconciler(ctx, cfg.ReconcileInterval) }()

	handler := httpapi.New(httpapi.Deps{Log: log, Metrics: reg, Auth: auth, Checks: checks, Idempotency: idem, Payments: svc,
		GatewayWebhookSecret: []byte(cfg.GatewayWebhookSecret), POSWebhookSecret: []byte(cfg.POSWebhookSecret)})
	srv := httpx.NewServer(":"+cfg.HTTPPort, handler)
	err = httpx.Run(ctx, srv, log, 10*time.Second)
	stop()
	wg.Wait()
	return err
}

// unknownCollectors — алерты: платежи в unknown и возраст самого старого (деньги «в подвешенном» состоянии).
func unknownCollectors(pool store.DBTX) []prometheus.Collector {
	st := store.New()
	q := func() (int64, float64) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		n, age, err := st.UnknownStats(ctx, pool)
		if err != nil {
			return -1, -1
		}
		return n, age
	}
	return []prometheus.Collector{
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "payment_unknown_payments",
			Help: "Платежей со статусом unknown (итог не получен от шлюза)."}, func() float64 { n, _ := q(); return float64(n) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "payment_unknown_oldest_age_seconds",
			Help: "Возраст самого старого платежа в unknown."}, func() float64 { _, a := q(); return a }),
	}
}
