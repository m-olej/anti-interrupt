package backend

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"anti-interrupt/internal/schema"
)

func Run(ctx context.Context, cfg Config) error {
	log := slog.With("service", "backend")

    log.Info("Starting")

	// Bootstrapping & migrations
	log.Info("Running migrations")

	err := schema.BootstrapKeyspace(cfg.nodes, cfg.keyspace, cfg.rf); if err != nil {
		return err
	}
	schema.Migrate(cfg.nodes, cfg.keyspace)

	// Routing
	mux := http.NewServeMux()
	mux.HandleFunc("/", RootHandler)

	srv := &http.Server {
		Addr: net.JoinHostPort(cfg.host, strconv.Itoa(cfg.port)),
		Handler: mux,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	log.Info("Listening", "addr", srv.Addr)

	select {
		case <- ctx.Done():
			log.Info("Received stop signal")
		case err := <- errCh:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
		return fmt.Errorf("Server: %w", err)
	}

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(ctx, 10 * time.Second)
	defer cancel()
	
	err = srv.Shutdown(shutdownCtx); if err != nil {
		srv.Close()
		return fmt.Errorf("Shutdown: %w", err)
	}
	
	<-errCh // Drain error channel
	log.Info("Stopped")
    return nil
}
