package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wyw14/solo-036/internal/evidence"
	"github.com/wyw14/solo-036/internal/journal"
	"github.com/wyw14/solo-036/internal/model"
	"github.com/wyw14/solo-036/internal/tracker"
)

// 证据包流程的哨兵错误，由 API 层映射为相应 HTTP 状态码。
var (
	ErrRunNotFound         = errors.New("run not found")
	ErrRunNotComplete      = errors.New("run is not in complete state")
	ErrDraftNotFound       = errors.New("evidence draft not found or already confirmed")
	ErrFingerprintMismatch = errors.New("evidence fingerprint does not match the prepared package")
	ErrVersionConflict     = errors.New("evidence version is already frozen")
	ErrPackageNotFound     = errors.New("frozen evidence package not found")
	ErrRevisionTip         = errors.New("a revision must build on the latest frozen version")
	ErrReasonRequired      = errors.New("revision_reason is required")
	ErrReviewerRequired    = errors.New("reviewer_id is required")
)

// PrepareEvidencePackage 为一个 complete 作业汇集证据草稿（不冻结、不落盘）。
// 可重复调用，每次生成新的 package_id 与当前时刻的快照视图。
func (r *Runtime) PrepareEvidencePackage(runID string) (*evidence.Package, error) {
	runRecord, err := r.completedRun(runID)
	if err != nil {
		return nil, err
	}
	version := r.Evidence.NextVersion(runID)
	pkg := r.buildEvidencePackage(runRecord, version, nil, "")
	r.Evidence.SaveDraft(pkg)
	if err := r.appendEvidenceEvent("evidence_prepared", pkg); err != nil {
		return nil, err
	}
	return pkg, nil
}

// ConfirmEvidencePackage 由复核人凭 package_id + 内容指纹二次确认冻结。
// 指纹不符返回 ErrFingerprintMismatch；冻结成功后包不可变并落盘。
func (r *Runtime) ConfirmEvidencePackage(runID, packageID, reviewerID, fingerprint, note string) (*evidence.Package, error) {
	if strings.TrimSpace(reviewerID) == "" {
		return nil, ErrReviewerRequired
	}
	draft, ok := r.Evidence.Draft(packageID)
	if !ok || draft.RunID != runID {
		return nil, ErrDraftNotFound
	}
	if strings.TrimSpace(fingerprint) != draft.Fingerprint {
		return nil, ErrFingerprintMismatch
	}
	frozenAt := time.Now().UTC()
	draft.Status = evidence.StatusFrozen
	draft.FrozenAt = &frozenAt
	draft.ReviewerID = strings.TrimSpace(reviewerID)
	draft.Note = strings.TrimSpace(note)

	frozen, err := r.Evidence.Freeze(draft)
	if err != nil {
		if errors.Is(err, evidence.ErrVersionFrozen) {
			return nil, ErrVersionConflict
		}
		return nil, err
	}
	// 先落盘后记事件：即使事件追加失败，冻结文件也已可追溯。
	if appendErr := r.appendEvidenceEvent("evidence_frozen", frozen); appendErr != nil {
		return frozen, fmt.Errorf("evidence frozen but journal append failed: %w", appendErr)
	}
	return frozen, nil
}

// EvidencePackages 返回一个作业的全部冻结版本（升序）。
func (r *Runtime) EvidencePackages(runID string) []*evidence.Package {
	return r.Evidence.Frozen(runID)
}

// EvidencePackage 返回指定冻结版本。
func (r *Runtime) EvidencePackage(runID string, version int) (*evidence.Package, error) {
	pkg, ok := r.Evidence.FrozenVersion(runID, version)
	if !ok {
		return nil, ErrPackageNotFound
	}
	return pkg, nil
}

// PrepareEvidenceRevision 基于指定冻结版（必须是最新版）生成补录修订草稿。
func (r *Runtime) PrepareEvidenceRevision(runID string, version int, reason string) (*evidence.Package, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, ErrReasonRequired
	}
	parent, ok := r.Evidence.FrozenVersion(runID, version)
	if !ok {
		return nil, ErrPackageNotFound
	}
	tip, hasTip := r.Evidence.LatestFrozen(runID)
	if !hasTip || tip.Version != version {
		return nil, ErrRevisionTip
	}
	runRecord, err := r.completedRun(runID)
	if err != nil {
		// 补录发生在作业结束之后，允许作业已不在内存：以父包记录的终态重建 run 视图。
		parentRun, ok := parentRunRecord(parent)
		if !ok {
			return nil, fmt.Errorf("rebuild run for revision: %w", err)
		}
		runRecord = model.Run{
			ID:        runID,
			Station:   parentRun.Station,
			State:     model.StateComplete,
			Epoch:     parentRun.Epoch,
			CreatedAt: parentRun.CreatedAt,
			UpdatedAt: time.Now().UTC(),
		}
	}
	nextVersion := r.Evidence.NextVersion(runID)
	pkg := r.buildEvidencePackage(runRecord, nextVersion, parent, reason)
	r.Evidence.SaveDraft(pkg)
	if err := r.appendEvidenceEvent("evidence_revised", pkg); err != nil {
		return nil, err
	}
	return pkg, nil
}

