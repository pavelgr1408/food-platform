package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/pavelgr1408/food-platform/pkg/config"
	"github.com/pavelgr1408/food-platform/pkg/kafka"
)

// Мок системы производства (:9092). Внешняя система без REST API: общается с платформой только через Kafka.
//
//	orders.state  ──►  production  ──►  orders.dish.status   (снимок статусов всех блюд заказа)
//	                               ──►  production.lead.time  (время приготовления по ресторанам)
//
// Правило взятия заказа в работу — ADR-10: paymentStatus=paid ИЛИ оплата не предоплатная
// (paymentMethod ∈ {courier, restaurant}). Неоплаченное состояние игнорируется, дедупликация по orderId
// (повторные revision того же заказа в работу не берутся).
//
// Ход заказа: queued (сразу) → через QUEUE_DELAY все блюда cooking → через COOK_TIME (+ i×STAGGER для i-го
// блюда) блюдо ready. Каждое изменение публикуется снимком всех блюд; updatedAt монотонен.
//
// Время приготовления: BASE_LEAD_MINUTES + PER_ORDER_MINUTES × активные заказы ресторана (currentLoad —
// доля от LOAD_CAPACITY); публикуется при первом заказе ресторана, при смене загрузки и каждые LEAD_INTERVAL.
//
// Env (все необязательны): MOCK_PRODUCTION_QUEUE_DELAY=2s, MOCK_PRODUCTION_COOK_TIME=3s, MOCK_PRODUCTION_STAGGER=0,
// MOCK_PRODUCTION_BASE_LEAD_MINUTES=15, MOCK_PRODUCTION_PER_ORDER_MINUTES=2, MOCK_PRODUCTION_LOAD_CAPACITY=10,
// MOCK_PRODUCTION_LEAD_INTERVAL=10s.
//
// Управление сценарием (Postman/newman):
//
//	POST /_admin/production/mode?queue=on|off            off — «авария очереди» (E3): консьюмер orders.state приостановлен
//	POST /_admin/production/timing?queue=&cook=&stagger= длительности для новых заказов (Go-формат, напр. 500ms)
//	POST /_admin/production/leadtime?restaurantId=&minutes=  зафиксировать время приготовления ресторана
//	                                                         (без minutes — снять фиксацию); публикуется сразу
//	POST /_admin/production/reset                        забыть заказы и настройки (204)
//	GET  /_inspect/production                            режим, тайминги, заказы, время приготовления
type productionMock struct {
	mu  sync.Mutex
	ctx context.Context
	env *Env
	pub kafka.Publisher
	cl  *kgo.Client // консьюмер orders.state (nil в unit-тестах)

	cfg     prodTiming
	paused  bool
	orders  map[string]*prodOrder
	leads   map[string]*prodLead
	ignored int
	lastAt  map[string]time.Time // монотонность updatedAt по заказу / measuredAt по ресторану
}

type prodTiming struct {
	Queue, Cook, Stagger         time.Duration
	BaseLead, PerOrder, Capacity int
	LeadInterval                 time.Duration
}

type prodOrder struct {
	ID, RestaurantID string
	Dishes           []*prodDish
	StartedAt        time.Time
	Done             bool
}

type prodDish struct{ ID, Status string }

type prodLead struct {
	RestaurantID string
	Active       int
	Override     *int
}

// Топики и типы событий производства.
const (
	topicOrdersState = "orders.state"
	topicDishStatus  = "orders.dish.status"
	topicLeadTime    = "production.lead.time"
)

func init() { Register("production", func() Module { return &productionMock{} }) }

func (*productionMock) Name() string        { return "production" }
func (*productionMock) DefaultAddr() string { return ":9092" }

func (m *productionMock) defaultTiming(r *config.Reader) prodTiming {
	return prodTiming{
		Queue:        r.Duration("MOCK_PRODUCTION_QUEUE_DELAY", 2*time.Second),
		Cook:         r.Duration("MOCK_PRODUCTION_COOK_TIME", 3*time.Second),
		Stagger:      r.Duration("MOCK_PRODUCTION_STAGGER", 0),
		BaseLead:     r.Int("MOCK_PRODUCTION_BASE_LEAD_MINUTES", 15),
		PerOrder:     r.Int("MOCK_PRODUCTION_PER_ORDER_MINUTES", 2),
		Capacity:     r.Int("MOCK_PRODUCTION_LOAD_CAPACITY", 10),
		LeadInterval: r.Duration("MOCK_PRODUCTION_LEAD_INTERVAL", 10*time.Second),
	}
}

