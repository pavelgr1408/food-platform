package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/domain"
	"github.com/pavelgr1408/food-platform/services/loyalty/internal/service"
)

const r2 = "a1000000-0000-4000-8000-000000000002"

func (e *env) managed(t *testing.T, a service.Actor, in service.ManagedInput) ([]string, service.ManagedPage) {
	t.Helper()
	p, err := e.svc.ListManaged(context.Background(), a, in)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(p.Items))
	for i, x := range p.Items {
		ids[i] = x.ID
	}
	return ids, p
}

func ip(n int) *int { return &n }

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Долг №89: HQ видит всё, владелец — только local-акции своих ресторанов; порядок — недавно изменённые первыми.
func TestListManagedScopeAndOrder(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	nat := e.must(t, hq, e.input(domain.ScopeNational, "", 0))
	e.clock.Advance(time.Minute)
	mine := e.must(t, owner, e.input(domain.ScopeLocal, r4, 1))
	e.clock.Advance(time.Minute)
	other := e.must(t, hq, e.input(domain.ScopeLocal, r1, 2))
	e.clock.Advance(time.Minute)
	done := e.must(t, owner, e.input(domain.ScopeLocal, r4, 3))
	if _, err := e.svc.Complete(ctx, owner, done.ID); err != nil { // updatedAt растёт: завершённая — самая свежая
		t.Fatal(err)
	}

	ids, p := e.managed(t, hq, service.ManagedInput{})
	if !same(ids, []string{done.ID, other.ID, mine.ID, nat.ID}) || p.Total != 4 || p.Limit != 20 || p.Offset != 0 {
		t.Fatalf("hq: %v total=%d", ids, p.Total)
	}
	ids, p = e.managed(t, owner, service.ManagedInput{})
	if !same(ids, []string{done.ID, mine.ID}) || p.Total != 2 {
		t.Fatalf("owner must see only own local promotions (any status): %v", ids)
	}
	// фильтры
	if ids, _ = e.managed(t, hq, service.ManagedInput{Scope: "national"}); !same(ids, []string{nat.ID}) {
		t.Fatalf("hq national: %v", ids)
	}
	if ids, _ = e.managed(t, hq, service.ManagedInput{RestaurantID: r1}); !same(ids, []string{other.ID}) {
		t.Fatalf("hq restaurant: %v", ids)
	}
	if ids, _ = e.managed(t, hq, service.ManagedInput{Statuses: []string{"completed"}}); !same(ids, []string{done.ID}) {
		t.Fatalf("completed: %v", ids)
	}
	if ids, p = e.managed(t, hq, service.ManagedInput{Statuses: []string{"draft", "completed", "draft"}}); len(ids) != 4 || p.Total != 4 {
		t.Fatalf("draft+completed: %v", ids)
	}
	if ids, _ = e.managed(t, hq, service.ManagedInput{Statuses: []string{"active"}}); len(ids) != 0 {
		t.Fatalf("active: %v", ids)
	}
	if ids, _ = e.managed(t, owner, service.ManagedInput{RestaurantID: r4, Statuses: []string{"draft"}}); !same(ids, []string{mine.ID}) {
		t.Fatalf("owner draft: %v", ids)
	}
	// owner с двумя ресторанами видит оба
	two := service.Actor{Principal: authclient.Principal{UserID: "own2", Roles: []string{"FRANCHISE_OWNER"}, Restaurants: []string{r1, r4}}}
	if ids, _ = e.managed(t, two, service.ManagedInput{}); len(ids) != 3 {
		t.Fatalf("two restaurants: %v", ids)
	}
	_ = r2
}

