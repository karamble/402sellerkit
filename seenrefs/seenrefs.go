// Package seenrefs is the durable seam.SeenRefs: a bill-once guard backed
// by any database/sql connection, so a service points the guard at its own
// database instead of reinventing the UNIQUE-index pattern. The core module
// stays dependency-free - the service brings its own driver.
package seenrefs

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// DefaultTable is the table name used when none is given.
const DefaultTable = "seen_refs"

var tableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// DDL returns the CREATE TABLE statement for the guard table. Run it at
// startup (it is idempotent) or fold it into the service's own migrations.
// The PRIMARY KEY on ref is the entire mechanism.
func DDL(table string) string {
	if table == "" {
		table = DefaultTable
	}
	return "CREATE TABLE IF NOT EXISTS " + table +
		" (ref TEXT PRIMARY KEY, seen_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)"
}

// SQL is a database/sql-backed seam.SeenRefs. MarkSettled is an INSERT; the
// primary key makes the second insert of a ref fail, which is the replay
// signal. Errors that are not constraint violations also report false -
// the guard fails CLOSED: a broken database can deny a grant but can never
// double-bill.
type SQL struct {
	db     *sql.DB
	insert string
}

// NewSQL builds the guard over db with the given table ("" =
// DefaultTable). It does not create the table - run DDL first. The insert
// uses the ? placeholder (sqlite, mysql); for another placeholder style use
// NewSQLWithInsert.
func NewSQL(db *sql.DB, table string) (*SQL, error) {
	if table == "" {
		table = DefaultTable
	}
	if !tableName.MatchString(table) {
		return nil, fmt.Errorf("seenrefs: invalid table name %q", table)
	}
	return &SQL{db: db, insert: "INSERT INTO " + table + " (ref) VALUES (?)"}, nil
}

// NewSQLWithInsert builds the guard with a caller-supplied insert statement
// taking the ref as its single parameter, for databases whose placeholder
// style is not ? (e.g. postgres: "INSERT INTO seen_refs (ref) VALUES ($1)").
// The statement must fail on duplicate ref - do not add ON CONFLICT DO
// NOTHING, that would turn every replay into a first-time grant.
func NewSQLWithInsert(db *sql.DB, insert string) *SQL {
	return &SQL{db: db, insert: insert}
}

// MarkSettled records ref and reports whether this is its first settlement.
// A duplicate-key failure is a replay (false). Any other database error is
// also false: fail closed, never double-bill on a broken guard.
func (s *SQL) MarkSettled(ref string) bool {
	_, err := s.db.Exec(s.insert, ref)
	if err == nil {
		return true
	}
	if !isDuplicate(err) {
		// Not a replay but a broken guard; the caller sees a replay and
		// withholds the grant rather than risking a double-bill.
		return false
	}
	return false
}

// isDuplicate matches the constraint-violation wording of the common
// drivers (sqlite: "UNIQUE constraint failed", mysql: "Duplicate entry",
// postgres: "duplicate key value"). It exists only to keep the distinction
// available for future diagnostics; both branches currently report false.
func isDuplicate(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate")
}