func (m *productionMock) resetLocked(def prodTiming) {
	m.orders, m.leads, m.lastAt = map[string]*prodOrder{}, map[string]*prodLead{}, map[string]time.Time{}
	m.cfg, m.paused, m.ignored = def, false, 0
}

// Register вешает админ-ручки и инициализирует состояние.
func (m *productionMock) Register(mux *http.ServeMux, env *Env) {
	r := config.FromEnv()
	m.env, m.ctx = env, context.Background()
	def := m.defaultTiming(r)
	if err := r.Err(); err != nil {
		env.Log.Error("production mock: configuration error, defaults used", "error", err)
	}
	m.mu.Lock()
	m.resetLocked(def)
	m.mu.Unlock()

	mux.HandleFunc("POST /_admin/production/mode", func(w http.ResponseWriter, req *http.Request) {
		switch v := req.URL.Query().Get("queue"); v {
		case "on", "off":
			m.setPaused(v == "off")
			writeJSON(w, http.StatusOK, map[string]string{"queue": v})
		default:
			http.Error(w, "queue must be on|off", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("POST /_admin/production/timing", func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		m.mu.Lock()
		defer m.mu.Unlock()
		next := m.cfg
		for name, dst := range map[string]*time.Duration{"queue": &next.Queue, "cook": &next.Cook, "stagger": &next.Stagger} {
			if v := q.Get(name); v != "" {
				d, err := time.ParseDuration(v)
				if err != nil || d < 0 || d > time.Hour {
					http.Error(w, name+" must be a duration between 0 and 1h", http.StatusBadRequest)
					return
				}
				*dst = d
			}
		}
		m.cfg = next
		writeJSON(w, http.StatusOK, m.timingView())
	})
	mux.HandleFunc("POST /_admin/production/leadtime", func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		rid := q.Get("restaurantId")
		if rid == "" {
			http.Error(w, "restaurantId is required", http.StatusBadRequest)
			return
		}
		var override *int
		if v := q.Get("minutes"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > 1440 {
				http.Error(w, "minutes must be an integer between 0 and 1440", http.StatusBadRequest)
				return
			}
			override = &n
		}
		m.mu.Lock()
		l := m.leadLocked(rid)
		l.Override = override
		minutes := m.publishLeadLocked(rid)
		m.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"restaurantId": rid, "leadTimeMinutes": minutes, "fixed": override != nil})
	})
	mux.HandleFunc("POST /_admin/production/reset", func(w http.ResponseWriter, _ *http.Request) {
		m.setPaused(false)
		m.mu.Lock()
		m.resetLocked(def)
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /_inspect/production", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, m.inspect())
	})
}

func (m *productionMock) timingView() map[string]any {
	return map[string]any{"queue": m.cfg.Queue.String(), "cook": m.cfg.Cook.String(), "stagger": m.cfg.Stagger.String(),
		"baseLeadMinutes": m.cfg.BaseLead, "perOrderMinutes": m.cfg.PerOrder}
}

func (m *productionMock) inspect() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	orders := make([]map[string]any, 0, len(m.orders))
	for _, o := range m.orders {
		ds := make([]map[string]string, 0, len(o.Dishes))
		for _, d := range o.Dishes {
			ds = append(ds, map[string]string{"dishId": d.ID, "status": d.Status})
		}
		orders = append(orders, map[string]any{"orderId": o.ID, "restaurantId": o.RestaurantID, "dishes": ds,
			"startedAt": o.StartedAt, "done": o.Done})
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i]["orderId"].(string) < orders[j]["orderId"].(string) })
	leads := make([]map[string]any, 0, len(m.leads))
	for _, l := range m.leads {
		leads = append(leads, map[string]any{"restaurantId": l.RestaurantID, "active": l.Active,
			"leadTimeMinutes": m.leadMinutes(l), "fixed": l.Override != nil})
	}
	sort.Slice(leads, func(i, j int) bool { return leads[i]["restaurantId"].(string) < leads[j]["restaurantId"].(string) })
	mode := "on"
	if m.paused {
		mode = "off"
	}
	return map[string]any{"queue": mode, "timing": m.timingView(), "orders": orders, "leadTimes": leads, "ignoredStates": m.ignored}
}

