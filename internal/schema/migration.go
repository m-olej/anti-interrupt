package schema

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/scylladb/gocqlx/v3"
	"github.com/scylladb/gocqlx/v3/migrate"

	"anti-interrupt/cql"
)


func runMigration(session gocqlx.Session) error {

	pending, err := migrate.Pending(context.Background(), session, cql.Migrations); if err != nil {
		slog.Error("Pending migration error:", "error", err);
		return err
	}

	migrations := len(pending)
	if  migrations != 0 {
		slog.Info(fmt.Sprintf("Applying %d migrations", migrations))
		if err := migrate.FromFS(context.Background(), session, cql.Migrations); err != nil {
			slog.Error("Migration error:", "error", err)
			return err
		}
		slog.Info("Migrations completed")
	} 

	return nil
}

func truncKeyspace(session gocqlx.Session) error {

	tablesToDrop := []string{
		"gocqlx_migrate",
		"audio_telemetry", 
		"floor_claims",
		"completed_speech_turns",
		"room_call_counters",
		"floor_heartbeats",
		"speaker_profiles",
		"active_room_by_user",
	}
	for _, table := range tablesToDrop {
		stmt := "DROP TABLE IF EXISTS " + table
		if err := session.ExecStmt(stmt); err != nil {
			return fmt.Errorf("Failed to execute: %s", stmt)
		}
	}

	return nil
}
