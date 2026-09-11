package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"
)

const maxDuckConns = 4

func Open(path string) (*DB, error) {
	return OpenWith(path, Options{})
}

func OpenWith(path string, opt Options) (*DB, error) {
	return openWith(path, opt, nil)
}

func openWith(path string, opt Options, wrap func(execer) execer) (*DB, error) {
	path, spill, err := prepareOpen(path)
	if err != nil {
		return nil, err
	}
	d := &DB{
		path:    path,
		spill:   spill,
		duckUI:  opt.DuckUI,
		mu:      make(chan struct{}, 1),
		maintCh: make(chan struct{}, 1),
		idx:     newSearchCache(path),
	}
	connector, err := duckdb.NewConnector(path, d.initConn)
	if err != nil {
		return nil, err
	}
	sqldb := sql.OpenDB(connector)
	sqldb.SetMaxOpenConns(1)
	sqldb.SetMaxIdleConns(1)
	d.sql = sqldb
	ctx := context.Background()
	if err := d.rejectUnsupportedSchema(ctx); err != nil {
		_ = d.Close()
		return nil, err
	}
	if _, err := sqldb.Exec(schema); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	if err := d.migrateSearch(ctx); err != nil {
		_ = d.Close()
		return nil, err
	}
	if err := d.migrateSchema(ctx); err != nil {
		_ = d.Close()
		return nil, err
	}
	if _, err := sqldb.Exec(schemaIndexes); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("schema indexes: %w", err)
	}
	var ex execer = sqldb
	if wrap != nil {
		ex = wrap(sqldb)
	}
	if err := bootstrapTrusted(ex, opt); err != nil {
		_ = d.Close()
		return nil, err
	}
	if err := d.lockSQL(); err != nil {
		_ = d.Close()
		return nil, err
	}
	d.settingsReady.Store(true)
	sqldb.SetMaxOpenConns(maxDuckConns)
	sqldb.SetMaxIdleConns(maxDuckConns)
	if d.writer, err = d.openReserved(ctx); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("writer conn: %w", err)
	}
	if d.maint, err = d.openReserved(ctx); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("maintenance conn: %w", err)
	}
	if opt.DuckUI {
		if err := d.DisableSearchAccel(ctx); err != nil {
			_ = d.Close()
			return nil, err
		}
	} else if err := d.ClearState(ctx, stateSearchAccel); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) initConn(ex driver.ExecerContext) error {
	if d.settingsReady.Load() {
		return nil
	}
	return execSetDriver(ex, "temp_directory", d.spill)
}

func execSetDriver(ex driver.ExecerContext, name, value string) error {
	q := "SET " + name + " = '" + strings.ReplaceAll(value, "'", "''") + "'"
	if _, err := ex.ExecContext(context.Background(), q, nil); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func bootstrapTrusted(ex execer, opt Options) error {
	if opt.DuckUI {
		if err := loadExtension(ex, "ui"); err != nil {
			return fmt.Errorf("duckdb ui: %w", err)
		}
	}
	return nil
}

func loadExtension(ex execer, name string) error {
	ctx := context.Background()
	if _, err := ex.ExecContext(ctx, "LOAD "+name); err == nil {
		return nil
	}
	if _, err := ex.ExecContext(ctx, "INSTALL "+name); err != nil {
		return err
	}
	_, err := ex.ExecContext(ctx, "LOAD "+name)
	return err
}

func (d *DB) lockSQL() error {
	if _, err := d.sql.Exec("SET enable_external_access = false"); err != nil {
		return fmt.Errorf("enable_external_access: %w", err)
	}
	if _, err := d.sql.Exec("SET lock_configuration = true"); err != nil {
		return fmt.Errorf("lock_configuration: %w", err)
	}
	return nil
}

func (d *DB) openReserved(ctx context.Context) (*sql.Conn, error) {
	c, err := d.sql.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if err := d.verifyConn(ctx, c); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (d *DB) reserved(ctx context.Context, slot **sql.Conn) (*sql.Conn, error) {
	if *slot != nil {
		if err := pingReserved(*slot); err == nil {
			return *slot, nil
		}
		_ = (*slot).Close()
		*slot = nil
	}
	c, err := d.openReserved(ctx)
	if err != nil {
		return nil, err
	}
	*slot = c
	return c, nil
}

func (d *DB) dropReserved(slot **sql.Conn) {
	if *slot == nil {
		return
	}
	_ = (*slot).Close()
	*slot = nil
}

func pingReserved(c *sql.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return c.PingContext(ctx)
}

func (d *DB) verifyConn(ctx context.Context, c *sql.Conn) error {
	if !d.settingsReady.Load() {
		return nil
	}
	spill, err := querySetting(ctx, c, "temp_directory")
	if err != nil {
		return err
	}
	if filepath.Clean(strings.Trim(strings.TrimSpace(spill), `"'`)) != filepath.Clean(d.spill) {
		return fmt.Errorf("temp_directory %q != %q", spill, d.spill)
	}
	ext, err := querySetting(ctx, c, "enable_external_access")
	if err != nil {
		return err
	}
	if !settingFalse(ext) {
		return fmt.Errorf("enable_external_access is %q", ext)
	}
	lock, err := querySetting(ctx, c, "lock_configuration")
	if err != nil {
		return err
	}
	if !settingTrue(lock) {
		return fmt.Errorf("lock_configuration is %q", lock)
	}
	return nil
}

func querySetting(ctx context.Context, c *sql.Conn, name string) (string, error) {
	var v string
	err := c.QueryRowContext(ctx, "SELECT current_setting('"+name+"')").Scan(&v)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return v, nil
}

func settingTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes":
		return true
	default:
		return false
	}
}

func settingFalse(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "0", "no":
		return true
	default:
		return false
	}
}

func (d *DB) Close() error {
	d.stopMaint()
	var err error
	if d.idx != nil {
		err = errors.Join(err, d.idx.Close())
	}
	if d.writer != nil {
		err = errors.Join(err, d.writer.Close())
		d.writer = nil
	}
	if d.maint != nil {
		err = errors.Join(err, d.maint.Close())
		d.maint = nil
	}
	if d.sql != nil {
		err = errors.Join(err, d.sql.Close())
	}
	return err
}

func (d *DB) SQL() *sql.DB {
	return d.sql
}

func (d *DB) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	if err := d.acquire(ctx); err != nil {
		return err
	}
	defer d.release()
	return d.withWriterTx(ctx, fn)
}

func (d *DB) withWriterTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.beginWriter(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) beginWriter(ctx context.Context) (*sql.Tx, error) {
	c, err := d.reserved(ctx, &d.writer)
	if err != nil {
		return nil, err
	}
	tx, err := c.BeginTx(ctx, nil)
	if err == nil {
		return tx, nil
	}
	if !deadConn(err) {
		return nil, err
	}
	d.dropReserved(&d.writer)
	c, err = d.reserved(ctx, &d.writer)
	if err != nil {
		return nil, err
	}
	return c.BeginTx(ctx, nil)
}

func deadConn(err error) bool {
	return err != nil && (errors.Is(err, sql.ErrConnDone) || errors.Is(err, driver.ErrBadConn))
}
