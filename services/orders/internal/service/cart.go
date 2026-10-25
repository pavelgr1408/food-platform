package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/orders/internal/clients"
	"github.com/pavelgr1408/food-platform/services/orders/internal/domain"
	"github.com/pavelgr1408/food-platform/services/orders/internal/store"
)

// Лимиты корзины (orders.yaml).
const (
	MaxCartLines  = 50
	MaxQuantity   = 99
	MaxModifiers  = 20
	maxAddressLen = 300
	maxNameLen    = 100
	maxEmailLen   = 254
)

// CartView — корзина с рассчитанными суммами.
type CartView struct {
	Cart          domain.Cart
	Applied       []domain.AppliedPromotion
	Subtotal      int64
	DiscountTotal int64
	Total         int64
}

func (s *Service) view(ctx context.Context, c domain.Cart) CartView {
	sub := c.Subtotal()
	d := s.disc.Apply(ctx, domain.DiscountInput{RestaurantID: c.RestaurantID, Currency: c.Currency, Fulfillment: c.Fulfillment,
		Lines: c.Items, Subtotal: sub, Now: s.now()})
	disc, total := domain.Net(sub, d)
	return CartView{Cart: c, Applied: d.Applied, Subtotal: sub, DiscountTotal: disc, Total: total}
}

// ItemInput — строка из запроса.
type ItemInput struct {
	MenuItemID  string
	Quantity    int
	ModifierIDs []string
}

// CreateCartInput — параметры createCart.
type CreateCartInput struct {
	RestaurantID string
	Guest        bool
	Contact      domain.Contact
}

func validateContact(c domain.Contact) (domain.Contact, error) {
	if utf8.RuneCountInString(c.Name) > maxNameLen {
		return c, badParam("contact.name is too long")
	}
	if c.Phone != "" {
		p, ok := domain.NormalizePhone(c.Phone)
		if !ok {
			return c, badParam("contact.phone must be in E.164 format, e.g. +79991234567")
		}
		c.Phone = p
	}
	if c.Email != "" {
		at := strings.IndexByte(c.Email, '@')
		if len(c.Email) > maxEmailLen || at < 1 || at == len(c.Email)-1 || strings.ContainsAny(c.Email, " \t\r\n") {
			return c, badParam("contact.email is not a valid address")
		}
	}
	c.Name = strings.TrimSpace(c.Name)
	return c, nil
}

// CreateCart создаёт корзину. userID пуст для неавторизованного запроса — тогда нужен guest=true.
func (s *Service) CreateCart(ctx context.Context, userID string, in CreateCartInput) (CartView, error) {
	if _, err := uuid.Parse(in.RestaurantID); err != nil {
		return CartView{}, badParam("restaurantId must be a UUID")
	}
	if userID == "" && !in.Guest {
		return CartView{}, errUnauth
	}
	contact, err := validateContact(in.Contact)
	if err != nil {
		return CartView{}, err
	}
	if _, err := s.rest.Get(ctx, in.RestaurantID); err != nil {
		if errors.Is(err, clients.ErrNotFound) {
			return CartView{}, errRestNotFound
		}
		return CartView{}, s.upstreamErr("restaurant", err, errRestDown)
	}
	c := domain.Cart{ID: uuid.NewString(), RestaurantID: in.RestaurantID, OwnerUserID: userID, Currency: s.cfg.DefaultCurrency, Contact: contact}
	if err := s.st.InsertCart(ctx, s.pool, c); err != nil {
		return CartView{}, err
	}
	got, err := s.st.GetCart(ctx, s.pool, c.ID, false)
	if err != nil {
		return CartView{}, err
	}
	return s.view(ctx, got), nil
}

// loadCart читает корзину и проверяет доступ; чужая корзина — 404 (существование не раскрываем).
func (s *Service) loadCart(ctx context.Context, userID, id string) (domain.Cart, error) {
	if _, err := uuid.Parse(id); err != nil {
		return domain.Cart{}, errCartNotFound
	}
	c, err := s.st.GetCart(ctx, s.pool, id, false)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !domain.CanBeAccessedBy(c.OwnerUserID, userID)) {
		return domain.Cart{}, errCartNotFound
	}
	return c, err
}

// GetCart возвращает корзину.
func (s *Service) GetCart(ctx context.Context, userID, id string) (CartView, error) {
	c, err := s.loadCart(ctx, userID, id)
	if err != nil {
		return CartView{}, err
	}
	return s.view(ctx, c), nil
}

func lineKey(it ItemInput) string {
	m := slices.Clone(it.ModifierIDs)
	slices.Sort(m)
	return it.MenuItemID + "|" + strings.Join(m, ",")
}

func validateItems(items []ItemInput) error {
	if len(items) > MaxCartLines {
		return badParam("items must contain at most 50 lines")
	}
	seen := make(map[string]bool, len(items))
	for i, it := range items {
		if _, err := uuid.Parse(it.MenuItemID); err != nil {
			return badParam("items[" + itoa(i) + "].menuItemId must be a UUID")
		}
		if it.Quantity < 1 || it.Quantity > MaxQuantity {
			return badParam("items[" + itoa(i) + "].quantity must be in 1..99")
		}
		if len(it.ModifierIDs) > MaxModifiers {
			return badParam("items[" + itoa(i) + "].modifierIds must contain at most 20 ids")
		}
		mseen := map[string]bool{}
		for _, m := range it.ModifierIDs {
			if _, err := uuid.Parse(m); err != nil {
				return badParam("items[" + itoa(i) + "].modifierIds must contain UUIDs")
			}
			if mseen[m] {
				return badParam("items[" + itoa(i) + "].modifierIds must not contain duplicates")
			}
			mseen[m] = true
		}
		k := lineKey(it)
		if seen[k] {
			return badParam("items[" + itoa(i) + "] duplicates an earlier line (same item and modifiers); merge quantities")
		}
		seen[k] = true
	}
	return nil
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}

