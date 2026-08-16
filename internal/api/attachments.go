package api

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"flightdeck/internal/auth"
	"flightdeck/internal/dto"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

const (
	// maxAttachmentFiles / maxAttachmentBytes bound one upload request. The
	// per-item lifetime cap lives in the service (MaxAttachmentsPerItem).
	maxAttachmentFiles = 4
	maxAttachmentBytes = 8 << 20 // 8MB per file
	// ingestAttachWindow: the public ingest key may only attach to freshly
	// created reporter/capture items — it can never decorate arbitrary board
	// history if the embedded key leaks.
	ingestAttachWindow = 15 * time.Minute
)

// allowedImageTypes are the sniffed (not declared) content types accepted.
var allowedImageTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
	"image/gif":  true,
}

// itemByIDOrRef resolves a path id that may be a UUID or a short ref.
func (s *Server) itemByIDOrRef(r *http.Request) (store.Item, error) {
	raw := r.PathValue("id")
	if id, err := uuid.Parse(raw); err == nil {
		return s.St.GetItem(r.Context(), id)
	}
	return s.St.GetItemByRef(r.Context(), raw)
}

// ingestUploadAttachments is the public-key path: multipart screenshots for a
// just-filed bug/capture item. Same posture as /ingest/bug (CORS, rate limit,
// ingest scope) plus the freshness/source guard.
func (s *Server) ingestUploadAttachments(w http.ResponseWriter, r *http.Request) {
	item, err := s.itemByIDOrRef(r)
	if err != nil {
		writeDBError(w, err)
		return
	}
	if item.Source != "bug_reporter" && item.Source != "capture" {
		writeError(w, http.StatusForbidden, "ingest keys can only attach to reporter/capture items")
		return
	}
	if time.Since(item.CreatedAt) > ingestAttachWindow {
		writeError(w, http.StatusForbidden, "attachment window for this report has closed")
		return
	}
	s.uploadAttachments(w, r, item)
}

// uploadAttachmentsAuthed is the write-scope path (UI, agents): any item, no
// freshness restriction.
func (s *Server) uploadAttachmentsAuthed(w http.ResponseWriter, r *http.Request) {
	item, err := s.itemByIDOrRef(r)
	if err != nil {
		writeDBError(w, err)
		return
	}
	s.uploadAttachments(w, r, item)
}

func (s *Server) uploadAttachments(w http.ResponseWriter, r *http.Request, item store.Item) {
	if s.Svc.Blob() == nil {
		writeError(w, http.StatusServiceUnavailable, "attachment storage not configured")
		return
	}
	// Cap the whole request: N files + form overhead.
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentFiles*maxAttachmentBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data: "+err.Error())
		return
	}

	actor := auth.Actor(r.Context())
	var saved []dto.Attachment
	files := 0
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "read multipart: "+err.Error())
			return
		}
		if part.FormName() != "files" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		files++
		if files > maxAttachmentFiles {
			_ = part.Close()
			writeError(w, http.StatusBadRequest, "at most "+strconv.Itoa(maxAttachmentFiles)+" files per request")
			return
		}
		att, herr := s.saveAttachmentPart(r, item, part, actor)
		_ = part.Close()
		if herr != nil {
			herr.write(w)
			return
		}
		saved = append(saved, att)
	}
	if files == 0 {
		writeError(w, http.StatusBadRequest, `no files — send images in a "files" form field`)
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

type httpErr struct {
	status int
	msg    string
}

func (e *httpErr) write(w http.ResponseWriter) { writeError(w, e.status, e.msg) }

// saveAttachmentPart buffers one multipart file (bounded), sniffs its real
// content type, and hands it to the service. The declared filename/extension is
// kept as display metadata only — the stored content type is the sniffed one.
func (s *Server) saveAttachmentPart(r *http.Request, item store.Item, part *multipart.Part, actor string) (dto.Attachment, *httpErr) {
	data, err := io.ReadAll(io.LimitReader(part, maxAttachmentBytes+1))
	if err != nil {
		return dto.Attachment{}, &httpErr{http.StatusBadRequest, "read file: " + err.Error()}
	}
	if len(data) > maxAttachmentBytes {
		return dto.Attachment{}, &httpErr{http.StatusRequestEntityTooLarge, part.FileName() + " exceeds 8MB"}
	}
	if len(data) == 0 {
		return dto.Attachment{}, &httpErr{http.StatusBadRequest, part.FileName() + " is empty"}
	}
	ctype := http.DetectContentType(data)
	if !allowedImageTypes[ctype] {
		return dto.Attachment{}, &httpErr{http.StatusUnsupportedMediaType, part.FileName() + ": only png, jpeg, webp, or gif images are accepted"}
	}
	att, err := s.Svc.AddAttachment(r.Context(), item.ID, part.FileName(), ctype, int64(len(data)), bytes.NewReader(data), actor)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTooManyAttachments):
			return dto.Attachment{}, &httpErr{http.StatusBadRequest, err.Error()}
		case errors.Is(err, service.ErrNoBlobStore):
			return dto.Attachment{}, &httpErr{http.StatusServiceUnavailable, err.Error()}
		default:
			return dto.Attachment{}, &httpErr{http.StatusInternalServerError, "store attachment failed"}
		}
	}
	return dto.ToAttachment(att, s.Svc.Blob().Bucket()), nil
}

func (s *Server) listItemAttachments(w http.ResponseWriter, r *http.Request) {
	item, err := s.itemByIDOrRef(r)
	if err != nil {
		writeDBError(w, err)
		return
	}
	rows, err := s.Svc.ListAttachments(r.Context(), item.ID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	bucket := ""
	if b := s.Svc.Blob(); b != nil {
		bucket = b.Bucket()
	}
	writeJSON(w, http.StatusOK, dto.ToAttachments(rows, bucket))
}

// getAttachment streams the image. Auth middleware already accepts ?api_key=
// (the SSE fallback), which is what lets plain <img> tags load these.
func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid attachment id")
		return
	}
	att, rc, err := s.Svc.OpenAttachment(r.Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrNoBlobStore) {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeDBError(w, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", att.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(att.SizeBytes, 10))
	w.Header().Set("Content-Disposition", `inline; filename="`+att.Filename+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = io.Copy(w, rc)
}

func (s *Server) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid attachment id")
		return
	}
	if err := s.Svc.DeleteAttachment(r.Context(), id); err != nil {
		writeDBError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
