module github.com/Shaj2x/devassist/services/indexer

go 1.24.0

toolchain go1.24.7

require (
	github.com/Shaj2x/devassist/libs/gocommon v0.0.0
	github.com/alicebob/miniredis/v2 v2.37.0
	github.com/jackc/pgx/v5 v5.7.5
	github.com/pgvector/pgvector-go v0.3.0
	github.com/redis/go-redis/v9 v9.7.3
	github.com/tree-sitter/go-tree-sitter v0.25.0
	github.com/tree-sitter/tree-sitter-go v0.25.0
	github.com/tree-sitter/tree-sitter-javascript v0.25.0
	github.com/tree-sitter/tree-sitter-python v0.25.0
	github.com/tree-sitter/tree-sitter-typescript v0.23.2
	golang.org/x/sync v0.13.0
	golang.org/x/time v0.14.0
)

require (
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.15.9 // indirect
	github.com/mattn/go-pointer v0.0.1 // indirect
	github.com/pierrec/lz4/v4 v4.1.15 // indirect
	github.com/segmentio/kafka-go v0.4.48 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/text v0.24.0 // indirect
)

// Resolve the shared library from the monorepo, so the service builds the
// same way with go.work (local dev) and without it (Docker, GOWORK=off).
replace github.com/Shaj2x/devassist/libs/gocommon => ../../libs/gocommon
