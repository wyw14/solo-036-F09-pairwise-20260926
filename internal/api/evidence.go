package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-036/internal/model"
)

type freezeEvidenceRequest struct {
	Reviewer        string `json:"reviewer"`
	ReviewNote      string `json:"review_note"`
	AcknowledgeGaps bool   `json:"acknowledge_gaps"`
	GapNote         string `json:"gap_note"`
}

type evidenceListResponse struct {
	RunID           string               `json:"run_id"`
	CurrentRevision int                  `json:"current_revision"`
	Packs           []model.EvidencePack `json:"packs"`
}

// BuildEvidence creates or rebuilds the open draft evidence pack for a
// completed run. Frozen revisions are never affected.
func (h *Handler) BuildEvidence(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	pack, err := h.runtime.BuildEvidencePack(runID)
	if err != nil {
		writeEvidenceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, pack)
}

// ListEvidence returns every evidence revision of a run and the current
// (highest frozen) revision.
func (h *Handler) ListEvidence(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	packs, err := h.runtime.EvidencePacks(runID)
	if err != nil {
		writeEvidenceError(w, r, err)
		return
	}
	current := 0
	for _, pack := range packs {
		if pack.Status == model.PackFrozen {
			current = pack.Revision
		}
	}
	writeJSON(w, http.StatusOK, evidenceListResponse{RunID: runID, CurrentRevision: current, Packs: packs})
}

// GetEvidence returns one evidence revision.
func (h *Handler) GetEvidence(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	revision, err := parseRevision(chi.URLParam(r, "revision"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	pack, err := h.runtime.EvidencePackRevision(runID, revision)
	if err != nil {
		writeEvidenceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pack)
}

// FreezeEvidence fixes a draft after reviewer confirmation. Reviewer is
// required and unacknowledged gaps block the freeze.
func (h *Handler) FreezeEvidence(w http.ResponseWriter, r *http.Request) {
	runID := chi.URLParam(r, "runID")
	revision, err := parseRevision(chi.URLParam(r, "revision"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	var request freezeEvidenceRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(request.Reviewer) == "" {
		writeError(w, r, http.StatusBadRequest, errors.New("reviewer is required to freeze an evidence pack"))
		return
	}
	pack, err := h.runtime.FreezeEvidencePack(runID, revision, request.Reviewer, request.ReviewNote, request.AcknowledgeGaps, request.GapNote)
	if err != nil {
		writeEvidenceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pack)
}

func parseRevision(value string) (int, error) {
	revision, err := strconv.Atoi(value)
	if err != nil || revision <= 0 {
		return 0, errors.New("revision must be a positive integer")
	}
	return revision, nil
}

// writeEvidenceError maps service errors: unknown run/revision -> 404, any
// other business-rule failure (not complete, gaps, already frozen) -> 409.
func writeEvidenceError(w http.ResponseWriter, r *http.Request, err error) {
	if strings.Contains(err.Error(), "not found") {
		writeError(w, r, http.StatusNotFound, err)
		return
	}
	writeError(w, r, http.StatusConflict, err)
}