func TestListManagedPaginationStable(t *testing.T) {
	e := newEnv(t) // часы не двигаем: updated_at равны
	for i := 0; i < 5; i++ {
		e.must(t, hq, e.input(domain.ScopeNational, "", i))
	}
	all, p := e.managed(t, hq, service.ManagedInput{})
	if len(all) != 5 || p.Total != 5 {
		t.Fatalf("%v", all)
	}
	var paged []string
	for off := 0; off < 5; off += 2 {
		ids, pg := e.managed(t, hq, service.ManagedInput{Limit: ip(2), Offset: ip(off)})
		if pg.Total != 5 {
			t.Fatalf("total on page: %d", pg.Total)
		}
		paged = append(paged, ids...)
	}
	if !same(all, paged) {
		t.Fatalf("pages must reproduce the full order: %v vs %v", all, paged)
	}
	if ids, p := e.managed(t, hq, service.ManagedInput{Offset: ip(100)}); len(ids) != 0 || p.Total != 5 {
		t.Fatalf("past the end: %v total=%d", ids, p.Total)
	}
}

func TestListManagedRightsAndValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.must(t, hq, e.input(domain.ScopeLocal, r1, 0))
	if c := code(func() error {
		_, err := e.svc.ListManaged(ctx, owner, service.ManagedInput{RestaurantID: r1})
		return err
	}()); c != "NOT_BOUND_TO_RESTAURANT" {
		t.Fatalf("owner → foreign restaurant: %s", c)
	}
	if c := code(func() error {
		_, err := e.svc.ListManaged(ctx, owner, service.ManagedInput{Scope: "national"})
		return err
	}()); c != "SCOPE_NOT_ALLOWED" {
		t.Fatalf("owner → national: %s", c)
	}
	unbound := service.Actor{Principal: authclient.Principal{UserID: "o", Roles: []string{"FRANCHISE_OWNER"}}}
	if c := code(func() error { _, err := e.svc.ListManaged(ctx, unbound, service.ManagedInput{}); return err }()); c != "NOT_BOUND_TO_RESTAURANT" {
		t.Fatalf("owner without restaurants: %s", c)
	}
	cust := service.Actor{Principal: authclient.Principal{UserID: "c", Roles: []string{"CUSTOMER"}}}
	if c := code(func() error { _, err := e.svc.ListManaged(ctx, cust, service.ManagedInput{}); return err }()); c != "ROLE_REQUIRED" {
		t.Fatalf("customer: %s", c)
	}
	e.au.down.Store(true) // fail-closed
	if c := code(func() error { _, err := e.svc.ListManaged(ctx, hq, service.ManagedInput{}); return err }()); c != "AUTH_UNAVAILABLE" {
		t.Fatalf("auth down: %s", c)
	}
	e.au.down.Store(false)

	bad := map[string]service.ManagedInput{
		"unknown status":  {Statuses: []string{"scheduled"}},
		"empty status":    {Statuses: []string{""}},
		"too many":        {Statuses: []string{"draft", "active", "completed", "draft"}},
		"unknown scope":   {Scope: "global"},
		"restaurant uuid": {RestaurantID: "nope"},
		"national + rest": {Scope: "national", RestaurantID: r1},
		"limit 0":         {Limit: ip(0)},
		"limit 51":        {Limit: ip(51)},
		"offset -1":       {Offset: ip(-1)},
	}
	for name, in := range bad {
		if c := code(func() error { _, err := e.svc.ListManaged(ctx, hq, in); return err }()); c != "INVALID_PARAMETER" {
			t.Errorf("%s: %s", name, c)
		}
	}
}

