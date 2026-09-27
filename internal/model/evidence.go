package model

import "time"

// EvidenceStatus describes whether a category of records was found for a run.
type EvidenceStatus string

const (
	EvidenceRecorded EvidenceStatus = "recorded"
	EvidenceMissing  EvidenceStatus = "missing"
)

// PackStatus is the lifecycle state of an evidence pack revision.
type PackStatus string

const (
	PackDraft  PackStatus = "draft"
	PackFrozen PackStatus = "frozen"
)

// Evidence categories, in the fixed order they appear in every pack.
const (
	CategoryRunState  = "run_state"
	CategoryRoute     = "route"
	CategoryPressure  = "pressure"
	CategoryTracking  = "tracking"
	CategoryPermits   = "permits"
	CategoryIncidents = "incidents"
	CategoryJournal   = "journal"
)

// EvidenceItem is one category's adopted records or its gap.
type EvidenceItem struct {
	Category    string         `json:"category"`
	Status      EvidenceStatus `json:"status"`
	Summary     string         `json:"summary"`
	Detail      any            `json:"detail,omitempty"`
	GapReason   string         `json:"gap_reason,omitempty"`
	RecordCount int            `json:"record_count"`
}

// EvidencePack is one immutable-addressable revision of a run's evidence.
type EvidencePack struct {
	RunID           string         `json:"run_id"`
	Revision        int            `json:"revision"`
	ExtendsRevision int            `json:"extends_revision"`
	BasedOnEpoch    int64          `json:"based_on_epoch"`
	ControlRevision string         `json:"control_revision"`
	CapturedAt      time.Time      `json:"captured_at"`
	Status          PackStatus     `json:"status"`
	Items           []EvidenceItem `json:"items"`
	GapsAcked       bool           `json:"gaps_acked"`
	GapNote         string         `json:"gap_note,omitempty"`
	Reviewer        string         `json:"reviewer,omitempty"`
	ReviewNote      string         `json:"review_note,omitempty"`
	FrozenAt        time.Time      `json:"frozen_at,omitempty"`
	ContentHash     string         `json:"content_hash,omitempty"`
}

// HasGaps reports whether any category was recorded as missing.
func (p EvidencePack) HasGaps() bool {
	for _, item := range p.Items {
		if item.Status == EvidenceMissing {
			return true
		}
	}
	return false
}