// Start подключает Kafka: консьюмер orders.state и периодическую публикацию времени приготовления.
func (m *productionMock) Start(ctx context.Context, env *Env) error {
	m.ctx = ctx
	if len(env.Brokers) == 0 {
		return fmt.Errorf("production mock needs KAFKA_BROKERS")
	}
	prod, err := kafka.NewProducer(env.Brokers, "mock-production")
	if err != nil {
		return err
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(env.Brokers...), kgo.ClientID("mock-production"),
		kgo.ConsumerGroup("mock-production"), kgo.ConsumeTopics(topicOrdersState),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		prod.Close()
		return err
	}
	m.mu.Lock()
	m.pub, m.cl = prod, cl
	m.mu.Unlock()
	env.wg.Add(1)
	go func() {
		defer env.wg.Done()
		defer prod.Close()
		defer cl.Close()
		m.run(ctx, cl)
	}()
	return nil
}

func (m *productionMock) run(ctx context.Context, cl *kgo.Client) {
	tick := time.NewTicker(m.leadInterval())
	defer tick.Stop()
	fetched := make(chan kgo.Fetches)
	go func() {
		defer close(fetched)
		for {
			f := cl.PollFetches(ctx)
			if f.IsClientClosed() || ctx.Err() != nil {
				return
			}
			select {
			case fetched <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.publishAllLeads()
		case f, ok := <-fetched:
			if !ok {
				return
			}
			f.EachError(func(t string, p int32, err error) {
				m.env.Log.Error("production mock: fetch error", "topic", t, "partition", p, "error", err)
			})
			f.EachRecord(func(r *kgo.Record) { m.onState(r.Value) })
		}
	}
}

func (m *productionMock) leadInterval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.LeadInterval <= 0 {
		return 10 * time.Second
	}
	return m.cfg.LeadInterval
}

// setPaused приостанавливает/возобновляет чтение orders.state (E3: «авария очереди»).
func (m *productionMock) setPaused(p bool) {
	m.mu.Lock()
	m.paused = p
	cl := m.cl
	m.mu.Unlock()
	if cl == nil {
		return
	}
	if p {
		cl.PauseFetchTopics(topicOrdersState)
	} else {
		cl.ResumeFetchTopics(topicOrdersState)
	}
}

type stateIn struct {
	OrderID       string `json:"orderId"`
	Revision      int    `json:"revision"`
	RestaurantID  string `json:"restaurantId"`
	PaymentMethod string `json:"paymentMethod"`
	PaymentStatus string `json:"paymentStatus"`
	Items         []struct {
		DishID string `json:"dishId"`
	} `json:"items"`
}

// eligible — правило ADR-10: производство действует, только если оплачено или предоплаты нет.
func (s stateIn) eligible() bool {
	return s.PaymentStatus == "paid" || s.PaymentMethod == "courier" || s.PaymentMethod == "restaurant"
}

