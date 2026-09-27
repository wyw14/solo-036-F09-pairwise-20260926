package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/wyw14/solo-036/internal/model"
)

// contentView is the canonical, hashable projection of a pack. It deliberately
// excludes mutable lifecycle fields (status, reviewer, frozen_at, content_hash)
// so the hash identifies the recorded content, not the freeze bookkeeping.
type contentView struct {
	RunID           string               `json:"run_id"`
	Revision        int                  `json:"revision"`
	ExtendsRevision int                  `json:"extends_revision"`
	BasedOnEpoch    int64                `json:"based_on_epoch"`
	ControlRevision string               `json:"control_revision"`
	CapturedAt      string               `json:"captured_at"`
	Items           []model.EvidenceItem `json:"items"`
}

// HashPack computes the sha256 content hash of a pack. The same recorded
// content always yields the same hash; any change to the records changes it.
func HashPack(pack model.EvidencePack) (string, error) {
	view := contentView{
		RunID:           pack.RunID,
		Revision:        pack.Revision,
		ExtendsRevision: pack.ExtendsRevision,
		BasedOnEpoch:    pack.BasedOnEpoch,
		ControlRevision: pack.ControlRevision,
		CapturedAt:      pack.CapturedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		Items:           pack.Items,
	}
	payload, err := json.Marshal(view)
	if err != nil {
		return "", fmt.Errorf("marshal evidence content: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}
