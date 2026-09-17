package sqliteread

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures under testdata/ are real SQLite databases built by
// testdata/generate.py; rerun that script to rebuild them. They exercise the
// store shapes Cursor produces — key/value tables, oversized values spilled
// onto overflow pages, and an unchecked-pointed write-ahead log — plus the
// edge cases a store can legally have.

// open copies a fixture into a temporary directory and opens it there.
// Opening a write-ahead-logged store makes SQLite create a -shm file beside
// it, which must not be left in the repository.
func open(t *testing.T, name string) *DB {
	t.Helper()
	db, err := Open(stage(t, name))
	if err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// stage copies a fixture and its write-ahead log into a temporary directory
// and returns the copy's path.
func stage(t *testing.T, name string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(name))
	stageFile(t, filepath.Join("testdata", name), dst)
	if wal := filepath.Join("testdata", name+"-wal"); fileExists(wal) {
		stageFile(t, wal, dst+"-wal")
	}
	return dst
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestTablesAndColumns(t *testing.T) {
	db := open(t, "kv.db")
	var names []string
	for _, table := range db.Tables() {
		names = append(names, table.Name)
	}
	want := []string{"ItemTable", "cursorDiskKV"}
	if got := strings.Join(names, ","); got != strings.Join(want, ",") {
		t.Fatalf("tables = %q, want %q", got, want)
	}
	table, ok := db.Table("cursordiskkv") // lookups are case-insensitive
	if !ok {
		t.Fatal("Table(cursordiskkv) not found")
	}
	if got := strings.Join(table.Columns, ","); got != "key,value" {
		t.Errorf("columns = %q, want %q", got, "key,value")
	}
}

func TestScanKeyValueStore(t *testing.T) {
	db := open(t, "kv.db")
	rows := map[string]string{}
	if err := db.Scan("cursorDiskKV", func(row Row) error {
		key, ok := row.Text("key")
		if !ok {
			return fmt.Errorf("row %d has no key", row.RowID)
		}
		value, _ := row.Text("value")
		rows[key] = value
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	// 400 bubbles plus the oversized value.
	if len(rows) != 401 {
		t.Fatalf("scanned %d rows, want 401", len(rows))
	}

	// A payload spanning overflow pages must come back whole and intact.
	var big struct{ Text string }
	if err := json.Unmarshal([]byte(rows["big"]), &big); err != nil {
		t.Fatalf("overflow value is not valid JSON: %v", err)
	}
	if len(big.Text) != 40000 || strings.Trim(big.Text, "x") != "" {
		t.Errorf("overflow value corrupted: %d bytes", len(big.Text))
	}

	// Rows spread across several leaf pages under an interior page.
	for i := range 400 {
		key := fmt.Sprintf("bubbleId:c1:b%04d", i)
		var rec struct {
			Index int    `json:"index"`
			Pad   string `json:"pad"`
		}
		if err := json.Unmarshal([]byte(rows[key]), &rec); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if rec.Index != i || len(rec.Pad) != 200 {
			t.Fatalf("%s decoded as %+v", key, rec)
		}
	}
}

func TestScanDecodesEveryValueType(t *testing.T) {
	db := open(t, "types.db")
	byID := map[int64]Row{}
	if err := db.Scan("vals", func(row Row) error {
		byID[row.RowID] = row
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(byID) != 12 {
		t.Fatalf("scanned %d rows, want 12", len(byID))
	}

	// An INTEGER PRIMARY KEY column aliases the rowid and is stored as NULL.
	if v, _ := byID[7].Value("id"); v != int64(7) {
		t.Errorf("id column = %#v, want int64(7)", v)
	}

	nulls := byID[1]
	for _, column := range []string{"n", "f", "t", "b"} {
		if v, ok := nulls.Value(column); !ok || v != nil {
			t.Errorf("row 1 %s = %#v, want nil", column, v)
		}
	}

	cases := []struct {
		id   int64
		want int64
	}{
		{2, 0}, {3, 1}, {4, -1}, {5, 127}, {6, 32767}, {7, 8388607},
		{8, 2147483647}, {9, 140737488355327}, {10, math.MaxInt64}, {11, math.MinInt64},
	}
	for _, c := range cases {
		v, _ := byID[c.id].Value("n")
		if v != c.want {
			t.Errorf("row %d n = %#v, want %d", c.id, v, c.want)
		}
	}

	if v, _ := byID[3].Value("f"); v != 1.5 {
		t.Errorf("row 3 f = %#v, want 1.5", v)
	}
	if v, _ := byID[12].Value("f"); v != 1e300 {
		t.Errorf("row 12 f = %#v, want 1e300", v)
	}
	if v, _ := byID[12].Value("t"); v != "unicode: héllo ☃ 𝄞" {
		t.Errorf("row 12 t = %#v", v)
	}
	if v, _ := byID[2].Value("t"); v != "" {
		t.Errorf("row 2 t = %#v, want empty string", v)
	}
	blob, _ := byID[3].Value("b")
	if got, ok := blob.([]byte); !ok || string(got) != "\x00\x01\x02" {
		t.Errorf("row 3 b = %#v, want []byte{0,1,2}", blob)
	}
}

func TestScanWithoutRowIDTable(t *testing.T) {
	// WITHOUT ROWID tables live in an index b-tree, where interior pages hold
	// keys of their own; a scan that only visited leaves would lose rows.
	db := open(t, "without_rowid.db")
	table, ok := db.Table("quoted name")
	if !ok {
		t.Fatal(`Table("quoted name") not found`)
	}
	if got := strings.Join(table.Columns, ","); got != "k,v" {
		t.Errorf("columns = %q, want %q", got, "k,v")
	}
	seen := map[string]bool{}
	if err := db.Scan("quoted name", func(row Row) error {
		key, _ := row.Text("k")
		if value, _ := row.Text("v"); !strings.HasSuffix(value, strings.Repeat("z", 40)) {
			return fmt.Errorf("row %s value truncated: %q", key, value)
		}
		seen[key] = true
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(seen) != 300 {
		t.Fatalf("scanned %d rows, want 300", len(seen))
	}
	for i := range 300 {
		if key := fmt.Sprintf("k%04d", i); !seen[key] {
			t.Fatalf("row %s missing from scan", key)
		}
	}
}

func TestScanDecodesUTF16Text(t *testing.T) {
	db := open(t, "utf16.db")
	rows := map[string]string{}
	if err := db.Scan("cursorDiskKV", func(row Row) error {
		key, _ := row.Text("key")
		value, _ := row.Text("value")
		rows[key] = value
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var rec struct{ Text string }
	if err := json.Unmarshal([]byte(rows["bubbleId:c1:b1"]), &rec); err != nil {
		t.Fatalf("UTF-16 value did not decode to JSON: %v (rows: %v)", err, rows)
	}
	if rec.Text != "héllo ☃ 𝄞" {
		t.Errorf("text = %q, want %q", rec.Text, "héllo ☃ 𝄞")
	}
}

func TestScanAppliesWriteAheadLog(t *testing.T) {
	db := open(t, "wal.db")
	rows := map[string]string{}
	if err := db.Scan("cursorDiskKV", func(row Row) error {
		key, _ := row.Text("key")
		value, _ := row.Text("value")
		rows[key] = value
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if rows["checkpointed"] != "from-db-file" {
		t.Errorf("checkpointed row = %q", rows["checkpointed"])
	}
	for i := range 50 {
		if key := fmt.Sprintf("in-log-%02d", i); rows[key] == "" {
			t.Errorf("row %s written only to the log is missing", key)
		}
	}
	if _, ok := rows["uncommitted"]; ok {
		t.Error("row from an uncommitted transaction leaked into the scan")
	}
	if len(rows) != 51 {
		t.Errorf("scanned %d rows, want 51", len(rows))
	}
}

func TestScanIgnoresWriteAheadLogWithForeignSalt(t *testing.T) {
	// A log left over from before a checkpoint reset must not be applied.
	dbPath := stage(t, "wal.db")
	wal := readFile(t, filepath.Join("testdata", "wal.db-wal"))
	wal[16] ^= 0xff // corrupt salt-1 in the log header
	if err := os.WriteFile(dbPath+"-wal", wal, 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	count := 0
	if err := db.Scan("cursorDiskKV", func(Row) error { count++; return nil }); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if count != 1 {
		t.Errorf("scanned %d rows, want only the 1 checkpointed row", count)
	}
}

func TestScanStopsEarly(t *testing.T) {
	db := open(t, "kv.db")
	count := 0
	if err := db.Scan("cursorDiskKV", func(Row) error {
		count++
		if count == 3 {
			return ErrStopScan
		}
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if count != 3 {
		t.Errorf("scan visited %d rows after ErrStopScan, want 3", count)
	}

	sentinel := errors.New("boom")
	err := db.Scan("cursorDiskKV", func(Row) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Errorf("Scan error = %v, want %v", err, sentinel)
	}
}

func TestScanUnknownTable(t *testing.T) {
	db := open(t, "kv.db")
	if err := db.Scan("nope", func(Row) error { return nil }); err == nil {
		t.Fatal("Scan of a missing table succeeded, want error")
	}
}

func TestScanPrefix(t *testing.T) {
	db := open(t, "kv.db")
	var keys []string
	if err := db.ScanPrefix("cursorDiskKV", "key", "bubbleId:c1:b00", func(row Row) error {
		key, _ := row.Text("key")
		keys = append(keys, key)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 100 {
		t.Fatalf("ScanPrefix returned %d rows, want 100", len(keys))
	}
	for _, key := range keys {
		if !strings.HasPrefix(key, "bubbleId:c1:b00") {
			t.Fatalf("ScanPrefix returned non-match %q", key)
		}
	}
}

func TestScanPrefixEmptyAndFullKey(t *testing.T) {
	db := open(t, "kv.db")
	all := 0
	if err := db.ScanPrefix("cursorDiskKV", "key", "", func(Row) error {
		all++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if all != 401 {
		t.Fatalf("empty prefix returned %d rows, want 401", all)
	}

	count := 0
	if err := db.ScanPrefix("cursorDiskKV", "key", "missing:", func(Row) error {
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("missing prefix returned %d rows", count)
	}

	var keys []string
	if err := db.ScanPrefix("cursorDiskKV", "key", "bubbleId:c1:b0000", func(row Row) error {
		key, _ := row.Text("key")
		keys = append(keys, key)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "bubbleId:c1:b0000" {
		t.Fatalf("full-key prefix returned %v", keys)
	}
}

func TestScanPrefixRejectsMissingSchema(t *testing.T) {
	db := open(t, "kv.db")
	if err := db.ScanPrefix("nope", "key", "x", func(Row) error { return nil }); err == nil {
		t.Fatal("ScanPrefix of a missing table succeeded")
	}
	if err := db.ScanPrefix("cursorDiskKV", "nope", "x", func(Row) error { return nil }); err == nil {
		t.Fatal("ScanPrefix of a missing column succeeded")
	}
}

func TestOpenRejectsNonDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("not a database", 20)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrNotSQLite) {
		t.Fatalf("Open(text file) error = %v, want ErrNotSQLite", err)
	}
}

func TestOpenRejectsTruncatedHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short.db")
	if err := os.WriteFile(path, []byte(magic), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open(truncated file) succeeded, want error")
	}
}

func TestOpenMissingFile(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "absent.db")); !os.IsNotExist(err) {
		t.Fatalf("Open(missing) error = %v, want not-exist", err)
	}
}

func TestScanDetectsCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.db")
	data := readFile(t, filepath.Join("testdata", "kv.db"))
	// Page 2 holds a b-tree page; a bogus page-type byte must be reported
	// rather than yielding silently wrong rows.
	data[4096] = 0x42
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		// Rejecting at Open time is an equally correct outcome.
		return
	}
	defer func() { _ = db.Close() }()
	scanned := false
	for _, table := range db.Tables() {
		if err := db.Scan(table.Name, func(Row) error { return nil }); err != nil {
			scanned = true
		}
	}
	if !scanned {
		t.Error("scanning a corrupted page succeeded, want an error")
	}
}

func stageFile(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.WriteFile(dst, readFile(t, src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestOpenNeverModifiesTheStore(t *testing.T) {
	// The single property that matters most when reading someone's live
	// editor database: reading it must leave it byte-for-byte untouched.
	for _, name := range []string{"kv.db", "wal.db"} {
		t.Run(name, func(t *testing.T) {
			path := stage(t, name)
			before := readFile(t, path)
			beforeWAL := []byte(nil)
			if fileExists(path + "-wal") {
				beforeWAL = readFile(t, path+"-wal")
			}

			db, err := Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			rows := 0
			for _, table := range db.Tables() {
				if err := db.Scan(table.Name, func(Row) error { rows++; return nil }); err != nil {
					t.Fatalf("Scan %s: %v", table.Name, err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if rows == 0 {
				t.Fatal("scanned no rows, so the comparison proves nothing")
			}

			if !bytes.Equal(before, readFile(t, path)) {
				t.Error("the store changed while being read")
			}
			if beforeWAL != nil && !bytes.Equal(beforeWAL, readFile(t, path+"-wal")) {
				t.Error("the write-ahead log changed while being read (a checkpoint ran)")
			}
		})
	}
}

func TestCopyStoreCarriesTheWriteAheadLog(t *testing.T) {
	// The fallback for a store that cannot be opened in place must copy the
	// log too, or the copy silently loses everything not yet checkpointed.
	source := stage(t, "wal.db")
	copyPath, err := copyStore(source)
	if err != nil {
		t.Fatalf("copyStore: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(copyPath))

	db, err := Open(copyPath)
	if err != nil {
		t.Fatalf("Open(copy): %v", err)
	}
	defer func() { _ = db.Close() }()
	rows := 0
	if err := db.Scan("cursorDiskKV", func(Row) error { rows++; return nil }); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if rows != 51 {
		t.Errorf("copy has %d rows, want 51 (1 checkpointed + 50 in the log)", rows)
	}
}

func TestFileURI(t *testing.T) {
	cases := map[string]string{
		"/home/dev/state.vscdb":      "file:///home/dev/state.vscdb",
		"/home/dev/my app/s.vscdb":   "file:///home/dev/my%20app/s.vscdb",
		"/home/dev/weird?name#x.db":  "file:///home/dev/weird%3Fname%23x.db",
		"/home/dev/100%/state.vscdb": "file:///home/dev/100%25/state.vscdb",
	}
	for input, want := range cases {
		if got := fileURI(input); got != want {
			t.Errorf("fileURI(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestOpenPathWithAwkwardCharacters(t *testing.T) {
	// A store under a path with spaces and URI syntax must still open: the
	// path becomes part of a SQLite URI, so it has to be escaped.
	dir := filepath.Join(t.TempDir(), "my project (100%)")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state #1.vscdb")
	stageFile(t, filepath.Join("testdata", "kv.db"), path)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	defer func() { _ = db.Close() }()
	rows := 0
	if err := db.Scan("cursorDiskKV", func(Row) error { rows++; return nil }); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if rows != 401 {
		t.Errorf("scanned %d rows, want 401", rows)
	}
}
