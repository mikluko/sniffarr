// Package database opens the SQLite database that the store and the queue
// share, and applies each one's migrations.
package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"runtime"
	"slices"
	"strings"

	"github.com/XSAM/otelsql"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	_ "modernc.org/sqlite"
)

// DB is a SQLite database in WAL mode. W is its only writing connection; R
// is a pool for reads. It is safe for concurrent use.
type DB struct {
	W, R *sql.DB
	path string
	regs []metric.Registration
}

// Options configures Open.
type Options struct {
	// Meter records the statement and pool metrics; the global MeterProvider
	// when nil.
	Meter metric.MeterProvider
	// Tracer creates the statement spans; the global TracerProvider when nil.
	Tracer trace.TracerProvider
}

// poolKey tells the write pool from the read pool on spans and metrics.
const poolKey = attribute.Key("db.pool")

// Open opens or creates the database at path. A statement gets a span only
// when its context carries a recording span; every statement is measured in
// db.client.operation.duration, with the Go function that issued it as
// code.function.name. Spans carry the statement text, never its arguments.
// The WAL's size is the gauge sniffarr.db.wal.size.
func Open(ctx context.Context, path string, opts Options) (*DB, error) {
	q := url.Values{"_pragma": {"journal_mode(WAL)", "busy_timeout(10000)", "foreign_keys(1)", "synchronous(NORMAL)",
		fmt.Sprintf("journal_size_limit(%d)", journalSizeLimit)}}
	dsn := "file:" + path + "?" + q.Encode()
	db := &DB{path: path}
	var err error
	if db.W, err = db.open(dsn+"&_txlock=immediate", "write", opts); err != nil {
		return nil, err
	}
	db.W.SetMaxOpenConns(1)
	if db.R, err = db.open(dsn, "read", opts); err != nil {
		db.Close()
		return nil, err
	}
	mp := opts.Meter
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	if err := db.observeWAL(mp.Meter("github.com/mikluko/sniffarr/internal/database")); err != nil {
		db.Close()
		return nil, err
	}
	_, err = db.W.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (component TEXT PRIMARY KEY, version INTEGER NOT NULL) WITHOUT ROWID")
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) open(dsn, pool string, opts Options) (*sql.DB, error) {
	o := []otelsql.Option{
		otelsql.WithAttributes(semconv.DBSystemNameSQLite, poolKey.String(pool)),
		otelsql.WithSpanOptions(otelsql.SpanOptions{
			OmitRows:             true,
			OmitConnResetSession: true,
			OmitConnectorConnect: true,
			SpanFilter: func(ctx context.Context, _ otelsql.Method, _ string, _ []driver.NamedValue) bool {
				return trace.SpanFromContext(ctx).IsRecording()
			},
		}),
		otelsql.WithAttributesGetter(caller),
		otelsql.WithInstrumentAttributesGetter(caller),
	}
	if opts.Meter != nil {
		o = append(o, otelsql.WithMeterProvider(opts.Meter))
	}
	if opts.Tracer != nil {
		o = append(o, otelsql.WithTracerProvider(opts.Tracer))
	}
	d, err := otelsql.Open("sqlite", dsn, o...)
	if err != nil {
		return nil, err
	}
	reg, err := otelsql.RegisterDBStatsMetrics(d, o...)
	if err != nil {
		d.Close()
		return nil, err
	}
	db.regs = append(db.regs, reg)
	return d, nil
}

// caller returns code.function.name for the innermost function on the stack
// outside database/sql and otelsql, or nothing when there is none, as on the
// pool's own connection opener.
func caller(context.Context, otelsql.Method, string, []driver.NamedValue) []attribute.KeyValue {
	var pcs [32]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs[:])])
	for {
		f, more := frames.Next()
		if !strings.HasPrefix(f.Function, "database/sql.") && !strings.HasPrefix(f.Function, "github.com/XSAM/otelsql") && !strings.HasPrefix(f.Function, "runtime.") {
			return []attribute.KeyValue{semconv.CodeFunctionName(f.Function)}
		}
		if !more {
			return nil
		}
	}
}

// Close closes the database.
func (db *DB) Close() error {
	var errs []error
	for _, reg := range db.regs {
		errs = append(errs, reg.Unregister())
	}
	for _, d := range []*sql.DB{db.R, db.W} {
		if d != nil {
			errs = append(errs, d.Close())
		}
	}
	return errors.Join(errs...)
}

// Migrate applies the *.sql files of migrations that component has not
// applied yet, in name order, each in a transaction of its own.
func (db *DB) Migrate(ctx context.Context, component string, migrations fs.FS) error {
	names, err := fs.Glob(migrations, "*.sql")
	if err != nil {
		return err
	}
	slices.Sort(names)
	var version int
	err = db.R.QueryRowContext(ctx, "SELECT version FROM schema_migrations WHERE component = ?", component).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for i := version; i < len(names); i++ {
		body, err := fs.ReadFile(migrations, names[i])
		if err != nil {
			return err
		}
		err = db.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return fmt.Errorf("%s %s: %w", component, names[i], err)
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (component, version) VALUES (?, ?) ON CONFLICT (component) DO UPDATE SET version = excluded.version", component, i+1)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Tx runs f in a write transaction, committing when f returns nil.
func (db *DB) Tx(ctx context.Context, f func(*sql.Tx) error) error {
	tx, err := db.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}
