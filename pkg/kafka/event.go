// Package kafka — события поверх Kafka (franz-go): конверт, транзакционный outbox,
// inbox (идемпотентный консьюмер), DLQ.
//
// Гарантии: at-least-once публикация (outbox) + дедупликация на стороне консьюмера
// (inbox) = эффективно однократная обработка. Порядок — в пределах ключа партиции
// (для заказных топиков — orderId); от внеочередных событий защищает revision в payload.
package kafka

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/google/uuid"
	"github.com/pavelgr1408/food-platform/pkg/logging"
	"github.com/pavelgr1408/food-platform/pkg/pgxutil"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Заголовки конверта (см. contracts/asyncapi: messageTraits.EventEnvelope).
const (
	HeaderEventID     = "event-id"
	HeaderEventType   = "event-type"
	HeaderOccurredAt  = "occurred-at"
	HeaderTraceparent = "traceparent"
	// Служебные заголовки DLQ.
	HeaderDLQError    = "x-error"
	HeaderDLQSource   = "x-source"
	HeaderDLQAttempts = "x-attempts"
	DLQSuffix         = ".dlq"
)

//go:embed sql/*.sql
var sqlFS embed.FS

// Migrations — источник миграций outbox/inbox для pgxutil.Migrate.
func Migrations() pgxutil.Source {
	sub, err := fs.Sub(sqlFS, "sql")
	if err != nil {
		panic(err)
	}
	return pgxutil.Source{Module: "kafka", FS: sub}
}

// Event — событие шины.
type Event struct {
	ID          uuid.UUID
	Type        string // например "orders.state.v1"
	Topic       string
	Key         string // ключ партиции
	OccurredAt  time.Time
	Traceparent string
	Payload     []byte
	// Extra — дополнительные заголовки записи (DLQ-метаданные и т.п.).
	Extra map[string]string
}

// NewEvent собирает событие: новый ID, время, traceparent из ctx, payload → JSON.
func NewEvent(ctx context.Context, topic, eventType, key string, payload any) (Event, error) {
	if topic == "" || eventType == "" || key == "" {
		return Event{}, errors.New("kafka: topic, event type and key are required")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("kafka: marshal payload: %w", err)
	}
	ev := Event{ID: uuid.New(), Type: eventType, Topic: topic, Key: key, OccurredAt: time.Now().UTC(), Payload: b}
	if t := logging.TraceID(ctx); t != "" {
		ev.Traceparent = "00-" + t + "-" + uuid.NewString()[:8] + uuid.NewString()[:8] + "-01"
	}
	return ev, nil
}

// Context возвращает ctx с trace id события — логи консьюмера коррелируются с запросом-источником.
func (e Event) Context(ctx context.Context) context.Context {
	if len(e.Traceparent) == 55 {
		ctx = logging.WithTraceID(ctx, e.Traceparent[3:35])
	}
	return logging.WithRequestID(ctx, e.ID.String())
}

func (e Event) record() *kgo.Record {
	h := []kgo.RecordHeader{
		{Key: HeaderEventID, Value: []byte(e.ID.String())},
		{Key: HeaderEventType, Value: []byte(e.Type)},
		{Key: HeaderOccurredAt, Value: []byte(e.OccurredAt.UTC().Format(time.RFC3339Nano))},
	}
	if e.Traceparent != "" {
		h = append(h, kgo.RecordHeader{Key: HeaderTraceparent, Value: []byte(e.Traceparent)})
	}
	for k, v := range e.Extra {
		h = append(h, kgo.RecordHeader{Key: k, Value: []byte(v)})
	}
	return &kgo.Record{Topic: e.Topic, Key: []byte(e.Key), Value: e.Payload, Headers: h}
}

// decode разбирает запись; ошибка означает «ядовитое» сообщение (→ DLQ).
func decode(r *kgo.Record) (Event, error) {
	ev := Event{Topic: r.Topic, Key: string(r.Key), Payload: r.Value}
	var haveID bool
	for _, h := range r.Headers {
		switch h.Key {
		case HeaderEventID:
			id, err := uuid.ParseBytes(h.Value)
			if err != nil {
				return ev, fmt.Errorf("invalid %s header: %w", HeaderEventID, err)
			}
			ev.ID, haveID = id, true
		case HeaderEventType:
			ev.Type = string(h.Value)
		case HeaderOccurredAt:
			t, err := time.Parse(time.RFC3339Nano, string(h.Value))
			if err != nil {
				return ev, fmt.Errorf("invalid %s header: %w", HeaderOccurredAt, err)
			}
			ev.OccurredAt = t
		case HeaderTraceparent:
			ev.Traceparent = string(h.Value)
		}
	}
	if !haveID {
		return ev, fmt.Errorf("missing %s header", HeaderEventID)
	}
	return ev, nil
}

// Publisher публикует события (реализация — Producer; в тестах — fake).
type Publisher interface {
	Publish(ctx context.Context, evs ...Event) error
}

func newID() uuid.UUID { return uuid.New() }
