package files

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"okf-converter/backend/internal/httpx"
	"okf-converter/backend/internal/middleware"
	"okf-converter/backend/internal/storage"
)

const (
	MaxFileSize        = 25 * 1024 * 1024
	presignedURLExpiry = 15 * time.Minute
)

// OnConfirmed, if set, is called after a file is successfully marked ready -
// the seam the conversion pipeline hooks into (wired in main.go). Kept as a
// plain func field rather than a required constructor arg so this package
// has no compile-time dependency on internal/convert.
type Handlers struct {
	repo    FileRepository
	outputs OutputRepository
	storage storage.Storage

	OnConfirmed func(ctx context.Context, file File, objectKey string)
}

func NewHandlers(repo FileRepository, outputs OutputRepository, store storage.Storage) *Handlers {
	return &Handlers{repo: repo, outputs: outputs, storage: store}
}

func (h *Handlers) UploadURL(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "Not authenticated.")
		return
	}

	var body struct {
		Filename    string `json:"filename"`
		ContentType string `json:"contentType"`
		Size        *int64 `json:"size"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.Filename == "" || body.ContentType == "" || body.Size == nil {
		httpx.Error(w, http.StatusBadRequest, "filename, contentType and size are required.")
		return
	}

	size := *body.Size
	if size <= 0 || size > MaxFileSize {
		httpx.Error(w, http.StatusBadRequest, "File must be between 1 byte and 25 MB.")
		return
	}

	ctx := r.Context()
	objectKey := storage.CreateObjectName(user.ID, body.Filename)

	file, err := h.repo.Create(ctx, user.ID, objectKey, body.Filename, body.ContentType, size)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	uploadURL, err := h.storage.PresignedPutURL(ctx, objectKey, presignedURLExpiry)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"file":      file,
		"uploadUrl": uploadURL,
	})
}

func (h *Handlers) Confirm(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "Not authenticated.")
		return
	}

	fileID := r.PathValue("id")
	ctx := r.Context()

	record, err := h.repo.FindForUser(ctx, fileID, user.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "File not found.")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	info, err := h.storage.StatObject(ctx, record.ObjectKey)
	if err != nil {
		httpx.Error(w, http.StatusConflict, "Upload was not found in storage. It may have failed or the link expired.")
		return
	}

	if info.Size != record.Size {
		_ = h.storage.RemoveObject(ctx, record.ObjectKey)
		_ = h.repo.Delete(ctx, record.ID)
		httpx.Error(w, http.StatusConflict, "Uploaded file does not match the declared size.")
		return
	}

	file, err := h.repo.MarkReady(ctx, record.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	if h.OnConfirmed != nil {
		h.OnConfirmed(ctx, file, record.ObjectKey)
	}

	httpx.JSON(w, http.StatusOK, map[string]File{"file": file})
}

// Outputs lists the converted chunk files produced for a source file, each
// with a short-lived presigned download URL.
func (h *Handlers) Outputs(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "Not authenticated.")
		return
	}

	fileID := r.PathValue("id")
	ctx := r.Context()

	if _, err := h.repo.FindForUser(ctx, fileID, user.ID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "File not found.")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	records, err := h.outputs.ListForFile(ctx, fileID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	outputs := make([]Output, 0, len(records))
	for _, rec := range records {
		url, err := h.storage.PresignedGetURL(ctx, rec.ObjectKey, presignedURLExpiry)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
			return
		}
		outputs = append(outputs, Output{ID: rec.ID, ChunkIndex: rec.ChunkIndex, Size: rec.Size, DownloadURL: url})
	}

	httpx.JSON(w, http.StatusOK, map[string]any{"outputs": outputs})
}
