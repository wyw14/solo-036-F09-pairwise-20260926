package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ItemStatus 是单个证据类别的采信状态。
type ItemStatus string

const (
	ItemAdopted    ItemStatus = "adopted"    // 已采用：记录完整且交叉一致
	ItemIncomplete ItemStatus = "incomplete" // 不完整：有记录但存在缺口，见 Gaps
	ItemMissing    ItemStatus = "missing"    // 缺失：没有找到该类别的任何记录
)

// PackageStatus 是证据包生命周期状态。
type PackageStatus string

const (
	StatusDraft  PackageStatus = "draft"  // 草稿：仅存内存，可重复生成
	StatusFrozen PackageStatus = "frozen" // 已冻结：经复核确认，落盘且不可变
)

// CategoryOrder 固定证据类别输出顺序，保证指纹稳定。
var CategoryOrder = []string{
	"run", "route", "pressure", "valves", "closures", "tracking", "permits", "incidents",
}

// SourceRef 指向一条证据的来源记录。
type SourceRef struct {
	Kind    string `json:"kind"`               // journal_event | control_snapshot | lineage
	Detail  string `json:"detail"`             // 人类可读的来源定位
	EventID string `json:"event_id,omitempty"` // 当 Kind=journal_event 时的事件 ID
}

// Item 是一个证据类别（运行/路线/压力/…）的采集结果。
type Item struct {
	Category string      `json:"category"`
	Status   ItemStatus  `json:"status"`
	Records  any         `json:"records"`             // 各类别具名结构（单值）或其切片；缺失为 nil
	Sources  []SourceRef `json:"sources"`
	Gaps     []string    `json:"gaps,omitempty"`
	Changed  bool        `json:"changed,omitempty"` // 仅修订包：相对父包同类项内容发生变化
}

// JournalTail 是打包时刻作业日志链尾，用于事后定位证据所依据的日志位置。
type JournalTail struct {
	Source       string `json:"source"` // file（哈希链）| memory（仅内存日志）
	LastSequence int64  `json:"last_sequence"`
	LastChecksum string `json:"last_checksum"`
	EventCount   int    `json:"event_count"` // 事件源内全部事件数
	RunEvents    int    `json:"run_events"`  // 属于本作业的事件数
}

// Summary 汇总各类别采信状态。
type Summary struct {
	Adopted    int `json:"adopted"`
	Missing    int `json:"missing"`
	Incomplete int `json:"incomplete"`
}

// Package 是一次作业取证生成的、可冻结的证据包（含修订版本）。
type Package struct {
	PackageID       string        `json:"package_id"`
	RunID           string        `json:"run_id"`
	Version         int           `json:"version"` // 与 RevisionNo 相同
	RevisionNo      int           `json:"revision_no"`
	RevisionOf      string        `json:"revision_of,omitempty"`
	RevisionReason  string        `json:"revision_reason,omitempty"`
	Supersedes      string        `json:"supersedes,omitempty"`
	Status          PackageStatus `json:"status"`
	PreparedAt      time.Time     `json:"prepared_at"`
	FrozenAt        *time.Time    `json:"frozen_at,omitempty"`
	ReviewerID      string        `json:"reviewer_id,omitempty"`
	Note            string        `json:"note,omitempty"`
	Fingerprint     string        `json:"fingerprint"`
	JournalTail     JournalTail   `json:"journal_tail"`
	ControlRevision string        `json:"control_revision"`
	Items           []Item        `json:"items"`
	Summary         Summary       `json:"summary"`
}

// 各类别记录载荷（具名结构，字段声明序即 JSON 输出序，保证指纹确定）。

