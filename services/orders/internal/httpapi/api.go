// Package httpapi — HTTP-слой orders-service (contracts/openapi/orders.yaml).
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/idempotency"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/service"
)

// Orders — сценарии, нужные HTTP-слою (реализация — service.Service).
type Orders interface {
	CreateCart(ctx context.Context, userID string, in service.CreateCartInput) (service.CartView, error)
	GetCart(ctx context.Context, userID, id string) (service.CartView, error)
	UpdateCartItems(ctx context.Context, userID, id string, items []service.ItemInput) (service.CartView, error)
	SetFulfillment(ctx context.Context, userID, id string, in service.FulfillmentInput) (service.CartView, error)
	CreateOrder(ctx context.Context, userID string, in service.CreateOrderInput, commit service.CommitFunc) (domain.Order, error)
	GetOrder(ctx context.Context, userID, id string) (domain.Order, error)
	GetEta(ctx context.Context, userID, id string) (service.Eta, error)
	SetPickupSlot(ctx context.Context, userID, id string, from, to time.Time) (domain.Order, error)
}

// Deps — зависимости HTTP-слоя.
type Deps struct {
	Log         *slog.Logger
	Metrics     *metrics.Registry
	Auth        authclient.Authenticator
	Checks      []httpx.Check
	Idempotency *idempotency.Store // nil в unit-тестах: тогда Idempotency-Key не обеспечивается
	Orders      Orders
}

type handlers struct {
	log *slog.Logger
	svc Orders
}

// New собирает маршруты и стандартную цепочку middleware.
func New(d Deps) http.Handler {
	h := &handlers{log: d.Log, svc: d.Orders}
	mux := http.NewServeMux()
	httpx.RegisterHealth(mux, d.Checks...)
	mux.Handle("GET /metrics", d.Metrics.Handler())

	mux.HandleFunc("POST /carts", h.createCart)
	mux.HandleFunc("GET /carts/{cartId}", h.getCart)
	mux.HandleFunc("PUT /carts/{cartId}/items", h.updateItems)
	mux.HandleFunc("PUT /carts/{cartId}/fulfillment", h.setFulfillment)

	var create http.Handler = http.HandlerFunc(h.createOrder)
	if d.Idempotency != nil {
		create = d.Idempotency.Middleware(idempotency.Options{
			Required: true,
			Log:      d.Log,
			// ключ принадлежит пользователю; ключи гостей различаются телом (cartId — неугадываемый UUID)
			Scope: func(r *http.Request) string { return authclient.FromContext(r.Context()).UserID },
		})(create)
	}
	mux.Handle("POST /orders", create)
	mux.HandleFunc("GET /orders/{orderId}", h.getOrder)
	mux.HandleFunc("GET /orders/{orderId}/eta", h.getEta)
	mux.HandleFunc("PUT /orders/{orderId}/pickup-slot", h.setPickupSlot)

	return httpx.Chain(httpx.TrackRoute(mux),
		httpx.RequestContext(), httpx.Recover(d.Log), httpx.AccessLog(d.Log),
		d.Metrics.Middleware(), authclient.Middleware(d.Auth))
}

func userID(r *http.Request) string { return authclient.FromContext(r.Context()).UserID }

// unavailableProblem — Problem с расширением unavailableItems (orders.yaml: UnavailableItemsProblem).
type unavailableProblem struct {
	httpx.Problem
	UnavailableItems []unavailableDTO `json:"unavailableItems"`
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ue *service.ItemsUnavailableError
	if errors.As(err, &ue) {
		p := unavailableProblem{
			Problem: httpx.Problem{Type: "about:blank", Title: http.StatusText(http.StatusConflict), Status: http.StatusConflict,
				Detail: "some items are unavailable; the cart keeps only available lines", Instance: r.URL.Path, Code: "ITEMS_UNAVAILABLE"},
			UnavailableItems: make([]unavailableDTO, 0, len(ue.Items)),
		}
		for _, u := range ue.Items {
			p.UnavailableItems = append(p.UnavailableItems, unavailableDTO{u.MenuItemID, u.ReasonCode, u.Reason})
		}
		w.Header().Set("Content-Type", httpx.ContentTypeProblem)
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(p)
		return
	}
	httpx.HandleError(w, r, h.log, err)
}