func (e *env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Удаляется только черновик: вместе с историей, без события; повтор — 404.
func TestDeleteDraftOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	d := e.must(t, owner, e.input(domain.ScopeLocal, r4, 1))
	if _, err := e.svc.Update(ctx, owner, d.ID, e.input(domain.ScopeLocal, r4, 2)); err != nil { // две записи истории
		t.Fatal(err)
	}
	if e.count(t, `SELECT count(*) FROM promotion_history WHERE promotion_id = $1`, d.ID) != 2 {
		t.Fatal("expected two history rows before delete")
	}
	if err := e.svc.Delete(ctx, owner, d.ID); err != nil {
		t.Fatalf("delete draft: %v", err)
	}
	if e.count(t, `SELECT count(*) FROM promotions WHERE id = $1`, d.ID) != 0 ||
		e.count(t, `SELECT count(*) FROM promotion_history WHERE promotion_id = $1`, d.ID) != 0 {
		t.Fatal("promotion and its history must be gone")
	}
	if len(e.events(t)) != 0 {
		t.Fatal("deleting a draft must not publish anything")
	}
	if c := code(e.svc.Delete(ctx, owner, d.ID)); c != "PROMOTION_NOT_FOUND" {
		t.Fatalf("repeat delete: %s", c)
	}
	if c := code(e.svc.Delete(ctx, owner, "not-a-uuid")); c != "PROMOTION_NOT_FOUND" {
		t.Fatalf("bad id: %s", c)
	}

	act := e.must(t, owner, e.input(domain.ScopeLocal, r4, 3))
	if _, err := e.svc.Activate(ctx, owner, act.ID); err != nil {
		t.Fatal(err)
	}
	if c := code(e.svc.Delete(ctx, owner, act.ID)); c != "PROMOTION_NOT_DRAFT" {
		t.Fatalf("active: %s", c)
	}
	if _, err := e.svc.Complete(ctx, owner, act.ID); err != nil {
		t.Fatal(err)
	}
	if c := code(e.svc.Delete(ctx, owner, act.ID)); c != "PROMOTION_NOT_DRAFT" {
		t.Fatalf("completed: %s", c)
	}
	if e.count(t, `SELECT count(*) FROM promotions WHERE id = $1`, act.ID) != 1 {
		t.Fatal("a non-draft promotion must stay")
	}
}

func TestDeleteRights(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	other := e.must(t, hq, e.input(domain.ScopeLocal, r1, 0))
	nat := e.must(t, hq, e.input(domain.ScopeNational, "", 0))
	if c := code(e.svc.Delete(ctx, owner, other.ID)); c != "NOT_BOUND_TO_RESTAURANT" {
		t.Fatalf("foreign restaurant: %s", c)
	}
	if c := code(e.svc.Delete(ctx, owner, nat.ID)); c != "SCOPE_NOT_ALLOWED" {
		t.Fatalf("national by owner: %s", c)
	}
	e.au.down.Store(true)
	if c := code(e.svc.Delete(ctx, hq, other.ID)); c != "AUTH_UNAVAILABLE" {
		t.Fatalf("auth down: %s", c)
	}
	e.au.down.Store(false)
	for _, id := range []string{other.ID, nat.ID} {
		if e.count(t, `SELECT count(*) FROM promotions WHERE id = $1`, id) != 1 {
			t.Fatal("a rejected delete must not remove anything")
		}
	}
	if err := e.svc.Delete(ctx, hq, nat.ID); err != nil {
		t.Fatalf("hq deletes national draft: %v", err)
	}
}

// Гонка «активировать» и «удалить»: ровно один исход, и они согласованы (нет ни активной удалённой, ни утраченной активации).
func TestDeleteRacesActivation(t *testing.T) {
	for i := 0; i < 12; i++ {
		e := newEnv(t)
		ctx := context.Background()
		d := e.must(t, owner, e.input(domain.ScopeLocal, r4, 1))
		var wg sync.WaitGroup
		var actErr, delErr error
		wg.Add(2)
		go func() { defer wg.Done(); _, actErr = e.svc.Activate(ctx, owner, d.ID) }()
		go func() { defer wg.Done(); delErr = e.svc.Delete(ctx, owner, d.ID) }()
		wg.Wait()
		rows := e.count(t, `SELECT count(*) FROM promotions WHERE id = $1`, d.ID)
		events := len(e.events(t))
		switch {
		case delErr == nil: // удаление выиграло: акции нет, активация — 404, события нет
			if rows != 0 || code(actErr) != "PROMOTION_NOT_FOUND" || events != 0 {
				t.Fatalf("iter %d delete won: rows=%d act=%v events=%d", i, rows, actErr, events)
			}
		default: // активация выиграла: акция активна, удаление — 409, событие одно
			if actErr != nil || code(delErr) != "PROMOTION_NOT_DRAFT" || rows != 1 || events != 1 {
				t.Fatalf("iter %d activate won: act=%v del=%v rows=%d events=%d", i, actErr, delErr, rows, events)
			}
		}
	}
}
