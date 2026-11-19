module anti-interrupt

go 1.27.3

require (
	github.com/gocql/gocql v1.7.0
	github.com/scylladb/gocqlx/v3 v3.0.4
	github.com/spf13/cobra v1.10.2
)

replace github.com/gocql/gocql => github.com/scylladb/gocql v1.20.0

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/klauspost/compress v1.20.4 // indirect
	github.com/scylladb/go-reflectx v1.0.1 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/sync v0.25.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
)