func (h *handlers) createCart(w http.ResponseWriter, r *http.Request) {
	var req cartCreateRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	v, err := h.svc.CreateCart(r.Context(), userID(r), service.CreateCartInput{
		RestaurantID: req.RestaurantID, Guest: req.Guest, Contact: req.Contact.domain()})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/carts/"+v.Cart.ID)
	httpx.WriteJSON(w, http.StatusCreated, toCart(v))
}

func (h *handlers) getCart(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.GetCart(r.Context(), userID(r), r.PathValue("cartId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toCart(v))
}

func (h *handlers) updateItems(w http.ResponseWriter, r *http.Request) {
	var req cartItemsUpdate
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.Items == nil {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "INVALID_PARAMETER", "items is required")
		return
	}
	items := make([]service.ItemInput, len(req.Items))
	for i, it := range req.Items {
		items[i] = service.ItemInput(it)
	}
	v, err := h.svc.UpdateCartItems(r.Context(), userID(r), r.PathValue("cartId"), items)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toCart(v))
}

func (h *handlers) setFulfillment(w http.ResponseWriter, r *http.Request) {
	var req fulfillmentRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	in := service.FulfillmentInput{Type: domain.FulfillmentType(req.Type)}
	if a := req.Address; a != nil {
		in.Address.Raw = a.Raw
		if l := a.Location; l != nil {
			if l.Lat == nil || l.Lon == nil {
				httpx.WriteProblem(w, r, http.StatusBadRequest, "INVALID_PARAMETER", "address.location needs both lat and lon")
				return
			}
			in.Address.Lat, in.Address.Lon = l.Lat, l.Lon
		}
	}
	v, err := h.svc.SetFulfillment(r.Context(), userID(r), r.PathValue("cartId"), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toCart(v))
}

func (h *handlers) createOrder(w http.ResponseWriter, r *http.Request) {
	var req orderCreateRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	var body bytes.Buffer
	claim := idempotency.ClaimFrom(r.Context())
	o, err := h.svc.CreateOrder(r.Context(), userID(r), service.CreateOrderInput{
		CartID: req.CartID, PaymentMethod: domain.PaymentMethod(req.PaymentMethod),
		Contact: req.Contact.domain(), Channel: req.Channel,
	}, func(ctx context.Context, tx pgx.Tx, o domain.Order) error {
		// Внутри транзакции заказа: ответ сохраняется под ключом атомарно с заказом.
		if err := json.NewEncoder(&body).Encode(toOrder(o)); err != nil {
			return err
		}
		if claim == nil {
			return nil
		}
		return claim.CompleteTx(ctx, tx, idempotency.Record{Status: http.StatusCreated, Body: body.Bytes(),
			Headers: map[string]string{"Content-Type": "application/json", "Location": "/orders/" + o.ID}})
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", "/orders/"+o.ID)
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(body.Bytes())
}

func (h *handlers) getOrder(w http.ResponseWriter, r *http.Request) {
	o, err := h.svc.GetOrder(r.Context(), userID(r), r.PathValue("orderId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toOrder(o))
}

func (h *handlers) getEta(w http.ResponseWriter, r *http.Request) {
	e, err := h.svc.GetEta(r.Context(), userID(r), r.PathValue("orderId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, etaDTO{ReadyAt: e.ReadyAt.UTC(), DeliveryAt: e.DeliveryAt, EstimateMinutes: e.EstimateMinutes, Approximate: e.Approximate})
}

func (h *handlers) setPickupSlot(w http.ResponseWriter, r *http.Request) {
	var req pickupSlotRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.From == nil || req.To == nil {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "INVALID_PARAMETER", "from and to are required")
		return
	}
	o, err := h.svc.SetPickupSlot(r.Context(), userID(r), r.PathValue("orderId"), *req.From, *req.To)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toOrder(o))
}
