package evidence

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// freezeablePackage 构建一个通过 Validate 的冻结态包（version=1 无父版，
// version>1 以 parent 为父版）。
func freezeablePackage(runID string, version int, parent *Package) *Package {
	in := happyInputs(runID)
	in.PackageID = "pkg-v" + strings.Repeat("x", version)
	in.Version = version
	in.Parent = parent
	if parent != nil {
		in.RevisionReason = "补录缺失记录"
	}
	pkg := Build(in)
	frozenAt := fixedTime().Add(time.Duration(version) * time.Minute)
	pkg.Status = StatusFrozen
	pkg.FrozenAt = &frozenAt
	pkg.ReviewerID = "rev-1"
	if err := pkg.Validate(); err != nil {
		panic(err)
	}
	return pkg
}

func TestFreezePersistsImmutableFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore()
	if err := store.ConfigurePersistence(dir); err != nil {
		t.Fatalf("configure: %v", err)
	}
	pkg := freezeablePackage("run-1", 1, nil)
	frozen, err := store.Freeze(pkg)
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if frozen.Status != StatusFrozen {
		t.Fatalf("status = %s, want frozen", frozen.Status)
	}
	path := filepath.Join(dir, "run-1", "1.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected frozen file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Fatalf("file permission = %o, want 0640", perm)
	}
	if _, ok := store.Draft(pkg.PackageID); ok {
		t.Fatal("draft must be removed after freeze")
	}
	if got, ok := store.FrozenVersion("run-1", 1); !ok || got.Fingerprint != pkg.Fingerprint {
		t.Fatal("frozen version not retrievable or fingerprint changed")
	}
}

func TestFreezeIdempotentForSameContent(t *testing.T) {
	dir := t.TempDir()
	store := NewStore()
	if err := store.ConfigurePersistence(dir); err != nil {
		t.Fatal(err)
	}
	first := freezeablePackage("run-1", 1, nil)
	if _, err := store.Freeze(first); err != nil {
		t.Fatalf("first freeze: %v", err)
	}
	again := freezeablePackage("run-1", 1, nil) // 同 package_id、同内容
	if _, err := store.Freeze(again); err != nil {
		t.Fatalf("idempotent freeze should succeed, got %v", err)
	}
}

func TestFreezeRejectsCompetingDraftForSameVersion(t *testing.T) {
	dir := t.TempDir()
	store := NewStore()
	if err := store.ConfigurePersistence(dir); err != nil {
		t.Fatal(err)
	}
	first := freezeablePackage("run-1", 1, nil)
	if _, err := store.Freeze(first); err != nil {
		t.Fatalf("first freeze: %v", err)
	}

	// 另一个有效草稿：同版本但不同 package_id（内容指纹因此不同）。
	competingInputs := happyInputs("run-1")
	competingInputs.PackageID = "pkg-competing"
	competing := Build(competingInputs)
	frozenAt := fixedTime()
	competing.Status = StatusFrozen
	competing.FrozenAt = &frozenAt
	competing.ReviewerID = "rev-2"
	if err := competing.Validate(); err != nil {
		t.Fatalf("competing package validate: %v", err)
	}
	if _, err := store.Freeze(competing); !errors.Is(err, ErrVersionFrozen) {
		t.Fatalf("got %v, want ErrVersionFrozen", err)
	}
}

func TestNextVersionAndRevisionChain(t *testing.T) {
	dir := t.TempDir()
	store := NewStore()
	if err := store.ConfigurePersistence(dir); err != nil {
		t.Fatal(err)
	}
	if got := store.NextVersion("run-1"); got != 1 {
		t.Fatalf("next version = %d, want 1", got)
	}
	v1 := freezeablePackage("run-1", 1, nil)
	if _, err := store.Freeze(v1); err != nil {
		t.Fatal(err)
	}
	v2 := freezeablePackage("run-1", 2, v1)
	if _, err := store.Freeze(v2); err != nil {
		t.Fatalf("freeze v2: %v", err)
	}
	if got := store.NextVersion("run-1"); got != 3 {
		t.Fatalf("next version = %d, want 3", got)
	}
	tip, ok := store.LatestFrozen("run-1")
	if !ok || tip.Version != 2 {
		t.Fatalf("latest version wrong: %+v", tip)
	}
	all := store.Frozen("run-1")
	if len(all) != 2 || all[0].Version != 1 || all[1].Version != 2 {
		t.Fatalf("frozen list ordering wrong: %+v", all)
	}
}

func TestReloadVerifiesAndRestores(t *testing.T) {
	dir := t.TempDir()
	first := NewStore()
	if err := first.ConfigurePersistence(dir); err != nil {
		t.Fatal(err)
	}
	v1 := freezeablePackage("run-1", 1, nil)
	if _, err := first.Freeze(v1); err != nil {
		t.Fatal(err)
	}

	reloaded := NewStore()
	if err := reloaded.ConfigurePersistence(dir); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := reloaded.FrozenVersion("run-1", 1)
	if !ok {
		t.Fatal("v1 not restored")
	}
	if got.Fingerprint != v1.Fingerprint || got.ReviewerID != "rev-1" {
		t.Fatal("restored package content mismatch")
	}
	if reloaded.NextVersion("run-1") != 2 {
		t.Fatal("reloaded store should continue versioning after restored v1")
	}
}

func TestReloadRejectsTamperedFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore()
	if err := store.ConfigurePersistence(dir); err != nil {
		t.Fatal(err)
	}
	v1 := freezeablePackage("run-1", 1, nil)
	if _, err := store.Freeze(v1); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "run-1", "1.json")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 篡改进入指纹的内容字段（压力值）但保留原指纹 → 重载重算指纹应失败。
	tampered := strings.Replace(string(payload), `"value": 10.5`, `"value": 11`, 1)
	if tampered == string(payload) {
		t.Fatal("test setup failed to alter a content field")
	}
	if err := os.WriteFile(path, []byte(tampered), 0o640); err != nil {
		t.Fatal(err)
	}
	reloaded := NewStore()
	if err := reloaded.ConfigurePersistence(dir); err == nil {
		t.Fatal("expected tampered evidence to fail verification on reload")
	}
}

func TestWriteFileRejectsImmutableConflict(t *testing.T) {
	// 空目录先配置（索引为空、directory 已设），再手工放入一个不同内容的 1.json，
	// 此时冻结另一个同版本包应在写盘阶段得到 ErrImmutableConflict。
	dir := t.TempDir()
	store := NewStore()
	if err := store.ConfigurePersistence(dir); err != nil {
		t.Fatal(err)
	}
	existing := freezeablePackage("run-1", 1, nil)
	existingBytes, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(dir, "run-1")
	if err := os.MkdirAll(runDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "1.json"), existingBytes, 0o640); err != nil {
		t.Fatal(err)
	}

	// 另一个内容不同的有效 v1 包（prepared_at 不同 → 指纹不同）。
	in := happyInputs("run-1")
	in.PackageID = "pkg-other"
	in.Now = fixedTime().Add(2 * time.Hour)
	other := Build(in)
	frozenAt := fixedTime()
	other.Status = StatusFrozen
	other.FrozenAt = &frozenAt
	other.ReviewerID = "rev-9"
	if err := other.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Freeze(other); !errors.Is(err, ErrImmutableConflict) {
		t.Fatalf("got %v, want ErrImmutableConflict", err)
	}
}
