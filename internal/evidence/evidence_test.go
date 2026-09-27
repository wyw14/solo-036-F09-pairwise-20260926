package evidence

import (
	"strings"
	"testing"
	"time"

	"github.com/wyw14/solo-036/internal/model"
)

func completedRun() model.Run {
	return model.Run{
		ID:        "run-1",
		Station:   "station-a",
		State:     model.StateComplete,
		Epoch:     7,
		CreatedAt: time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC),
	}
}

func fullSource() Source {
	return Source{
		Run: completedRun(),
		Operation: model.OperationRecord{
			Run:      completedRun(),
			Route:    model.RouteState{ID: "main-a-c", FromStation: "station-a", ToStation: "station-c"},
			Pressure: model.PressureState{StationID: "station-c", RunID: "run-1", Value: 0, Target: 0, Mode: model.CompressorStopped},
			Tracking: []model.TrackingState{
				{RunID: "run-1", StationID: "station-b", SourceID: "mag-1", Generation: 1, Position: 86.4, Online: true},
				{RunID: "run-1", StationID: "station-b", SourceID: "ac-1", Generation: 1, Position: 86.4, Online: true},
			},
			Permits: []model.PermitState{{RunID: "run-1", Name: "repressurization", Generation: 1, Granted: true}},
		},
		Routes: []model.RouteState{{ID: "main-a-c", FromStation: "station-a", ToStation: "station-c"}},
		Incidents: []model.Incident{
			{ID: "i-1", RunID: "run-1", Severity: "warning", Message: "late signal"},
		},
		JournalEvents: []model.Event{
			{ID: "e-1", RunID: "run-1", Kind: "launch", Payload: "main-a-c", At: time.Date(2026, 9, 26, 8, 1, 0, 0, time.UTC)},
			{ID: "e-2", RunID: "run-1", Kind: "complete", Payload: "main-a-c", At: time.Date(2026, 9, 26, 8, 59, 0, 0, time.UTC)},
		},
		Generation:      1,
		ControlRevision: "rev-1",
	}
}

func itemByCategory(pack model.EvidencePack, category string) model.EvidenceItem {
	for _, item := range pack.Items {
		if item.Category == category {
			return item
		}
	}
	return model.EvidenceItem{}
}

