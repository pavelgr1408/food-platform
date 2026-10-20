// gateway-service: единая точка входа (S5, ADR-11). Проверяет JWT локально по JWKS auth-service,
// применяет лимиты/CORS/ограничение тела и проксирует запросы по таблице маршрутов.
// Своей БД и Kafka нет. /metrics — на отдельном порту (METRICS_ADDR), наружу не публикуется.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/authclient"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/logging"
	"github.com/pavelgr1408/food-platform/pkg/metrics"
	"github.com/pavelgr1408/food-platform/services/gateway/internal/clientip"
	"github.com/pavelgr1408/food-platform/services/gateway/internal/config"
	"github.com/pavelgr1408/food-platform/services/gateway/internal/gateway"
	"github.com/pavelgr1408/food-platform/services/gateway/internal/introspect"
	"github.com/pavelgr1408/food-platform/services/gateway/internal/routes"
)

func main() {
	cfg, err := config.Load()
	log := logging.New(logging.Options{Service: "gateway", Env: cfg.Env, Level: cfg.LogLevel})
	if err != nil {
		log.Error("configuration error", "error", err)
		os.Exit(1)
	}
	if err := run(cfg, log); err != nil {
		log.Error("service stopped with error", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	table, err := routes.Load()
	if err != nil {
		return err
	}
	verifier, err := authclient.NewJWKSVerifier(authclient.JWKSOptions{
		URL: cfg.Auth.JWKSURL, Issuer: cfg.Auth.Issuer, Audience: cfg.Auth.Audience, Log: log,
	})
	if err != nil {
		return err
	}
	// Старт не зависит от доступности auth: ключи подтягиваются в фоне, до этого readiness = 503
	// (трафик не идёт), а liveness не страдает, поэтому под не уходит в рестарт-цикл.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); warmup(ctx, verifier, log) }()

	reg := metrics.New("gateway")
	handler, err := gateway.New(gateway.Deps{
		Log: log, Metrics: reg, Routes: table, Upstreams: cfg.Upstreams,
		Verifier:     verifier,
		Introspector: introspect.New(cfg.Upstreams["auth"].String(), cfg.IntrospectCacheTTL, cfg.IntrospectTimeout),
		ClientIP:     clientip.Resolver{Trusted: cfg.TrustedProxyCIDRs},
		CORSOrigins:  cfg.CORSOrigins,
		MaxBody:      cfg.MaxBodyBytes,
		Timeout:      cfg.UpstreamTimeout,
		Limits: gateway.Limits{
			RPS: cfg.RateRPS, Burst: cfg.RateBurst, AuthPerMin: cfg.AuthRatePerMin, AuthBurst: cfg.AuthBurst,
		},
		Checks: []httpx.Check{{Name: "jwks", Fn: func(context.Context) error {
			if !verifier.Ready() {
				return errors.New("no signing keys loaded yet")
			}
			return nil
		}}},
	})
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", reg.Handler())
	metricsSrv := httpx.NewServer(cfg.MetricsAddr, mux)

	errc := make(chan error, 2)
	go func() { errc <- httpx.Run(ctx, metricsSrv, log, 5*time.Second) }()
	go func() {
		errc <- httpx.Run(ctx, httpx.NewServer(":"+cfg.HTTPPort, handler), log, 10*time.Second)
	}()
	// Любой из серверов завершился (ошибка или сигнал) — останавливаем второй.
	err = <-errc
	stop()
	err = errors.Join(err, <-errc)
	wg.Wait()
	return err
}

// warmup повторяет загрузку JWKS с нарастающей паузой, пока ключи не получены или не отменён ctx.
func warmup(ctx context.Context, v *authclient.JWKSVerifier, log *slog.Logger) {
	delay := time.Second
	for {
		wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := v.Warmup(wctx)
		cancel()
		if err == nil {
			log.Info("jwks loaded")
			return
		}
		log.Warn("jwks not available yet", "error", err, "retry_in", delay.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < 15*time.Second {
			delay *= 2
		}
	}
}
