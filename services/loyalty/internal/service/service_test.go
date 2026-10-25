package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/authz"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/domain"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/service"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/testkit"
)

const (
	r1 = "a1000000-0000-4000-8000-000000000001"
	r4 = "a1000000-0000-4000-8000-000000000004"
)

var (
	hq    = service.Actor{Principal: authclient.Principal{UserID: "hq", Roles: []string{"HQ_STAFF"}}}
	owner = service.Actor{Principal: authclient.Principal{UserID: "owner4", Roles: []string{"FRANCHISE_OWNER"}, Restaurants: []string{r4}}}
)

type env struct {
	svc   *service.Service
	pool  *pgxpool.Pool
	clock *testkit.Clock
	au    *flakyAuthz
}

// flakyAuthz — claims-политика, которую можно «сломать» (auth недоступен).
type flakyAuthz struct{ down atomic.Bool }

func (f *flakyAuthz) CanManagePromotion(ctx context.Context, r authz.Request) (authz.Decision, error) {
	if f.down.Load() {
		return authz.Decision{}, authz.ErrUnavailable
	}
	return authz.Claims{}.CanManagePromotion(ctx, r)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testkit.Pool(t)
	clock := testkit.NewClock()
	au := &flakyAuthz{}
	return &env{svc: service.New(service.Deps{Pool: pool, Authz: au, Now: clock.Now}), pool: pool, clock: clock, au: au}
}

func (e *env) input(scope domain.Scope, rest string, prio int) domain.Promotion {
	now := e.clock.Now()
	return domain.Promotion{Title: "Акция", Scope: scope, RestaurantID: rest, Priority: prio,
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(48 * time.Hour), Discount: domain.Discount{Type: domain.DiscountPercent, Value: 1000}}
}

