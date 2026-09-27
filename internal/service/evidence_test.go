package service_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wyw14/solo-036/internal/api"
	"github.com/wyw14/solo-036/internal/model"
	"github.com/wyw14/solo-036/internal/service"
)

type testClient struct {
	t      *testing.T
	router http.Handler
}

func (c *testClient) do(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	c.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (c *testClient) decode(path string, target any) {
	c.t.Helper()
	status, raw := c.do(http.MethodGet, path, nil)
	if status != http.StatusOK {
		c.t.Fatalf("GET %s -> %d: %s", path, status, raw)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		c.t.Fatalf("decode %s: %v", path, err)
	}
}

type evidenceItemJSON struct {
	Category    string `json:"category"`
	Status      string `json:"status"`
	RecordCount int    `json:"record_count"`
}

type evidencePackJSON struct {
	RunID           string             `json:"run_id"`
	Revision        int                `json:"revision"`
	ExtendsRevision int                `json:"extends_revision"`
	Status          string             `json:"status"`
	Reviewer        string             `json:"reviewer"`
	ContentHash     string             `json:"content_hash"`
	Items           []evidenceItemJSON `json:"items"`
}

type evidenceListJSON struct {
	CurrentRevision int                `json:"current_revision"`
	Packs           []evidencePackJSON `json:"packs"`
}

func itemStatus(pack evidencePackJSON, category string) string {
	for _, item := range pack.Items {
		if item.Category == category {
			return item.Status
		}
	}
	return ""
}

func itemCount(pack evidencePackJSON, category string) int {
	for _, item := range pack.Items {
		if item.Category == category {
			return item.RecordCount
		}
	}
	return -1
}

// driveToComplete launches a run, records two tracking observations, receives
// the pig, and completes the run. It returns the completed run id.
func driveToComplete(t *testing.T, client *testClient) string {
	t.Helper()
	status, raw := client.do(http.MethodPost, "/api/operations", map[string]any{
		"station_id":      "station-a",
		"route_id":        "main-station-a-station-c",
		"target_pressure": 10.0,
	})
	if status != http.StatusCreated {
		t.Fatalf("launch -> %d: %s", status, raw)
	}
	var operation struct {
		Run model.Run `json:"run"`
	}
	if err := json.Unmarshal(raw, &operation); err != nil {
		t.Fatalf("decode launch: %v", err)
	}
	runID := operation.Run.ID

	base := time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)
	tracks := []struct {
		source   string
		position float64
		at       time.Time
	}{
		{"magnetic/sensor-1", 86.4, base},
		{"acoustic/sensor-2", 86.5, base.Add(10 * time.Second)},
	}
	for _, track := range tracks {
		status, raw = client.do(http.MethodPost, "/api/operations/"+runID+"/tracking", map[string]any{
			"station_id":  "station-b",
			"source_id":   track.source,
			"position":    track.position,
			"observed_at": track.at.Format(time.RFC3339),
		})
		if status != http.StatusOK {
			t.Fatalf("tracking -> %d: %s", status, raw)
		}
	}

	status, raw = client.do(http.MethodPost, "/api/operations/"+runID+"/receive", map[string]any{
		"station_id":      "station-c",
		"isolation_epoch": 5,
	})
	if status != http.StatusOK {
		t.Fatalf("receive -> %d: %s", status, raw)
	}
	status, raw = client.do(http.MethodPost, "/api/operations/"+runID+"/complete", map[string]any{
		"route_id": "main-station-a-station-c",
	})
	if status != http.StatusOK {
		t.Fatalf("complete -> %d: %s", status, raw)
	}
	return runID
}

func newClient(t *testing.T) *testClient {
	t.Helper()
	runtime := service.NewRuntime()
	return &testClient{t: t, router: api.NewRouter(runtime)}
}

