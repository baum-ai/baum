// Package sqliteread reads local SQLite stores that agents keep their session
// data in — today, Cursor's `state.vscdb` chat store.
//
// It is a thin read-only layer over modernc.org/sqlite, a pure-Go SQLite that
// keeps the platform's CGO_ENABLED=0 builds working. The package exists to
// concentrate the rules for touching someone's live editor database in one
// place:
//
//   - Stores are opened with the URI parameter `mode=ro`, so SQLite refuses
//     every write to the store. A store being read while Cursor is running is
//     the normal case, and WAL-mode SQLite serves concurrent readers.
//   - A write-ahead log is applied, so rows Cursor has not checkpointed yet
//     are visible. This is why `mode=ro` is used rather than `immutable=1`,
//     which would ignore the log and quietly report stale data.
//   - Opening a WAL-mode store makes SQLite create a `<path>-shm` coordination
//     file beside it when none exists. That file is ephemeral bookkeeping, not
//     a change to the store; the store itself is never modified.
//   - If the store cannot be opened in place at all — a read-only directory,
//     say — it is copied to a temporary file and the copy is read instead, so
//     the answer stays correct rather than silently degrading.
//
// The surface is deliberately small: list the tables, scan one wholly or by an
// indexed text prefix. Callers get decoded rows and do their own interpretation.
package sqliteread

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// driverName is the driver modernc.org/sqlite registers.
const driverName = "sqlite"

// magic opens every SQLite database file.
const magic = "SQLite format 3\x00"

// ErrNotSQLite reports a file that does not carry the SQLite header magic.
var ErrNotSQLite = errors.New("not a SQLite database file")

// ErrStopScan ends a Scan early without reporting an error.
var ErrStopScan = errors.New("stop scan")

// Value is one decoded column value: nil, int64, float64, string, or []byte.
// It is an alias rather than a named type so that database/sql can scan
// directly into it, which it only does for a plain any.
type Value = any

// Table is one table in the schema.
type Table struct {
	Name string
	// SQL is the CREATE statement as stored in sqlite_master.
	SQL string
	// Columns are the declared column names, in declaration order.
	Columns []string
}

// Row is one decoded row. Values is positional and parallel to Columns.
type Row struct {
	Columns []string
	Values  []Value
	// RowID is the row's integer key, set only for rowid tables.
	RowID int64
}

// Value returns the value of the named column.
func (r Row) Value(column string) (Value, bool) {
	for i, name := range r.Columns {
		if name == column && i < len(r.Values) {
			return r.Values[i], true
		}
	}
	return nil, false
}

// Text returns the named column as a string, decoding BLOB values as bytes.
// It reports false when the column is missing or holds NULL.
func (r Row) Text(column string) (string, bool) {
	v, ok := r.Value(column)
	if !ok {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	case []byte:
		return string(t), true
	case int64:
		return fmt.Sprint(t), true
	case float64:
		return fmt.Sprint(t), true
	}
	return "", false
}

// DB is an open read-only SQLite store.
type DB struct {
	db     *sql.DB
	tables []Table
	// tempCopy is the temporary copy being read, when the store could not be
	// opened in place; it is removed on Close.
	tempCopy string
}

