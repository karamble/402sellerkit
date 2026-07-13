package seenrefs

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
)

// memDriver is a minimal database/sql driver emulating a single
// unique-keyed table, so the guard is tested through the real database/sql
// machinery without adding a driver dependency to the core module.
type memDriver struct {
	mu   sync.Mutex
	rows map[string]struct{}
	fail error // when set, every Exec fails with this error
}

func (d *memDriver) Open(string) (driver.Conn, error) { return &memConn{d: d}, nil }

type memConn struct{ d *memDriver }

func (c *memConn) Prepare(query string) (driver.Stmt, error) { return &memStmt{d: c.d}, nil }
func (c *memConn) Close() error                              { return nil }
func (c *memConn) Begin() (driver.Tx, error)                 { return nil, errors.New("no tx") }

type memStmt struct{ d *memDriver }

func (s *memStmt) Close() error  { return nil }
func (s *memStmt) NumInput() int { return 1 }
func (s *memStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, io.EOF
}
func (s *memStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.d.mu.Lock()
	defer s.d.mu.Unlock()
	if s.d.fail != nil {
		return nil, s.d.fail
	}
	ref := args[0].(string)
	if _, ok := s.d.rows[ref]; ok {
		return nil, errors.New("UNIQUE constraint failed: seen_refs.ref")
	}
	s.d.rows[ref] = struct{}{}
	return driver.RowsAffected(1), nil
}

var registerOnce sync.Once

func openMem(t *testing.T, d *memDriver) *sql.DB {
	t.Helper()
	registerOnce.Do(func() { sql.Register("seenrefs-mem", &routingDriver{}) })
	routing.Lock()
	routing.current = d
	routing.Unlock()
	db, err := sql.Open("seenrefs-mem", "mem")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// routingDriver lets each test install its own memDriver behind the single
// registered driver name (sql.Register forbids re-registration).
type routingDriver struct{}

var routing struct {
	sync.Mutex
	current *memDriver
}

func (routingDriver) Open(name string) (driver.Conn, error) {
	routing.Lock()
	defer routing.Unlock()
	return routing.current.Open(name)
}

func TestMarkSettledFirstThenReplay(t *testing.T) {
	db := openMem(t, &memDriver{rows: map[string]struct{}{}})
	g, err := NewSQL(db, "")
	if err != nil {
		t.Fatal(err)
	}
	if !g.MarkSettled("dcr402:abc") {
		t.Fatal("first ref must be first-time")
	}
	if g.MarkSettled("dcr402:abc") {
		t.Fatal("replayed ref must not be first-time")
	}
	if !g.MarkSettled("dcr402:def") {
		t.Fatal("a different ref is first-time")
	}
}

func TestMarkSettledFailsClosed(t *testing.T) {
	db := openMem(t, &memDriver{rows: map[string]struct{}{}, fail: errors.New("disk I/O error")})
	g, err := NewSQL(db, "")
	if err != nil {
		t.Fatal(err)
	}
	if g.MarkSettled("dcr402:abc") {
		t.Fatal("a broken guard must fail closed (report replay)")
	}
}

func TestNewSQLRejectsBadTable(t *testing.T) {
	db := openMem(t, &memDriver{rows: map[string]struct{}{}})
	if _, err := NewSQL(db, "refs; DROP TABLE users"); err == nil {
		t.Fatal("want error for invalid table name")
	}
}

func TestDDLShape(t *testing.T) {
	ddl := DDL("")
	want := "CREATE TABLE IF NOT EXISTS seen_refs (ref TEXT PRIMARY KEY, seen_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)"
	if ddl != want {
		t.Fatalf("DDL = %q", ddl)
	}
}