// UpdateCartItems заменяет состав корзины и пересчитывает цены по menu-service.
// Недоступные позиции удаляются; тогда вместе с обновлённой корзиной возвращается
// *ItemsUnavailableError (409). menu недоступен → 503, корзина не меняется.
func (s *Service) UpdateCartItems(ctx context.Context, userID, id string, items []ItemInput) (CartView, error) {
	if err := validateItems(items); err != nil {
		return CartView{}, err
	}
	cart, err := s.loadCart(ctx, userID, id)
	if err != nil {
		return CartView{}, err
	}
	if cart.Status != domain.CartOpen {
		return CartView{}, errCartClosed
	}

	var (
		lines       []domain.Line
		unavailable []Unavailable
		currency    = cart.Currency
	)
	if len(items) > 0 {
		in := make([]clients.MenuLineInput, len(items))
		for i, it := range items {
			in[i] = clients.MenuLineInput(it)
		}
		res, err := s.menu.Validate(ctx, cart.RestaurantID, in)
		switch {
		case errors.Is(err, clients.ErrNotFound): // у ресторана нет меню: ничего заказать нельзя
			for _, it := range items {
				unavailable = append(unavailable, Unavailable{it.MenuItemID, "ITEM_NOT_FOUND", "Меню ресторана не найдено"})
			}
		case err != nil:
			return CartView{}, s.upstreamErr("menu", err, errMenuDown)
		default:
			if res.Currency != "" {
				currency = res.Currency
			}
			for i, l := range res.Lines {
				if !l.Available {
					unavailable = append(unavailable, Unavailable{l.MenuItemID, l.ReasonCode, l.Reason})
					continue
				}
				lines = append(lines, domain.Line{MenuItemID: l.MenuItemID, Name: l.Name, Quantity: l.Quantity,
					ModifierIDs: nonNil(items[i].ModifierIDs), UnitPrice: l.UnitPrice, LineTotal: l.LineTotal})
			}
		}
	}

	var out domain.Cart
	err = pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := s.st.GetCart(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.Status != domain.CartOpen {
			return errCartClosed
		}
		if _, err := s.st.ReplaceCartItems(ctx, tx, id, currency, lines); err != nil {
			return err
		}
		out, err = s.st.GetCart(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return CartView{}, err
	}
	v := s.view(ctx, out)
	if len(unavailable) > 0 {
		s.log.InfoContext(ctx, "cart items dropped as unavailable", "cart_id", id, "items", shortList(unavailable))
		return v, &ItemsUnavailableError{Items: unavailable}
	}
	return v, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// FulfillmentInput — параметры setCartFulfillment.
type FulfillmentInput struct {
	Type    domain.FulfillmentType
	Address domain.Address
}

// SetFulfillment выбирает способ получения.
func (s *Service) SetFulfillment(ctx context.Context, userID, id string, in FulfillmentInput) (CartView, error) {
	if !in.Type.Valid() {
		return CartView{}, badParam("type must be delivery or pickup")
	}
	addr := in.Address
	if in.Type == domain.FulfillmentDelivery {
		addr.Raw = strings.TrimSpace(addr.Raw)
		switch {
		case addr.Raw == "":
			return CartView{}, unprocessable("ADDRESS_REQUIRED", "address.raw is required for delivery")
		case utf8.RuneCountInString(addr.Raw) > maxAddressLen:
			return CartView{}, badParam("address.raw is too long")
		case (addr.Lat == nil) != (addr.Lon == nil):
			return CartView{}, badParam("address.location needs both lat and lon")
		case addr.Lat != nil && (*addr.Lat < -90 || *addr.Lat > 90 || *addr.Lon < -180 || *addr.Lon > 180):
			return CartView{}, badParam("address.location is out of range")
		}
	}
	cart, err := s.loadCart(ctx, userID, id)
	if err != nil {
		return CartView{}, err
	}
	if cart.Status != domain.CartOpen {
		return CartView{}, errCartClosed
	}
	if in.Type == domain.FulfillmentDelivery {
		info, err := s.rest.Get(ctx, cart.RestaurantID)
		if err != nil {
			if errors.Is(err, clients.ErrNotFound) {
				return CartView{}, errRestNotFound
			}
			return CartView{}, s.upstreamErr("restaurant", err, errRestDown)
		}
		if !info.DeliveryEnabled {
			return CartView{}, unprocessable("DELIVERY_NOT_AVAILABLE", "the restaurant does not offer delivery; choose pickup")
		}
		if err := checkZone(info, addr); err != nil { // S7: координаты и радиус доставки
			return CartView{}, err
		}
	}
	var out domain.Cart
	err = pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := s.st.GetCart(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.Status != domain.CartOpen {
			return errCartClosed
		}
		if err := s.st.SetCartFulfillment(ctx, tx, id, in.Type, addr); err != nil {
			return err
		}
		out, err = s.st.GetCart(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return CartView{}, err
	}
	return s.view(ctx, out), nil
}
