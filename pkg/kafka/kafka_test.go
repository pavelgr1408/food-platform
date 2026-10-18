package kafka_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/pkg/logging"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil/pgtest"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

func setup(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	pool := pgtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	if err := pgxutil.Migrate(ctx, pool, nil, kafka.Migrations()); err != nil {
		t.Fatal(err)
	}
	return ctx, pool
}

type fakePub struct {
	mu   sync.Mutex
	got  []kafka.Event
	fail atomic.Bool
}

func (f *fakePub) Publish(_ context.Context, evs ...kafka.Event) error {
	if f.fail.Load() {
		return errors.New("broker down")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, evs...)
	return nil
}
func (f *fakePub) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.got) }

func mustEvent(t *testing.T, topic, key string, payload any) kafka.Event {
	t.Helper()
	ev, err := kafka.NewEvent(logging.WithTraceID(context.Background(), "0af7651916cd43dd8448eb211c80319c"), topic, "test.v1", key, payload)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestOutboxAtomicWithBusinessTx(t *testing.T) {
	ctx, pool := setup(t)
	_, _ = pool.Exec(ctx, `CREATE TABLE orders(id text primary key)`)
	// откат бизнес-транзакции откатывает и событие
	_ = pgxutil.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, _ = tx.Exec(ctx, `INSERT INTO orders VALUES ('o1')`)
		if err := kafka.Enqueue(ctx, tx, mustEvent(t, "orders.state", "o1", map[string]int{"revision": 1})); err != nil {
			return err
		}
		return errors.New("rollback")
	})
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&n)
	if n != 0 {
		t.Fatalf("event leaked from rolled back tx: %d", n)
	}
	// коммит — заказ и событие вместе
	if err := pgxutil.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, _ = tx.Exec(ctx, `INSERT INTO orders VALUES ('o2')`)
		return kafka.Enqueue(ctx, tx, mustEvent(t, "orders.state", "o2", map[string]int{"revision": 1}))
	}); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&n)
	if n != 1 {
		t.Fatalf("pending=%d", n)
	}
}

// Сценарий S2-DoD: «процесс умер между записью и публикацией» — событие не теряется:
// новый relay (после рестарта) доставляет его.
func TestRelayDeliversAfterProducerCrash(t *testing.T) {
	ctx, pool := setup(t)
	if err := kafka.Enqueue(ctx, pool, mustEvent(t, "orders.state", "o1", map[string]int{"revision": 1})); err != nil {
		t.Fatal(err)
	}
	pub := &fakePub{}
	pub.fail.Store(true)
	r := &kafka.Relay{Pool: pool, Pub: pub}
	if n, err := r.Flush(ctx); err == nil || n != 0 {
		t.Fatalf("expected publish error, n=%d err=%v", n, err)
	}
	var attempts int
	var lastErr *string
	_ = pool.QueryRow(ctx, `SELECT attempts, last_error FROM outbox`).Scan(&attempts, &lastErr)
	if attempts != 1 || lastErr == nil {
		t.Fatalf("attempts=%d lastErr=%v", attempts, lastErr)
	}
	pub.fail.Store(false)
	r2 := &kafka.Relay{Pool: pool, Pub: pub} // «новый процесс»
	if n, err := r2.Flush(ctx); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if n, _ := r2.Flush(ctx); n != 0 || pub.count() != 1 {
		t.Fatalf("published twice? n=%d total=%d", n, pub.count())
	}
}

