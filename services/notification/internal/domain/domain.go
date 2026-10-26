// Package domain — чистые правила notification-service: платформы и токены, соответствие статуса заказа
// типу уведомления, тексты по языкам. Без ввода-вывода.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Platform — платформа push-токена (OpenAPI PushPlatform).
type Platform string

// Платформы push-провайдеров.
const (
	APNS   Platform = "apns"
	FCM    Platform = "fcm"
	Huawei Platform = "huawei"
)

// Valid — известная платформа.
func (p Platform) Valid() bool { return p == APNS || p == FCM || p == Huawei }

// MessagePlatform — значение PushNotification.platform в AsyncAPI (ios|android|huawei).
func (p Platform) MessagePlatform() string {
	switch p {
	case APNS:
		return "ios"
	case FCM:
		return "android"
	}
	return "huawei"
}

// Locale — язык уведомлений.
type Locale string

// Поддержанные языки.
const (
	RU Locale = "ru"
	EN Locale = "en"
)

// DefaultLocale — язык по умолчанию.
const DefaultLocale = RU

// ParseAcceptLanguage выбирает язык по заголовку Accept-Language: первый из поддержанных тегов в порядке
// перечисления (q-значения не учитываются), иначе язык по умолчанию.
func ParseAcceptLanguage(h string) Locale {
	for _, part := range strings.Split(h, ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		if i := strings.IndexAny(tag, "-_"); i >= 0 {
			tag = tag[:i]
		}
		switch Locale(tag) {
		case RU:
			return RU
		case EN:
			return EN
		}
	}
	return DefaultLocale
}

// Ограничения токена.
const (
	MaxTokenLen      = 4096
	MaxTokensPerUser = 10
)

// ErrInvalidToken — токен не прошёл проверку формата.
var ErrInvalidToken = errors.New("invalid push token")

// ValidateToken: непустой, не длиннее MaxTokenLen, без пробелов и управляющих символов.
func ValidateToken(t string) error {
	if t == "" || len(t) > MaxTokenLen {
		return fmt.Errorf("%w: length must be 1..%d", ErrInvalidToken, MaxTokenLen)
	}
	for _, r := range t {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("%w: must not contain whitespace or control characters", ErrInvalidToken)
		}
	}
	return nil
}

// DeviceToken — зарегистрированное устройство.
type DeviceToken struct {
	ID                 string
	UserID             string
	Platform           Platform
	Token              string
	PermissionsGranted bool
	Locale             Locale
	RegisteredAt       time.Time
	UpdatedAt          time.Time
}

// Type — тип уведомления (AsyncAPI PushNotification.data.type).
type Type string

// Типы уведомлений.
const (
	OrderCreated     Type = "order_created"
	OrderReady       Type = "order_ready"
	OnDelivery       Type = "on_delivery"
	Delivered        Type = "delivered"
	HandedOver       Type = "handed_over"
	OrderCancelled   Type = "order_cancelled"
	OrderNotReceived Type = "order_not_received"
)

// TypeFor — какое уведомление порождает статус заказа. pending_payment, cooking, closed — никакое.
func TypeFor(status string) (Type, bool) {
	switch status {
	case "accepted":
		return OrderCreated, true
	case "ready":
		return OrderReady, true
	case "on_delivery":
		return OnDelivery, true
	case "delivered":
		return Delivered, true
	case "handed_over":
		return HandedOver, true
	case "cancelled":
		return OrderCancelled, true
	case "not_received":
		return OrderNotReceived, true
	}
	return "", false
}

// Priority — срочность push: «заказ готов», «курьер в пути», «доставлен», «отменён» — высокая.
type Priority string

// Приоритеты.
const (
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
)

// PriorityOf — приоритет по типу.
func PriorityOf(t Type) Priority {
	switch t {
	case OrderReady, OnDelivery, Delivered, OrderCancelled:
		return PriorityHigh
	}
	return PriorityNormal
}

// Text — заголовок и тело уведомления.
type Text struct{ Title, Body string }