func (e *env) must(t *testing.T, a service.Actor, in domain.Promotion) domain.Promotion {
	t.Helper()
	p, err := e.svc.Create(context.Background(), a, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return p
}

func (e *env) events(t *testing.T) []service.DiscountEvent {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT payload FROM outbox WHERE topic = $1 ORDER BY id`, service.TopicOut)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []service.DiscountEvent
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		var ev service.DiscountEvent
		if err := json.Unmarshal(b, &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

func code(err error) string {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Code
	}
	var ce *service.ConflictError
	if errors.As(err, &ce) {
		return "PROMOTION_CONFLICT"
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

func TestLifecycleHistoryAndEvents(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.must(t, owner, e.input(domain.ScopeLocal, r4, 5))
	if p.Status != domain.StatusDraft || p.Version != 1 || p.CreatedBy != "owner4" {
		t.Fatalf("created: %+v", p)
	}
	if len(e.events(t)) != 0 {
		t.Fatal("a draft must not be published")
	}

	act, err := e.svc.Activate(ctx, owner, p.ID)
	if err != nil || act.Status != domain.StatusActive || act.Version != 2 {
		t.Fatalf("activate: %+v %v", act, err)
	}
	evs := e.events(t)
	if len(evs) != 1 || evs[0].PromotionID != p.ID || evs[0].Version != 2 || evs[0].Status != "active" ||
		evs[0].Scope != "local" || evs[0].RestaurantID != r4 || evs[0].Discount.Type != "percent" || evs[0].Discount.Value != 1000 ||
		evs[0].Currency != "RUB" || evs[0].Priority != 5 || evs[0].Conditions.MenuItemIDs == nil {
		t.Fatalf("event: %+v", evs)
	}

	again, err := e.svc.Activate(ctx, owner, p.ID)
	if err != nil || again.Version != 2 || len(e.events(t)) != 1 {
		t.Fatalf("repeated activation must be a no-op: %+v %v events=%d", again, err, len(e.events(t)))
	}

	done, err := e.svc.Complete(ctx, owner, p.ID)
	if err != nil || done.Status != domain.StatusCompleted || done.Version != 3 {
		t.Fatalf("complete: %+v %v", done, err)
	}
	evs = e.events(t)
	if len(evs) != 2 || evs[1].Status != "completed" || evs[1].Version != 3 {
		t.Fatalf("completion event: %+v", evs)
	}
	if again, err := e.svc.Complete(ctx, owner, p.ID); err != nil || again.Version != 3 || len(e.events(t)) != 2 {
		t.Fatalf("repeated completion must be a no-op: %+v %v", again, err)
	}

	h, err := e.svc.History(ctx, owner, p.ID)
	if err != nil || len(h) != 3 || h[0].Action != "created" || h[1].Action != "activated" || h[2].Action != "completed" {
		t.Fatalf("history: %+v %v", h, err)
	}
	var snap struct {
		Status  string `json:"status"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(h[1].Snapshot, &snap); err != nil || snap.Status != "active" || snap.Version != 2 {
		t.Fatalf("snapshot: %s %v", h[1].Snapshot, err)
	}
}

func TestCompletingDraftPublishesNothing(t *testing.T) {
	e := newEnv(t)
	p := e.must(t, hq, e.input(domain.ScopeNational, "", 0))
	done, err := e.svc.Complete(context.Background(), hq, p.ID)
	if err != nil || done.Status != domain.StatusCompleted || len(e.events(t)) != 0 {
		t.Fatalf("%+v %v events=%d", done, err, len(e.events(t)))
	}
	if code(func() error { _, err := e.svc.Activate(context.Background(), hq, p.ID); return err }()) != "PROMOTION_COMPLETED" {
		t.Fatal("a completed promotion cannot be activated")
	}
}

func TestRights(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if c := code(func() error { _, err := e.svc.Create(ctx, owner, e.input(domain.ScopeNational, "", 0)); return err }()); c != "SCOPE_NOT_ALLOWED" {
		t.Fatalf("owner → national: %s", c)
	}
	if c := code(func() error { _, err := e.svc.Create(ctx, owner, e.input(domain.ScopeLocal, r1, 0)); return err }()); c != "NOT_BOUND_TO_RESTAURANT" {
		t.Fatalf("owner → other restaurant: %s", c)
	}
	other := e.must(t, hq, e.input(domain.ScopeLocal, r1, 0))
	for name, fn := range map[string]func() error{
		"get":      func() error { _, err := e.svc.Get(ctx, owner, other.ID); return err },
		"history":  func() error { _, err := e.svc.History(ctx, owner, other.ID); return err },
		"activate": func() error { _, err := e.svc.Activate(ctx, owner, other.ID); return err },
		"complete": func() error { _, err := e.svc.Complete(ctx, owner, other.ID); return err },
		"update": func() error {
			_, err := e.svc.Update(ctx, owner, other.ID, e.input(domain.ScopeLocal, r1, 1))
			return err
		},
	} {
		if c := code(fn()); c != "NOT_BOUND_TO_RESTAURANT" {
			t.Errorf("%s of another restaurant's promotion: %s", name, c)
		}
	}
	if _, err := e.svc.Get(ctx, hq, other.ID); err != nil {
		t.Fatalf("hq: %v", err)
	}

	// владелец не может «переехать» чужой акцией на свой ресторан и не расширяет охват
	mine := e.must(t, owner, e.input(domain.ScopeLocal, r4, 0))
	if c := code(func() error {
		_, err := e.svc.Update(ctx, owner, mine.ID, e.input(domain.ScopeNational, "", 0))
		return err
	}()); c != "SCOPE_NOT_ALLOWED" {
		t.Fatalf("widening: %s", c)
	}
	if c := code(func() error {
		_, err := e.svc.Update(ctx, hq, mine.ID, e.input(domain.ScopeNational, "", 0))
		return err
	}()); c != "SCOPE_IMMUTABLE" {
		t.Fatalf("hq scope change: %s", c)
	}
	if c := code(func() error { _, err := e.svc.Update(ctx, hq, mine.ID, e.input(domain.ScopeLocal, r1, 0)); return err }()); c != "SCOPE_IMMUTABLE" {
		t.Fatalf("hq restaurant change: %s", c)
	}

	// auth недоступен → fail-closed
	e.au.down.Store(true)
	for name, fn := range map[string]func() error{
		"create":   func() error { _, err := e.svc.Create(ctx, hq, e.input(domain.ScopeNational, "", 0)); return err },
		"get":      func() error { _, err := e.svc.Get(ctx, hq, mine.ID); return err },
		"activate": func() error { _, err := e.svc.Activate(ctx, hq, mine.ID); return err },
	} {
		if c := code(fn()); c != "AUTH_UNAVAILABLE" {
			t.Errorf("%s with auth down: %s", name, c)
		}
	}
	e.au.down.Store(false)
	// публичный просмотр от auth не зависит
	e.au.down.Store(true)
	if _, err := e.svc.ListActive(ctx, ""); err != nil {
		t.Fatalf("list must not depend on auth: %v", err)
	}
}

func TestNotFoundAndValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, id := range []string{"not-a-uuid", "a1000000-0000-4000-8000-0000000000ff"} {
		if c := code(func() error { _, err := e.svc.Get(ctx, hq, id); return err }()); c != "PROMOTION_NOT_FOUND" {
			t.Errorf("Get(%q): %s", id, c)
		}
	}
	bad := e.input(domain.ScopeNational, "", 0)
	bad.Discount.Value = 0
	if c := code(func() error { _, err := e.svc.Create(ctx, hq, bad); return err }()); c != "PROMOTION_INVALID" {
		t.Fatalf("invalid input: %s", c)
	}
}

