package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/notification/internal/domain"
	"github.com/pavelgr1408/food-platform/services/notification/internal/service"
)

// Сквозной тест на in-memory брокере: orders.status → консьюмер (inbox) → очередь → отправка провайдеру.
// Повторная публикация того же события и «ядовитая» запись не дают лишнего push и не блокируют остальные.
func TestOrdersStatusEndToEndWithFakeBroker(t *testing.T) {
	e := setup(t, service.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(3, "orders.status", "orders.status.dlq"))
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()
	brokers := cluster.ListenAddrs()

	e.register(t, "alice", domain.FCM, "dev-1")
	// отправка идёт по реальным часам: сервис в этом тесте работает «как в бою»
	svc := service.New(service.Deps{Pool: e.pool, Sender: e.prov, Log: testkitLog(), Config: service.Config{}})

	cons, err := kafka.NewConsumer(kafka.ConsumerConfig{Brokers: brokers, Group: "notification-service", Topics: service.Topics(), Pool: e.pool,
		Handler: svc.HandleEvent, MaxAttempts: 2, Backoff: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer cons.Close()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() { _ = cons.Run(runCtx) }()
	go svc.RunSender(runCtx, 20*time.Millisecond)

	prod, err := kafka.NewProducer(brokers, "orders-test")
	if err != nil {
		t.Fatal(err)
	}
	defer prod.Close()
	order := newOrder()
	now := time.Now().UTC()
	mk := func(status string) kafka.Event {
		ev, err := kafka.NewEvent(ctx, "orders.status", "orders.status.v1", order, map[string]any{
			"orderId": order, "orderNumber": "R4-1", "status": status, "userId": "alice", "updatedAt": now})
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}
	ready := mk("ready")
	if err := prod.Publish(ctx, mk("accepted"), mk("cooking"), ready, ready, mk("handed_over"), mk("closed")); err != nil {
		t.Fatal(err)
	}
	raw, _ := kgo.NewClient(kgo.SeedBrokers(brokers...))
	defer raw.Close()
	if err := raw.ProduceSync(ctx, &kgo.Record{Topic: "orders.status", Key: []byte("poison"), Value: []byte("x")}).FirstErr(); err != nil {
		t.Fatal(err)
	}

	// ждём три push: accepted, ready, handed_over (cooking и closed уведомлений не порождают)
	deadline := time.Now().Add(30 * time.Second)
	for len(e.prov.Messages()) < 3 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond) // и убеждаемся, что лишнего не пришло
	got := map[domain.Type]int{}
	for _, m := range e.prov.Messages() {
		got[m.Type]++
	}
	if len(got) != 3 || got[domain.OrderCreated] != 1 || got[domain.OrderReady] != 1 || got[domain.HandedOver] != 1 {
		t.Fatalf("pushes by type: %v", got)
	}

	// «ядовитая» запись попала в DLQ
	dlq, _ := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumeTopics("orders.status.dlq"), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	defer dlq.Close()
	found := false
	for end := time.Now().Add(20 * time.Second); !found && time.Now().Before(end); {
		pctx, c := context.WithTimeout(ctx, 2*time.Second)
		dlq.PollFetches(pctx).EachRecord(func(r *kgo.Record) {
			if string(r.Key) == "poison" {
				found = true
			}
		})
		c()
	}
	if !found {
		t.Fatal("poison record must go to orders.status.dlq")
	}
}