func TestBuildRecordsEveryCategory(t *testing.T) {
	pack, err := Build(fullSource(), 1, 0, time.Date(2026, 9, 26, 9, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(pack.Items) != 7 {
		t.Fatalf("expected 7 categories, got %d", len(pack.Items))
	}
	for _, category := range []string{
		model.CategoryRunState, model.CategoryRoute, model.CategoryPressure,
		model.CategoryTracking, model.CategoryPermits, model.CategoryIncidents, model.CategoryJournal,
	} {
		item := itemByCategory(pack, category)
		if item.Category == "" {
			t.Errorf("missing category %s", category)
			continue
		}
		if item.Status != model.EvidenceRecorded {
			t.Errorf("category %s should be recorded, got %s (%s)", category, item.Status, item.GapReason)
		}
	}
	if pack.HasGaps() {
		t.Error("full source should have no gaps")
	}
	if pack.Status != model.PackDraft {
		t.Errorf("new pack should be draft, got %s", pack.Status)
	}
}

func TestBuildRequiresCompleteRun(t *testing.T) {
	src := fullSource()
	src.Run.State = model.StateTracking
	if _, err := Build(src, 1, 0, time.Now()); err == nil {
		t.Fatal("expected error building evidence for a non-complete run")
	}
}

func TestBuildFlagsGaps(t *testing.T) {
	src := fullSource()
	src.Operation.Tracking = nil
	src.Operation.Pressure = model.PressureState{}
	src.Operation.Permits = nil
	src.Operation.Route = model.RouteState{}
	src.JournalEvents = nil
	src.Incidents = nil
	pack, err := Build(src, 1, 0, time.Now())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !pack.HasGaps() {
		t.Fatal("expected gaps")
	}
	for _, category := range []string{model.CategoryRoute, model.CategoryPressure, model.CategoryTracking, model.CategoryPermits, model.CategoryJournal} {
		item := itemByCategory(pack, category)
		if item.Status != model.EvidenceMissing {
			t.Errorf("category %s should be missing, got %s", category, item.Status)
		}
		if item.GapReason == "" {
			t.Errorf("category %s gap should carry a reason", category)
		}
	}
	// Zero incidents is a clean run, not a gap.
	if item := itemByCategory(pack, model.CategoryIncidents); item.Status != model.EvidenceRecorded {
		t.Errorf("zero incidents should still be recorded, got %s", item.Status)
	}
}

func TestRouteRecoveredFromJournalAfterLeaseRelease(t *testing.T) {
	src := fullSource()
	// Completion released the lease: no live attribution, route kept in Routes.
	src.Operation.Route = model.RouteState{}
	pack, err := Build(src, 1, 0, time.Now())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	item := itemByCategory(pack, model.CategoryRoute)
	if item.Status != model.EvidenceRecorded {
		t.Fatalf("route should be recovered from journal, got %s: %s", item.Status, item.GapReason)
	}
	if !strings.Contains(item.Summary, "main-a-c") {
		t.Errorf("route summary should name the route, got %q", item.Summary)
	}
}

func TestHashStableAndContentSensitive(t *testing.T) {
	at := time.Date(2026, 9, 26, 9, 5, 0, 0, time.UTC)
	pack, err := Build(fullSource(), 1, 0, at)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	first, err := HashPack(pack)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	second, err := HashPack(pack)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if first != second {
		t.Error("hash must be stable for identical content")
	}
	changed := pack
	changed.Items = append(append([]model.EvidenceItem(nil), pack.Items...), model.EvidenceItem{Category: "extra", Status: model.EvidenceRecorded})
	other, err := HashPack(changed)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if other == first {
		t.Error("hash must change when content changes")
	}
	// Freeze bookkeeping must not affect the content hash.
	frozen := pack
	frozen.Status = model.PackFrozen
	frozen.Reviewer = "ops-lead"
	frozenHash, err := HashPack(frozen)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if frozenHash != first {
		t.Error("hash must not depend on freeze bookkeeping")
	}
}

func TestStoreFreezeLifecycle(t *testing.T) {
	store := NewStore()
	pack, err := Build(fullSource(), 1, 0, time.Now())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := store.SaveDraft(pack); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	frozen, err := store.Freeze("run-1", 1, "ops-lead", "looks good", false, "")
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if frozen.Status != model.PackFrozen || frozen.Reviewer != "ops-lead" || frozen.ContentHash == "" {
		t.Errorf("frozen pack incomplete: %+v", frozen)
	}
	// Re-freezing a frozen revision is rejected.
	if _, err := store.Freeze("run-1", 1, "someone-else", "", false, ""); err == nil {
		t.Error("re-freezing a frozen revision must fail")
	}
	// Overwriting a frozen revision is rejected.
	if err := store.SaveDraft(pack); err == nil {
		t.Error("replacing a frozen revision must fail")
	}
	// The stored frozen revision is unchanged by later drafts.
	next := pack
	next.Revision = 2
	next.ExtendsRevision = 1
	if err := store.SaveDraft(next); err != nil {
		t.Fatalf("save next draft: %v", err)
	}
	got, ok := store.Get("run-1", 1)
	if !ok {
		t.Fatal("revision 1 missing")
	}
	if got.ContentHash != frozen.ContentHash || got.Reviewer != "ops-lead" {
		t.Error("frozen revision 1 must be untouched by revision 2")
	}
	if store.LatestFrozenRevision("run-1") != 1 {
		t.Errorf("latest frozen revision should be 1, got %d", store.LatestFrozenRevision("run-1"))
	}
}

func TestStoreFreezeRequiresReviewer(t *testing.T) {
	store := NewStore()
	pack, _ := Build(fullSource(), 1, 0, time.Now())
	_ = store.SaveDraft(pack)
	if _, err := store.Freeze("run-1", 1, "", "", false, ""); err == nil {
		t.Error("freeze without reviewer must fail")
	}
}

func TestStoreFreezeRequiresGapAcknowledgement(t *testing.T) {
	store := NewStore()
	src := fullSource()
	src.Operation.Tracking = nil
	pack, _ := Build(src, 1, 0, time.Now())
	_ = store.SaveDraft(pack)
	if _, err := store.Freeze("run-1", 1, "ops-lead", "", false, ""); err == nil {
		t.Error("freeze with unacknowledged gaps must fail")
	}
	frozen, err := store.Freeze("run-1", 1, "ops-lead", "", true, "tracking gap accepted")
	if err != nil {
		t.Fatalf("freeze with acknowledged gaps: %v", err)
	}
	if !frozen.GapsAcked || frozen.GapNote != "tracking gap accepted" {
		t.Errorf("gap acknowledgement not recorded: %+v", frozen)
	}
}

func TestStoreUnfreezeRestoresDraft(t *testing.T) {
	store := NewStore()
	pack, _ := Build(fullSource(), 1, 0, time.Now())
	_ = store.SaveDraft(pack)
	if _, err := store.Freeze("run-1", 1, "ops-lead", "", false, ""); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if err := store.Unfreeze("run-1", 1); err != nil {
		t.Fatalf("unfreeze: %v", err)
	}
	got, _ := store.Get("run-1", 1)
	if got.Status != model.PackDraft || got.Reviewer != "" || got.ContentHash != "" {
		t.Errorf("unfreeze should restore a clean draft, got %+v", got)
	}
}