// onState обрабатывает одно событие orders.state.
func (m *productionMock) onState(raw []byte) {
	var s stateIn
	if err := json.Unmarshal(raw, &s); err != nil || s.OrderID == "" || s.RestaurantID == "" {
		m.env.Log.Warn("production mock: bad orders.state payload", "error", err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// ресторан виден уже по неоплаченному состоянию — оценка времени будет готова к моменту оплаты
	if _, known := m.leads[s.RestaurantID]; !known {
		m.leadLocked(s.RestaurantID)
		m.publishLeadLocked(s.RestaurantID)
	}
	if !s.eligible() {
		m.ignored++
		return
	}
	if _, started := m.orders[s.OrderID]; started {
		return // дедупликация: заказ уже в работе
	}
	o := &prodOrder{ID: s.OrderID, RestaurantID: s.RestaurantID, StartedAt: time.Now().UTC()}
	seen := map[string]bool{}
	for _, it := range s.Items {
		if it.DishID != "" && !seen[it.DishID] {
			seen[it.DishID] = true
			o.Dishes = append(o.Dishes, &prodDish{ID: it.DishID, Status: "queued"})
		}
	}
	if len(o.Dishes) == 0 {
		m.ignored++
		return
	}
	m.orders[o.ID] = o
	m.leadLocked(o.RestaurantID).Active++
	m.publishDishesLocked(o)
	m.publishLeadLocked(o.RestaurantID)

	cfg := m.cfg
	m.env.AfterD(m.ctx, cfg.Queue, func() { m.advance(o, -1, "cooking") })
	if cfg.Stagger == 0 { // все блюда готовы одновременно — один снимок
		m.env.AfterD(m.ctx, cfg.Queue+cfg.Cook, func() { m.advance(o, -1, "ready") })
		return
	}
	for i := range o.Dishes {
		i := i
		m.env.AfterD(m.ctx, cfg.Queue+cfg.Cook+time.Duration(i)*cfg.Stagger, func() { m.advance(o, i, "ready") })
	}
}

// advance меняет статус блюда i (i<0 — всех) и публикует снимок. Устаревшие таймеры (после reset) ничего не делают.
func (m *productionMock) advance(o *prodOrder, i int, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.orders[o.ID] != o {
		return
	}
	for j, d := range o.Dishes {
		if i < 0 || i == j {
			d.Status = status
		}
	}
	m.publishDishesLocked(o)
	if status == "ready" && !o.Done {
		for _, d := range o.Dishes {
			if d.Status != "ready" {
				return
			}
		}
		o.Done = true
		if l := m.leadLocked(o.RestaurantID); l.Active > 0 {
			l.Active--
		}
		m.publishLeadLocked(o.RestaurantID)
	}
}

// mono возвращает now, но не раньше предыдущего значения ключа (+1µs): updatedAt/measuredAt монотонны.
func (m *productionMock) mono(key string) time.Time {
	now := time.Now().UTC().Truncate(time.Microsecond)
	if last, ok := m.lastAt[key]; ok && !now.After(last) {
		now = last.Add(time.Microsecond)
	}
	m.lastAt[key] = now
	return now
}

type dishItemOut struct {
	DishID string `json:"dishId"`
	Status string `json:"status"`
}

func (m *productionMock) publishDishesLocked(o *prodOrder) {
	ds := make([]dishItemOut, 0, len(o.Dishes))
	for _, d := range o.Dishes {
		ds = append(ds, dishItemOut{DishID: d.ID, Status: d.Status})
	}
	m.publishLocked(topicDishStatus, "orders.dish.status.v1", o.ID, map[string]any{
		"orderId": o.ID, "dishes": ds, "updatedAt": m.mono("order:" + o.ID)})
}

func (m *productionMock) leadLocked(rid string) *prodLead {
	l := m.leads[rid]
	if l == nil {
		l = &prodLead{RestaurantID: rid}
		m.leads[rid] = l
	}
	return l
}

func (m *productionMock) leadMinutes(l *prodLead) int {
	if l.Override != nil {
		return *l.Override
	}
	return m.cfg.BaseLead + l.Active*m.cfg.PerOrder
}

// publishLeadLocked публикует текущее время приготовления ресторана и возвращает его.
func (m *productionMock) publishLeadLocked(rid string) int {
	l := m.leadLocked(rid)
	minutes := m.leadMinutes(l)
	load := 0.0
	if m.cfg.Capacity > 0 {
		load = float64(l.Active) / float64(m.cfg.Capacity)
		if load > 1 {
			load = 1
		}
	}
	m.publishLocked(topicLeadTime, "production.lead.time.v1", rid, map[string]any{
		"restaurantId": rid, "currentLoad": load, "leadTimeMinutes": minutes, "measuredAt": m.mono("rest:" + rid)})
	return minutes
}

func (m *productionMock) publishAllLeads() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for rid := range m.leads {
		m.publishLeadLocked(rid)
	}
}

// publishLocked отправляет событие; ошибка публикации логируется (мок не ретраит — как и «настоящая» система).
func (m *productionMock) publishLocked(topic, typ, key string, payload any) {
	if m.pub == nil {
		return
	}
	ev, err := kafka.NewEvent(m.ctx, topic, typ, key, payload)
	if err == nil {
		ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
		err = m.pub.Publish(ctx, ev)
		cancel()
	}
	if err != nil && m.ctx.Err() == nil {
		m.env.Log.Error("production mock: publish failed", "topic", topic, "key", key, "error", err)
	}
}
