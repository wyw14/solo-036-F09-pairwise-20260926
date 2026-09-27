package evidence

import (
	"testing"
	"time"

	"github.com/wyw14/solo-036/internal/model"
)

func fixedTime() time.Time { return time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC) }

func happySnapshot(runID string) model.ControlSnapshot {
	return model.ControlSnapshot{
		Stations: []model.Station{
			{ID: "station-a", Name: "North", Role: model.StationLauncher, Kilometer: 0, PressureLimit: 12.5, Enabled: true},
			{ID: "station-b", Name: "Central", Role: model.StationIntermediate, Kilometer: 86.4, PressureLimit: 11.8, Enabled: true},
			{ID: "station-c", Name: "South", Role: model.StationReceiver, Kilometer: 173.2, PressureLimit: 11.2, Enabled: true},
		},
		Routes: []model.RouteState{
			{ID: "main-a-c", FromStation: "station-a", ToStation: "station-c", Reserved: false},
		},
		Pressures: []model.PressureState{
			{StationID: "station-a", RunID: runID, Value: 10.5, Target: 10.5, Mode: model.CompressorHolding, ObservedAt: fixedTime()},
			{StationID: "station-c", RunID: runID, Value: 0, Target: 0, Mode: model.CompressorStopped, ObservedAt: fixedTime()},
		},
		Valves: []model.ValveState{
			{ID: "station-a/main", StationID: "station-a", RunID: runID, Position: model.ValveOpen, CommandEpoch: 2, ObservedAt: fixedTime()},
			{ID: "station-c/main", StationID: "station-c", RunID: runID, Position: model.ValveClosed, CommandEpoch: 5, ObservedAt: fixedTime()},
		},
		Closures: []model.ClosureState{
			{StationID: "station-a", RunID: runID, Locked: true, CommandEpoch: 2, ObservedAt: fixedTime()},
		},
		Tracking: []model.TrackingState{
			{RunID: runID, StationID: "station-b", SourceID: "mag-1", Generation: 1, Position: 86.4, Online: true, ObservedAt: fixedTime()},
			{RunID: runID, StationID: "station-b", SourceID: "ac-1", Generation: 1, Position: 86.4, Online: true, ObservedAt: fixedTime()},
		},
		Permits: []model.PermitState{
			{RunID: runID, Name: "repressurization", Generation: 1, Granted: true, Reason: "continuous durable passage evidence", UpdatedAt: fixedTime()},
		},
		Incidents: []model.Incident{},
	}
}

func happyEvents(runID string) []model.Event {
	return []model.Event{
		{ID: runID + "/loaded", RunID: runID, Kind: "state", Epoch: 1, Payload: "loaded", At: fixedTime()},
		{ID: runID + "/launch", RunID: runID, Kind: "launch", Epoch: 2, Payload: "main-a-c", At: fixedTime()},
		{ID: runID + "/recv", RunID: runID, Kind: "receiving", Epoch: 5, Payload: "station-c", At: fixedTime()},
		{ID: runID + "/complete", RunID: runID, Kind: "complete", Payload: "main-a-c", At: fixedTime()},
	}
}

func happyInputs(runID string) Inputs {
	return Inputs{
		PackageID:       "pkg-1",
		Version:         1,
		Run:             model.Run{ID: runID, Station: "station-a", State: model.StateComplete, Epoch: 6, CreatedAt: fixedTime(), UpdatedAt: fixedTime()},
		Snapshot:        happySnapshot(runID),
		RunEvents:       happyEvents(runID),
		Tail:            JournalTail{Source: "memory", LastChecksum: "abc", EventCount: 4, RunEvents: 4},
		ControlRevision: "ctrl-1",
		Now:             fixedTime(),
	}
}

func itemByCategory(pkg *Package, category string) Item {
	for _, item := range pkg.Items {
		if item.Category == category {
			return item
		}
	}
	return Item{}
}

func TestBuildAllCategoriesAdopted(t *testing.T) {
	pkg := Build(happyInputs("run-1"))
	if len(pkg.Items) != len(CategoryOrder) {
		t.Fatalf("got %d items, want %d", len(pkg.Items), len(CategoryOrder))
	}
	for _, item := range pkg.Items {
		if item.Status != ItemAdopted {
			t.Errorf("category %s = %s, want adopted; gaps=%v", item.Category, item.Status, item.Gaps)
		}
	}
	if pkg.Summary.Adopted != len(CategoryOrder) || pkg.Summary.Missing != 0 || pkg.Summary.Incomplete != 0 {
		t.Errorf("unexpected summary %+v", pkg.Summary)
	}
	if err := pkg.Validate(); err != nil {
		t.Fatalf("draft package validate: %v", err)
	}
}

