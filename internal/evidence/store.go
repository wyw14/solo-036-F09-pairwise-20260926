package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ErrImmutableConflict 表示同一 run/version 的冻结文件已存在，
// 但其内容指纹与本次写入不同——磁盘上已确认的证据被改动，拒绝覆盖。
var ErrImmutableConflict = errors.New("frozen evidence package conflicts with an existing immutable file")

// ErrVersionFrozen 表示该 run/version 已在内存索引中冻结为不同内容，
// 典型场景是两个草稿并发 confirm 同一版本。
var ErrVersionFrozen = errors.New("evidence version is already frozen with different content")

// Store 保存证据包草稿（仅内存）与冻结版本（内存索引 + 可选落盘）。
type Store struct {
	mu        sync.RWMutex
	directory string // "" 表示仅内存模式
	drafts    map[string]*Package
	frozen    map[string]map[int]*Package
}

func NewStore() *Store {
	return &Store{
		drafts: make(map[string]*Package),
		frozen: make(map[string]map[int]*Package),
	}
}

// ConfigurePersistence 打开（或创建）证据目录并加载其中所有已冻结包。
// 可重复调用以从同一目录重建索引；加载时对每个包重算指纹，任何篡改或
// 结构错误都使整个加载失败（fail-closed）。
func (s *Store) ConfigurePersistence(directory string) error {
	cleaned := filepath.Clean(directory)
	if strings.TrimSpace(directory) == "" || cleaned == "." {
		return errors.New("evidence directory is required")
	}
	if err := os.MkdirAll(cleaned, 0o750); err != nil {
		return fmt.Errorf("create evidence directory: %w", err)
	}
	loaded, err := scanDirectory(cleaned)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.directory = cleaned
	s.frozen = loaded
	return nil
}

func scanDirectory(directory string) (map[string]map[int]*Package, error) {
	out := make(map[string]map[int]*Package)
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		runID := filepath.Base(filepath.Dir(path))
		versionText := strings.TrimSuffix(filepath.Base(path), ".json")
		version, err := strconv.Atoi(versionText)
		if err != nil || version < 1 {
			return fmt.Errorf("evidence file %s has an invalid version name", path)
		}
		pkg, err := readFrozenFile(path)
		if err != nil {
			return err
		}
		if pkg.RunID != runID {
			return fmt.Errorf("evidence file %s run_id %q does not match its directory %q", path, pkg.RunID, runID)
		}
		if pkg.Version != version {
			return fmt.Errorf("evidence file %s version %d does not match its name", path, pkg.Version)
		}
		if _, exists := out[runID][version]; exists {
			return fmt.Errorf("duplicate frozen evidence version for run %s version %d", runID, version)
		}
		if out[runID] == nil {
			out[runID] = make(map[int]*Package)
		}
		out[runID][version] = pkg
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func readFrozenFile(path string) (*Package, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open evidence file %s: %w", path, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16*1024*1024))
	decoder.DisallowUnknownFields()
	var pkg Package
	if err := decoder.Decode(&pkg); err != nil {
		return nil, fmt.Errorf("decode evidence file %s: %w", path, err)
	}
	if pkg.Status != StatusFrozen {
		return nil, fmt.Errorf("evidence file %s is not frozen", path)
	}
	if err := pkg.Validate(); err != nil {
		return nil, fmt.Errorf("validate evidence file %s: %w", path, err)
	}
	if got := ComputeFingerprint(&pkg); got != pkg.Fingerprint {
		return nil, fmt.Errorf("evidence file %s fingerprint mismatch", path)
	}
	return &pkg, nil
}

// SaveDraft 保存一个草稿。草稿只存在于内存，重启后可重新 prepare。
func (s *Store) SaveDraft(pkg *Package) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drafts[pkg.PackageID] = pkg
}

// Draft 返回指定草稿。
func (s *Store) Draft(packageID string) (*Package, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pkg, ok := s.drafts[packageID]
	return clonePackage(pkg), ok
}

// NextVersion 返回该作业下一个待使用的版本号（已冻结最大版本 + 1）。
func (s *Store) NextVersion(runID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	max := 0
	for version := range s.frozen[runID] {
		if version > max {
			max = version
		}
	}
	return max + 1
}

