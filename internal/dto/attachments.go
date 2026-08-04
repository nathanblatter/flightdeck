package dto

import (
	"time"

	"flightdeck/internal/store"
)

// Attachment is attachment metadata. Bytes are fetched via URL (the API,
// key-authed) or straight from object storage at Bucket/ObjectKey — the MinIO
// reference is included so agents with storage access can go direct.
type Attachment struct {
	ID          string    `json:"id"`
	ItemID      string    `json:"item_id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	Bucket      string    `json:"bucket,omitempty"`
	ObjectKey   string    `json:"object_key"`
	URL         string    `json:"url"`
	Actor       string    `json:"actor"`
	CreatedAt   time.Time `json:"created_at"`
}

func ToAttachment(a store.Attachment, bucket string) Attachment {
	return Attachment{
		ID:          a.ID.String(),
		ItemID:      a.ItemID.String(),
		Filename:    a.Filename,
		ContentType: a.ContentType,
		SizeBytes:   a.SizeBytes,
		Bucket:      bucket,
		ObjectKey:   a.ObjectKey,
		URL:         "/api/attachments/" + a.ID.String(),
		Actor:       a.Actor,
		CreatedAt:   a.CreatedAt,
	}
}

func ToAttachments(rows []store.Attachment, bucket string) []Attachment {
	out := make([]Attachment, 0, len(rows))
	for _, a := range rows {
		out = append(out, ToAttachment(a, bucket))
	}
	return out
}
