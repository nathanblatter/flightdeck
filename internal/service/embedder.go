package service

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"
	"unicode/utf8"

	"flightdeck/internal/embed"
	"flightdeck/internal/pgvec"
	"flightdeck/internal/store"
)

const (
	embedTickInterval = 15 * time.Second // how often to drain the embed backlog
	embedBatchSize    = 50               // rows embedded per OpenAI request
	embedMaxChars     = 8000             // cap per-row text to bound token cost
)

// RunEmbedder backfills missing embeddings for items AND all activity
// with a non-empty body: once on startup, then on a ticker until ctx
// is cancelled. It is a no-op when embeddings aren't configured (no
// OPENAI_API_KEY), so semantic search simply stays dark and the rest of the
// system is unaffected. Items are picked up whenever their embedding is NULL —
// on first creation, or after a content edit resets it (see UpdateItem).
// Content the API rejects outright (a 4xx) is parked with a 'failed' marker so
// one poison row can never stall the backlog behind it.
func (s *Service) RunEmbedder(ctx context.Context) {
	if !s.emb.Enabled() {
		log.Println("embedder: OPENAI_API_KEY not set — semantic search disabled")
		return
	}
	log.Printf("embedder: enabled (model=%s)", s.emb.Model())
	s.embedBacklogOnce(ctx)
	t := time.NewTicker(embedTickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.embedBacklogOnce(ctx)
		}
	}
}

func (s *Service) embedBacklogOnce(ctx context.Context) {
	s.drainBacklog(ctx, "items", s.itemBatch)
	s.drainBacklog(ctx, "activity", s.activityBatch)
}

// embedRow is one backlog row to embed, plus the callbacks to persist the
// result — shared by the item and activity drains.
type embedRow struct {
	text  string
	save  func(ctx context.Context, vec []float32) error
	park  func(ctx context.Context) error // mark poison so it's never retried
	label string                          // for logs (ref or id)
}

// drainBacklog embeds one kind of backlog in batches until it's empty or a
// transient failure says try again next tick.
func (s *Service) drainBacklog(ctx context.Context, what string, nextBatch func(ctx context.Context) ([]embedRow, error)) {
	for {
		rows, err := nextBatch(ctx)
		if err != nil {
			log.Printf("embedder: list %s backlog: %v", what, err)
			return
		}
		if len(rows) == 0 {
			return
		}
		if ok := s.embedBatch(ctx, what, rows); !ok {
			return // transient failure — retry next tick
		}
		if len(rows) < embedBatchSize {
			return
		}
	}
}

// embedBatch embeds a batch in one API call; if the whole call fails it falls
// back to per-row embedding so a single poison row is isolated and parked
// instead of stalling everything behind it. Returns false on a transient
// failure (worth retrying next tick), true when the batch was fully handled.
func (s *Service) embedBatch(ctx context.Context, what string, rows []embedRow) bool {
	inputs := make([]string, len(rows))
	for i, r := range rows {
		inputs[i] = r.text
	}
	vecs, err := s.emb.Embed(ctx, inputs)
	if err != nil {
		if !embed.IsPoison(err) {
			log.Printf("embedder: %s batch of %d: %v", what, len(rows), err)
			return false
		}
		// The API rejected the batch input — some row is poison. Retry rows
		// individually so the good ones land and the bad ones get parked.
		log.Printf("embedder: %s batch rejected, isolating per row: %v", what, err)
		return s.embedRowsIndividually(ctx, what, rows)
	}
	for i, r := range rows {
		if err := s.saveVec(ctx, what, r, vecs[i]); err != nil {
			log.Printf("embedder: %s %s: save: %v", what, r.label, err)
			return false
		}
	}
	return true
}

func (s *Service) embedRowsIndividually(ctx context.Context, what string, rows []embedRow) bool {
	for _, r := range rows {
		vecs, err := s.emb.Embed(ctx, []string{r.text})
		if err != nil {
			if !embed.IsPoison(err) {
				// Transient mid-loop (outage, rate limit) — stop and retry next tick.
				log.Printf("embedder: %s %s: %v", what, r.label, err)
				return false
			}
			log.Printf("embedder: %s %s: rejected by API, parking: %v", what, r.label, err)
			if perr := r.park(ctx); perr != nil {
				log.Printf("embedder: %s %s: park: %v", what, r.label, perr)
				return false
			}
			continue
		}
		if err := s.saveVec(ctx, what, r, vecs[0]); err != nil {
			log.Printf("embedder: %s %s: save: %v", what, r.label, err)
			return false
		}
	}
	return true
}

// saveVec persists one embedding, parking the row instead when the model
// returned an unusable vector (wrong dimensionality).
func (s *Service) saveVec(ctx context.Context, what string, r embedRow, vec []float32) error {
	if len(vec) != embed.Dims {
		log.Printf("embedder: %s %s: unexpected dim %d, parking", what, r.label, len(vec))
		return r.park(ctx)
	}
	return r.save(ctx, vec)
}

// itemBatch loads the next batch of items needing an embedding.
func (s *Service) itemBatch(ctx context.Context) ([]embedRow, error) {
	items, err := s.St.ListItemsNeedingEmbedding(ctx, embedBatchSize)
	if err != nil {
		return nil, err
	}
	model := s.emb.Model()
	rows := make([]embedRow, len(items))
	for i, it := range items {
		it := it
		rows[i] = embedRow{
			text:  embedText(it.Title, it.Body),
			label: it.Ref,
			save: func(ctx context.Context, vec []float32) error {
				return s.St.SetItemEmbedding(ctx, store.SetItemEmbeddingParams{
					ID: it.ID, Embedding: pgvec.New(vec), EmbeddingModel: model,
				})
			},
			park: func(ctx context.Context) error {
				return s.St.MarkItemEmbeddingFailed(ctx, it.ID)
			},
		}
	}
	return rows, nil
}

// activityBatch loads the next batch of activity
// needing an embedding.
func (s *Service) activityBatch(ctx context.Context) ([]embedRow, error) {
	acts, err := s.St.ListActivityNeedingEmbedding(ctx, embedBatchSize)
	if err != nil {
		return nil, err
	}
	model := s.emb.Model()
	rows := make([]embedRow, len(acts))
	for i, a := range acts {
		a := a
		rows[i] = embedRow{
			text:  truncBytes(a.Body, embedMaxChars),
			label: a.ID.String(),
			save: func(ctx context.Context, vec []float32) error {
				return s.St.InsertActivityEmbedding(ctx, store.InsertActivityEmbeddingParams{
					ActivityID: a.ID, Embedding: pgvec.New(vec), Model: model,
				})
			},
			park: func(ctx context.Context) error {
				return s.St.InsertActivityEmbedding(ctx, store.InsertActivityEmbeddingParams{
					ActivityID: a.ID, Model: "failed",
				})
			},
		}
	}
	return rows, nil
}

// embedText builds the string fed to the embedding model: title plus body,
// capped so a giant row can't blow up token cost.
func embedText(title, body string) string {
	text := title
	if body != "" {
		text += "\n\n" + body
	}
	return truncBytes(text, embedMaxChars)
}

// truncBytes caps s at max bytes without splitting a multi-byte rune (invalid
// UTF-8 would be rejected by the embeddings API).
func truncBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// envFloat reads a float env var, falling back to def.
func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
