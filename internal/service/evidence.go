package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/wyw14/solo-036/internal/evidence"
	"github.com/wyw14/solo-036/internal/model"
)

// BuildEvidencePack assembles a draft evidence pack for a completed run from
// its scattered records. If the run's latest revision is still a draft, that
// draft is rebuilt in place; once a revision is frozen, later builds start a
// new revision extending the latest frozen one. Frozen revisions are never
// touched.
func (r *Runtime) BuildEvidencePack(runID string) (model.EvidencePack, error) {
	runRecord, ok := r.Runs.Get(runID)
	if !ok {
		return model.EvidencePack{}, fmt.Errorf("run %s not found", runID)
	}
	snapshot := r.Snapshot()
	operation, ok := snapshot.Operation(runID)
	if !ok {
		return model.EvidencePack{}, fmt.Errorf("run %s not found", runID)
	}
	incidents := make([]model.Incident, 0)
	for _, incident := range snapshot.Incidents {
		if incident.RunID == runID {
			incidents = append(incidents, incident)
		}
	}
	events := make([]model.Event, 0)
	for _, event := range r.Journal.Events() {
		if event.RunID == runID {
			events = append(events, event)
		}
	}
	revision, extends := r.nextEvidenceRevision(runID)
	pack, err := evidence.Build(evidence.Source{
		Run:             runRecord,
		Operation:       operation,
		Routes:          snapshot.Routes,
		Incidents:       incidents,
		JournalEvents:   events,
		Generation:      r.Tracker.Generation(runID),
		ControlRevision: snapshot.Revision,
	}, revision, extends, time.Now().UTC())
	if err != nil {
		return model.EvidencePack{}, err
	}
	if err := r.Evidence.SaveDraft(pack); err != nil {
		return model.EvidencePack{}, err
	}
	return pack, nil
}

// nextEvidenceRevision decides where a new draft goes. An unfixed top
// revision is rebuilt; otherwise the build opens a fresh revision extending
// the latest frozen one.
func (r *Runtime) nextEvidenceRevision(runID string) (revision, extends int) {
	packs := r.Evidence.List(runID)
	if len(packs) == 0 {
		return 1, 0
	}
	latest := packs[len(packs)-1]
	if latest.Status == model.PackDraft {
		return latest.Revision, latest.ExtendsRevision
	}
	return latest.Revision + 1, r.Evidence.LatestFrozenRevision(runID)
}

// FreezeEvidencePack fixes a draft revision after reviewer confirmation. The
// reviewer is required, and unacknowledged gaps block the freeze. A frozen
// revision is immutable; the freeze is also written to the durable journal,
// and a journal failure rolls the revision back to draft so the two never
// disagree.
func (r *Runtime) FreezeEvidencePack(runID string, revision int, reviewer, reviewNote string, ackGaps bool, gapNote string) (model.EvidencePack, error) {
	if _, ok := r.Runs.Get(runID); !ok {
		return model.EvidencePack{}, fmt.Errorf("run %s not found", runID)
	}
	reviewer = strings.TrimSpace(reviewer)
	pack, err := r.Evidence.Freeze(runID, revision, reviewer, reviewNote, ackGaps, strings.TrimSpace(gapNote))
	if err != nil {
		return model.EvidencePack{}, err
	}
	payload := fmt.Sprintf("revision=%d reviewer=%s hash=%s", pack.Revision, pack.Reviewer, pack.ContentHash)
	if eventErr := r.appendEvent(model.Event{
		RunID:   runID,
		Kind:    "evidence_frozen",
		Epoch:   pack.BasedOnEpoch,
		Payload: payload,
	}); eventErr != nil {
		_ = r.Evidence.Unfreeze(runID, revision)
		return model.EvidencePack{}, fmt.Errorf("freeze evidence: journal: %w", eventErr)
	}
	return pack, nil
}

// EvidencePacks returns every evidence revision for a run, oldest first.
func (r *Runtime) EvidencePacks(runID string) ([]model.EvidencePack, error) {
	if _, ok := r.Runs.Get(runID); !ok {
		return nil, fmt.Errorf("run %s not found", runID)
	}
	return r.Evidence.List(runID), nil
}

// EvidencePackRevision returns one evidence revision.
func (r *Runtime) EvidencePackRevision(runID string, revision int) (model.EvidencePack, error) {
	if _, ok := r.Runs.Get(runID); !ok {
		return model.EvidencePack{}, fmt.Errorf("run %s not found", runID)
	}
	pack, ok := r.Evidence.Get(runID, revision)
	if !ok {
		return model.EvidencePack{}, fmt.Errorf("evidence revision %d for run %s not found", revision, runID)
	}
	return pack, nil
}
