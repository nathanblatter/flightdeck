package api

import (
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// optStr returns a *string for a query param, nil when absent/empty.
func optStr(q url.Values, key string) *string {
	if v := q.Get(key); v != "" {
		return &v
	}
	return nil
}

// optUUID parses a uuid query param into a nullable pgtype.UUID.
func optUUID(q url.Values, key string) (pgtype.UUID, error) {
	v := q.Get(key)
	if v == "" {
		return pgtype.UUID{}, nil
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}

// optTime parses an RFC3339 timestamp query param into *time.Time.
func optTime(q url.Values, key string) (*time.Time, error) {
	v := q.Get(key)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// optInt32 parses an int query param into *int32.
func optInt32(q url.Values, key string) (*int32, error) {
	v := q.Get(key)
	if v == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil, err
	}
	n32 := int32(n)
	return &n32, nil
}

func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

// pgUUIDFromPtr parses an optional uuid string into a nullable pgtype.UUID.
func pgUUIDFromPtr(s *string) (pgtype.UUID, error) {
	if s == nil || *s == "" {
		return pgtype.UUID{}, nil
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}
