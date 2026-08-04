package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/google/uuid"

	"flightdeck/internal/blob"
	"flightdeck/internal/store"
)

// MaxAttachmentsPerItem bounds how many files an item can accumulate — the
// ingest endpoint's key is public (embedded in page HTML), so the cap is
// enforced server-side, not just in widgets.
const MaxAttachmentsPerItem = 8

// ErrNoBlobStore is returned when attachments are used on an instance without
// object storage configured; the API maps it to 503.
var ErrNoBlobStore = errors.New("attachment storage not configured")

// ErrTooManyAttachments maps to 400.
var ErrTooManyAttachments = fmt.Errorf("an item can hold at most %d attachments", MaxAttachmentsPerItem)

// SetBlob wires the object store (nil = attachments disabled).
func (s *Service) SetBlob(b blob.Store) { s.blob = b }

// Blob returns the configured object store, or nil.
func (s *Service) Blob() blob.Store { return s.blob }

// AddAttachment stores the bytes in the object store, records the metadata row,
// and emits an item.attachment_added event. The object is written first and
// best-effort deleted if the row insert fails — an orphaned object is
// harmless; a row without its object would 404 forever.
func (s *Service) AddAttachment(ctx context.Context, itemID uuid.UUID, filename, contentType string, size int64, r io.Reader, actor string) (store.Attachment, error) {
	if s.blob == nil {
		return store.Attachment{}, ErrNoBlobStore
	}
	item, err := s.St.GetItem(ctx, itemID)
	if err != nil {
		return store.Attachment{}, err
	}
	n, err := s.St.CountAttachmentsForItem(ctx, itemID)
	if err != nil {
		return store.Attachment{}, err
	}
	if n >= MaxAttachmentsPerItem {
		return store.Attachment{}, ErrTooManyAttachments
	}

	key := fmt.Sprintf("items/%s/%s-%s", itemID, uuid.NewString()[:8], sanitizeFilename(filename))
	if err := s.blob.Put(ctx, key, contentType, size, r); err != nil {
		return store.Attachment{}, fmt.Errorf("store object: %w", err)
	}

	var att store.Attachment
	err = s.St.WithTx(ctx, func(q *store.Queries) error {
		var err error
		att, err = q.CreateAttachment(ctx, store.CreateAttachmentParams{
			ItemID:      itemID,
			Filename:    filename,
			ContentType: contentType,
			SizeBytes:   size,
			ObjectKey:   key,
			Actor:       actor,
		})
		if err != nil {
			return err
		}
		return s.enqueue(ctx, q, item.ProjectID, "item.attachment_added", map[string]any{
			"item_id": itemID, "attachment_id": att.ID, "filename": filename, "actor": actor,
		})
	})
	if err != nil {
		_ = s.blob.Delete(ctx, key)
		return store.Attachment{}, err
	}
	return att, nil
}

// ListAttachments returns an item's attachment metadata.
func (s *Service) ListAttachments(ctx context.Context, itemID uuid.UUID) ([]store.Attachment, error) {
	return s.St.ListAttachmentsForItem(ctx, itemID)
}

// OpenAttachment returns the metadata row plus a reader over the blob bytes.
func (s *Service) OpenAttachment(ctx context.Context, id uuid.UUID) (store.Attachment, io.ReadCloser, error) {
	att, err := s.St.GetAttachment(ctx, id)
	if err != nil {
		return store.Attachment{}, nil, err
	}
	if s.blob == nil {
		return att, nil, ErrNoBlobStore
	}
	rc, err := s.blob.Get(ctx, att.ObjectKey)
	if err != nil {
		return att, nil, fmt.Errorf("open object %q: %w", att.ObjectKey, err)
	}
	return att, rc, nil
}

// DeleteAttachment removes the row, then the object (row is authoritative; a
// leftover object on a failed second step is only wasted space).
func (s *Service) DeleteAttachment(ctx context.Context, id uuid.UUID) error {
	att, err := s.St.DeleteAttachment(ctx, id)
	if err != nil {
		return err
	}
	if s.blob != nil {
		_ = s.blob.Delete(ctx, att.ObjectKey)
	}
	return nil
}

// sanitizeFilename keeps object keys tame: base name only, spaces collapsed,
// anything outside a safe set dropped, bounded length.
func sanitizeFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" || strings.Trim(out, ".") == "" {
		out = "file"
	}
	if len(out) > 100 {
		out = out[len(out)-100:]
	}
	return out
}
