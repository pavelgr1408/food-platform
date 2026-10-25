package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/domain"
	"github.com/pavelgr1408/food-platform/services/delivery/internal/store"
)

// Callback — уведомление курьерской системы (DeliveryStatusCallback, delivery.yaml).
type Callback struct {
	EventID      string
	OrderID      string
	Status       string
	Reason       string
	CourierID    string
	CourierName  string
	CourierPhone string
	EtaAt        *time.Time
	Paid         bool
	Timestamp    time.Time
}

// Validate проверяет структуру уведомления.
func (c Callback) Validate() error {
	if err := c.validate(); err != nil {
		return &ValidationError{Msg: err.Error()}
	}
	return nil
}

// ValidationError — уведомление не соответствует контракту (400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// IsValidation — ошибка структуры уведомления.
func IsValidation(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

func (c Callback) validate() error {
	if c.EventID == "" || len(c.EventID) > 128 {
		return fmt.Errorf("eventId is required (max 128 chars)")
	}
	if _, err := uuid.Parse(c.OrderID); err != nil {
		return fmt.Errorf("orderId must be a UUID")
	}
	switch domain.Status(c.Status) {
	case domain.StatusAssigned, domain.StatusPickedUp, domain.StatusEnRoute, domain.StatusDelivered, domain.StatusFailed:
	default:
		return fmt.Errorf("status must be one of assigned, picked_up, en_route, delivered, failed")
	}
	if c.Timestamp.IsZero() {
		return fmt.Errorf("timestamp is required")
	}
	return nil
}

// HandleCallback применяет вебхук курьерской системы. Подпись проверил HTTP-слой. Повторная доставка
// (тот же eventId) — no-op. Неизвестная доставка — ErrNotFound-ошибка (404: курьерская система повторит).
func (s *Service) HandleCallback(ctx context.Context, c Callback) error {
	if err := c.Validate(); err != nil {
		return err
	}
	d0, err := s.st.GetByOrder(ctx, s.pool, c.OrderID, false)
	if errors.Is(err, store.ErrNotFound) {
		s.m.Webhooks.WithLabelValues("not_found").Inc()
		return errNotFound
	}
	if err != nil {
		return err
	}
	result := "ignored"
	var out domain.Delivery
	err = pgxutil.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		fresh, err := s.st.TryRecordWebhook(ctx, tx, c.EventID, d0.ID)
		if err != nil {
			return err
		}
		if !fresh {
			result = "duplicate"
			return nil
		}
		d, err := s.st.GetByID(ctx, tx, d0.ID, true)
		if err != nil {
			return err
		}
		o := Observation{Status: domain.Status(c.Status), Reason: domain.FailReason(c.Reason), CourierID: c.CourierID,
			CourierName: c.CourierName, CourierPhone: c.CourierPhone, EtaAt: c.EtaAt, Paid: c.Paid}
		ch, err := s.applyTx(ctx, tx, &d, o, sourceHook)
		if err != nil {
			return err
		}
		if ch {
			result = "applied"
		}
		out = d
		return nil
	})
	if err != nil {
		return err
	}
	s.m.Webhooks.WithLabelValues(result).Inc()
	if result == "applied" {
		s.wake()
		s.afterApply(ctx, out, nil, time.Time{})
	}
	return nil
}
