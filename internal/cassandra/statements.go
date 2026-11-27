package cassandra

// All prepared statements go here as variables
import "github.com/scylladb/gocqlx/v3/qb"

// SELECT id, name FROM users WHERE id = ? ALLOW FILTERING
var TestStmt, TestParams = qb.Select("").
	Columns("id", "name").
	Where(qb.Eq("id")).
	AllowFiltering().
	ToCql()
