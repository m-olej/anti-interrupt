package schema

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3"
	"github.com/scylladb/gocqlx/v3/migrate"

	"anti-interrupt/cql"
)


func Migrate(nodes []string, keyspace string) error {
	cl := gocql.NewCluster(nodes...)


	// Anchored session
	cl.Keyspace = keyspace
	sess, err := gocqlx.WrapSession(cl.CreateSession()); if err != nil {
		slog.Error("[Migration] Couldn't create anti_interrupt session")
		return err
	}
	defer sess.Close()

	pending, err := migrate.Pending(context.Background(), sess, cql.Migrations); if err != nil {
		slog.Error("Pending migration error:", "error", err);
	}
	migrations := len(pending)
	if  migrations != 0 {
		slog.Info(fmt.Sprintf("Applying %d migrations", migrations))
		if err := migrate.FromFS(context.Background(), sess, cql.Migrations); err != nil {
			slog.Error("Migration error:", "error", err)
		}
		slog.Info("Migrations completed")
	} 

	return nil
}
