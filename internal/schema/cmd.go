package schema

import (
	"context"
	"log/slog"
	"anti-interrupt/internal/config"
    "github.com/spf13/cobra"
)

func NewCommand() *cobra.Command {
	var cassCfg config.CassandraConfig
    cmd := &cobra.Command{
        Use:   "migrate",
        Short: "Cassandra db migration script",
        RunE: func(cmd *cobra.Command, _ []string) error {
            return Run(cmd.Context(), cassCfg)
        },
    }
	
	defaultnodes := make([]string, 1)
	defaultnodes[0] = "127.0.0.1:9042"
	cmd.Flags().StringSliceVar(
		&cassCfg.Nodes, "nodes", defaultnodes, 
		"Cassandra cluster node address:port pair (repeatable)",
		)

	cmd.Flags().IntVar(
		&cassCfg.Replication_factor, "rf", 3, 
		"Replication factor of the project keyspace (default: 3)",
		)

	cmd.Flags().StringVar(
		&cassCfg.Keyspace, "keyspace", "anti_interrupt",
		"Cassandra keyspace of the project (default: anti_interrupt)",
		)

	cmd.Flags().BoolVar(
		&cassCfg.Reset, "clean", false, 
		"Clean entire keyspace before applying migrations",
		)

    return cmd
}

func Run(ctx context.Context, cfg config.CassandraConfig) error {
	log := slog.With("service", "migration")

	err := cfg.Validate(); if err != nil {
		log.Error("Config validation error: ", "error", err)
		return err
	}

	if cfg.Reset {
		err = truncKeyspace(cfg.Nodes, cfg.Keyspace); if err != nil {
			log.Error("Pre-migration clean error: ", "error", err)
			return err
		}
	}

	// Bootstrapping & migrations
	log.Info("Running migrations")

	err = bootstrapKeyspace(cfg.Nodes, cfg.Keyspace, cfg.Replication_factor); if err != nil {
		log.Error("Error during bootstrapping: ", "error", err)
		return err
	}
	err = runMigration(cfg.Nodes, cfg.Keyspace); if err != nil {
		log.Error("Error during migration: ", "error", err)	
		return err
	}

    <-ctx.Done()

    log.Info("shutting down")
    return nil
}
