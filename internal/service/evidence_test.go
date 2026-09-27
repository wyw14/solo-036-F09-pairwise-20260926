package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wyw14/solo-036/internal/evidence"
	"github.com/wyw14/solo-036/internal/tracker"
)

const (
	testRouteID  = "main-station-a-station-c"
	testLauncher = "station-a"
	testMid      = "station-b"
	testReceiver = "station-c"
)

// completeRunThroughFlow 在给定 runtime 上驱动真实作业流程直到 complete。
func completeRunThroughFlowOn(t *testing.T, r *Runtime) string {
	t.Helper()
	op, err := r.Launch(context.Background(), LaunchRequest{
		StationID:      testLauncher,
		RouteID:        testRouteID,
		TargetPressure: 10.5,
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	runID := op.Run.ID
	now := time.Now().UTC()
	for _, source := range []string{"mag-1", "ac-1"} {
		if _, err := r.Track(runID, TrackingRequest{
			StationID:  testMid,
			SourceID:   source,
			Kind:       tracker.SourceMagnetic,
			Position:   86.4,
			ObservedAt: now,
		}); err != nil {
			t.Fatalf("track %s: %v", source, err)
		}
	}
	if _, err := r.GrantPassage(runID, testMid); err != nil {
		t.Fatalf("grant passage: %v", err)
	}
	if _, err := r.Receive(runID, ReceiveRequest{StationID: testReceiver, IsolationEpoch: 5}); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if _, err := r.Complete(runID, testRouteID); err != nil {
		t.Fatalf("complete: %v", err)
	}
	return runID
}

// completeRunThroughFlow 驱动真实作业流程直到 complete，并返回 runtime 与 runID。
func completeRunThroughFlow(t *testing.T) (*Runtime, string) {
	t.Helper()
	r := NewRuntime()
	r.Bootstrap()
	return r, completeRunThroughFlowOn(t, r)
}

func prepareAndFreeze(t *testing.T, r *Runtime, runID, reviewer string) *evidence.Package {
	t.Helper()
	draft, err := r.PrepareEvidencePackage(runID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if draft.Status != evidence.StatusDraft || draft.Fingerprint == "" {
		t.Fatalf("bad draft: %+v", draft)
	}
	frozen, err := r.ConfirmEvidencePackage(runID, draft.PackageID, reviewer, draft.Fingerprint, "")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	return frozen
}

func TestEvidenceRejectsNonCompleteRun(t *testing.T) {
	r := NewRuntime()
	if _, err := r.CreateOperation(testLauncher); err != nil {
		t.Fatal(err)
	}
	ops := r.Operations()
	if len(ops) != 1 {
		t.Fatalf("operations = %d, want 1", len(ops))
	}
	if _, err := r.PrepareEvidencePackage("missing-run"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("got %v, want ErrRunNotFound", err)
	}
	if _, err := r.PrepareEvidencePackage(ops[0].ID); !errors.Is(err, ErrRunNotComplete) {
		t.Fatalf("got %v, want ErrRunNotComplete", err)
	}
}

func TestEvidencePrepareConfirmFreezeFlow(t *testing.T) {
	r, runID := completeRunThroughFlow(t)

	draft, err := r.PrepareEvidencePackage(runID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if draft.Version != 1 || draft.RevisionNo != 1 {
		t.Fatalf("version = %d/%d, want 1/1", draft.Version, draft.RevisionNo)
	}
	if draft.Summary.Adopted != len(evidence.CategoryOrder) {
		t.Fatalf("happy flow should adopt all categories, summary=%+v items=%v", draft.Summary, draft.Items)
	}

	// 缺复核人 → 400 类错误。
	if _, err := r.ConfirmEvidencePackage(runID, draft.PackageID, "  ", draft.Fingerprint, ""); !errors.Is(err, ErrReviewerRequired) {
		t.Fatalf("got %v, want ErrReviewerRequired", err)
	}
	// 指纹不符 → 409。
	if _, err := r.ConfirmEvidencePackage(runID, draft.PackageID, "rev-1", "deadbeef", ""); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("got %v, want ErrFingerprintMismatch", err)
	}

	frozen, err := r.ConfirmEvidencePackage(runID, draft.PackageID, "rev-1", draft.Fingerprint, "")
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if frozen.Status != evidence.StatusFrozen || frozen.FrozenAt == nil || frozen.ReviewerID != "rev-1" {
		t.Fatalf("frozen package wrong: %+v", frozen)
	}

	// 同一草稿成功确认后已消费，重复 confirm → 404。
	if _, err := r.ConfirmEvidencePackage(runID, draft.PackageID, "rev-1", draft.Fingerprint, ""); !errors.Is(err, ErrDraftNotFound) {
		t.Fatalf("got %v, want ErrDraftNotFound", err)
	}

	packages := r.EvidencePackages(runID)
	if len(packages) != 1 || packages[0].Fingerprint != frozen.Fingerprint {
		t.Fatalf("package list wrong: %+v", packages)
	}
	if _, fingerprint, valid, err := r.VerifyEvidencePackage(runID, 1); err != nil || !valid || fingerprint != frozen.Fingerprint {
		t.Fatalf("verify: valid=%v err=%v", valid, err)
	}

	kinds := map[string]bool{}
	for _, event := range r.Journal.Events() {
		if event.RunID == runID {
			kinds[event.Kind] = true
		}
	}
	if !kinds["evidence_prepared"] || !kinds["evidence_frozen"] {
		t.Fatalf("expected evidence events in journal, got %v", kinds)
	}
}

func TestEvidenceRevisionChain(t *testing.T) {
	r, runID := completeRunThroughFlow(t)
	v1 := prepareAndFreeze(t, r, runID, "rev-1")
	v1Fingerprint := v1.Fingerprint

	// 无补录原因 → 400。
	if _, err := r.PrepareEvidenceRevision(runID, 1, "  "); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("got %v, want ErrReasonRequired", err)
	}

	// 冻结 v2 后再回头改 v1 → 必须基于最新版，拒绝分叉。
	v2Draft, err := r.PrepareEvidenceRevision(runID, 1, "late incident note")
	if err != nil {
		t.Fatalf("prepare revision v2: %v", err)
	}
	if _, err := r.ConfirmEvidencePackage(runID, v2Draft.PackageID, "rev-1", v2Draft.Fingerprint, ""); err != nil {
		t.Fatalf("confirm v2: %v", err)
	}
	if _, err := r.PrepareEvidenceRevision(runID, 1, "fork"); !errors.Is(err, ErrRevisionTip) {
		t.Fatalf("got %v, want ErrRevisionTip", err)
	}

	// 父版本内容指纹保持不变，新版本以父链指向它。
	gotV1, err := r.EvidencePackage(runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if gotV1.Fingerprint != v1Fingerprint {
		t.Fatal("frozen v1 content must not change after a revision")
	}
	gotV2, err := r.EvidencePackage(runID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if gotV2.Supersedes != v1.PackageID || gotV2.RevisionOf != v1.PackageID || gotV2.RevisionReason != "late incident note" {
		t.Fatalf("v2 linkage wrong: %+v", gotV2)
	}
}

func TestEvidenceRevisionCapturesLaterIncident(t *testing.T) {
	r, runID := completeRunThroughFlow(t)
	v1 := prepareAndFreeze(t, r, runID, "rev-1")

	// 完工后补录一条（低级）事故：v2 应在 incidents 项标 changed。
	r.recordIncident(runID, "low", "late logged remark")
	v2Draft, err := r.PrepareEvidenceRevision(runID, 1, "append late incident")
	if err != nil {
		t.Fatal(err)
	}
	var incidentsChanged bool
	for _, item := range v2Draft.Items {
		if item.Category == "incidents" && item.Changed {
			incidentsChanged = true
		}
	}
	if !incidentsChanged {
		t.Fatal("revision must mark the incidents item changed")
	}
	v2, err := r.ConfirmEvidencePackage(runID, v2Draft.PackageID, "rev-1", v2Draft.Fingerprint, "")
	if err != nil {
		t.Fatal(err)
	}
	if v2.Fingerprint == v1.Fingerprint {
		t.Fatal("revision with new content must have a different fingerprint")
	}
}

func TestEvidencePersistenceSurvivesRestart(t *testing.T) {
	dir := t.TempDir()

	first := NewRuntime()
	if err := first.ConfigurePersistence(dir); err != nil {
		t.Fatal(err)
	}
	first.Bootstrap()
	runID := completeRunThroughFlowOn(t, first)
	v1 := prepareAndFreeze(t, first, runID, "rev-1")
	if err := first.PersistSnapshot(); err != nil {
		t.Fatalf("persist snapshot: %v", err)
	}

	// 模拟重启：新 runtime 配置同一目录（证据包自动加载），再恢复控制快照。
	second := NewRuntime()
	if err := second.ConfigurePersistence(dir); err != nil {
		t.Fatalf("restart configure: %v", err)
	}
	second.Bootstrap()
	if _, err := second.RecoverPersistent(); err != nil {
		t.Fatalf("recover: %v", err)
	}

	got, err := second.EvidencePackage(runID, 1)
	if err != nil {
		t.Fatalf("read frozen package after restart: %v", err)
	}
	if got.Fingerprint != v1.Fingerprint || got.ReviewerID != "rev-1" {
		t.Fatal("restored frozen package content mismatch")
	}
	if _, fingerprint, valid, err := second.VerifyEvidencePackage(runID, 1); err != nil || !valid || fingerprint != v1.Fingerprint {
		t.Fatalf("verify after restart: valid=%v err=%v", valid, err)
	}

	// 恢复后可在最新冻结版上继续补录出 v2。
	v2Draft, err := second.PrepareEvidenceRevision(runID, 1, "post-restart supplement")
	if err != nil {
		t.Fatalf("revision after restart: %v", err)
	}
	if v2Draft.Version != 2 || v2Draft.Supersedes != v1.PackageID {
		t.Fatalf("v2 linkage wrong: %+v", v2Draft)
	}
	if _, err := second.ConfirmEvidencePackage(runID, v2Draft.PackageID, "rev-2", v2Draft.Fingerprint, ""); err != nil {
		t.Fatalf("confirm v2 after restart: %v", err)
	}
}
