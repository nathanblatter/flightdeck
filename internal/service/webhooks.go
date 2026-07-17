package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"flightdeck/internal/store"
)

const (
	webhookMaxAttempts = 8
	webhookLeaseBatch  = 50
)

// enqueue writes an event into the durable outbox inside the caller's
// transaction (transactional outbox) — so the event is committed atomically with
// the originating write and a separate worker delivers it with retry. Failures
// to even build the payload are swallowed: a webhook must never break a write.
func (s *Service) enqueue(ctx context.Context, q *store.Queries, projectID uuid.UUID, event string, data any) error {
	payload, err := json.Marshal(map[string]any{
		"event":      event,
		"project_id": projectID.String(),
		"at":         time.Now().UTC(),
		"data":       data,
	})
	if err != nil {
		log.Printf("webhook enqueue marshal (%s): %v", event, err)
		return nil
	}
	_, err = q.EnqueueWebhookEvent(ctx, store.EnqueueWebhookEventParams{
		ProjectID: pgtype.UUID{Bytes: projectID, Valid: true},
		Event:     event,
		Payload:   payload,
	})
	return err
}

// RunWebhookWorker drains the outbox on a ticker until ctx is cancelled.
func (s *Service) RunWebhookWorker(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.processWebhookBatch(ctx)
		}
	}
}

// RunWebhookWorkerOnce drains one batch — exported for tests and one-shot use.
func (s *Service) RunWebhookWorkerOnce(ctx context.Context) { s.processWebhookBatch(ctx) }

func (s *Service) processWebhookBatch(ctx context.Context) {
	events, err := s.St.LeaseWebhookEvents(ctx, webhookLeaseBatch)
	if err != nil {
		log.Printf("webhook lease: %v", err)
		return
	}
	for _, ev := range events {
		s.deliverEvent(ctx, ev)
	}
}

// deliverEvent fans one outbox event out to every currently-matching webhook.
// Delivery is tracked per subscriber (delivered_hook_ids), so a retry only
// re-POSTs to the hooks that failed — never duplicating to ones that already
// ACKed. On full success (or no subscribers) the event is marked delivered;
// otherwise it's rescheduled with exponential backoff until webhookMaxAttempts,
// then parked (a distinct dead-letter state, not delivered).
func (s *Service) deliverEvent(ctx context.Context, ev store.WebhookEvent) {
	hooks, err := s.St.ListActiveWebhooksForEvent(ctx, store.ListActiveWebhooksForEventParams{
		ProjectID: ev.ProjectID,
		Event:     ev.Event,
	})
	if err != nil {
		s.rescheduleEvent(ctx, ev, "lookup: "+err.Error(), ev.DeliveredHookIds)
		return
	}
	already := make(map[uuid.UUID]bool, len(ev.DeliveredHookIds))
	for _, id := range ev.DeliveredHookIds {
		already[id] = true
	}
	delivered := ev.DeliveredHookIds
	allOK := true
	lastErr := ""
	for _, h := range hooks {
		if already[h.ID] {
			continue
		}
		if derr := s.deliver(ctx, h, ev.Event, ev.Payload); derr != nil {
			allOK = false
			lastErr = derr.Error()
		} else {
			delivered = append(delivered, h.ID)
		}
	}
	if allOK {
		if err := s.St.MarkWebhookEventDelivered(ctx, store.MarkWebhookEventDeliveredParams{ID: ev.ID, LastError: ""}); err != nil {
			log.Printf("webhook mark delivered: %v", err)
		}
		return
	}
	if int(ev.Attempts)+1 >= webhookMaxAttempts {
		log.Printf("webhook event %s parked after %d attempts: %s", ev.ID, ev.Attempts+1, lastErr)
		if err := s.St.ParkWebhookEvent(ctx, store.ParkWebhookEventParams{
			ID: ev.ID, LastError: lastErr, DeliveredHookIds: delivered,
		}); err != nil {
			log.Printf("webhook park: %v", err)
		}
		return
	}
	s.rescheduleEvent(ctx, ev, lastErr, delivered)
}

func (s *Service) rescheduleEvent(ctx context.Context, ev store.WebhookEvent, lastErr string, delivered []uuid.UUID) {
	backoff := time.Duration(math.Min(math.Pow(2, float64(ev.Attempts)), 3600)) * time.Second
	if err := s.St.RescheduleWebhookEvent(ctx, store.RescheduleWebhookEventParams{
		ID:               ev.ID,
		NextAttemptAt:    time.Now().Add(backoff),
		LastError:        lastErr,
		DeliveredHookIds: delivered,
	}); err != nil {
		log.Printf("webhook reschedule: %v", err)
	}
}

// deliver POSTs a payload to one subscriber with an optional HMAC signature.
func (s *Service) deliver(ctx context.Context, h store.Webhook, event string, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Flightdeck-Event", event)
	if h.Secret != "" {
		mac := hmac.New(sha256.New, []byte(h.Secret))
		mac.Write(payload)
		req.Header.Set("X-Flightdeck-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
