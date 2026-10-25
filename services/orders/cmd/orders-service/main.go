// orders-service: корзина, заказ, жизненный цикл (UC-ORD-01/03/05, ADR-10).
// Публикует orders.state и orders.status через transactional outbox; создание заказа
// идемпотентно по Idempotency-Key. Эталон слоёв — services/auth.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // distroless без zoneinfo: часовые пояса ресторанов (номер заказа по местной дате)

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/idempotency"
	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/pkg/logging"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/orders/internal/clients"
	"github.com/pavelgr1408/food-platform/services/orders/internal/config"
	"github.com/pavelgr1408/food-platform/services/orders/internal/httpapi"
	"github.com/pavelgr1408/food-platform/services/orders/internal/service"
	"github.com/pavelgr1408/food-platform/services/orders/migrations"
)

func main() {
	cfg, err := config.Load()
	log := logging.New(logging.Options{Service: "orders", Env: cfg.Env, Level: cfg.LogLevel})
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

	pool, err := pgxutil.Open(ctx, cfg.DatabaseURL, pgxutil.Options{AppName: "orders-service"})
	if err != nil {
		return err
	}
	defer pool.Close()

	// Миграции: сервиса + outbox/inbox + ключи идемпотентности. В k8s — отдельной Job: `--migrate-only`.
	if err := pgxutil.Migrate(ctx, pool, log,
		pgxutil.Source{Module: "orders", FS: migrations.FS},
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
	reg := metrics.New("orders")
	m := service.NewMetrics()
	reg.MustRegister("orders", m.Collectors()...)
	reg.MustRegister("orders", service.StuckCollectors(pool)...)
	checks := []httpx.Check{{Name: "db", Fn: pgxutil.Ping(pool)}}

	var wg sync.WaitGroup
	var waker service.Waker
	// Outbox relay → Kafka. Без KAFKA_BROKERS заказы работают, события копятся в outbox.
	if len(cfg.KafkaBrokers) > 0 {
		prod, err := kafka.NewProducer(cfg.KafkaBrokers, "orders-service")
		if err != nil {
			return err
		}
		defer prod.Close()
		reg.MustRegister("orders", kafka.OutboxCollectors(pool)...)
		checks = append(checks, httpx.Check{Name: "kafka", Fn: prod.Ping})
		relay := &kafka.Relay{Pool: pool, Pub: prod, Log: log}
		waker = relay
		wg.Add(1)
		go func() { defer wg.Done(); _ = relay.Run(ctx) }()
	} else {
		log.Warn("KAFKA_BROKERS is empty: events accumulate in the outbox and are not published")
	}

	idem := idempotency.NewStore(pool)
	wg.Add(1)
	go func() { defer wg.Done(); idem.Run(ctx, log) }()

	// Исходящий клиент: таймаут на попытку, один ретрай, circuit breaker (по экземпляру на upstream).
	up := func() *httpx.Client {
		return httpx.NewClient(httpx.ClientConfig{Timeout: cfg.UpstreamTimeout, MaxRetries: 1})
	}
	promoTTL := cfg.PromotionCacheTTL
	if promoTTL == 0 {
		promoTTL = -1 // 0 в окружении = без кэша
	}
	svc := service.New(service.Deps{
		Pool:        pool,
		Menu:        &clients.Menu{BaseURL: cfg.MenuURL, HTTP: up()},
		Restaurants: &clients.Restaurant{BaseURL: cfg.RestaurantURL, HTTP: up()},
		Waker:       waker,
		Log:         log,
		Metrics:     m,
		Config: service.Config{PaymentTimeout: cfg.PaymentTimeout, UnknownGrace: cfg.UnknownGrace, CartTTL: cfg.CartTTL,
			PrepMinutes: cfg.PrepMinutes, DeliveryMinutes: cfg.DeliveryMinutes, LeadTimeTTL: cfg.LeadTimeTTL, DeliveryEtaTTL: cfg.DeliveryEtaTTL,
			NotReceivedAfter: cfg.NotReceivedAfter, PromotionCacheTTL: promoTTL},
	})
	wg.Add(1)
	go func() { defer wg.Done(); svc.RunJanitor(ctx, cfg.JanitorInterval) }()

	// Консьюмеры Kafka. Группы раздельные: «ядовитое» или медленное событие производства не задерживает оплату.
	//   orders-service            orders.payments.status (S3): paid → accepted + повторный orders.state (ADR-10);
	//   orders-service-production orders.dish.status и production.lead.time (S4): accepted → cooking → ready, ETA;
	//   orders-service-delivery   orders.delivery.status (S7): ready → on_delivery → delivered → closed, E2;
	//   orders-service-promotions promotions.discount (S8): реплика акций для расчёта скидки (читается с начала компактного топика).
	if len(cfg.KafkaBrokers) > 0 {
		for _, c := range []struct {
			group  string
			topics []string
			h      kafka.TxHandler
		}{
			{"orders-service", []string{service.TopicPaymentStatus}, svc.HandlePaymentStatus},
			{"orders-service-production", []string{service.TopicDishStatus, service.TopicLeadTime}, svc.HandleProduction},
			{"orders-service-delivery", []string{service.TopicDeliveryStatus}, svc.HandleDeliveryStatus},
			{"orders-service-promotions", []string{service.TopicPromotions}, svc.HandlePromotion},
		} {
			cons, err := kafka.NewConsumer(kafka.ConsumerConfig{Brokers: cfg.KafkaBrokers, Group: c.group,
				Topics: c.topics, Pool: pool, Handler: c.h, Log: log})
			if err != nil {
				return err
			}
			defer cons.Close()
			wg.Add(1)
			go func(group string) {
				defer wg.Done()
				if err := cons.Run(ctx); err != nil && ctx.Err() == nil {
					log.Error("consumer stopped", "group", group, "error", err)
				}
			}(c.group)
		}
	}

	handler := httpapi.New(httpapi.Deps{Log: log, Metrics: reg, Auth: auth, Checks: checks, Idempotency: idem, Orders: svc})
	srv := httpx.NewServer(":"+cfg.HTTPPort, handler)
	err = httpx.Run(ctx, srv, log, 10*time.Second)
	stop()
	wg.Wait()
	return err
}
