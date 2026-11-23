package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"anti-interrupt/internal/config"
)

func Run(ctx context.Context, agentCfg config.AgentConfig, cassCfg config.CassandraConfig) error {
	log := slog.With("service", "backend")

    log.Info("Starting")

	// Routing
	mux := http.NewServeMux()
	mux.HandleFunc("/", RootHandler)

	srv := &http.Server {
		Addr: net.JoinHostPort(agentCfg.Address, strconv.Itoa(agentCfg.Port)),
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
	
	err := srv.Shutdown(shutdownCtx); if err != nil {
		srv.Close()
		return fmt.Errorf("Shutdown: %w", err)
	}
	
	<-errCh // Drain error channel
	log.Info("Stopped")
    return nil
}