func TestConflictsE2(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.must(t, hq, e.input(domain.ScopeNational, "", 1))
	if _, err := e.svc.Activate(ctx, hq, a.ID); err != nil {
		t.Fatal(err)
	}

	b := e.must(t, owner, e.input(domain.ScopeLocal, r4, 1)) // тот же приоритет, пересекается → конфликт
	_, err := e.svc.Activate(ctx, owner, b.ID)
	var ce *service.ConflictError
	if !errors.As(err, &ce) || len(ce.IDs) != 1 || ce.IDs[0] != a.ID {
		t.Fatalf("want conflict with %s, got %v", a.ID, err)
	}
	if got, _ := e.svc.Get(ctx, owner, b.ID); got.Status != domain.StatusDraft || got.Version != 1 {
		t.Fatalf("a conflicting activation must not change the promotion: %+v", got)
	}

	// другой приоритет → не конфликт
	c := e.must(t, owner, e.input(domain.ScopeLocal, r4, 2))
	if _, err := e.svc.Activate(ctx, owner, c.ID); err != nil {
		t.Fatalf("different priority: %v", err)
	}
	// stackable с тем же приоритетом → не конфликт
	st := e.input(domain.ScopeLocal, r4, 1)
	st.Conditions.Stackable = true
	d := e.must(t, owner, st)
	if _, err := e.svc.Activate(ctx, owner, d.ID); err != nil {
		t.Fatalf("stackable: %v", err)
	}
	// после завершения национальной конфликт исчезает
	if _, err := e.svc.Complete(ctx, hq, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Activate(ctx, owner, b.ID); err != nil {
		t.Fatalf("after completion: %v", err)
	}

	// изменение действующей акции тоже проверяет конфликты: поднимаем приоритет b до приоритета c
	up := e.input(domain.ScopeLocal, r4, 2)
	if _, err := e.svc.Update(ctx, owner, b.ID, up); !errors.As(err, &ce) || ce.IDs[0] != c.ID {
		t.Fatalf("update into conflict: %v", err)
	}
}

func TestUpdate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.must(t, owner, e.input(domain.ScopeLocal, r4, 1))

	in := e.input(domain.ScopeLocal, r4, 1)
	in.Title, in.Discount = "Новое имя", domain.Discount{Type: domain.DiscountFixed, Value: 10000}
	up, err := e.svc.Update(ctx, owner, p.ID, in)
	if err != nil || up.Title != "Новое имя" || up.Version != 2 || up.Status != domain.StatusDraft || up.Discount.Type != domain.DiscountFixed {
		t.Fatalf("draft update: %+v %v", up, err)
	}
	if len(e.events(t)) != 0 {
		t.Fatal("updating a draft must not publish")
	}

	if _, err := e.svc.Activate(ctx, owner, p.ID); err != nil {
		t.Fatal(err)
	}
	in.Title = "Для всех"
	up, err = e.svc.Update(ctx, owner, p.ID, in)
	if err != nil || up.Version != 4 || up.Status != domain.StatusActive {
		t.Fatalf("active update: %+v %v", up, err)
	}
	evs := e.events(t)
	if len(evs) != 2 || evs[1].Version != 4 || evs[1].Title != "Для всех" || evs[1].Discount.Type != "fixed" {
		t.Fatalf("update must republish the snapshot: %+v", evs)
	}

	// истёкший срок у действующей нельзя, завершённую менять нельзя
	exp := in
	exp.StartsAt, exp.EndsAt = e.clock.Now().Add(-48*time.Hour), e.clock.Now().Add(-time.Hour)
	if c := code(func() error { _, err := e.svc.Update(ctx, owner, p.ID, exp); return err }()); c != "PROMOTION_EXPIRED" {
		t.Fatalf("expired: %s", c)
	}
	if _, err := e.svc.Complete(ctx, owner, p.ID); err != nil {
		t.Fatal(err)
	}
	if c := code(func() error { _, err := e.svc.Update(ctx, owner, p.ID, in); return err }()); c != "PROMOTION_COMPLETED" {
		t.Fatalf("completed: %s", c)
	}
	h, _ := e.svc.History(ctx, owner, p.ID)
	if len(h) != 5 {
		t.Fatalf("history length %d: %+v", len(h), h)
	}
}

func TestActivationOfExpiredPromotion(t *testing.T) {
	e := newEnv(t)
	p := e.must(t, hq, e.input(domain.ScopeNational, "", 0))
	e.clock.Advance(72 * time.Hour)
	if c := code(func() error { _, err := e.svc.Activate(context.Background(), hq, p.ID); return err }()); c != "PROMOTION_EXPIRED" {
		t.Fatalf("got %s", c)
	}
}