func TestRunIncompleteWithoutCompleteEvent(t *testing.T) {
	in := happyInputs("run-1")
	in.RunEvents = in.RunEvents[:3] // 去掉 complete 事件
	pkg := Build(in)
	if got := itemByCategory(pkg, "run"); got.Status != ItemIncomplete {
		t.Fatalf("run status = %s, want incomplete", got.Status)
	}
}

func TestRunIncompleteWhenNotCompleteState(t *testing.T) {
	in := happyInputs("run-1")
	in.Run.State = model.StateTracking
	pkg := Build(in)
	if got := itemByCategory(pkg, "run"); got.Status != ItemIncomplete {
		t.Fatalf("run status = %s, want incomplete", got.Status)
	}
}

func TestRouteIncompleteOnPayloadMismatch(t *testing.T) {
	in := happyInputs("run-1")
	in.RunEvents[3].Payload = "main-a-other"
	pkg := Build(in)
	if got := itemByCategory(pkg, "route"); got.Status != ItemIncomplete || len(got.Gaps) == 0 {
		t.Fatalf("route = %+v, want incomplete with gap", got)
	}
}

func TestRouteMissingWithoutEvents(t *testing.T) {
	in := happyInputs("run-1")
	in.RunEvents[1].Payload = "" // launch
	in.RunEvents[3].Payload = "" // complete
	pkg := Build(in)
	if got := itemByCategory(pkg, "route"); got.Status != ItemMissing {
		t.Fatalf("route status = %s, want missing", got.Status)
	}
}

func TestPressureMissingAndIncomplete(t *testing.T) {
	in := happyInputs("run-1")
	in.Snapshot.Pressures = nil
	pkg := Build(in)
	if got := itemByCategory(pkg, "pressure"); got.Status != ItemMissing {
		t.Fatalf("pressure status = %s, want missing", got.Status)
	}

	in = happyInputs("run-1")
	in.Snapshot.Pressures = in.Snapshot.Pressures[:1] // 只剩 launcher，缺 receiver
	pkg = Build(in)
	if got := itemByCategory(pkg, "pressure"); got.Status != ItemIncomplete {
		t.Fatalf("pressure status = %s, want incomplete", got.Status)
	}
}

func TestValvesMissingAndEpochMismatch(t *testing.T) {
	in := happyInputs("run-1")
	in.Snapshot.Valves = nil
	pkg := Build(in)
	if got := itemByCategory(pkg, "valves"); got.Status != ItemMissing {
		t.Fatalf("valves status = %s, want missing", got.Status)
	}

	in = happyInputs("run-1")
	in.Snapshot.Valves[1].CommandEpoch = 99 // receiver 阀与 receiving epoch=5 不符
	pkg = Build(in)
	if got := itemByCategory(pkg, "valves"); got.Status != ItemIncomplete {
		t.Fatalf("valves status = %s, want incomplete", got.Status)
	}
}

func TestClosuresMissingAndUnlocked(t *testing.T) {
	in := happyInputs("run-1")
	in.Snapshot.Closures = nil
	pkg := Build(in)
	if got := itemByCategory(pkg, "closures"); got.Status != ItemMissing {
		t.Fatalf("closures status = %s, want missing", got.Status)
	}

	in = happyInputs("run-1")
	in.Snapshot.Closures[0].Locked = false
	pkg = Build(in)
	if got := itemByCategory(pkg, "closures"); got.Status != ItemIncomplete {
		t.Fatalf("closures status = %s, want incomplete", got.Status)
	}
}

func TestTrackingIncompleteSingleSourceAndMissing(t *testing.T) {
	in := happyInputs("run-1")
	in.Snapshot.Tracking = in.Snapshot.Tracking[:1] // 单一物理源
	pkg := Build(in)
	if got := itemByCategory(pkg, "tracking"); got.Status != ItemIncomplete {
		t.Fatalf("tracking status = %s, want incomplete", got.Status)
	}

	in = happyInputs("run-1")
	in.Snapshot.Tracking = nil
	pkg = Build(in)
	if got := itemByCategory(pkg, "tracking"); got.Status != ItemMissing {
		t.Fatalf("tracking status = %s, want missing", got.Status)
	}
}

