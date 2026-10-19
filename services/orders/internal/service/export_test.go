package service

import "context"

// CancelIfUnpaid открывает cancelIfUnpaid тестам (имитация гонки «таймер ↔ оплата»).
func (s *Service) CancelIfUnpaid(ctx context.Context, id string) (bool, error) {
	return s.cancelIfUnpaid(ctx, id)
}
