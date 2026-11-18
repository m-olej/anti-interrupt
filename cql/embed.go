package cql

import "embed"

// A compiler directive to embed the migration files inside the binary
//go:embed *.cql
var Migrations embed.FS

