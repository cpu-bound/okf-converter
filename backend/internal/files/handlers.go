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
	MaxFileSize        = 500 * 1024 * 1024
	presignedURLExpiry = 15 * time.Minute
	// resultURLExpiry is long-lived relative to presignedURLExpiry because
	// this URL is handed back before conversion even starts - the caller
	// may not check back for a while, and (unlike the upload PUT) it must
	// still be valid whenever they do.
	resultURLExpiry = 24 * time.Hour
)

// Handlers serves the upload side of the API. Both func fields are seams the
// conversion pipeline hooks into (wired in main.go), kept as plain fields
// rather than required constructor arguments so this package has no
// compile-time dependency on internal/convert.
type Handlers struct {
	repo    FileRepository
	outputs OutputRepository
	jobs    JobRepository
	storage storage.Storage

	// EnqueueConversion is called to push a conversion job for a file - on
	// first upload confirmation (retryOf nil) and on retry (retryOf pointing
	// at the job attempt being retried).
	EnqueueConversion func(ctx context.Context, file File, objectKey string, retryOf *string)

	// SupportedFormat reports whether the pipeline can convert a document,
	// so an unconvertible upload is refused before a presigned URL is even
	// issued rather than being accepted and failing in a worker later. When
	// nil, every format is accepted.
	SupportedFormat func(contentType, filename string) bool

	// SupportedFormatMessage is shown to the user when SupportedFormat says
	// no.
	SupportedFormatMessage string
}

func NewHandlers(repo FileRepository, outputs OutputRepository, jobs JobRepository, store storage.Storage) *Handlers {
	return &Handlers{repo: repo, outputs: outputs, jobs: jobs, storage: store}
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
		httpx.Error(w, http.StatusBadRequest, "File must be between 1 byte and 500 MB.")
		return
	}

	// Documents arrive from an untrusted client, so the format is checked on
	// the way in: an upload the pipeline could never convert is refused here
	// instead of consuming storage and a worker before failing.
	if h.SupportedFormat != nil && !h.SupportedFormat(body.ContentType, body.Filename) {
		message := h.SupportedFormatMessage
		if message == "" {
			message = "Unsupported file format."
		}
		httpx.Error(w, http.StatusUnsupportedMediaType, message)
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

	// Presigning is a pure signature over bucket/key/expiry - it doesn't
	// require the object to exist, so the result URL can be handed back
	// now even though conversion hasn't run yet. It simply 404s from MinIO
	// until the worker writes storage.ResultObjectName(record.ObjectKey).
	resultURL, err := h.storage.PresignedGetURL(ctx, storage.ResultObjectName(record.ObjectKey), resultURLExpiry)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	if h.EnqueueConversion != nil {
		h.EnqueueConversion(ctx, file, record.ObjectKey, nil)
	}

	httpx.JSON(w, http.StatusOK, map[string]any{"file": file, "resultUrl": resultURL})
}

// Status returns a file's current state, so the frontend can poll for
// conversion progress without depending on any server-side session or
// connection - a plain, stateless, per-request read.
func (h *Handlers) Status(w http.ResponseWriter, r *http.Request) {
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

	httpx.JSON(w, http.StatusOK, map[string]File{"file": record.File()})
}

// Retry re-enqueues a conversion job for a file whose previous attempt
// failed. It's idempotent by design rather than by erroring: whether this
// particular call is the one that wins the failed->converting transition
// or not, it always responds with the file's current state and a fresh
// result URL, so a duplicate/concurrent retry call never double-enqueues
// and is always safe to repeat.
func (h *Handlers) Retry(w http.ResponseWriter, r *http.Request) {
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

	file, won, err := h.repo.MarkRetrying(ctx, record.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	if won {
		var retryOf *string
		prevJob, err := h.jobs.LatestForFile(ctx, record.ID)
		if err == nil {
			retryOf = &prevJob.ID
		} else if !errors.Is(err, ErrNoJobs) {
			httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
			return
		}

		if h.EnqueueConversion != nil {
			h.EnqueueConversion(ctx, file, record.ObjectKey, retryOf)
		}
	}

	resultURL, err := h.storage.PresignedGetURL(ctx, storage.ResultObjectName(record.ObjectKey), resultURLExpiry)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	httpx.JSON(w, http.StatusOK, map[string]any{"file": file, "resultUrl": resultURL})
}

// Outputs lists the files of the bundle produced for a source file - in
// reading order, starting with index.md - each with a short-lived presigned
// download URL.
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
		outputs = append(outputs, Output{ID: rec.ID, Name: rec.Name, Position: rec.Position, Size: rec.Size, DownloadURL: url})
	}

	httpx.JSON(w, http.StatusOK, map[string]any{"outputs": outputs})
}
