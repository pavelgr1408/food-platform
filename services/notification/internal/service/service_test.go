package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/kafka"
	"github.com/pavelgr1408/food-platform/services/notification/internal/domain"
	"github.com/pavelgr1408/food-platform/services/notification/internal/push"
	"github.com/pavelgr1408/food-platform/services/notification/internal/service"
	"github.com/pavelgr1408/food-platform/services/notification/internal/store"
	"github.com/pavelgr1408/food-platform/services/notification/internal/testkit"
)

type env struct {
	pool  *pgxpool.Pool
	svc   *service.Service
	prov  *testkit.Provider
	clock *testkit.Clock
	st    *store.Store
}

func setup(t *testing.T, cfg service.Config) *env {
	t.Helper()
	pool := testkit.Pool(t)
	clock, prov := testkit.NewClock(), testkit.NewProvider()
	svc := service.New(service.Deps{Pool: pool, Sender: prov, Log: testkit.Log(), Clock: clock.Now, Config: cfg})
	return &env{pool: pool, svc: svc, prov: prov, clock: clock, st: store.New()}
}

func (e *env) register(t *testing.T, user string, p domain.Platform, token string) domain.DeviceToken {
	t.Helper()
	tok, _, err := e.svc.Register(context.Background(), service.RegisterInput{UserID: user, Platform: p, Token: token, Permissions: true, Locale: domain.RU})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// status публикует orders.status через inbox-обработчик, как это делает консьюмер.
func (e *env) status(t *testing.T, orderID, user, status, reason string, at time.Time) error {
	t.Helper()
	payload := map[string]any{"orderId": orderID, "orderNumber": "R4-261025-0001", "status": status, "updatedAt": at.UTC().Format(time.RFC3339Nano)}
	if user != "" {
		payload["userId"] = user
	}
	if reason != "" {
		payload["reason"] = reason
	}
	return e.handle(t, payload)
}

func (e *env) handle(t *testing.T, payload any) error {
	t.Helper()
	b, _ := json.Marshal(payload)
	ev := kafka.Event{ID: uuid.New(), Topic: service.TopicStatus, Type: "orders.status.v1", Payload: b}
	_, err := kafka.ProcessOnce(context.Background(), e.pool, "notification-service", ev, e.svc.HandleEvent)
	return err
}

func (e *env) rows(t *testing.T, orderID string) []store.Row {
	t.Helper()
	r, err := e.st.ByOrder(context.Background(), e.pool, orderID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func newOrder() string { return uuid.NewString() }

func TestRegisterCreateUpdateAndList(t *testing.T) {
	e := setup(t, service.Config{})
	ctx := context.Background()
	tok, created, err := e.svc.Register(ctx, service.RegisterInput{UserID: "u1", Platform: domain.FCM, Token: "t1", Permissions: true, Locale: domain.RU})
	if err != nil || !created || tok.ID == "" || tok.Locale != domain.RU || !tok.PermissionsGranted {
		t.Fatalf("%+v %v %v", tok, created, err)
	}
	e.clock.Advance(time.Hour)
	again, created, err := e.svc.Register(ctx, service.RegisterInput{UserID: "u1", Platform: domain.FCM, Token: "t1", Permissions: false, Locale: domain.EN})
	if err != nil || created || again.ID != tok.ID || again.PermissionsGranted || again.Locale != domain.EN || !again.RegisteredAt.Equal(tok.RegisteredAt) || tok.RegisteredAt.Nanosecond()%1000 != 0 {
		t.Fatalf("re-registration must update in place and keep registeredAt: %+v %v %v", again, created, err)
	}
	// тот же токен на другой платформе — другое устройство
	other := e.register(t, "u1", domain.APNS, "t1")
	if other.ID == tok.ID {
		t.Fatal("(platform, token) is the key")
	}
	list, err := e.svc.List(ctx, "u1")
	if err != nil || len(list) != 2 || list[0].ID != other.ID {
		t.Fatalf("newest first: %+v %v", list, err)
	}
	if l, _ := e.svc.List(ctx, "someone-else"); len(l) != 0 {
		t.Fatalf("foreign tokens must not leak: %+v", l)
	}
}

func TestRegisterValidation(t *testing.T) {
	e := setup(t, service.Config{})
	for name, in := range map[string]service.RegisterInput{
		"platform": {UserID: "u", Platform: "sms", Token: "x"},
		"empty":    {UserID: "u", Platform: domain.FCM, Token: ""},
		"space":    {UserID: "u", Platform: domain.FCM, Token: "a b"},
		"long":     {UserID: "u", Platform: domain.FCM, Token: strings.Repeat("x", 5000)},
	} {
		_, _, err := e.svc.Register(context.Background(), in)
		var he *httpx.Error
		if !errors.As(err, &he) || he.Status != 400 || he.Code != "VALIDATION_FAILED" {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, _, err := e.svc.Register(context.Background(), service.RegisterInput{Platform: domain.FCM, Token: "x"})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != 401 {
		t.Errorf("guest: %v", err)
	}
}

func TestRegisterMovesTokenToNewUserAndDropsPending(t *testing.T) {
	e := setup(t, service.Config{})
	ctx := context.Background()
	a := e.register(t, "alice", domain.FCM, "shared")
	order := newOrder()
	if err := e.status(t, order, "alice", "ready", "", e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if len(e.rows(t, order)) != 1 {
		t.Fatal("alice's notification must be queued")
	}
	e.clock.Advance(time.Minute)
	b, created, err := e.svc.Register(ctx, service.RegisterInput{UserID: "bob", Platform: domain.FCM, Token: "shared", Permissions: true, Locale: domain.RU})
	if err != nil || created || b.ID != a.ID || b.UserID != "bob" || !b.RegisteredAt.After(a.RegisteredAt) {
		t.Fatalf("token must move to bob: %+v %v %v", b, created, err)
	}
	if l, _ := e.svc.List(ctx, "alice"); len(l) != 0 {
		t.Fatalf("alice lost the device: %+v", l)
	}
	if n, err := e.svc.Tick(ctx); err != nil || n != 0 || e.prov.CallCount() != 0 {
		t.Fatalf("alice's pending push must not reach bob: n=%d calls=%d err=%v", n, e.prov.CallCount(), err)
	}
}

func TestRegisterKeepsTenNewestTokens(t *testing.T) {
	e := setup(t, service.Config{})
	var first domain.DeviceToken
	for i := 0; i < 12; i++ {
		tok := e.register(t, "u", domain.FCM, fmt.Sprintf("t%02d", i))
		if i == 0 {
			first = tok
		}
		e.clock.Advance(time.Second)
	}
	list, _ := e.svc.List(context.Background(), "u")
	if len(list) != domain.MaxTokensPerUser {
		t.Fatalf("got %d tokens", len(list))
	}
	for _, tk := range list {
		if tk.ID == first.ID || tk.Token == "t01" {
			t.Fatalf("oldest tokens must be evicted: %s", tk.Token)
		}
	}
	if list[0].Token != "t11" {
		t.Fatalf("newest first, got %s", list[0].Token)
	}
}

func TestRegisterConcurrentSameToken(t *testing.T) {
	e := setup(t, service.Config{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, ids := 0, map[string]bool{}
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tok, c, err := e.svc.Register(context.Background(), service.RegisterInput{UserID: fmt.Sprintf("u%d", i%2), Platform: domain.APNS, Token: "same", Permissions: true, Locale: domain.RU})
			if err != nil {
				t.Errorf("register: %v", err)
				return
			}
			mu.Lock()
			ids[tok.ID] = true
			if c {
				created++
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if created != 1 || len(ids) != 1 {
		t.Fatalf("exactly one token must be created: created=%d ids=%d", created, len(ids))
	}
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM device_tokens WHERE token='same'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
}

func TestDelete(t *testing.T) {
	e := setup(t, service.Config{})
	ctx := context.Background()
	tok := e.register(t, "alice", domain.FCM, "a1")
	assert404 := func(err error) {
		t.Helper()
		var he *httpx.Error
		if !errors.As(err, &he) || he.Status != 404 || he.Code != "DEVICE_TOKEN_NOT_FOUND" {
			t.Fatalf("want 404 DEVICE_TOKEN_NOT_FOUND, got %v", err)
		}
	}
	assert404(e.svc.Delete(ctx, "mallory", tok.ID)) // чужой
	assert404(e.svc.Delete(ctx, "alice", uuid.NewString()))
	assert404(e.svc.Delete(ctx, "alice", "not-a-uuid"))
	// неотправленные уведомления удаляются вместе с токеном
	order := newOrder()
	_ = e.status(t, order, "alice", "ready", "", e.clock.Now())
	if err := e.svc.Delete(ctx, "alice", tok.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.rows(t, order)) != 0 {
		t.Fatal("pending notifications must go with the token")
	}
	assert404(e.svc.Delete(ctx, "alice", tok.ID)) // повтор
}

func TestHandleEventQueuesPerDeviceWithLocale(t *testing.T) {
	e := setup(t, service.Config{})
	ctx := context.Background()
	e.register(t, "alice", domain.APNS, "ios-1")
	if _, _, err := e.svc.Register(ctx, service.RegisterInput{UserID: "alice", Platform: domain.FCM, Token: "and-1", Permissions: true, Locale: domain.EN}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.Register(ctx, service.RegisterInput{UserID: "alice", Platform: domain.Huawei, Token: "hw-off", Permissions: false, Locale: domain.RU}); err != nil {
		t.Fatal(err)
	}
	e.register(t, "bob", domain.FCM, "bob-1")
	order := newOrder()
	if err := e.status(t, order, "alice", "ready", "", e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	rows := e.rows(t, order)
	if len(rows) != 2 {
		t.Fatalf("only alice's devices with permission: %+v", rows)
	}
	if n, err := e.svc.Tick(ctx); err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got := map[string]push.Message{}
	for _, m := range e.prov.Messages() {
		got[m.Token] = m
	}
	if got["ios-1"].Title != "Заказ готов" || got["and-1"].Title != "Order ready" || got["bob-1"].Token != "" || got["hw-off"].Token != "" {
		t.Fatalf("texts follow device locale: %+v", got)
	}
	if m := got["ios-1"]; m.Priority != domain.PriorityHigh || m.OrderID != order || m.Type != domain.OrderReady || m.Platform != domain.APNS {
		t.Fatalf("%+v", m)
	}
	for _, r := range e.rows(t, order) {
		if r.Status != domain.StatusSent || r.Attempts != 1 {
			t.Fatalf("%+v", r)
		}
	}
}

func TestHandleEventSkipsWhatShouldNotBePushed(t *testing.T) {
	e := setup(t, service.Config{MaxEventAge: time.Hour})
	e.register(t, "alice", domain.FCM, "a1")
	now := e.clock.Now()
	for _, st := range []string{"pending_payment", "cooking", "closed"} {
		order := newOrder()
		if err := e.status(t, order, "alice", st, "", now); err != nil || len(e.rows(t, order)) != 0 {
			t.Errorf("%s must not notify (err=%v)", st, err)
		}
	}
	guest := newOrder()
	if err := e.status(t, guest, "", "ready", "", now); err != nil || len(e.rows(t, guest)) != 0 {
		t.Errorf("guest/fax order must not notify (err=%v)", err)
	}
	noDevices := newOrder()
	if err := e.status(t, noDevices, "nobody", "ready", "", now); err != nil || len(e.rows(t, noDevices)) != 0 {
		t.Errorf("user without devices: err=%v", err)
	}
	stale := newOrder()
	if err := e.status(t, stale, "alice", "ready", "", now.Add(-2*time.Hour)); err != nil || len(e.rows(t, stale)) != 0 {
		t.Errorf("stale event must not notify (err=%v)", err)
	}
}

func TestHandleEventMalformedIsAnErrorForDLQ(t *testing.T) {
	e := setup(t, service.Config{})
	for name, p := range map[string]any{
		"no order id":  map[string]any{"status": "ready", "updatedAt": e.clock.Now()},
		"bad order id": map[string]any{"orderId": "x", "status": "ready", "updatedAt": e.clock.Now()},
		"no status":    map[string]any{"orderId": newOrder(), "updatedAt": e.clock.Now()},
		"no time":      map[string]any{"orderId": newOrder(), "status": "ready"},
	} {
		if err := e.handle(t, p); err == nil {
			t.Errorf("%s: must fail (→ retries → orders.status.dlq)", name)
		}
	}
	ev := kafka.Event{ID: uuid.New(), Topic: service.TopicStatus, Payload: []byte("{not json")}
	if _, err := kafka.ProcessOnce(context.Background(), e.pool, "notification-service", ev, e.svc.HandleEvent); err == nil {
		t.Error("broken JSON must fail")
	}
}

func TestDuplicateDeliveryDoesNotDuplicatePush(t *testing.T) {
	e := setup(t, service.Config{})
	ctx := context.Background()
	e.register(t, "alice", domain.FCM, "a1")
	order := newOrder()
	at := e.clock.Now()
	// то же событие дважды (inbox отсекает по event-id) и то же содержимое новым event-id (UNIQUE отсекает)
	payload := map[string]any{"orderId": order, "orderNumber": "N1", "status": "ready", "userId": "alice", "updatedAt": at}
	b, _ := json.Marshal(payload)
	ev := kafka.Event{ID: uuid.New(), Topic: service.TopicStatus, Payload: b}
	for i := 0; i < 2; i++ {
		if _, err := kafka.ProcessOnce(ctx, e.pool, "notification-service", ev, e.svc.HandleEvent); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.handle(t, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(e.prov.Messages()); n != 1 {
		t.Fatalf("exactly one push per (order, type, device), got %d", n)
	}
	// после отправки повтор события тоже ничего не шлёт
	_ = e.handle(t, payload)
	_, _ = e.svc.Tick(ctx)
	if n := len(e.prov.Messages()); n != 1 {
		t.Fatalf("resend after sent: %d", n)
	}
	// другой тип по тому же заказу — отдельное уведомление
	_ = e.status(t, order, "alice", "handed_over", "", at.Add(time.Second))
	_, _ = e.svc.Tick(ctx)
	if n := len(e.prov.Messages()); n != 2 {
		t.Fatalf("handed_over is a separate notification: %d", n)
	}
}

func TestRetryWithBackoffThenSuccess(t *testing.T) {
	e := setup(t, service.Config{BackoffBase: 2 * time.Second, MaxAttempts: 6})
	ctx := context.Background()
	e.register(t, "alice", domain.FCM, "a1")
	order := newOrder()
	_ = e.status(t, order, "alice", "ready", "", e.clock.Now())
	e.prov.SetDefault(push.Retry)

	if n, _ := e.svc.Tick(ctx); n != 1 {
		t.Fatalf("first attempt: %d", n)
	}
	if n, _ := e.svc.Tick(ctx); n != 0 {
		t.Fatal("must wait for backoff (2s)")
	}
	e.clock.Advance(1900 * time.Millisecond)
	if n, _ := e.svc.Tick(ctx); n != 0 {
		t.Fatal("still waiting")
	}
	e.clock.Advance(200 * time.Millisecond)
	if n, _ := e.svc.Tick(ctx); n != 1 {
		t.Fatal("second attempt after 2s")
	}
	e.clock.Advance(3 * time.Second) // второй интервал — 4 с
	if n, _ := e.svc.Tick(ctx); n != 0 {
		t.Fatal("backoff doubles: 4s")
	}
	e.prov.SetDefault(push.Sent)
	e.clock.Advance(2 * time.Second)
	if n, _ := e.svc.Tick(ctx); n != 1 {
		t.Fatal("third attempt")
	}
	r := e.rows(t, order)
	if len(r) != 1 || r[0].Status != domain.StatusSent || r[0].Attempts != 3 {
		t.Fatalf("%+v", r)
	}
	if len(e.prov.Messages()) != 1 {
		t.Fatal("one successful push")
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	e := setup(t, service.Config{BackoffBase: time.Second, BackoffMax: time.Second, MaxAttempts: 3})
	ctx := context.Background()
	e.register(t, "alice", domain.FCM, "a1")
	order := newOrder()
	_ = e.status(t, order, "alice", "ready", "", e.clock.Now())
	e.prov.SetDefault(push.Retry)
	for i := 0; i < 6; i++ {
		_, _ = e.svc.Tick(ctx)
		e.clock.Advance(2 * time.Second)
	}
	r := e.rows(t, order)
	if len(r) != 1 || r[0].Status != domain.StatusFailed || r[0].Attempts != 3 || !strings.Contains(r[0].LastError, "max attempts") {
		t.Fatalf("%+v", r)
	}
	if e.prov.CallCount() != 3 {
		t.Fatalf("calls: %d", e.prov.CallCount())
	}
}

func TestPermanentRejectionIsNotRetried(t *testing.T) {
	e := setup(t, service.Config{})
	ctx := context.Background()
	e.register(t, "alice", domain.FCM, "a1")
	order := newOrder()
	_ = e.status(t, order, "alice", "ready", "", e.clock.Now())
	e.prov.SetDefault(push.Failed)
	_, _ = e.svc.Tick(ctx)
	e.clock.Advance(time.Hour)
	_, _ = e.svc.Tick(ctx)
	r := e.rows(t, order)
	if r[0].Status != domain.StatusFailed || r[0].Attempts != 1 || e.prov.CallCount() != 1 {
		t.Fatalf("%+v calls=%d", r, e.prov.CallCount())
	}
}

func TestInvalidTokenIsRevokedAndOtherDevicesStillGetPush(t *testing.T) {
	e := setup(t, service.Config{})
	ctx := context.Background()
	dead := e.register(t, "alice", domain.APNS, "dead")
	e.register(t, "alice", domain.FCM, "alive")
	e.prov.Set("dead", push.InvalidToken)
	order := newOrder()
	_ = e.status(t, order, "alice", "ready", "", e.clock.Now())
	_, _ = e.svc.Tick(ctx)
	list, _ := e.svc.List(ctx, "alice")
	if len(list) != 1 || list[0].Token != "alive" {
		t.Fatalf("dead token must be removed: %+v (was %s)", list, dead.ID)
	}
	if m := e.prov.Messages(); len(m) != 1 || m[0].Token != "alive" {
		t.Fatalf("%+v", m)
	}
	// следующий статус дойдёт только до живого устройства
	_ = e.status(t, order, "alice", "handed_over", "", e.clock.Now())
	_, _ = e.svc.Tick(ctx)
	if e.prov.CallCount() != 3 {
		t.Fatalf("dead device must not be called again: %d calls", e.prov.CallCount())
	}
}

func TestPermissionsRevokedAfterQueueing(t *testing.T) {
	e := setup(t, service.Config{})
	ctx := context.Background()
	e.register(t, "alice", domain.FCM, "a1")
	order := newOrder()
	_ = e.status(t, order, "alice", "ready", "", e.clock.Now())
	if _, _, err := e.svc.Register(ctx, service.RegisterInput{UserID: "alice", Platform: domain.FCM, Token: "a1", Permissions: false, Locale: domain.RU}); err != nil {
		t.Fatal(err)
	}
	_, _ = e.svc.Tick(ctx)
	r := e.rows(t, order)
	if r[0].Status != domain.StatusSkipped || e.prov.CallCount() != 0 {
		t.Fatalf("%+v calls=%d", r, e.prov.CallCount())
	}
}

func TestStaleQueuedNotificationsExpire(t *testing.T) {
	e := setup(t, service.Config{MaxEventAge: time.Hour, BackoffBase: time.Minute})
	ctx := context.Background()
	e.register(t, "alice", domain.FCM, "a1")
	order := newOrder()
	_ = e.status(t, order, "alice", "ready", "", e.clock.Now())
	e.prov.SetDefault(push.Retry) // провайдер лежит
	_, _ = e.svc.Tick(ctx)
	e.clock.Advance(90 * time.Minute)
	e.prov.SetDefault(push.Sent) // ожил, но «заказ готов» полуторачасовой давности уже никому не нужен
	_, _ = e.svc.Tick(ctx)
	r := e.rows(t, order)
	if r[0].Status != domain.StatusSkipped || r[0].LastError != "expired" || len(e.prov.Messages()) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestParallelSendersNeverSendTwice(t *testing.T) {
	e := setup(t, service.Config{Workers: 4, Batch: 100})
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		e.register(t, "alice", domain.FCM, fmt.Sprintf("dev-%d", i))
	}
	for i := 0; i < 20; i++ {
		_ = e.status(t, newOrder(), "alice", "ready", "", e.clock.Now())
	}
	e.prov.Delay = 5 * time.Millisecond
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ { // «реплики»: несколько циклов отправки одновременно
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := e.svc.Tick(ctx); err != nil {
					t.Errorf("tick: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	msgs := e.prov.Messages()
	if len(msgs) != 100 {
		t.Fatalf("20 orders × 5 devices = 100 pushes, got %d", len(msgs))
	}
	seen := map[string]bool{}
	for _, m := range msgs {
		if seen[m.ID] {
			t.Fatalf("notification %s sent twice", m.ID)
		}
		seen[m.ID] = true
	}
}

func TestCrashedSenderLeaseExpires(t *testing.T) {
	e := setup(t, service.Config{Lease: 30 * time.Second})
	ctx := context.Background()
	e.register(t, "alice", domain.FCM, "a1")
	order := newOrder()
	_ = e.status(t, order, "alice", "ready", "", e.clock.Now())
	// «упавший» процесс взял уведомление в аренду и не отчитался
	due, err := e.st.Claim(ctx, e.pool, e.clock.Now(), 30*time.Second, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("%v %v", due, err)
	}
	if n, _ := e.svc.Tick(ctx); n != 0 {
		t.Fatal("leased notification must not be taken twice")
	}
	e.clock.Advance(31 * time.Second)
	if n, _ := e.svc.Tick(ctx); n != 1 || len(e.prov.Messages()) != 1 {
		t.Fatal("after the lease the notification returns to work")
	}
}

func TestPurgeRemovesOldClosedNotifications(t *testing.T) {
	e := setup(t, service.Config{Retention: time.Hour})
	ctx := context.Background()
	e.register(t, "alice", domain.FCM, "a1")
	order := newOrder()
	_ = e.status(t, order, "alice", "ready", "", e.clock.Now())
	_, _ = e.svc.Tick(ctx)
	if len(e.rows(t, order)) != 1 {
		t.Fatal("sent row must stay for a while")
	}
	e.clock.Advance(2 * time.Hour)
	_, _ = e.svc.Tick(ctx)
	if len(e.rows(t, order)) != 0 {
		t.Fatal("closed rows older than retention must be purged")
	}
}

func TestHandlerNeverCallsProvider(t *testing.T) {
	// сбой push не должен влиять на заказ: обработка orders.status по сети ничего не вызывает
	e := setup(t, service.Config{})
	e.prov.SetDefault(push.Retry)
	e.register(t, "alice", domain.FCM, "a1")
	for _, st := range []string{"accepted", "ready", "on_delivery", "delivered", "handed_over", "cancelled", "not_received"} {
		if err := e.status(t, newOrder(), "alice", st, "", e.clock.Now()); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
	}
	if e.prov.CallCount() != 0 {
		t.Fatal("consumer must not call providers")
	}
}

func TestCancelledTextDependsOnReason(t *testing.T) {
	e := setup(t, service.Config{})
	e.register(t, "alice", domain.FCM, "a1")
	order := newOrder()
	_ = e.status(t, order, "alice", "cancelled", "payment_timeout", e.clock.Now())
	_, _ = e.svc.Tick(context.Background())
	m := e.prov.Messages()
	if len(m) != 1 || !strings.Contains(m[0].Body, "не был оплачен") || m[0].Type != domain.OrderCancelled {
		t.Fatalf("%+v", m)
	}
}

func testkitLog() *slog.Logger { return testkit.Log() }
