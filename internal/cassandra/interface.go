package cassandra

import (
	"time"

	"github.com/scylladb/gocqlx/v3"
)

type CassOp interface {
	execute() (any, error)
}

type OpConfig struct {
	session gocqlx.Session
	timeout time.Duration
}

// Abstracted cassandra interface
// - each specific db request should be implemented as a function
// - takes session as input
// - uses prepared statement (from statements.go)
// - Query should be adequately configured (idempodency flag, retry policy, ...)
// - Do not rely on query level timeouts, use context.WithTimeout and wrap the request with it

// Functions:
// - Telemetry upsert
// - Claim insert
// - Heartbeat upsert
// - Completed turn (UUID)
// - Counter increment
// - Lookup-batch
