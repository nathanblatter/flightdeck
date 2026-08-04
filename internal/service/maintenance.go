package service

import (
	"context"
	"log"
	"time"
)

// Retention windows for the daily maintenance sweep.
const (
	softDeleteRetention   = 30 * 24 * time.Hour // purge items soft-deleted longer ago
	activityRetention     = 90 * 24 * time.Hour // purge low-signal activity older than
	webhookEventRetention = 7 * 24 * time.Hour  // purge delivered outbox rows older than
	parkedEventRetention  = 30 * 24 * time.Hour // purge dead-lettered outbox rows older than
	usageRetention        = 60 * 24 * time.Hour // purge tool-call / search analytics older than
)

// RunMaintenance sweeps once on startup, then daily until ctx is cancelled:
// hard-deletes long-soft-deleted items, trims low-signal activity, and purges
// delivered outbox rows — keeping indexes and FTS lean as the store grows.
func (s *Service) RunMaintenance(ctx context.Context) {
	s.runMaintenanceOnce(ctx)
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runMaintenanceOnce(ctx)
		}
	}
}

func (s *Service) runMaintenanceOnce(ctx context.Context) {
	now := time.Now()
	// Remove blobs owned by items about to be hard-deleted — after the purge
	// the cascade leaves nothing pointing at them.
	if s.blob != nil {
		if keys, err := s.St.ListPurgeableAttachmentKeys(ctx, ptrTime(now.Add(-softDeleteRetention))); err != nil {
			log.Printf("maintenance: list purgeable attachments: %v", err)
		} else {
			for _, k := range keys {
				if err := s.blob.Delete(ctx, k); err != nil {
					log.Printf("maintenance: delete blob %s: %v", k, err)
				}
			}
		}
	}
	if n, err := s.St.PurgeSoftDeletedItems(ctx, ptrTime(now.Add(-softDeleteRetention))); err != nil {
		log.Printf("maintenance: purge soft-deleted items: %v", err)
	} else if n > 0 {
		log.Printf("maintenance: purged %d soft-deleted items", n)
	}
	if n, err := s.St.PurgeOldActivity(ctx, now.Add(-activityRetention)); err != nil {
		log.Printf("maintenance: purge old activity: %v", err)
	} else if n > 0 {
		log.Printf("maintenance: purged %d old activity rows", n)
	}
	if n, err := s.St.PurgeDeliveredWebhookEvents(ctx, ptrTime(now.Add(-webhookEventRetention))); err != nil {
		log.Printf("maintenance: purge delivered webhook events: %v", err)
	} else if n > 0 {
		log.Printf("maintenance: purged %d delivered webhook events", n)
	}
	if n, err := s.St.PurgeParkedWebhookEvents(ctx, ptrTime(now.Add(-parkedEventRetention))); err != nil {
		log.Printf("maintenance: purge parked webhook events: %v", err)
	} else if n > 0 {
		log.Printf("maintenance: purged %d parked webhook events", n)
	}
	if n, err := s.St.PurgeOldToolCalls(ctx, now.Add(-usageRetention)); err != nil {
		log.Printf("maintenance: purge tool calls: %v", err)
	} else if n > 0 {
		log.Printf("maintenance: purged %d tool-call rows", n)
	}
	if n, err := s.St.PurgeOldSearchLog(ctx, now.Add(-usageRetention)); err != nil {
		log.Printf("maintenance: purge search log: %v", err)
	} else if n > 0 {
		log.Printf("maintenance: purged %d search-log rows", n)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