func TestConcurrentRelaysNoDuplicates(t *testing.T) {
	ctx, pool := setup(t)
	for i := 0; i < 200; i++ {
		if err := kafka.Enqueue(ctx, pool, mustEvent(t, "orders.state", "k", i)); err != nil {
			t.Fatal(err)
		}
	}
	pub := &fakePub{}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := &kafka.Relay{Pool: pool, Pub: pub, BatchSize: 20}
			for {
				n, err := r.Flush(ctx)
				if err != nil || n == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, e := range pub.got {
		if seen[e.ID.String()] {
			t.Fatal("duplicate publish across relays")
		}
		seen[e.ID.String()] = true
	}
	if len(seen) != 200 {
		t.Fatalf("published %d of 200", len(seen))
	}
}

func TestProcessOnceIdempotent(t *testing.T) {
	ctx, pool := setup(t)
	_, _ = pool.Exec(ctx, `CREATE TABLE effects(n int)`)
	ev := mustEvent(t, "orders.state", "o1", 1)
	h := func(ctx context.Context, tx pgx.Tx, _ kafka.Event) error {
		_, err := tx.Exec(ctx, `INSERT INTO effects VALUES (1)`)
		return err
	}
	// ошибка обработчика откатывает и запись inbox, и эффект
	if _, err := kafka.ProcessOnce(ctx, pool, "payment", ev, func(ctx context.Context, tx pgx.Tx, e kafka.Event) error {
		_ = h(ctx, tx, e)
		return errors.New("fail")
	}); err == nil {
		t.Fatal("expected error")
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM effects`).Scan(&n)
	if n != 0 {
		t.Fatal("effect must roll back with failed handler")
	}
	// конкурентная доставка одного и того же события — эффект ровно один
	var wg sync.WaitGroup
	var processed atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := kafka.ProcessOnce(ctx, pool, "payment", ev, h)
			if err != nil {
				t.Error(err)
			}
			if ok {
				processed.Add(1)
			}
		}()
	}
	wg.Wait()
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM effects`).Scan(&n)
	if n != 1 || processed.Load() != 1 {
		t.Fatalf("effects=%d processed=%d, want 1/1", n, processed.Load())
	}
	// другой консьюмер того же события — независимый inbox
	if ok, _ := kafka.ProcessOnce(ctx, pool, "production", ev, h); !ok {
		t.Fatal("other consumer must process once as well")
	}
}

// Сквозной тест на in-memory брокере (kfake): outbox → Kafka → consumer+inbox;
// дубликат (тот же event-id) игнорируется; падающий обработчик → DLQ; «ядовитая» запись → DLQ.
func TestEndToEndWithFakeBroker(t *testing.T) {
	ctx, pool := setup(t)
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(3, "orders.state", "orders.state.dlq"))
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()
	brokers := cluster.ListenAddrs()

	prod, err := kafka.NewProducer(brokers, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer prod.Close()

	_, _ = pool.Exec(ctx, `CREATE TABLE effects(order_id text)`)
	cctx, stop := context.WithCancel(ctx)
	defer stop()
	cons, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers: brokers, Group: "payment", Topics: []string{"orders.state"}, Pool: pool,
		MaxAttempts: 2, Backoff: 10 * time.Millisecond,
		Handler: func(ctx context.Context, tx pgx.Tx, ev kafka.Event) error {
			if string(ev.Key) == "bad" {
				return errors.New("cannot handle")
			}
			_, err := tx.Exec(ctx, `INSERT INTO effects VALUES ($1)`, ev.Key)
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cons.Close()
	go func() { _ = cons.Run(cctx) }()

	good := mustEvent(t, "orders.state", "ok-1", map[string]int{"revision": 1})
	bad := mustEvent(t, "orders.state", "bad", map[string]int{"revision": 1})
	// good публикуем дважды (доставка at-least-once)
	if err := prod.Publish(ctx, good, good, bad); err != nil {
		t.Fatal(err)
	}
	// «ядовитая» запись без заголовков конверта
	raw, _ := kgo.NewClient(kgo.SeedBrokers(brokers...))
	defer raw.Close()
	if err := raw.ProduceSync(ctx, &kgo.Record{Topic: "orders.state", Key: []byte("poison"), Value: []byte("x")}).FirstErr(); err != nil {
		t.Fatal(err)
	}

	// ждём: 1 эффект и 2 записи в DLQ
	dlq, _ := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumeTopics("orders.state.dlq"), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	defer dlq.Close()
	got := map[string]string{}
	deadline := time.Now().Add(30 * time.Second)
	for len(got) < 2 && time.Now().Before(deadline) {
		pctx, c := context.WithTimeout(ctx, 2*time.Second)
		dlq.PollFetches(pctx).EachRecord(func(r *kgo.Record) {
			for _, h := range r.Headers {
				if h.Key == kafka.HeaderDLQError {
					got[string(r.Key)] = string(h.Value)
				}
			}
		})
		c()
	}
	if len(got) != 2 || got["bad"] == "" || got["poison"] == "" {
		t.Fatalf("DLQ content: %v", got)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM effects WHERE order_id='ok-1'`).Scan(&n)
	if n != 1 {
		t.Fatalf("duplicate delivery produced %d effects, want 1", n)
	}
}

func TestEventContextRestoresTrace(t *testing.T) {
	ev := mustEvent(t, "t", "k", 1)
	ctx := ev.Context(context.Background())
	if logging.TraceID(ctx) != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("trace id: %q", logging.TraceID(ctx))
	}
}