// HasFrozen 报告该作业是否已有冻结包。
func (s *Store) HasFrozen(runID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.frozen[runID]) > 0
}

// FrozenVersion 返回指定的冻结版本。
func (s *Store) FrozenVersion(runID string, version int) (*Package, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pkg, ok := s.frozen[runID][version]
	return clonePackage(pkg), ok
}

// LatestFrozen 返回该作业最新冻结版本（无则 false）。
func (s *Store) LatestFrozen(runID string) (*Package, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	versions := s.frozen[runID]
	if len(versions) == 0 {
		return nil, false
	}
	max := 0
	for version := range versions {
		if version > max {
			max = version
		}
	}
	return clonePackage(versions[max]), true
}

// Frozen 返回该作业全部冻结版本，按版本升序。
func (s *Store) Frozen(runID string) []*Package {
	s.mu.RLock()
	defer s.mu.RUnlock()
	versions := s.frozen[runID]
	out := make([]*Package, 0, len(versions))
	for _, pkg := range versions {
		out = append(out, pkg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return clonePackages(out)
}

// Freeze 校验并冻结一个包：原子落盘（若配置目录）、建立冻结索引、
// 删除对应草稿。同版本同指纹重复冻结按幂等处理；同版本不同指纹则
// 返回 ErrImmutableConflict，绝不覆盖既有文件。
func (s *Store) Freeze(pkg *Package) (*Package, error) {
	if err := pkg.Validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.frozen[pkg.RunID][pkg.Version]; ok {
		if existing.PackageID == pkg.PackageID && existing.Fingerprint == pkg.Fingerprint {
			delete(s.drafts, pkg.PackageID)
			return clonePackage(existing), nil
		}
		return nil, ErrVersionFrozen
	}

	if s.directory != "" {
		if err := s.writeFile(pkg); err != nil {
			return nil, err
		}
	}
	if s.frozen[pkg.RunID] == nil {
		s.frozen[pkg.RunID] = make(map[int]*Package)
	}
	stored := clonePackage(pkg)
	s.frozen[pkg.RunID][pkg.Version] = stored
	delete(s.drafts, pkg.PackageID)
	return clonePackage(stored), nil
}

func (s *Store) writeFile(pkg *Package) error {
	runDir := filepath.Join(s.directory, pkg.RunID)
	if err := os.MkdirAll(runDir, 0o750); err != nil {
		return fmt.Errorf("create evidence run directory: %w", err)
	}
	target := filepath.Join(runDir, fmt.Sprintf("%d.json", pkg.Version))
	if _, err := os.Stat(target); err == nil {
		// 文件已存在：仅当与本次内容逐字节一致时幂等放行，否则拒绝覆盖。
		existing, readErr := readFrozenFile(target)
		if readErr != nil || existing.Fingerprint != pkg.Fingerprint || existing.PackageID != pkg.PackageID {
			return ErrImmutableConflict
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat evidence file: %w", err)
	}

	payload, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal evidence package: %w", err)
	}
	temporary, err := os.CreateTemp(runDir, "pkg-*.tmp")
	if err != nil {
		return fmt.Errorf("create evidence temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o640); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set evidence file permissions: %w", err)
	}
	if _, err := temporary.Write(append(payload, '\n')); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write evidence file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync evidence file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close evidence file: %w", err)
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return fmt.Errorf("commit evidence file: %w", err)
	}
	committed = true
	return nil
}

// clonePackage 返回包的深拷贝（经 JSON 往返），避免调用方持有内部指针后改写。
func clonePackage(pkg *Package) *Package {
	if pkg == nil {
		return nil
	}
	payload, err := json.Marshal(pkg)
	if err != nil {
		return nil
	}
	var copyPkg Package
	if err := json.Unmarshal(payload, &copyPkg); err != nil {
		return nil
	}
	return &copyPkg
}

func clonePackages(pkgs []*Package) []*Package {
	out := make([]*Package, 0, len(pkgs))
	for _, pkg := range pkgs {
		out = append(out, clonePackage(pkg))
	}
	return out
}
