package schema

import (
	"fmt"
	"log/slog"

	"github.com/gocql/gocql"
)

// 2-phase migration:
// 	1. Bootstrap keyspace -> create keyspace in an un-anchored session
//	2. Migrate schema -> requires the session to be attached to keyspace
func bootstrapKeyspace(nodes []string, keyspace string, replication_factor int) error {

	slog.Info("[Bootstraping] Connecting to cluster")

	boot, err := gocql.NewCluster(nodes...).CreateSession(); if err != nil {
		slog.Error(fmt.Sprintf("Couldn't connect to cluster at: %v", nodes))
		return err
	}
	defer boot.Close()

	err = boot.Query( // explicit Query, because DDL isn't supported by gocqlx
		`CREATE KEYSPACE IF NOT EXISTS %s WITH REPLICATION = 
		{'class':'SimpleStrategy','replication_factor':%d}`,
		keyspace, replication_factor).Exec()
	if err != nil {
		slog.Error(fmt.Sprintf("[Bootstraping] Failed to create keyspace: %s", keyspace), "error", err)
		return err
	}


	slog.Info("[Bootstraping] Bootstrapped succesfully")
	return nil
}