func TestTrackingMarksSupersededGenerations(t *testing.T) {
	in := happyInputs("run-1")
	in.Snapshot.Tracking = append(in.Snapshot.Tracking,
		model.TrackingState{RunID: "run-1", StationID: "station-b", SourceID: "old-1", Generation: 1, Position: 10, Online: true, ObservedAt: fixedTime().Add(-time.Hour)},
		model.TrackingState{RunID: "run-1", StationID: "station-b", SourceID: "mag-2", Generation: 2, Position: 20, Online: true, ObservedAt: fixedTime()},
		model.TrackingState{RunID: "run-1", StationID: "station-b", SourceID: "ac-2", Generation: 2, Position: 21, Online: true, ObservedAt: fixedTime()},
	)
	pkg := Build(in)
	records := itemByCategory(pkg, "tracking").Records.([]TrackingRecord)
	superseded, current := 0, 0
	for _, record := range records {
		if record.Superseded {
			superseded++
		} else {
			current++
		}
	}
	// gen1 的三条（含原始两条）为旧代，gen2 两条为当前代
	if superseded != 3 || current != 2 {
		t.Fatalf("superseded=%d current=%d, want 3/2", superseded, current)
	}
	if got := itemByCategory(pkg, "tracking"); got.Status != ItemAdopted {
		t.Fatalf("tracking status = %s, want adopted", got.Status)
	}
}

func TestPermitsIncompleteWhenAbsentOrDenied(t *testing.T) {
	in := happyInputs("run-1")
	in.Snapshot.Permits = nil
	pkg := Build(in)
	if got := itemByCategory(pkg, "permits"); got.Status != ItemIncomplete {
		t.Fatalf("permits status = %s, want incomplete", got.Status)
	}

	in = happyInputs("run-1")
	in.Snapshot.Permits[0].Granted = false
	in.Snapshot.Permits[0].Reason = "evidence gap"
	pkg = Build(in)
	if got := itemByCategory(pkg, "permits"); got.Status != ItemIncomplete {
		t.Fatalf("permits status = %s, want incomplete", got.Status)
	}
}

func TestIncidentsIncompleteOnCritical(t *testing.T) {
	in := happyInputs("run-1")
	in.Snapshot.Incidents = []model.Incident{
		{ID: "i-1", RunID: "run-1", Severity: "high", Message: "overpressure", At: fixedTime()},
	}
	pkg := Build(in)
	if got := itemByCategory(pkg, "incidents"); got.Status != ItemIncomplete {
		t.Fatalf("incidents status = %s, want incomplete", got.Status)
	}
}

func TestFingerprintStableAndSensitiveAndFreezeAgnostic(t *testing.T) {
	first := Build(happyInputs("run-1"))
	second := Build(happyInputs("run-1"))
	if first.Fingerprint != second.Fingerprint || first.Fingerprint == "" {
		t.Fatal("identical inputs must yield the same non-empty fingerprint")
	}

	mutated := happyInputs("run-1")
	mutated.Snapshot.Pressures[0].Value = 11.0
	if Build(mutated).Fingerprint == first.Fingerprint {
		t.Fatal("changing a record must change the fingerprint")
	}

	// 写入冻结元数据不应改变指纹。
	frozenAt := fixedTime().Add(time.Minute)
	first.Status = StatusFrozen
	first.FrozenAt = &frozenAt
	first.ReviewerID = "rev-1"
	first.Note = "confirmed"
	if ComputeFingerprint(first) != first.Fingerprint {
		t.Fatal("freeze metadata must not change the fingerprint")
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("frozen package validate: %v", err)
	}
}

func TestRevisionMarksChangedItems(t *testing.T) {
	parent := Build(happyInputs("run-1"))
	parent.Status = StatusFrozen
	frozenAt := fixedTime()
	parent.FrozenAt = &frozenAt
	parent.ReviewerID = "rev-1"
	if err := parent.Validate(); err != nil {
		t.Fatalf("parent validate: %v", err)
	}

	in := happyInputs("run-1")
	in.PackageID = "pkg-2"
	in.Version = 2
	in.Parent = parent
	in.RevisionReason = "补充事故记录"
	in.Snapshot.Incidents = []model.Incident{
		{ID: "i-9", RunID: "run-1", Severity: "low", Message: "minor", At: fixedTime()},
	}
	child := Build(in)
	if err := child.Validate(); err != nil {
		t.Fatalf("revision validate: %v", err)
	}
	if child.RevisionNo != 2 || child.Supersedes != parent.PackageID || child.RevisionOf != parent.PackageID {
		t.Fatalf("revision linkage wrong: %+v", child)
	}
	if child.Fingerprint == parent.Fingerprint {
		t.Fatal("revision content differs, fingerprints must differ")
	}
	incidents := itemByCategory(child, "incidents")
	if !incidents.Changed {
		t.Fatal("incidents item must be marked changed")
	}
	if run := itemByCategory(child, "run"); run.Changed {
		t.Fatal("unchanged run item must not be marked changed")
	}
}
