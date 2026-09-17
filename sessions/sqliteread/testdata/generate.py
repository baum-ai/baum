#!/usr/bin/env python3
"""Regenerate the sqliteread test fixtures.

Run from this directory with any Python 3 that has the stdlib sqlite3 module:

    python3 generate.py

The fixtures are committed so the tests need no Python or SQLite binary at
test time; this script exists so they can be rebuilt and reviewed.
"""

import json
import os
import shutil
import sqlite3
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))


def fresh(name):
    path = os.path.join(HERE, name)
    for suffix in ("", "-wal", "-shm"):
        if os.path.exists(path + suffix):
            os.remove(path + suffix)
    return path


def kv_store():
    """A VS Code / Cursor shaped key-value store.

    Includes a value far larger than one page (overflow chain) and enough
    rows to push the table b-tree past a single leaf page.
    """
    path = fresh("kv.db")
    conn = sqlite3.connect(path)
    conn.execute("PRAGMA page_size=4096")
    conn.execute("VACUUM")
    conn.execute("CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)")
    conn.execute("CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)")
    conn.execute(
        "INSERT INTO ItemTable VALUES (?, ?)",
        ("composer.composerData", json.dumps({"allComposers": [{"composerId": "c1"}]})),
    )
    conn.execute(
        "INSERT INTO cursorDiskKV VALUES (?, ?)",
        ("big", json.dumps({"text": "x" * 40000})),
    )
    for i in range(400):
        conn.execute(
            "INSERT INTO cursorDiskKV VALUES (?, ?)",
            (f"bubbleId:c1:b{i:04d}", json.dumps({"index": i, "pad": "p" * 200})),
        )
    conn.commit()
    conn.close()


def types_store():
    """Every column type and integer width a store can hold."""
    path = fresh("types.db")
    conn = sqlite3.connect(path)
    conn.execute("CREATE TABLE vals (id INTEGER PRIMARY KEY, n INTEGER, f REAL, t TEXT, b BLOB)")
    rows = [
        (1, None, None, None, None),
        (2, 0, 0.0, "", b""),
        (3, 1, 1.5, "one", b"\x00\x01\x02"),
        (4, -1, -2.25, "negative", b"\xff"),
        (5, 127, 3.5, "i8", b"\x7f"),
        (6, 32767, 4.5, "i16", b"\x7f\xff"),
        (7, 8388607, 5.5, "i24", b"a" * 3),
        (8, 2147483647, 6.5, "i32", b"a" * 4),
        (9, 140737488355327, 7.5, "i48", b"a" * 6),
        (10, 9223372036854775807, 8.5, "i64", b"a" * 8),
        (11, -9223372036854775808, -0.5, "min", b"a" * 9),
        (12, 42, 1e300, "unicode: héllo ☃ 𝄞", b"\x80\x81"),
    ]
    conn.executemany("INSERT INTO vals VALUES (?, ?, ?, ?, ?)", rows)
    conn.commit()
    conn.close()


def without_rowid_store():
    """A WITHOUT ROWID table, stored in an index b-tree."""
    path = fresh("without_rowid.db")
    conn = sqlite3.connect(path)
    conn.execute("PRAGMA page_size=512")
    conn.execute("VACUUM")
    conn.execute('CREATE TABLE "quoted name" (k TEXT PRIMARY KEY, v TEXT NOT NULL) WITHOUT ROWID')
    for i in range(300):
        conn.execute("INSERT INTO 'quoted name' VALUES (?, ?)", (f"k{i:04d}", f"v{i}" + "z" * 40))
    conn.commit()
    conn.close()


def utf16_store():
    """A UTF-16 encoded database: rare, but legal and readable."""
    path = fresh("utf16.db")
    conn = sqlite3.connect(path)
    conn.execute("PRAGMA encoding='UTF-16le'")
    conn.execute("CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)")
    conn.execute(
        "INSERT INTO cursorDiskKV VALUES (?, ?)",
        ("bubbleId:c1:b1", json.dumps({"text": "héllo ☃ 𝄞"})),
    )
    conn.commit()
    conn.close()


def wal_store():
    """A database whose newest rows live only in an unchecked-pointed log.

    SQLite checkpoints when the last connection closes, so the files are
    copied out while the writing connection is still open.
    """
    path = fresh("wal.db")
    with tempfile.TemporaryDirectory() as tmp:
        live = os.path.join(tmp, "live.db")
        conn = sqlite3.connect(live)
        conn.execute("PRAGMA journal_mode=WAL")
        conn.execute("PRAGMA wal_autocheckpoint=0")
        conn.execute("CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)")
        conn.execute("INSERT INTO cursorDiskKV VALUES ('checkpointed', 'from-db-file')")
        conn.commit()
        conn.execute("PRAGMA wal_checkpoint(FULL)")
        for i in range(50):
            conn.execute(
                "INSERT INTO cursorDiskKV VALUES (?, ?)",
                (f"in-log-{i:02d}", json.dumps({"i": i, "pad": "w" * 100})),
            )
        conn.commit()
        # An uncommitted transaction must be invisible to the reader.
        conn.execute("BEGIN")
        conn.execute("INSERT INTO cursorDiskKV VALUES ('uncommitted', 'must-not-appear')")
        shutil.copyfile(live, path)
        shutil.copyfile(live + "-wal", path + "-wal")
        conn.rollback()
        conn.close()


if __name__ == "__main__":
    kv_store()
    types_store()
    without_rowid_store()
    utf16_store()
    wal_store()
    print("wrote fixtures to", HERE)
