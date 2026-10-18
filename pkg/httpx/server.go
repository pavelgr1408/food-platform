package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// NewServer — http.Server с безопасными таймаутами (защита от slowloris).
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
}

// Run запускает сервер и блокируется до ctx.Done() или фатальной ошибки;
// затем корректно останавливает (graceful shutdown). Передавайте ctx из signal.NotifyContext.
func Run(ctx context.Context, srv *http.Server, log *slog.Logger, shutdownTimeout time.Duration) error {
	errc := make(chan error, 1)
	go func() {
		log.Info("http server started", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	log.Info("http server shutting down")
	return srv.Shutdown(sctx)
}
