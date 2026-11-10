package pg

// Integration smoke substrate for the v2 pg-impl repos (Slice 14).
//
// pgxmock (used by the *_test.go unit tests) matches SQL strings against
// expectations but never executes real Postgres. This file stands up a
// real, throwaway PostgreSQL via fergusstrange/embedded-postgres/v2 so the
// substrate semantics pgxmock can't validate — advisory locks, CHECK
// constraints, FK CASCADE, partial unique indexes, ON CONFLICT inference,
// array casts, nextval-in-Tx, gen-marker resulting state — get exercised
// against a genuine server.
//
// Schema setup mirrors production deploy (infrastructure/playbooks/deploy.yml):
// load the prod schema baseline migrations/schema.sql FIRST, then apply the
// post-baseline *_up.sql migrations on top, in sorted order. This is the
// single source of truth — no parallel "test schema" file.
//
// Lazy startup: embedded-pg boots at most once per package process, on the
// first test that calls requireIntegration. `go test -short` skips all of
// it (and pays no startup cost), so unit-only runs stay fast.
//
// pgxmock is retained permanently for error-injection paths real pg can't
// easily reproduce (Tx commit failure, RowsAffected surfacing). The two
// layers are complementary, not redundant.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	integrationTemplatePrefix = "salem_smoke_template_"
	integrationDBPrefix       = "salem_smoke_test_"
)

var (
	epgOnce    sync.Once
	epgErr     error
	epg        *embeddedpostgres.EmbeddedPostgres
	adminDSN   string // points at the server's default "postgres" db
	templateDB string // pre-migrated template cloned per test
)

// requireIntegration skips under `go test -short`, otherwise lazily boots
// the embedded server and builds the migrated template database (once per
// package process). Call it at the top of every integration test.
func requireIntegration(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping postgres integration test in -short mode")
	}
	epgOnce.Do(startEmbeddedPostgres)
	if epgErr != nil {
		t.Fatalf("start embedded postgres: %v", epgErr)
	}
}

// TestMain stops the embedded server after the package's tests finish, but
// only if a test actually started it (lazy: -short or unit-only runs never
// boot it, so there's nothing to stop). Close removes the instance's
// workspace and data with it.
//
// If the test process dies before this runs (panic, -timeout kill, Ctrl-C),
// v2's supervisor, a separate process that watches this one, stops postgres.
// The instance's workspace (about 70 MB under the OS temp directory) stays
// behind; only Close removes it, and OS temp cleanup reclaims it later.
// Each instance gets its own uniquely named workspace, so a leftover cannot
// block the next run. (v1 shared one extraction directory and erased it on
// every start, so an orphaned postgres holding a file open there wedged
// every later run.)
//
// eptest.Run is v2's TestMain helper, but it starts the server eagerly; the
// lazy start here keeps -short and unit-only runs free of it.
func TestMain(m *testing.M) {
	code := m.Run()
	if epg != nil {
		_ = epg.Close()
	}
	os.Exit(code)
}

func startEmbeddedPostgres() {
	// Target PostgreSQL 17 to match the production baseline dump
	// (migrations/schema.sql is pg_dump from prod 17.10, and uses pg17
	// features like the \restrict guard). v2's default is 18.
	//
	// Encoding UTF8 + Locale C: the Windows initdb default is WIN1252,
	// which can't store UTF-8 content the migrations carry (e.g. "→" in
	// comments). Prod is UTF8; the C locale pairs with any encoding and
	// avoids Windows locale-name quirks. Collation/locale parity is
	// explicitly out of scope for this smoke harness. (These are also v2's
	// defaults; they stay explicit because the harness depends on them.)
	//
	// Port(0) lets the server pick a free loopback port; ConnectionURL()
	// reports it after Start. No DataPath: the cluster lives in the
	// instance's workspace, so initdb runs fresh with these settings and
	// Close removes it.
	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V17).
		Port(0).
		Encoding("UTF8").
		Locale("C")
	epg = embeddedpostgres.NewDatabase(cfg)
	if err := epg.Start(); err != nil {
		// A failed Start cleans up its own workspace. TestMain skips a nil
		// epg, so there is nothing left for it to stop.
		epgErr = fmt.Errorf("embedded-postgres start: %w", err)
		epg = nil
		return
	}

	adminDSN = epg.ConnectionURL()

	templateDB = integrationTemplatePrefix + randomHex16()
	if err := createTemplate(context.Background(), templateDB); err != nil {
		epgErr = fmt.Errorf("build template database: %w", err)
		return
	}
}

// createTemplate creates an empty database, applies the full schema
// (baseline + post-baseline migrations) into it, and leaves it as the
// clone source. The pool is closed before returning so the template has
// no open connections (a precondition for CREATE DATABASE ... TEMPLATE).
func createTemplate(ctx context.Context, name string) error {
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("admin pool: %w", err)
	}
	defer admin.Close()

	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoteIdent(name)); err != nil {
		return fmt.Errorf("create template db: %w", err)
	}

	pool, err := pgxpool.New(ctx, dsnForDB(name))
	if err != nil {
		return fmt.Errorf("template pool: %w", err)
	}
	defer pool.Close()

	if err := applyAllMigrations(ctx, pool); err != nil {
		return err
	}
	return nil
}

// integrationFixture is one isolated test database cloned from the
// pre-migrated template.
type integrationFixture struct {
	Pool   *pgxpool.Pool
	DBName string
}