type RunRecord struct {
	RunID     string    `json:"run_id"`
	Station   string    `json:"station"`
	State     string    `json:"state"`
	Epoch     int64     `json:"epoch"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RouteRecord struct {
	RouteID     string `json:"route_id"`
	FromStation string `json:"from_station"`
	ToStation   string `json:"to_station"`
	LeaseEpoch  int64  `json:"lease_epoch"`
	Released    bool   `json:"released"` // 完工后租约已释放属预期
}

type PressureRecord struct {
	StationID  string    `json:"station_id"`
	Role       string    `json:"role"`
	Value      float64   `json:"value"`
	Target     float64   `json:"target"`
	Mode       string    `json:"mode"`
	ObservedAt time.Time `json:"observed_at"`
}

type ValveRecord struct {
	ValveID      string    `json:"valve_id"`
	StationID    string    `json:"station_id"`
	Position     string    `json:"position"`
	CommandEpoch int64     `json:"command_epoch"`
	ObservedAt   time.Time `json:"observed_at"`
}

type ClosureRecord struct {
	StationID    string    `json:"station_id"`
	Locked       bool      `json:"locked"`
	CommandEpoch int64     `json:"command_epoch"`
	ObservedAt   time.Time `json:"observed_at"`
}

type TrackingRecord struct {
	StationID  string    `json:"station_id"`
	SourceID   string    `json:"source_id"`
	Kind       string    `json:"kind,omitempty"`
	Generation int64     `json:"generation"`
	Position   float64   `json:"position"`
	Online     bool      `json:"online"`
	ObservedAt time.Time `json:"observed_at"`
	Superseded bool      `json:"superseded"` // generation 小于本作业最大代
}

type PermitRecord struct {
	Name       string    `json:"name"`
	Generation int64     `json:"generation"`
	Granted    bool      `json:"granted"`
	Reason     string    `json:"reason"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type IncidentRecord struct {
	ID       string    `json:"id"`
	Severity string    `json:"severity"`
	Message  string    `json:"message"`
	At       time.Time `json:"at"`
}

// canonicalPackage 只包含"被复核的内容"。冻结动作相关字段
// （status/frozen_at/reviewer_id/note/fingerprint）刻意排除，
// 因此 prepare 时算出的指纹在 confirm 写入这些字段后保持不变。
type canonicalPackage struct {
	PackageID       string          `json:"package_id"`
	RunID           string          `json:"run_id"`
	Version         int             `json:"version"`
	RevisionNo      int             `json:"revision_no"`
	RevisionOf      string          `json:"revision_of"`
	RevisionReason  string          `json:"revision_reason"`
	Supersedes      string          `json:"supersedes"`
	PreparedAt      time.Time       `json:"prepared_at"`
	JournalTail     JournalTail     `json:"journal_tail"`
	ControlRevision string          `json:"control_revision"`
	Items           []canonicalItem `json:"items"`
	Summary         Summary         `json:"summary"`
}

// canonicalItem 排除仅用于展示差异的 Changed 字段，使修订前后的
// 同源内容可直接比较。
type canonicalItem struct {
	Category string      `json:"category"`
	Status   ItemStatus  `json:"status"`
	Records  any         `json:"records"`
	Sources  []SourceRef `json:"sources"`
	Gaps     []string    `json:"gaps,omitempty"`
}

func canonicalizeItem(item Item) canonicalItem {
	return canonicalItem{
		Category: item.Category,
		Status:   item.Status,
		Records:  item.Records,
		Sources:  item.Sources,
		Gaps:     item.Gaps,
	}
}

// ComputeFingerprint 返回证据包被冻结内容的 SHA-256（十六进制）。
// 它是确定性的：结构字段序固定，多值记录由 builder 预先排序。
func ComputeFingerprint(p *Package) string {
	if p == nil {
		return ""
	}
	canonicalItems := make([]canonicalItem, 0, len(p.Items))
	for _, item := range p.Items {
		canonicalItems = append(canonicalItems, canonicalizeItem(item))
	}
	canonical := canonicalPackage{
		PackageID:       p.PackageID,
		RunID:           p.RunID,
		Version:         p.Version,
		RevisionNo:      p.RevisionNo,
		RevisionOf:      p.RevisionOf,
		RevisionReason:  p.RevisionReason,
		Supersedes:      p.Supersedes,
		PreparedAt:      p.PreparedAt,
		JournalTail:     p.JournalTail,
		ControlRevision: p.ControlRevision,
		Items:           canonicalItems,
		Summary:         p.Summary,
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		// 包内无 chan/func 等不可编码字段，正常不可达。
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
// itemFingerprint 计算单个条目（排除 Changed）的指纹，供修订 diff 使用。
func itemFingerprint(item Item) string {
	payload, err := json.Marshal(canonicalizeItem(item))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// UnmarshalJSON 按 category 把 records 还原为具体的记录结构。
// 若直接反序列化到 any，记录会退化为 map[string]any，JSON 输出按键名
// 排序，与结构声明序不同，导致落盘重载后指纹重算不一致。
func (i *Item) UnmarshalJSON(data []byte) error {
	type itemAlias Item
	head := itemAlias{}
	// 指针嵌入使 itemAlias 的字段位于深度 1，外层 Records 位于深度 0，
	// 避免同名 JSON 字段冲突，records 由外层 RawMessage 接管。
	auxiliary := struct {
		*itemAlias
		Records json.RawMessage `json:"records"`
	}{itemAlias: &head}
	if err := json.Unmarshal(data, &auxiliary); err != nil {
		return err
	}
	*i = Item(head)
	raw := auxiliary.Records
	if len(raw) == 0 || string(raw) == "null" {
		i.Records = nil
		return nil
	}
	target, err := decodeRecords(i.Category, raw)
	if err != nil {
		return fmt.Errorf("category %s records: %w", i.Category, err)
	}
	i.Records = target
	return nil
}

func decodeRecords(category string, raw json.RawMessage) (any, error) {
	var target any
	switch category {
	case "run":
		target = &RunRecord{}
	case "route":
		target = &RouteRecord{}
	case "pressure":
		target = &[]PressureRecord{}
	case "valves":
		target = &[]ValveRecord{}
	case "closures":
		target = &[]ClosureRecord{}
	case "tracking":
		target = &[]TrackingRecord{}
	case "permits":
		target = &[]PermitRecord{}
	case "incidents":
		target = &[]IncidentRecord{}
	default:
		return nil, fmt.Errorf("unknown category %q", category)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return nil, err
	}
	return dereferenceRecords(target), nil
}

func dereferenceRecords(target any) any {
	switch value := target.(type) {
	case *RunRecord:
		return *value
	case *RouteRecord:
		return *value
	case *[]PressureRecord:
		return *value
	case *[]ValveRecord:
		return *value
	case *[]ClosureRecord:
		return *value
	case *[]TrackingRecord:
		return *value
	case *[]PermitRecord:
		return *value
	case *[]IncidentRecord:
		return *value
	default:
		return target
	}
}

// Validate 校验证据包自身结构完整性，落盘与启动加载时调用。
func (p *Package) Validate() error {
	if p == nil {
		return errors.New("evidence package is nil")
	}
	if p.PackageID == "" {
		return errors.New("evidence package id is required")
	}
	if p.RunID == "" {
		return errors.New("evidence package run id is required")
	}
	if p.Version < 1 || p.RevisionNo < 1 || p.Version != p.RevisionNo {
		return fmt.Errorf("evidence package version is invalid: version=%d revision_no=%d", p.Version, p.RevisionNo)
	}
	switch p.Status {
	case StatusDraft, StatusFrozen:
	default:
		return fmt.Errorf("evidence package status %q is invalid", p.Status)
	}
	if p.PreparedAt.IsZero() {
		return errors.New("evidence package prepared_at is required")
	}
	if p.Fingerprint == "" {
		return errors.New("evidence package fingerprint is required")
	}
	if got := ComputeFingerprint(p); got != p.Fingerprint {
		return errors.New("evidence package fingerprint does not match content")
	}
	if len(p.Items) != len(CategoryOrder) {
		return fmt.Errorf("evidence package has %d items, want %d", len(p.Items), len(CategoryOrder))
	}
	isRevision := p.Version > 1
	for i, category := range CategoryOrder {
		if p.Items[i].Category != category {
			return fmt.Errorf("evidence package item %d is %q, want %q", i, p.Items[i].Category, category)
		}
	}
	if isRevision {
		if p.RevisionOf == "" || p.Supersedes == "" || p.RevisionReason == "" {
			return errors.New("revision package requires revision_of, supersedes and revision_reason")
		}
		if p.RevisionOf != p.Supersedes {
			return errors.New("revision package revision_of and supersedes must reference the same parent")
		}
		if p.RevisionOf == p.PackageID {
			return errors.New("revision package cannot supersede itself")
		}
	} else if p.RevisionOf != "" || p.Supersedes != "" || p.RevisionReason != "" {
		return errors.New("first evidence version must not carry revision fields")
	}
	if p.Status == StatusFrozen {
		if p.ReviewerID == "" {
			return errors.New("frozen evidence package requires reviewer_id")
		}
		if p.FrozenAt == nil || p.FrozenAt.IsZero() {
			return errors.New("frozen evidence package requires frozen_at")
		}
	}
	return nil
}