type key struct {
	t      Type
	reason string
}

// templates: ключ сообщения → язык → шаблон тела (%s — номер заказа). Ключ с причиной точнее ключа без неё.
var templates = map[Locale]map[key]Text{
	RU: {
		{OrderCreated, ""}:                    {"Заказ принят", "Заказ №%s принят в работу."},
		{OrderReady, ""}:                      {"Заказ готов", "Заказ №%s готов."},
		{OnDelivery, ""}:                      {"Курьер в пути", "Курьер забрал заказ №%s и едет к вам."},
		{Delivered, ""}:                       {"Заказ доставлен", "Заказ №%s доставлен. Приятного аппетита!"},
		{HandedOver, ""}:                      {"Заказ выдан", "Заказ №%s выдан. Приятного аппетита!"},
		{OrderCancelled, ""}:                  {"Заказ отменён", "Заказ №%s отменён."},
		{OrderCancelled, "payment_timeout"}:   {"Заказ отменён", "Заказ №%s отменён: он не был оплачен вовремя."},
		{OrderCancelled, "out_of_stock"}:      {"Заказ отменён", "Заказ №%s отменён: позиций нет в наличии."},
		{OrderCancelled, "restaurant_closed"}: {"Заказ отменён", "Заказ №%s отменён: ресторан закрыт."},
		{OrderNotReceived, ""}:                {"Заказ не получен", "Заказ №%s не был получен."},
		{OrderNotReceived, "pickup_timeout"}:  {"Заказ не получен", "Заказ №%s не забрали вовремя."},
		{OrderNotReceived, "undelivered"}:     {"Не удалось доставить", "Курьеру не удалось доставить заказ №%s."},
	},
	EN: {
		{OrderCreated, ""}:                    {"Order accepted", "Order #%s has been accepted."},
		{OrderReady, ""}:                      {"Order ready", "Order #%s is ready."},
		{OnDelivery, ""}:                      {"Courier on the way", "The courier picked up order #%s and is on the way."},
		{Delivered, ""}:                       {"Order delivered", "Order #%s has been delivered. Enjoy your meal!"},
		{HandedOver, ""}:                      {"Order handed over", "Order #%s has been handed over. Enjoy your meal!"},
		{OrderCancelled, ""}:                  {"Order cancelled", "Order #%s has been cancelled."},
		{OrderCancelled, "payment_timeout"}:   {"Order cancelled", "Order #%s was cancelled: it was not paid in time."},
		{OrderCancelled, "out_of_stock"}:      {"Order cancelled", "Order #%s was cancelled: items are out of stock."},
		{OrderCancelled, "restaurant_closed"}: {"Order cancelled", "Order #%s was cancelled: the restaurant is closed."},
		{OrderNotReceived, ""}:                {"Order not received", "Order #%s was not received."},
		{OrderNotReceived, "pickup_timeout"}:  {"Order not received", "Order #%s was not picked up in time."},
		{OrderNotReceived, "undelivered"}:     {"Delivery failed", "The courier could not deliver order #%s."},
	},
}

// Render собирает текст уведомления на языке locale; неизвестный язык — язык по умолчанию.
func Render(t Type, reason string, locale Locale, orderNumber string) Text {
	set, ok := templates[locale]
	if !ok {
		set = templates[DefaultLocale]
	}
	tpl, ok := set[key{t, reason}]
	if !ok {
		tpl = set[key{t, ""}]
	}
	return Text{Title: tpl.Title, Body: fmt.Sprintf(tpl.Body, orderNumber)}
}

// Status — состояние строки очереди уведомлений.
type Status string

// Состояния уведомления.
const (
	StatusPending Status = "pending"
	StatusSent    Status = "sent"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
)

// Notification — уведомление, ожидающее отправки (или отправленное) на одно устройство.
type Notification struct {
	ID          string
	OrderID     string
	OrderNumber string
	Type        Type
	Reason      string
	Priority    Priority
	Title, Body string
	TokenID     string
	Attempts    int
	EventAt     time.Time
}