// newFixture clones a fresh database from the template (Postgres does a
// filesystem-level copy — far cheaper than re-running migrations) and
// returns a pool against it. The database is dropped on test cleanup.
func newFixture(t *testing.T) *integrationFixture {
	t.Helper()
	requireIntegration(t)

	dbName := integrationDBPrefix + randomHex16()
	ctx := t.Context()

	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	defer admin.Close()

	if _, err := admin.Exec(ctx,
		fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", quoteIdent(dbName), quoteIdent(templateDB))); err != nil {
		t.Fatalf("clone test db: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsnForDB(dbName))
	if err != nil {
		t.Fatalf("test pool: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		dropDatabase(dbName)
	})
	return &integrationFixture{Pool: pool, DBName: dbName}
}

// dropDatabase removes a test database. Prefix-guarded: it refuses to drop
// anything not created by this harness, so a bug in name handling can't
// take out an unrelated database.
func dropDatabase(name string) {
	if !strings.HasPrefix(name, integrationDBPrefix) {
		return
	}
	admin, err := pgxpool.New(context.Background(), adminDSN)
	if err != nil {
		return
	}
	defer admin.Close()
	// WITH (FORCE) terminates any lingering connections so the drop
	// doesn't hang on a leaked session.
	_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+quoteIdent(name)+" WITH (FORCE)")
}

// applyAllMigrations applies the prod schema baseline, then every
// post-baseline *_up.sql migration in sorted (chronological) order.
func applyAllMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	dir, err := findMigrationsDir()
	if err != nil {
		return err
	}

	// Everything runs on ONE pinned connection. The baseline dump sets
	// `search_path = ''` (persistent, session-level) so its own
	// schema-qualified DDL is unambiguous — but that state leaks to
	// whatever connection runs next. If migrations rotated onto a pooled
	// connection that still had the empty search_path, their unqualified
	// table refs (ALTER TABLE pay_ledger ...) would fail with "relation
	// does not exist". Pinning one connection + resetting search_path
	// after the baseline avoids it. (Prod doesn't hit this: deploy loads
	// schema.sql via psql, then the migration runner connects fresh.)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()

	// 1. Baseline: migrations/schema.sql (prod pg_dump). Strip psql
	//    meta-commands (\restrict / \unrestrict) — those are psql client
	//    directives, not SQL, and the pgx wire protocol can't run them.
	baseline, err := os.ReadFile(filepath.Join(dir, "schema.sql"))
	if err != nil {
		return fmt.Errorf("read schema baseline: %w", err)
	}
	if _, err := conn.Exec(ctx, stripPsqlMetaCommands(string(baseline))); err != nil {
		return fmt.Errorf("apply schema baseline: %w", err)
	}

	// Restore a normal search_path so post-baseline migrations resolve
	// unqualified identifiers against public.
	if _, err := conn.Exec(ctx, "SET search_path TO public, pg_catalog"); err != nil {
		return fmt.Errorf("reset search_path: %w", err)
	}

	// 2. Post-baseline migrations, sorted. The ZBBS-WORK-NNN- prefix
	//    sorts chronologically by lexical order (zero-padded numbers).
	ups, err := upMigrations(dir)
	if err != nil {
		return err
	}
	for _, name := range ups {
		sqlBytes, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := conn.Exec(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}

// upMigrations returns the sorted list of *_up.sql filenames in dir — empty
// when the baseline has folded them all in (after a re-snapshot the migration
// files are deleted and schema.sql alone is the whole schema, LLM-43). The
// wrong-dir guard lives in applyAllMigrations' schema.sql read, which fails
// loudly if the dir is wrong or the baseline is missing, so an empty list here
// no longer needs to be treated as an error.
func upMigrations(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var ups []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), "_up.sql") {
			ups = append(ups, e.Name())
		}
	}
	sort.Strings(ups)
	return ups, nil
}

// stripPsqlMetaCommands removes lines that are psql backslash directives
// (e.g. \restrict, \unrestrict) which pg_dump 17+ emits but the pgx
// protocol cannot execute. Everything else is left byte-for-byte.
func stripPsqlMetaCommands(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24) // tolerate long dump lines
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(line), `\`) {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// findMigrationsDir locates the repo's migrations/ directory by walking up
// from this source file (runtime.Caller) until a sibling "migrations" dir
// is found. Avoids brittle ../../../../ relative paths.
func findMigrationsDir() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		candidate := filepath.Join(dir, "migrations")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("migrations dir not found walking up from %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
}

// dsnForDB returns the admin DSN with its database path swapped to name.
// Parsed via net/url rather than string replacement so it's robust to the
// shape of the DSN.
func dsnForDB(name string) string {
	u, err := url.Parse(adminDSN)
	if err != nil {
		// adminDSN is built by us and always valid; fall back defensively.
		return adminDSN
	}
	u.Path = "/" + name
	// ConnectionURL sets an escaped path; clear it so String() encodes the
	// new Path instead of keeping the old database name.
	u.RawPath = ""
	return u.String()
}

// quoteIdent safely quotes a SQL identifier for interpolation into
// CREATE/DROP DATABASE (which can't be parameterized). Generated names are
// already restricted to [a-z0-9_], so this is belt-and-suspenders.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func randomHex16() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is catastrophic and not expected; panic in
		// test-support code is acceptable.
		panic(fmt.Sprintf("randomHex16: %v", err))
	}
	return hex.EncodeToString(b[:])
}
