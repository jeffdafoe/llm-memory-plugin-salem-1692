module github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine

go 1.26.0

toolchain go1.27.4

require (
	github.com/coder/websocket v1.8.15
	github.com/fergusstrange/embedded-postgres/v2 v2.0.1
	github.com/jackc/pgx/v5 v5.13.0
	github.com/pashagolub/pgxmock/v6 v6.0.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/telemetry v0.0.0-20261010193011-f71dc92da45e // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
	golang.org/x/vuln v1.9.0 // indirect
)

tool golang.org/x/vuln/cmd/govulncheck
