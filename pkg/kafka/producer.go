package kafka

import (
	"context"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Producer — синхронный продюсер (acks=all, идемпотентная запись franz-go по умолчанию).
type Producer struct{ cl *kgo.Client }

// NewProducer подключается к брокерам. clientID — для диагностики на брокере.
func NewProducer(brokers []string, clientID string, opts ...kgo.Opt) (*Producer, error) {
	base := []kgo.Opt{kgo.SeedBrokers(brokers...), kgo.ClientID(clientID), kgo.RequiredAcks(kgo.AllISRAcks())}
	cl, err := kgo.NewClient(append(base, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("kafka producer: %w", err)
	}
	return &Producer{cl: cl}, nil
}

// Publish отправляет события и ждёт подтверждения всех; порядок записей с одним ключом сохраняется.
func (p *Producer) Publish(ctx context.Context, evs ...Event) error {
	if len(evs) == 0 {
		return nil
	}
	recs := make([]*kgo.Record, len(evs))
	for i, e := range evs {
		recs[i] = e.record()
	}
	return p.cl.ProduceSync(ctx, recs...).FirstErr()
}

// Ping — проверка связи с брокером для readiness.
func (p *Producer) Ping(ctx context.Context) error { return p.cl.Ping(ctx) }

// Close закрывает клиента.
func (p *Producer) Close() { p.cl.Close() }