func TestEvidencePackEndToEndFreezeAndRevise(t *testing.T) {
	client := newClient(t)
	runID := driveToComplete(t, client)
	evidencePath := "/api/operations/" + runID + "/evidence"

	// No packs before the first build.
	var list evidenceListJSON
	client.decode(evidencePath, &list)
	if len(list.Packs) != 0 || list.CurrentRevision != 0 {
		t.Fatalf("expected no evidence initially, got %+v", list)
	}

	// Build the first draft after completion.
	status, raw := client.do(http.MethodPost, evidencePath, nil)
	if status != http.StatusCreated {
		t.Fatalf("build evidence -> %d: %s", status, raw)
	}
	var rev1 evidencePackJSON
	if err := json.Unmarshal(raw, &rev1); err != nil {
		t.Fatalf("decode rev1: %v", err)
	}
	if rev1.Revision != 1 || rev1.Status != string(model.PackDraft) || rev1.ExtendsRevision != 0 {
		t.Fatalf("unexpected rev1: %+v", rev1)
	}
	if len(rev1.Items) != 7 {
		t.Fatalf("expected 7 evidence categories, got %d", len(rev1.Items))
	}
	// Route lease was released on completion, but the route is recovered from
	// the journal; tracking and journal events are recorded.
	for category, want := range map[string]string{
		model.CategoryRunState:  string(model.EvidenceRecorded),
		model.CategoryRoute:     string(model.EvidenceRecorded),
		model.CategoryPressure:  string(model.EvidenceRecorded),
		model.CategoryTracking:  string(model.EvidenceRecorded),
		model.CategoryIncidents: string(model.EvidenceRecorded),
		model.CategoryJournal:   string(model.EvidenceRecorded),
		// No passage permit was granted in this run: an explicit gap.
		model.CategoryPermits: string(model.EvidenceMissing),
	} {
		if got := itemStatus(rev1, category); got != want {
			t.Errorf("category %s: want %s, got %s", category, want, got)
		}
	}
	journalCountRev1 := itemCount(rev1, model.CategoryJournal)
	if journalCountRev1 <= 0 {
		t.Fatal("journal category should record events")
	}

	// Freezing with gaps but without acknowledgement is rejected.
	status, _ = client.do(http.MethodPost, evidencePath+"/1/freeze", map[string]any{
		"reviewer": "ops-lead",
	})
	if status != http.StatusConflict {
		t.Fatalf("freeze without gap ack -> want 409, got %d", status)
	}

	// A reviewer is mandatory.
	status, _ = client.do(http.MethodPost, evidencePath+"/1/freeze", map[string]any{
		"acknowledge_gaps": true,
	})
	if status != http.StatusBadRequest {
		t.Fatalf("freeze without reviewer -> want 400, got %d", status)
	}

	// Reviewer confirms and acknowledges the permit gap: revision fixed.
	status, raw = client.do(http.MethodPost, evidencePath+"/1/freeze", map[string]any{
		"reviewer":         "ops-lead",
		"review_note":      "permit gap is expected for this line section",
		"acknowledge_gaps": true,
		"gap_note":         "no repressurization required",
	})
	if status != http.StatusOK {
		t.Fatalf("freeze with ack -> %d: %s", status, raw)
	}
	if err := json.Unmarshal(raw, &rev1); err != nil {
		t.Fatalf("decode frozen rev1: %v", err)
	}
	if rev1.Status != string(model.PackFrozen) || rev1.Reviewer != "ops-lead" || rev1.ContentHash == "" {
		t.Fatalf("rev1 freeze incomplete: %+v", rev1)
	}
	frozenHash := rev1.ContentHash

	// Re-freezing the confirmed revision is impossible.
	status, _ = client.do(http.MethodPost, evidencePath+"/1/freeze", map[string]any{
		"reviewer":         "someone-else",
		"acknowledge_gaps": true,
	})
	if status != http.StatusConflict {
		t.Fatalf("re-freeze rev1 -> want 409, got %d", status)
	}

	// Building again after the freeze opens a NEW revision extending rev1;
	// it never overwrites the confirmed one.
	status, raw = client.do(http.MethodPost, evidencePath, nil)
	if status != http.StatusCreated {
		t.Fatalf("build rev2 -> %d: %s", status, raw)
	}
	var rev2 evidencePackJSON
	if err := json.Unmarshal(raw, &rev2); err != nil {
		t.Fatalf("decode rev2: %v", err)
	}
	if rev2.Revision != 2 || rev2.ExtendsRevision != 1 || rev2.Status != string(model.PackDraft) {
		t.Fatalf("rev2 should extend frozen rev1 as a draft, got %+v", rev2)
	}
	// The freeze itself is a journaled event, so the new revision's journal
	// category reflects additional records and its content differs.
	if itemCount(rev2, model.CategoryJournal) != journalCountRev1+1 {
		t.Errorf("rev2 journal count should be %d after the freeze event, got %d",
			journalCountRev1+1, itemCount(rev2, model.CategoryJournal))
	}

	// Revision 1 is byte-for-byte unchanged after revision 2 appeared.
	var stillRev1 evidencePackJSON
	client.decode(evidencePath+"/1", &stillRev1)
	if stillRev1.ContentHash != frozenHash || stillRev1.Reviewer != "ops-lead" || stillRev1.Status != string(model.PackFrozen) {
		t.Fatalf("confirmed rev1 was altered: %+v", stillRev1)
	}

	// Confirm revision 2 as well; current moves to 2, rev1 stays intact.
	status, raw = client.do(http.MethodPost, evidencePath+"/2/freeze", map[string]any{
		"reviewer":         "ops-lead",
		"acknowledge_gaps": true,
	})
	if status != http.StatusOK {
		t.Fatalf("freeze rev2 -> %d: %s", status, raw)
	}
	client.decode(evidencePath, &list)
	if list.CurrentRevision != 2 || len(list.Packs) != 2 {
		t.Fatalf("expected 2 packs with current=2, got %+v", list)
	}
	client.decode(evidencePath+"/1", &stillRev1)
	if stillRev1.ContentHash != frozenHash {
		t.Fatal("rev1 hash changed after rev2 was frozen")
	}
}

