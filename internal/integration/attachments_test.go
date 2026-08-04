package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"testing"

	"flightdeck/internal/auth"
	"flightdeck/internal/blob"
	"flightdeck/internal/dto"
	"flightdeck/internal/store"
)

// pngBytes is a minimal payload http.DetectContentType sniffs as image/png.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...)

func multipartUpload(t *testing.T, url, key string, files map[string][]byte) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, data := range files {
		fw, err := mw.CreateFormFile("files", name)
		if err != nil {
			t.Fatalf("form file: %v", err)
		}
		if _, err := fw.Write(data); err != nil {
			t.Fatalf("write part: %v", err)
		}
	}
	mw.Close()
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-API-Key", key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, body
}

func TestIngestAttachmentsFlow(t *testing.T) {
	ts, st, svc, ingestKey := setupHTTP(t)
	svc.SetBlob(blob.NewMemory())
	mkProject(t, st, "attproj")

	readKey := "fd_test_read_key"
	if _, err := st.CreateAPIKey(context.Background(), store.CreateAPIKeyParams{
		Name: "test-rw", KeyHash: auth.HashKey(readKey), Scopes: []string{auth.ScopeRead, auth.ScopeWrite},
	}); err != nil {
		t.Fatalf("create rw key: %v", err)
	}

	// File a bug through the widget endpoint.
	res, body := doJSON(t, http.MethodPost, ts.URL+"/api/ingest/bug", ingestKey, map[string]any{
		"site": "attproj", "message": "screenshot bug", "severity": "high",
	}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("ingest bug: %d %s", res.StatusCode, body)
	}
	var item dto.Item
	if err := json.Unmarshal(body, &item); err != nil {
		t.Fatalf("decode item: %v", err)
	}

	// Attach two screenshots with the public ingest key.
	res, body = multipartUpload(t, ts.URL+"/api/ingest/attachments/"+item.ID, ingestKey,
		map[string][]byte{"one.png": pngBytes, "two.png": pngBytes})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d %s", res.StatusCode, body)
	}
	var atts []dto.Attachment
	if err := json.Unmarshal(body, &atts); err != nil {
		t.Fatalf("decode attachments: %v", err)
	}
	if len(atts) != 2 {
		t.Fatalf("uploaded %d attachments, want 2", len(atts))
	}
	if atts[0].ContentType != "image/png" {
		t.Fatalf("sniffed content type = %q", atts[0].ContentType)
	}

	// Non-image bytes are rejected regardless of filename.
	res, body = multipartUpload(t, ts.URL+"/api/ingest/attachments/"+item.ID, ingestKey,
		map[string][]byte{"evil.png": []byte("#!/bin/sh\necho pwned")})
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("non-image upload: %d %s, want 415", res.StatusCode, body)
	}

	// The read API lists them and streams the bytes back.
	res, body = doJSON(t, http.MethodGet, ts.URL+"/api/items/"+item.ID+"/attachments", readKey, nil, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", res.StatusCode, body)
	}
	if err := json.Unmarshal(body, &atts); err != nil || len(atts) != 2 {
		t.Fatalf("list decode: %v (%d)", err, len(atts))
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+atts[0].URL+"?api_key="+readKey, nil)
	blobRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get blob: %v", err)
	}
	blobBody, _ := io.ReadAll(blobRes.Body)
	blobRes.Body.Close()
	if blobRes.StatusCode != http.StatusOK || !bytes.Equal(blobBody, pngBytes) {
		t.Fatalf("blob roundtrip: %d, %d bytes", blobRes.StatusCode, len(blobBody))
	}
	if ct := blobRes.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("blob content type = %q", ct)
	}

	// The ingest key cannot attach once the freshness window has closed.
	if _, err := st.Pool.Exec(context.Background(),
		`UPDATE items SET created_at = now() - interval '1 hour' WHERE id = $1`, item.ID); err != nil {
		t.Fatalf("age item: %v", err)
	}
	res, body = multipartUpload(t, ts.URL+"/api/ingest/attachments/"+item.ID, ingestKey,
		map[string][]byte{"late.png": pngBytes})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("stale upload: %d %s, want 403", res.StatusCode, body)
	}

	// The write key still can (board/agent path), and delete works.
	res, body = multipartUpload(t, ts.URL+"/api/items/"+item.ID+"/attachments", readKey,
		map[string][]byte{"authed.png": pngBytes})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("authed upload: %d %s", res.StatusCode, body)
	}
	if err := json.Unmarshal(body, &atts); err != nil || len(atts) != 1 {
		t.Fatalf("authed decode: %v", err)
	}
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/api/attachments/"+atts[0].ID, nil)
	req.Header.Set("X-API-Key", readKey)
	delRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	delRes.Body.Close()
	if delRes.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d, want 204", delRes.StatusCode)
	}
}

// Without a blob store configured, uploads answer 503 and the report itself is
// unaffected.
func TestAttachmentsDisabledWithoutBlobStore(t *testing.T) {
	ts, st, _, ingestKey := setupHTTP(t)
	mkProject(t, st, "noblob")

	res, body := doJSON(t, http.MethodPost, ts.URL+"/api/ingest/bug", ingestKey, map[string]any{
		"site": "noblob", "message": "no storage here",
	}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("ingest bug: %d %s", res.StatusCode, body)
	}
	var item dto.Item
	if err := json.Unmarshal(body, &item); err != nil {
		t.Fatalf("decode: %v", err)
	}
	res, body = multipartUpload(t, ts.URL+"/api/ingest/attachments/"+item.ID, ingestKey,
		map[string][]byte{"s.png": pngBytes})
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("upload without store: %d %s, want 503", res.StatusCode, body)
	}
}