func TestListActive(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := e.clock.Now()
	mk := func(a service.Actor, scope domain.Scope, rest string, prio int, from, to time.Time) domain.Promotion {
		in := e.input(scope, rest, prio)
		in.StartsAt, in.EndsAt = from, to
		in.Conditions.Stackable = true // чтобы не конфликтовали
		p := e.must(t, a, in)
		if _, err := e.svc.Activate(ctx, a, p.ID); err != nil {
			t.Fatalf("activate: %v", err)
		}
		return p
	}
	nat := mk(hq, domain.ScopeNational, "", 1, now.Add(-time.Hour), now.Add(time.Hour))
	loc4 := mk(owner, domain.ScopeLocal, r4, 9, now.Add(-time.Hour), now.Add(time.Hour))
	loc1 := mk(hq, domain.ScopeLocal, r1, 5, now.Add(-time.Hour), now.Add(time.Hour))
	future := mk(hq, domain.ScopeNational, "", 7, now.Add(time.Hour), now.Add(2*time.Hour))
	draft := e.must(t, hq, e.input(domain.ScopeNational, "", 3))
	done := mk(hq, domain.ScopeNational, "", 4, now.Add(-time.Hour), now.Add(time.Hour))
	if _, err := e.svc.Complete(ctx, hq, done.ID); err != nil {
		t.Fatal(err)
	}

	ids := func(ps []domain.Promotion) []string {
		out := []string{}
		for _, p := range ps {
			out = append(out, p.ID)
		}
		return out
	}
	got, err := e.svc.ListActive(ctx, "")
	if err != nil || len(got) != 1 || got[0].ID != nat.ID {
		t.Fatalf("no restaurant → national only: %v %v", ids(got), err)
	}
	got, _ = e.svc.ListActive(ctx, r4)
	if len(got) != 2 || got[0].ID != loc4.ID || got[1].ID != nat.ID { // приоритет 9, затем 1
		t.Fatalf("restaurant 4: %v", ids(got))
	}
	got, _ = e.svc.ListActive(ctx, "A1000000-0000-4000-8000-000000000001")
	if len(got) != 2 || got[0].ID != loc1.ID {
		t.Fatalf("restaurant 1 (uppercase id): %v", ids(got))
	}
	if c := code(func() error { _, err := e.svc.ListActive(ctx, "zzz"); return err }()); c != "VALIDATION_FAILED" {
		t.Fatalf("bad id: %s", c)
	}

	// время идёт: стартует «будущая», заканчиваются остальные; черновик и завершённая не появляются
	e.clock.Advance(90 * time.Minute)
	got, _ = e.svc.ListActive(ctx, r4)
	if len(got) != 1 || got[0].ID != future.ID {
		t.Fatalf("after 90 min: %v (draft=%s)", ids(got), draft.ID)
	}
}

func TestParallelConflictingActivationsOnlyOneWins(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const n = 8
	ids := make([]string, n)
	for i := range ids {
		ids[i] = e.must(t, hq, e.input(domain.ScopeNational, "", 3)).ID
	}
	var wg sync.WaitGroup
	var ok, conflicts atomic.Int32
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.Activate(ctx, hq, id)
			var ce *service.ConflictError
			switch {
			case err == nil:
				ok.Add(1)
			case errors.As(err, &ce):
				conflicts.Add(1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || conflicts.Load() != n-1 {
		t.Fatalf("ok=%d conflicts=%d, want 1 and %d", ok.Load(), conflicts.Load(), n-1)
	}
	if len(e.events(t)) != 1 {
		t.Fatalf("exactly one event expected, got %d", len(e.events(t)))
	}
}

func TestWakerCalledAfterPublishOnly(t *testing.T) {
	pool := testkit.Pool(t)
	clock := testkit.NewClock()
	w := &countWaker{}
	svc := service.New(service.Deps{Pool: pool, Authz: authz.Claims{}, Waker: w, Now: clock.Now})
	in := domain.Promotion{Title: "x", Scope: domain.ScopeNational, StartsAt: clock.Now(), EndsAt: clock.Now().Add(time.Hour),
		Discount: domain.Discount{Type: domain.DiscountPercent, Value: 100}}
	p, err := svc.Create(context.Background(), hq, in)
	if err != nil || w.n.Load() != 0 {
		t.Fatalf("create must not wake: %v %d", err, w.n.Load())
	}
	if _, err := svc.Activate(context.Background(), hq, p.ID); err != nil || w.n.Load() != 1 {
		t.Fatalf("activate must wake once: %v %d", err, w.n.Load())
	}
}

type countWaker struct{ n atomic.Int32 }

func (c *countWaker) Wake() { c.n.Add(1) }
