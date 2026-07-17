// Package pgvec wraps pgvector.Vector with NULL handling. pgvector.Vector itself
// implements driver.Valuer/sql.Scanner (which pgx v5 uses as a fallback, so no
// codec registration is needed) but its Scan errors on a NULL source. Since the
// items.embedding column is nullable — NULL until embedded, and reset to NULL on
// edit — every `RETURNING *` / `SELECT i.*` would otherwise fail. Vector adds a
// Valid flag and the nil handling, mirroring the pgtype null-wrapper convention.
package pgvec

import (
	"database/sql/driver"

	"github.com/pgvector/pgvector-go"
)

// Vector is a nullable pgvector value.
type Vector struct {
	Vec   pgvector.Vector
	Valid bool // false ⇒ SQL NULL
}

// New builds a non-NULL Vector from a float32 slice.
func New(v []float32) Vector {
	return Vector{Vec: pgvector.NewVector(v), Valid: true}
}

// Slice returns the underlying floats (nil when NULL).
func (v Vector) Slice() []float32 {
	if !v.Valid {
		return nil
	}
	return v.Vec.Slice()
}

// Scan implements sql.Scanner, treating a nil source as SQL NULL.
func (v *Vector) Scan(src any) error {
	if src == nil {
		v.Vec, v.Valid = pgvector.Vector{}, false
		return nil
	}
	if err := v.Vec.Scan(src); err != nil {
		return err
	}
	v.Valid = true
	return nil
}

// Value implements driver.Valuer, emitting NULL when invalid.
func (v Vector) Value() (driver.Value, error) {
	if !v.Valid {
		return nil, nil
	}
	return v.Vec.Value()
}
