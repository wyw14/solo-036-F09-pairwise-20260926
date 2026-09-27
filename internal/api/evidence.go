package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-036/internal/evidence"
	"github.com/wyw14/solo-036/internal/service"
)

type confirmEvidenceRequest struct {
	PackageID  string `json:"package_id"`
	ReviewerID string `json:"reviewer_id"`
	Fingerprint string `json:"fingerprint"`
	Note       string `json:"note"`
}

type reviseEvidenceRequest struct {
	RevisionReason string `json:"revision_reason"`
}

func (h *Handler) PrepareEvidence(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	pkg, err := h.runtime.PrepareEvidencePackage(runID)
	if err != nil {
		writeEvidenceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, pkg)
}

func (h *Handler) ConfirmEvidence(w http.ResponseWriter, r *http.Request) {
	var request confirmEvidenceRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	pkg, err := h.runtime.ConfirmEvidencePackage(
		chi.URLParam(r, "runID"),
		strings.TrimSpace(request.PackageID),
		request.ReviewerID,
		strings.TrimSpace(request.Fingerprint),
		request.Note,
	)
	if err != nil {
		writeEvidenceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pkg)
}

func (h *Handler) ListEvidence(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	packages := h.runtime.EvidencePackages(runID)
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "packages": packages})
}

func (h *Handler) GetEvidence(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	version, ok := parseEvidenceVersion(w, r)
	if !ok {
		return
	}
	pkg, err := h.runtime.EvidencePackage(runID, version)
	if err != nil {
		writeEvidenceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pkg)
}

func (h *Handler) ReviseEvidence(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	version, ok := parseEvidenceVersion(w, r)
	if !ok {
		return
	}
	var request reviseEvidenceRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	pkg, err := h.runtime.PrepareEvidenceRevision(runID, version, request.RevisionReason)
	if err != nil {
		writeEvidenceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, pkg)
}

func (h *Handler) VerifyEvidence(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	version, ok := parseEvidenceVersion(w, r)
	if !ok {
		return
	}
	pkg, fingerprint, valid, err := h.runtime.VerifyEvidencePackage(runID, version)
	if err != nil {
		writeEvidenceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"package_id":  pkg.PackageID,
		"version":     version,
		"fingerprint": fingerprint,
		"valid":       valid,
	})
}

func parseEvidenceVersion(w http.ResponseWriter, r *http.Request) (int, bool) {
	value := chi.URLParam(r, "version")
	version, err := strconv.Atoi(value)
	if err != nil || version < 1 {
		writeError(w, r, http.StatusBadRequest, errors.New("version must be a positive integer"))
		return 0, false
	}
	return version, true
}

// writeEvidenceError 把证据包流程的哨兵错误映射为对应 HTTP 状态码。
func writeEvidenceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, service.ErrRunNotFound),
		errors.Is(err, service.ErrDraftNotFound),
		errors.Is(err, service.ErrPackageNotFound):
		writeError(w, r, http.StatusNotFound, err)
	case errors.Is(err, service.ErrRunNotComplete),
		errors.Is(err, service.ErrFingerprintMismatch),
		errors.Is(err, service.ErrVersionConflict),
		errors.Is(err, service.ErrRevisionTip):
		writeError(w, r, http.StatusConflict, err)
	case errors.Is(err, service.ErrReviewerRequired),
		errors.Is(err, service.ErrReasonRequired):
		writeError(w, r, http.StatusBadRequest, err)
	case errors.Is(err, evidence.ErrImmutableConflict):
		writeError(w, r, http.StatusInternalServerError, err)
	default:
		writeError(w, r, http.StatusInternalServerError, err)
	}
}