// VerifyEvidencePackage 重算指定冻结版指纹并返回包与是否一致。
func (r *Runtime) VerifyEvidencePackage(runID string, version int) (*evidence.Package, string, bool, error) {
	pkg, err := r.EvidencePackage(runID, version)
	if err != nil {
		return nil, "", false, err
	}
	fingerprint := evidence.ComputeFingerprint(pkg)
	return pkg, fingerprint, fingerprint == pkg.Fingerprint, nil
}

func (r *Runtime) completedRun(runID string) (model.Run, error) {
	runRecord, ok := r.Runs.Get(runID)
	if !ok {
		return model.Run{}, ErrRunNotFound
	}
	if runRecord.State != model.StateComplete {
		return model.Run{}, ErrRunNotComplete
	}
	return runRecord, nil
}

// buildEvidencePackage 在各子系统锁外采集输入后调用纯函数组装证据包。
// Runtime.mu 与 evidence.Store 锁在此期间均不被持有。
func (r *Runtime) buildEvidencePackage(runRecord model.Run, version int, parent *evidence.Package, reason string) *evidence.Package {
	snapshot := r.Snapshot()
	runEvents, tail := r.runJournal(runRecord.ID)
	lineage := r.collectLineage(runRecord.ID, snapshot)
	return evidence.Build(evidence.Inputs{
		PackageID:       model.NewRevision(),
		Version:         version,
		Run:             runRecord,
		Snapshot:        snapshot,
		RunEvents:       runEvents,
		Tail:            tail,
		ControlRevision: snapshot.Revision,
		Lineage:         lineage,
		Parent:          parent,
		RevisionReason:  reason,
		Now:             time.Now().UTC(),
	})
}

// runJournal 优先采用已验链的持久事件源（重启后内存 journal 为空）；
// 未配置持久化时回退到内存 journal。
func (r *Runtime) runJournal(runID string) ([]model.Event, evidence.JournalTail) {
	r.mu.Lock()
	persistent := r.control.Persistent
	r.mu.Unlock()

	if persistent != nil {
		all, err := persistent.Events()
		if err == nil {
			sequence, checksum := persistent.Tail()
			return filterRunEvents(all, runID), evidence.JournalTail{
				Source:       "file",
				LastSequence: sequence,
				LastChecksum: checksum,
				EventCount:   len(all),
				RunEvents:    countRunEvents(all, runID),
			}
		}
		// 持久事件读取失败时退化为内存日志，并在 source 上保留提示。
	}
	all := r.Journal.Events()
	return filterRunEvents(all, runID), evidence.JournalTail{
		Source:       "memory",
		LastChecksum: journal.Checksum(r.Journal),
		EventCount:   len(all),
		RunEvents:    countRunEvents(all, runID),
	}
}

func filterRunEvents(events []model.Event, runID string) []model.Event {
	out := make([]model.Event, 0)
	for _, event := range events {
		if event.RunID == runID {
			out = append(out, event)
		}
	}
	return out
}

func countRunEvents(events []model.Event, runID string) int {
	count := 0
	for _, event := range events {
		if event.RunID == runID {
			count++
		}
	}
	return count
}

// collectLineage 按快照中本作业的 (站点,代) 组合采集观测血缘，
// 仅用于丰富 tracking 记录的 source kind；重启后为空不影响主判定。
func (r *Runtime) collectLineage(runID string, snapshot model.ControlSnapshot) []tracker.SourceObservation {
	type stationGeneration struct {
		stationID  string
		generation int64
	}
	keys := make(map[stationGeneration]bool)
	for _, tracking := range snapshot.Tracking {
		if tracking.RunID == runID {
			keys[stationGeneration{tracking.StationID, tracking.Generation}] = true
		}
	}
	out := make([]tracker.SourceObservation, 0)
	for key := range keys {
		out = append(out, r.Lineage.Observations(runID, key.stationID, key.generation)...)
	}
	return out
}

func (r *Runtime) appendEvidenceEvent(kind string, pkg *evidence.Package) error {
	payload := pkg.PackageID
	switch kind {
	case "evidence_revised":
		payload = pkg.PackageID + ":" + pkg.RevisionOf
	case "evidence_frozen":
		// 复核人身份与内容指纹一并锚定到哈希链，事后可核对"谁确认了哪个版本"。
		payload = fmt.Sprintf("%s:%s:%s", pkg.PackageID, pkg.Fingerprint, pkg.ReviewerID)
	}
	return r.appendEvent(model.Event{
		RunID:   pkg.RunID,
		Kind:    kind,
		Epoch:   int64(pkg.Version),
		Payload: payload,
	})
}

func parentRunRecord(parent *evidence.Package) (evidence.RunRecord, bool) {
	for _, item := range parent.Items {
		if item.Category != "run" {
			continue
		}
		if record, ok := item.Records.(evidence.RunRecord); ok {
			return record, true
		}
	}
	return evidence.RunRecord{}, false
}
