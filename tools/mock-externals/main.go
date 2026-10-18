// mock-externals — один бинарь с моками внешних систем платформы (план §5).
//
//	MOCKS=ping,payment,pos,production,courier,push   (модули появляются с S3…S9)
//	MOCK_DELAY=2s                                    задержка асинхронных эффектов
//	MOCK_<NAME>_ADDR=:9091                           переопределение адреса listener'а
//	KAFKA_BROKERS=localhost:29092
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pavelgr1408/food-platform/pkg/config"
	"github.com/pavelgr1408/food-platform/pkg/httpx"
	"github.com/pavelgr1408/food-platform/pkg/logging"
	"github.com/pavelgr1408/food-platform/tools/mock-externals/mock"
)

func main() {
	r := config.FromEnv()
	names := r.List("MOCKS", []string{"ping"})
	delay := r.Duration("MOCK_DELAY", 2*time.Second)
	brokers := r.List("KAFKA_BROKERS", []string{"localhost:29092"})
	logLevel := r.String("LOG_LEVEL", "info")
	log := logging.New(logging.Options{Service: "mock-externals", Level: logLevel})
	if err := r.Err(); err != nil {
		log.Error("configuration error", "error", err)
		os.Exit(1)
	}
	mods, err := mock.Build(names)
	if err != nil {
		log.Error("cannot build mocks", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	env := mock.NewEnv(log, brokers, delay)

	errc := make(chan error, len(mods))
	for _, m := range mods {
		rec := mock.NewRecorder(500)
		mux := http.NewServeMux()
		m.Register(mux, env)
		mock.MountAdmin(mux, env, rec)
		addr := config.FromEnv().String("MOCK_"+strings.ToUpper(m.Name())+"_ADDR", m.DefaultAddr())
		h := httpx.Chain(httpx.TrackRoute(mux), httpx.Standard(log), rec.Middleware)
		srv := httpx.NewServer(addr, h)
		if err := m.Start(ctx, env); err != nil {
			log.Error("mock start failed", "mock", m.Name(), "error", err)
			os.Exit(1)
		}
		log.Info("mock enabled", "mock", m.Name(), "addr", addr)
		go func() { errc <- httpx.Run(ctx, srv, log, 5*time.Second) }()
	}
	for range mods { // ждём остановки всех listener'ов
		if err := <-errc; err != nil {
			log.Error("mock server failed", "error", err)
			stop() // остальные завершатся gracefully
		}
	}
	env.Wait()
}