// Open opens the store at path for reading and loads its schema.
func Open(path string) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := checkMagic(abs); err != nil {
		return nil, err
	}

	handle, err := openReadOnly(abs)
	tempCopy := ""
	if err != nil {
		// The store may be unopenable in place (a read-only directory denies
		// SQLite the coordination file a write-ahead log needs). Reading a
		// copy keeps the log applied, where immutable=1 would drop it.
		copyPath, copyErr := copyStore(abs)
		if copyErr != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		handle, err = openReadOnly(copyPath)
		if err != nil {
			os.RemoveAll(filepath.Dir(copyPath))
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		tempCopy = copyPath
	}

	db := &DB{db: handle, tempCopy: tempCopy}
	if db.tables, err = db.readSchema(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return db, nil
}

// Close releases the store and removes any temporary copy.
func (db *DB) Close() error {
	var errs []error
	if db.db != nil {
		errs = append(errs, db.db.Close())
		db.db = nil
	}
	if db.tempCopy != "" {
		errs = append(errs, os.RemoveAll(filepath.Dir(db.tempCopy)))
		db.tempCopy = ""
	}
	return errors.Join(errs...)
}

// Tables returns the schema's tables, in sqlite_master order. SQLite's own
// internal tables are omitted.
func (db *DB) Tables() []Table {
	out := make([]Table, len(db.tables))
	copy(out, db.tables)
	return out
}

// Table looks up one table by name, case-insensitively as SQLite does.
func (db *DB) Table(name string) (Table, bool) {
	for _, table := range db.tables {
		if strings.EqualFold(table.Name, name) {
			return table, true
		}
	}
	return Table{}, false
}

// Scan calls fn for every row of the named table. Scanning stops and the
// error is returned when fn returns one; returning ErrStopScan stops without
// an error.
func (db *DB) Scan(name string, fn func(Row) error) error {
	table, ok := db.Table(name)
	if !ok {
		return fmt.Errorf("no such table: %s", name)
	}
	err := db.scanRows(table, db.queryRows, fn)
	if errors.Is(err, ErrStopScan) {
		return nil
	}
	return err
}

// ScanPrefix calls fn for rows whose named text column starts with prefix.
// The range predicate lets SQLite use the column's index instead of scanning
// the entire table. The column and table must both exist in the loaded schema.
func (db *DB) ScanPrefix(name, column, prefix string, fn func(Row) error) error {
	table, ok := db.Table(name)
	if !ok {
		return fmt.Errorf("no such table: %s", name)
	}
	column, ok = tableColumn(table, column)
	if !ok {
		return fmt.Errorf("no such column %s in table %s", column, name)
	}
	if prefix == "" {
		return db.Scan(table.Name, fn)
	}
	upper, ok := prefixUpperBound(prefix)
	if !ok {
		return fmt.Errorf("prefix %q has no finite upper bound", prefix)
	}
	query := func(table Table) (*sql.Rows, error) {
		projection := "*"
		if !hasRowIDColumn(table.Columns) {
			projection = "rowid, *"
		}
		statement := "SELECT " + projection + " FROM " + quoteIdentifier(table.Name) +
			" WHERE " + quoteIdentifier(column) + " >= ? AND " + quoteIdentifier(column) + " < ?"
		rows, err := db.db.Query(statement, prefix, upper)
		if err == nil || projection == "*" {
			return rows, err
		}
		statement = "SELECT * FROM " + quoteIdentifier(table.Name) +
			" WHERE " + quoteIdentifier(column) + " >= ? AND " + quoteIdentifier(column) + " < ?"
		return db.db.Query(statement, prefix, upper)
	}
	err := db.scanRows(table, query, fn)
	if errors.Is(err, ErrStopScan) {
		return nil
	}
	return err
}

func (db *DB) scanRows(table Table, query func(Table) (*sql.Rows, error), fn func(Row) error) error {
	// Ask for the rowid alongside the row so callers can identify it. A
	// WITHOUT ROWID table has none, and a table may define a column of its
	// own called "rowid"; either way the plain projection is the fallback.
	rows, err := query(table)
	if err != nil {
		return err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	withRowID := len(columns) == len(table.Columns)+1
	if withRowID {
		columns = columns[1:]
	}

	// Scan copies driver-owned bytes when the destination is a plain any, so
	// a decoded row stays valid after the scan moves on.
	for rows.Next() {
		values := make([]Value, len(columns))
		targets := make([]any, 0, len(columns)+1)
		var rowID sql.NullInt64
		if withRowID {
			targets = append(targets, &rowID)
		}
		for i := range values {
			targets = append(targets, &values[i])
		}
		if err := rows.Scan(targets...); err != nil {
			return err
		}
		if err := fn(Row{Columns: columns, Values: values, RowID: rowID.Int64}); err != nil {
			return err
		}
	}
	return rows.Err()
}

func tableColumn(table Table, name string) (string, bool) {
	for _, column := range table.Columns {
		if strings.EqualFold(column, name) {
			return column, true
		}
	}
	return name, false
}

func prefixUpperBound(prefix string) (string, bool) {
	bytes := []byte(prefix)
	for i := len(bytes) - 1; i >= 0; i-- {
		if bytes[i] != 0xff {
			bytes[i]++
			return string(bytes[:i+1]), true
		}
	}
	return "", false
}

// queryRows selects a table's rows, preferring a projection that includes the
// rowid.
func (db *DB) queryRows(table Table) (*sql.Rows, error) {
	quoted := quoteIdentifier(table.Name)
	if !hasRowIDColumn(table.Columns) {
		if rows, err := db.db.Query("SELECT rowid, * FROM " + quoted); err == nil {
			return rows, nil
		}
	}
	return db.db.Query("SELECT * FROM " + quoted)
}

// hasRowIDColumn reports whether a table declares a column that would collide
// with the implicit rowid aliases.
func hasRowIDColumn(columns []string) bool {
	for _, column := range columns {
		switch strings.ToLower(column) {
		case "rowid", "oid", "_rowid_":
			return true
		}
	}
	return false
}

// readSchema lists the user tables and their columns.
func (db *DB) readSchema() ([]Table, error) {
	rows, err := db.db.Query(
		`SELECT name, COALESCE(sql, '') FROM sqlite_master
		 WHERE type = 'table' AND name NOT LIKE 'sqlite@_%' ESCAPE '@'
		 ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	var tables []Table
	for rows.Next() {
		var table Table
		if err := rows.Scan(&table.Name, &table.SQL); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, table)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}

	for i := range tables {
		if tables[i].Columns, err = db.columnsOf(tables[i].Name); err != nil {
			return nil, err
		}
	}
	return tables, nil
}

// columnsOf reads a table's declared columns from SQLite itself, rather than
// parsing the CREATE statement.
func (db *DB) columnsOf(table string) ([]string, error) {
	rows, err := db.db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, fmt.Errorf("read columns of %s: %w", table, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	return columns, rows.Err()
}

// openReadOnly opens a store that SQLite may not write to.
func openReadOnly(path string) (*sql.DB, error) {
	handle, err := sql.Open(driverName, fileURI(path)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	// database/sql opens lazily, so the store is only really proven readable
	// once a connection is established.
	if err := handle.Ping(); err != nil {
		handle.Close()
		return nil, err
	}
	return handle, nil
}

// checkMagic rejects a file that is not a SQLite database before the driver
// sees it, so callers get a clear answer instead of a driver-shaped one.
func checkMagic(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	header := make([]byte, len(magic))
	if _, err := io.ReadFull(file, header); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("%s: %w", path, ErrNotSQLite)
		}
		return err
	}
	if string(header) != magic {
		return fmt.Errorf("%s: %w", path, ErrNotSQLite)
	}
	return nil
}

// copyStore copies a store and its write-ahead log into a temporary
// directory, returning the path of the copied store.
func copyStore(path string) (string, error) {
	dir, err := os.MkdirTemp("", "recountable-store-")
	if err != nil {
		return "", err
	}
	target := filepath.Join(dir, filepath.Base(path))
	if err := copyFile(path, target); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	// The log must travel with the store, or the copy loses everything not
	// yet checkpointed. A missing log simply means there is nothing to carry.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := copyFile(path+suffix, target+suffix); err != nil && !os.IsNotExist(err) {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return target, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// fileURI renders a filesystem path as a SQLite URI filename, so that query
// parameters can be appended to it. SQLite percent-decodes the path, so any
// character it would otherwise read as syntax has to be escaped.
func fileURI(path string) string {
	// SQLite's URI filenames use forward slashes on every platform, and a
	// Windows drive path becomes /C:/....
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var b strings.Builder
	b.WriteString("file://")
	for i := 0; i < len(path); i++ {
		switch c := path[i]; {
		case c == '/' || c == ':' || c == '.' || c == '-' || c == '_' || c == '~' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'):
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// quoteIdentifier renders a table name for interpolation into a statement.
// Table names come from the store's own schema rather than from user input,
// but they still have to survive quotes and spaces.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
