package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/service"
)

type flakyPub struct {
	inner kafka.Publisher
	fail  bool
}

func (f *flakyPub) Publish(ctx context.Context, evs ...kafka.Event) error {
	if f.fail {
		return errors.New("broker is down")
	}
	return f.inner.Publish(ctx, evs...)
}

// DoD S2: «убить процесс между записью заказа и публикацией» — событие всё равно уходит.
// Модель: сервис создал заказ и «умер» (relay не работал); новый экземпляр с чистой памятью
// поднимает relay над той же БД и доставляет события в Kafka (kfake), ровно по одному разу.
func TestOrderEventsSurviveCrashBeforePublish(t *testing.T) {
	e := newEnv(t)
	o := e.order(t, "", domain.PayOnlineCard) // relay нет: процесс «упал» сразу после коммита

	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(3, "orders.state", "orders.status"))
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()
	brokers := cluster.ListenAddrs()
	prod, err := kafka.NewProducer(brokers, "orders-restarted")
	if err != nil {
		t.Fatal(err)
	}
	defer prod.Close()

	// сначала брокер «недоступен»: события не теряются и остаются неопубликованными
	fp := &flakyPub{inner: prod, fail: true}
	relay := &kafka.Relay{Pool: e.pool, Pub: fp, Log: testkitLog()}
	if _, err := relay.Flush(ctx); err == nil {
		t.Fatal("flush must fail while the broker is down")
	}
	var pending int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&pending)
	if pending != 2 {
		t.Fatalf("events must stay pending, got %d", pending)
	}
	// брокер вернулся
	fp.fail = false
	n, err := relay.Flush(ctx)
	if err != nil || n != 2 {
		t.Fatalf("flush: n=%d err=%v", n, err)
	}
	if n, _ := relay.Flush(ctx); n != 0 {
		t.Fatalf("second flush must publish nothing (no duplicates), got %d", n)
	}

	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumeTopics("orders.state", "orders.status"),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	got := map[string]*kgo.Record{}
	deadline := time.Now().Add(20 * time.Second)
	for len(got) < 2 && time.Now().Before(deadline) {
		pctx, c := context.WithTimeout(ctx, 2*time.Second)
		cl.PollFetches(pctx).EachRecord(func(r *kgo.Record) { got[r.Topic] = r })
		c()
	}
	if len(got) != 2 {
		t.Fatalf("got topics: %v", got)
	}
	for topic, r := range got {
		if string(r.Key) != o.ID {
			t.Errorf("%s: partition key %q, want orderId %s", topic, r.Key, o.ID)
		}
		hdr := map[string]string{}
		for _, h := range r.Headers {
			hdr[h.Key] = string(h.Value)
		}
		if hdr["event-id"] == "" || hdr["occurred-at"] == "" || hdr["event-type"] != map[string]string{"orders.state": service.TypeState, "orders.status": service.TypeStatus}[topic] {
			t.Errorf("%s: envelope headers: %v", topic, hdr)
		}
	}
	var st service.OrderStateEvent
	if err := json.Unmarshal(got["orders.state"].Value, &st); err != nil || st.OrderID != o.ID || st.Revision != 1 || st.PaymentStatus != "pending" {
		t.Fatalf("state payload: %v %+v", err, st)
	}
}
