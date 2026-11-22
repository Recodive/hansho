// Package store is the hub's SQLite persistence layer (modernc.org/sqlite, no cgo).
package store

import (
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

var (
	// ErrNotFound means the requested row does not exist.
	ErrNotFound = errors.New("store: not found")
	// ErrTicketUsed means the pairing ticket was already redeemed.
	ErrTicketUsed = errors.New("store: ticket already used")
	// ErrTicketExpired means the pairing ticket's TTL elapsed, or the user was unpaired since it was issued.
	ErrTicketExpired = errors.New("store: ticket expired")
	// ErrTooManyDevices means the pairing already has MaxDevicesPerPairing active devices.
	ErrTooManyDevices = errors.New("store: too many devices")
)

// Store wraps the SQLite database. Now is the clock used by methods that take
// no explicit time (tests replace it).
type Store struct {
	db  *sql.DB
	Now func() time.Time
	// stmts caches prepared statements by their SQL text. Without it SQLite parses every statement on every call,
	// which was about a quarter of the hub's CPU under load. Only fixed SQL goes through it (see exec, query, queryRow);
	// statements with a variable number of placeholders use s.db directly so the cache stays bounded.
	stmts sync.Map // string -> *sql.Stmt
}

// Open opens (creating if needed) the database at path, enables WAL and
// foreign keys, and applies embedded migrations in order.
func Open(path string) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, Now: time.Now}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	s.stmts.Range(func(_, v any) bool { v.(*sql.Stmt).Close(); return true })
	return s.db.Close()
}

func (s *Store) stmt(q string) (*sql.Stmt, error) {
	if st, ok := s.stmts.Load(q); ok {
		return st.(*sql.Stmt), nil
	}
	st, err := s.db.Prepare(q)
	if err != nil {
		return nil, err
	}
	if prev, loaded := s.stmts.LoadOrStore(q, st); loaded {
		st.Close()
		return prev.(*sql.Stmt), nil
	}
	return st, nil
}

func (s *Store) exec(q string, args ...any) (sql.Result, error) {
	st, err := s.stmt(q)
	if err != nil {
		return nil, err
	}
	return st.Exec(args...)
}

func (s *Store) query(q string, args ...any) (*sql.Rows, error) {
	st, err := s.stmt(q)
	if err != nil {
		return nil, err
	}
	return st.Query(args...)
}

func (s *Store) queryRow(q string, args ...any) *sql.Row {
	st, err := s.stmt(q)
	if err != nil {
		return s.db.QueryRow(q, args...) // reports the same prepare error through Row.Scan
	}
	return st.QueryRow(args...)
}

func (s *Store) migrate() error {
	if _, err := s.exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		var n int
		if err := s.queryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		sqlText, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(sqlText)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, name, s.Now().Unix()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// NewID returns prefix + 24 random hex characters, e.g. NewID("pair_").
func NewID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return prefix + hex.EncodeToString(b)
}

func unix(t time.Time) int64 { return t.Unix() }

func fromUnix(v int64) time.Time { return time.Unix(v, 0) }

// nullTime converts a nullable unix column to a time (zero when NULL).
func nullTime(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.Unix(v.Int64, 0)
}

func nullUnix(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }
