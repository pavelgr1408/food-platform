package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
)

// ConsumerConfig — настройки идемпотентного консьюмера.
type ConsumerConfig struct {
	Brokers     []string
	Group       string // имя consumer group; оно же имя inbox-консьюмера
	Topics      []string
	Pool        *pgxpool.Pool
	Handler     TxHandler
	DLQ         Publisher     // куда писать «ядовитые»; по умолчанию — свой продюсер
	MaxAttempts int           // попыток обработки до DLQ, по умолчанию 5
	Backoff     time.Duration // базовая пауза между попытками, по умолчанию 200ms
	MaxBackoff  time.Duration // потолок, по умолчанию 5s
	Log         *slog.Logger
	Opts        []kgo.Opt // дополнительные опции franz-go (тесты, TLS/SASL)
}

// Consumer читает топики группой, обрабатывает записи последовательно в пределах
// партиции через inbox и коммитит оффсет только после успеха (или после записи в DLQ).
type Consumer struct {
	cfg ConsumerConfig
	cl  *kgo.Client
	own *Producer // создан нами для DLQ
}

// NewConsumer создаёт консьюмера. Автокоммит выключен.
func NewConsumer(cfg ConsumerConfig) (*Consumer, error) {
	if cfg.Group == "" || len(cfg.Topics) == 0 || cfg.Pool == nil || cfg.Handler == nil {
		return nil, errors.New("kafka consumer: Group, Topics, Pool and Handler are required")
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = 200 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 5 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	opts := append([]kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...), kgo.ClientID(cfg.Group),
		kgo.ConsumerGroup(cfg.Group), kgo.ConsumeTopics(cfg.Topics...),
		kgo.DisableAutoCommit(), kgo.BlockRebalanceOnPoll(),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	}, cfg.Opts...)
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("kafka consumer: %w", err)
	}
	c := &Consumer{cfg: cfg, cl: cl}
	if cfg.DLQ == nil {
		p, err := NewProducer(cfg.Brokers, cfg.Group+"-dlq", cfg.Opts...)
		if err != nil {
			cl.Close()
			return nil, err
		}
		c.own, c.cfg.DLQ = p, p
	}
	return c, nil
}

// Run блокируется до отмены ctx.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		fetches := c.cl.PollFetches(ctx)
		if fetches.IsClientClosed() || ctx.Err() != nil {
			return ctx.Err()
		}
		fetches.EachError(func(t string, p int32, err error) {
			c.cfg.Log.ErrorContext(ctx, "kafka fetch error", "topic", t, "partition", p, "error", err)
		})
		var failed error
		fetches.EachRecord(func(r *kgo.Record) {
			if failed != nil {
				return
			}
			if err := c.handle(ctx, r); err != nil {
				failed = err
				return
			}
			if err := c.cl.CommitRecords(ctx, r); err != nil {
				c.cfg.Log.ErrorContext(ctx, "kafka commit failed", "error", err)
			}
		})
		c.cl.AllowRebalance()
		if failed != nil {
			return failed // отмена контекста во время обработки
		}
	}
}

// handle: до MaxAttempts попыток → DLQ. Возвращает ошибку только при отмене ctx.
func (c *Consumer) handle(ctx context.Context, r *kgo.Record) error {
	ev, err := decode(r)
	if err != nil {
		return c.dead(ctx, r, ev, err, 0)
	}
	var last error
	backoff := c.cfg.Backoff
	for attempt := 1; attempt <= c.cfg.MaxAttempts; attempt++ {
		processed, err := ProcessOnce(ctx, c.cfg.Pool, c.cfg.Group, ev, c.cfg.Handler)
		if err == nil {
			if !processed {
				c.cfg.Log.DebugContext(ctx, "duplicate event skipped", "event_id", ev.ID, "topic", r.Topic)
			}
			return nil
		}
		last = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.cfg.Log.WarnContext(ev.Context(ctx), "event handling failed", "event_id", ev.ID, "topic", r.Topic, "attempt", attempt, "error", err)
		if attempt < c.cfg.MaxAttempts {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			if backoff *= 2; backoff > c.cfg.MaxBackoff {
				backoff = c.cfg.MaxBackoff
			}
		}
	}
	return c.dead(ctx, r, ev, last, c.cfg.MaxAttempts)
}

// dead публикует запись в <topic>.dlq и возвращает nil (запись «закрыта»).
// Если DLQ недоступен — повторяем, пока жив ctx: терять сообщение хуже, чем ждать.
func (c *Consumer) dead(ctx context.Context, r *kgo.Record, ev Event, cause error, attempts int) error {
	if ev.ID.String() == "00000000-0000-0000-0000-000000000000" {
		ev.ID = newID()
	}
	ev.Topic = r.Topic + DLQSuffix
	ev.Key = string(r.Key)
	if ev.Key == "" {
		ev.Key = "unkeyed"
	}
	ev.Extra = map[string]string{
		HeaderDLQError:    cause.Error(),
		HeaderDLQSource:   fmt.Sprintf("%s/%d@%d", r.Topic, r.Partition, r.Offset),
		HeaderDLQAttempts: strconv.Itoa(attempts),
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	backoff := c.cfg.Backoff
	for {
		if err := c.cfg.DLQ.Publish(ctx, ev); err == nil {
			c.cfg.Log.ErrorContext(ctx, "event moved to DLQ", "topic", ev.Topic, "source", ev.Extra[HeaderDLQSource], "cause", cause.Error())
			return nil
		} else if ctx.Err() != nil {
			return ctx.Err()
		} else {
			c.cfg.Log.ErrorContext(ctx, "DLQ publish failed", "error", err)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff *= 2; backoff > c.cfg.MaxBackoff {
			backoff = c.cfg.MaxBackoff
		}
	}
}

// Close останавливает консьюмера (и собственный DLQ-продюсер).
func (c *Consumer) Close() {
	c.cl.Close()
	if c.own != nil {
		c.own.Close()
	}
}
