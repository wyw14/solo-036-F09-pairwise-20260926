package evidence

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/wyw14/solo-036/internal/model"
)

// Store keeps every evidence pack revision for every run. Frozen revisions are
// immutable: they can never be overwritten, re-frozen, or edited. Corrections
// after a freeze only ever create a new, higher revision.
type Store struct {
	mu    sync.RWMutex
	packs map[string]map[int]model.EvidencePack
}

func NewStore() *Store {
	return &Store{packs: make(map[string]map[int]model.EvidencePack)}
}

// NextRevision returns the revision number a new draft for runID should use:
// one past the highest existing revision, or 1 for a first pack.
func (s *Store) NextRevision(runID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nextRevisionLocked(runID)
}

func (s *Store) nextRevisionLocked(runID string) int {
	revisions, ok := s.packs[runID]
	if !ok || len(revisions) == 0 {
		return 1
	}
	highest := 0
	for revision := range revisions {
		if revision > highest {
			highest = revision
		}
	}
	return highest + 1
}

// LatestFrozenRevision returns the highest frozen revision for runID, or 0.
func (s *Store) LatestFrozenRevision(runID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	latest := 0
	for revision, pack := range s.packs[runID] {
		if pack.Status == model.PackFrozen && revision > latest {
			latest = revision
		}
	}
	return latest
}

// SaveDraft stores a draft pack. A draft may be rebuilt in place, but the
// target revision must not already be frozen — frozen content is immutable.
func (s *Store) SaveDraft(pack model.EvidencePack) error {
	if pack.RunID == "" || pack.Revision <= 0 {
		return errors.New("evidence pack identity is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	revisions, ok := s.packs[pack.RunID]
	if !ok {
		revisions = make(map[int]model.EvidencePack)
		s.packs[pack.RunID] = revisions
	}
	if existing, exists := revisions[pack.Revision]; exists && existing.Status == model.PackFrozen {
		return fmt.Errorf("evidence revision %d for run %s is frozen and cannot be replaced", pack.Revision, pack.RunID)
	}
	pack.Status = model.PackDraft
	revisions[pack.Revision] = pack
	return nil
}

// Freeze marks a draft revision as reviewed and immutable. The reviewer is
// required; a pack with unacknowledged gaps cannot be frozen. Freezing a
// revision that is already frozen, or does not exist, is an error.
func (s *Store) Freeze(runID string, revision int, reviewer, reviewNote string, ackGaps bool, gapNote string) (model.EvidencePack, error) {
	if reviewer == "" {
		return model.EvidencePack{}, errors.New("reviewer is required to freeze an evidence pack")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pack, err := s.draftLocked(runID, revision)
	if err != nil {
		return model.EvidencePack{}, err
	}
	if pack.HasGaps() && !ackGaps {
		return model.EvidencePack{}, errors.New("evidence pack has unacknowledged gaps")
	}
	hash, err := HashPack(pack)
	if err != nil {
		return model.EvidencePack{}, err
	}
	pack.Status = model.PackFrozen
	pack.Reviewer = reviewer
	pack.ReviewNote = reviewNote
	pack.GapsAcked = ackGaps
	pack.GapNote = gapNote
	pack.FrozenAt = time.Now().UTC()
	pack.ContentHash = hash
	s.packs[runID][revision] = pack
	return pack, nil
}

// Unfreeze reverts a just-frozen revision back to draft. It exists solely to
// roll back a freeze whose audit event could not be journaled; it is not
// exposed through any API and must only be called immediately after Freeze.
func (s *Store) Unfreeze(runID string, revision int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pack, ok := s.packs[runID][revision]
	if !ok {
		return fmt.Errorf("evidence revision %d for run %s not found", revision, runID)
	}
	if pack.Status != model.PackFrozen {
		return fmt.Errorf("evidence revision %d for run %s is not frozen", revision, runID)
	}
	pack.Status = model.PackDraft
	pack.Reviewer = ""
	pack.ReviewNote = ""
	pack.GapsAcked = false
	pack.GapNote = ""
	pack.FrozenAt = time.Time{}
	pack.ContentHash = ""
	s.packs[runID][revision] = pack
	return nil
}

// Get returns one revision of a run's evidence.
func (s *Store) Get(runID string, revision int) (model.EvidencePack, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pack, ok := s.packs[runID][revision]
	return pack, ok
}

// List returns every revision of a run's evidence, ordered by revision.
func (s *Store) List(runID string) []model.EvidencePack {
	s.mu.RLock()
	defer s.mu.RUnlock()
	revisions := s.packs[runID]
	out := make([]model.EvidencePack, 0, len(revisions))
	for _, pack := range revisions {
		out = append(out, pack)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Revision < out[j].Revision })
	return out
}

func (s *Store) draftLocked(runID string, revision int) (model.EvidencePack, error) {
	pack, ok := s.packs[runID][revision]
	if !ok {
		return model.EvidencePack{}, fmt.Errorf("evidence revision %d for run %s not found", revision, runID)
	}
	if pack.Status == model.PackFrozen {
		return model.EvidencePack{}, fmt.Errorf("evidence revision %d for run %s is already frozen", revision, runID)
	}
	return pack, nil
}
