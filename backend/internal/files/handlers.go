package files

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"okf-converter/backend/internal/bundle"
	"okf-converter/backend/internal/httpx"
	"okf-converter/backend/internal/middleware"
	"okf-converter/backend/internal/storage"
)

const (
	MaxFileSize        = 500 * 1024 * 1024
	presignedURLExpiry = 15 * time.Minute
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
		httpx.Error(w, http.StatusUnauthorized, "No autenticado.")
		return
	}

	var body struct {
		Filename    string `json:"filename"`
		ContentType string `json:"contentType"`
		Size        *int64 `json:"size"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.Filename == "" || body.ContentType == "" || body.Size == nil {
		httpx.Error(w, http.StatusBadRequest, "filename, contentType y size son obligatorios.")
		return
	}

	size := *body.Size
	if size <= 0 || size > MaxFileSize {
		httpx.Error(w, http.StatusBadRequest, "El archivo debe pesar entre 1 byte y 500 MB.")
		return
	}

	// Documents arrive from an untrusted client, so the format is checked on
	// the way in: an upload the pipeline could never convert is refused here
	// instead of consuming storage and a worker before failing.
	if h.SupportedFormat != nil && !h.SupportedFormat(body.ContentType, body.Filename) {
		message := h.SupportedFormatMessage
		if message == "" {
			message = "Formato de archivo no soportado."
		}
		httpx.Error(w, http.StatusUnsupportedMediaType, message)
		return
	}

	ctx := r.Context()
	objectKey := storage.CreateObjectName(user.ID, body.Filename)

	file, err := h.repo.Create(ctx, user.ID, objectKey, body.Filename, body.ContentType, size)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Ocurrió un error inesperado.")
		return
	}

	uploadURL, err := h.storage.PresignedPutURL(ctx, objectKey, presignedURLExpiry)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Ocurrió un error inesperado.")
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
		httpx.Error(w, http.StatusUnauthorized, "No autenticado.")
		return
	}

	fileID := r.PathValue("id")
	ctx := r.Context()

	record, err := h.repo.FindForUser(ctx, fileID, user.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "Archivo no encontrado.")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "Ocurrió un error inesperado.")
		return
	}

	info, err := h.storage.StatObject(ctx, record.ObjectKey)
	if err != nil {
		httpx.Error(w, http.StatusConflict, "No se encontró la subida en el almacenamiento: pudo haber fallado o el enlace expiró.")
		return
	}

	if info.Size != record.Size {
		_ = h.storage.RemoveObject(ctx, record.ObjectKey)
		_ = h.repo.Delete(ctx, record.ID)
		httpx.Error(w, http.StatusConflict, "El archivo subido no coincide con el tamaño declarado.")
		return
	}

	file, err := h.repo.MarkReady(ctx, record.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Ocurrió un error inesperado.")
		return
	}

	if h.EnqueueConversion != nil {
		h.EnqueueConversion(ctx, file, record.ObjectKey, nil)
	}

	// No download URL is handed back here. Conversion has not run, so there
	// is no bundle yet - and whether there will ever be one to download is
	// decided by validation, not by this request. The client polls the file's
	// status and asks for the bundle when it is published (DownloadBundle).
	httpx.JSON(w, http.StatusOK, map[string]any{"file": file})
}

// Status returns a file's current state - including the validation report of
// the bundle built for it, when there is one - so the frontend can poll for
// conversion progress without depending on any server-side session or
// connection: a plain, stateless, per-request read.
func (h *Handlers) Status(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "No autenticado.")
		return
	}

	fileID := r.PathValue("id")
	ctx := r.Context()

	record, err := h.repo.FindForUser(ctx, fileID, user.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "Archivo no encontrado.")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "Ocurrió un error inesperado.")
		return
	}

	response := map[string]any{"file": record.File()}

	// The report is stored as the pipeline serialized it, so it goes out
	// verbatim rather than being decoded and re-encoded by a package that has
	// no reason to know its shape.
	if len(record.ValidationReport) > 0 {
		response["validation_report"] = json.RawMessage(record.ValidationReport)
	}

	httpx.JSON(w, http.StatusOK, response)
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
		httpx.Error(w, http.StatusUnauthorized, "No autenticado.")
		return
	}

	fileID := r.PathValue("id")
	ctx := r.Context()

	record, err := h.repo.FindForUser(ctx, fileID, user.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "Archivo no encontrado.")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "Ocurrió un error inesperado.")
		return
	}

	file, won, err := h.repo.MarkRetrying(ctx, record.ID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Ocurrió un error inesperado.")
		return
	}

	if won {
		var retryOf *string
		prevJob, err := h.jobs.LatestForFile(ctx, record.ID)
		if err == nil {
			retryOf = &prevJob.ID
		} else if !errors.Is(err, ErrNoJobs) {
			httpx.Error(w, http.StatusInternalServerError, "Ocurrió un error inesperado.")
			return
		}

		if h.EnqueueConversion != nil {
			h.EnqueueConversion(ctx, file, record.ObjectKey, retryOf)
		}
	}

	httpx.JSON(w, http.StatusOK, map[string]any{"file": file})
}

// DownloadBundle streams the packaged bundle to its owner.
//
// The download goes through the API rather than a presigned URL because this
// is the only place where the two conditions §6 puts on it can actually be
// enforced: that the caller owns the file, and that the bundle was validated
// and published. A presigned URL, once handed out, answers to whoever holds
// it and knows nothing about either.
//
// The archive is streamed from object storage rather than read into memory,
// so a large bundle costs the API a buffer rather than its whole size.
func (h *Handlers) DownloadBundle(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "No autenticado.")
		return
	}

	ctx := r.Context()

	record, err := h.repo.FindForUser(ctx, r.PathValue("id"), user.ID)
	if err != nil {
		// A file belonging to someone else is reported as missing rather than
		// forbidden: whether a given id exists is not this user's business.
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "Archivo no encontrado.")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "Ocurrió un error inesperado.")
		return
	}

	if !record.Published() {
		httpx.Error(w, http.StatusConflict, unpublishedReason(record))
		return
	}

	objectKey := storage.ResultObjectName(record.ObjectKey)

	// Stat first, so a bundle that is missing from storage is reported as an
	// error with a status code instead of as an empty 200 - once the body
	// starts, the response can no longer say anything went wrong.
	info, err := h.storage.StatObject(ctx, objectKey)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "El bundle no está disponible en el almacenamiento.")
		return
	}

	object, err := h.storage.GetObject(ctx, objectKey)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "El bundle no está disponible en el almacenamiento.")
		return
	}
	defer object.Close()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="`+bundle.RootName(record.OriginalName)+`.zip"`)
	// The name is derived from a user-supplied filename, so make sure no
	// browser tries to interpret the body as anything other than a zip.
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if _, err := io.Copy(w, object); err != nil {
		// The status line and Content-Length are already on the wire, so
		// there is nothing left to tell the client: it will see a body that
		// is shorter than announced and treat the transfer as failed. All
		// that remains is to leave a trace on our side.
		log.Printf("descarga del bundle del archivo %s interrumpida: %v", record.ID, err)
	}
}