func TestEvidenceRequiresCompletionAndExists(t *testing.T) {
	client := newClient(t)

	// Evidence for a run that was launched but never completed is rejected.
	status, raw := client.do(http.MethodPost, "/api/operations", map[string]any{
		"station_id":      "station-a",
		"route_id":        "main-station-a-station-c",
		"target_pressure": 10.0,
	})
	if status != http.StatusCreated {
		t.Fatalf("launch -> %d: %s", status, raw)
	}
	var operation struct {
		Run model.Run `json:"run"`
	}
	_ = json.Unmarshal(raw, &operation)

	status, _ = client.do(http.MethodPost, "/api/operations/"+operation.Run.ID+"/evidence", nil)
	if status != http.StatusConflict {
		t.Fatalf("evidence for unfinished run -> want 409, got %d", status)
	}

	// Release the route so a subsequent run in this test can launch.
	if status, _ = client.do(http.MethodDelete, "/api/operations/"+operation.Run.ID, nil); status != http.StatusNoContent {
		t.Fatalf("cancel unfinished run -> want 204, got %d", status)
	}

	// Unknown run and unknown revision are 404s.
	status, _ = client.do(http.MethodGet, "/api/operations/does-not-exist/evidence", nil)
	if status != http.StatusNotFound {
		t.Fatalf("evidence for unknown run -> want 404, got %d", status)
	}

	completed := driveToComplete(t, client)
	status, _ = client.do(http.MethodGet, "/api/operations/"+completed+"/evidence/99", nil)
	if status != http.StatusNotFound {
		t.Fatalf("unknown revision -> want 404, got %d", status)
	}
	status, _ = client.do(http.MethodPost, "/api/operations/"+completed+"/evidence/99/freeze", map[string]any{
		"reviewer": "ops-lead",
	})
	if status != http.StatusNotFound {
		t.Fatalf("freeze unknown revision -> want 404, got %d", status)
	}

	// A non-numeric revision is a 400.
	status, _ = client.do(http.MethodGet, "/api/operations/"+completed+"/evidence/abc", nil)
	if status != http.StatusBadRequest {
		t.Fatalf("non-numeric revision -> want 400, got %d", status)
	}
}