// unpublishedReason explains why a bundle cannot be downloaded yet, in terms
// of what the user can do about it.
func unpublishedReason(record FileRecord) string {
	switch record.Status {
	case "failed":
		if record.Validation != nil && *record.Validation == "invalid" {
			return "El bundle generado no superó la validación, así que no se publicó. Revisa el detalle y vuelve a intentarlo."
		}
		return "La conversión falló, así que no hay bundle para descargar. Puedes reintentarla."
	case "pending":
		return "La subida del archivo todavía no se ha confirmado."
	default:
		return "El bundle todavía se está generando. Inténtalo de nuevo en unos segundos."
	}
}

// Outputs lists the files of the bundle produced for a source file - in
// reading order, starting with index.md - each with a short-lived presigned
// download URL.
func (h *Handlers) Outputs(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "No autenticado.")
		return
	}

	fileID := r.PathValue("id")
	ctx := r.Context()

	record, err := h.repo.FindForUser(ctx, fileID, user.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "Archivo no encontrado.")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "Ocurrió un error inesperado.")
		return
	}

	// Same gate as the archive download: the individual files of a bundle
	// that was never published are not on offer either.
	if !record.Published() {
		httpx.Error(w, http.StatusConflict, unpublishedReason(record))
		return
	}

	records, err := h.outputs.ListForFile(ctx, fileID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Ocurrió un error inesperado.")
		return
	}

	outputs := make([]Output, 0, len(records))
	for _, rec := range records {
		url, err := h.storage.PresignedGetURL(ctx, rec.ObjectKey, presignedURLExpiry)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "Ocurrió un error inesperado.")
			return
		}
		outputs = append(outputs, Output{ID: rec.ID, Name: rec.Name, Position: rec.Position, Size: rec.Size, DownloadURL: url})
	}

	httpx.JSON(w, http.StatusOK, map[string]any{"outputs": outputs})
}
