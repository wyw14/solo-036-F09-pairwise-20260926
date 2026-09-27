package evidence

import (
	"fmt"
	"time"

	"github.com/wyw14/solo-036/internal/model"
)

// Source is the read-only view of scattered run records that Build assembles
// into a pack. The service layer fills it from the run store, the control
// snapshot, the tracker, and the journal; evidence never reaches back into
// those systems itself.
type Source struct {
	Run             model.Run
	Operation       model.OperationRecord
	Routes          []model.RouteState
	Incidents       []model.Incident
	JournalEvents   []model.Event
	Generation      int64
	ControlRevision string
}

// journalSummary is the recorded detail for the journal category.
type journalSummary struct {
	EventCount int       `json:"event_count"`
	FirstAt    time.Time `json:"first_at,omitempty"`
	LastAt     time.Time `json:"last_at,omitempty"`
	Kinds      []string  `json:"kinds"`
}

// Build assembles one evidence pack revision for a completed run. Every
// category appears exactly once, in fixed order, either as recorded evidence
// or as an explicit gap with a reason.
func Build(src Source, revision, extendsRevision int, capturedAt time.Time) (model.EvidencePack, error) {
	if src.Run.ID == "" {
		return model.EvidencePack{}, fmt.Errorf("run is required to build evidence")
	}
	if src.Run.State != model.StateComplete {
		return model.EvidencePack{}, fmt.Errorf("run %s is %s, not complete", src.Run.ID, src.Run.State)
	}
	items := []model.EvidenceItem{
		runStateItem(src.Run),
		routeItem(src.Operation.Route, src.Routes, src.JournalEvents),
		pressureItem(src.Operation.Pressure),
		trackingItem(src.Operation.Tracking, src.Generation),
		permitsItem(src.Operation.Permits),
		incidentsItem(src.Incidents),
		journalItem(src.JournalEvents),
	}
	return model.EvidencePack{
		RunID:           src.Run.ID,
		Revision:        revision,
		ExtendsRevision: extendsRevision,
		BasedOnEpoch:    src.Run.Epoch,
		ControlRevision: src.ControlRevision,
		CapturedAt:      capturedAt.UTC(),
		Status:          model.PackDraft,
		Items:           items,
	}, nil
}

func runStateItem(run model.Run) model.EvidenceItem {
	return model.EvidenceItem{
		Category:    model.CategoryRunState,
		Status:      model.EvidenceRecorded,
		Summary:     fmt.Sprintf("run reached %s at epoch %d", run.State, run.Epoch),
		Detail:      run,
		RecordCount: 1,
	}
}

// routeItem resolves which route the run used. Completion releases the lease
// and clears the run attribution, so the durable source is the journal: the
// launch/complete events carry the route id. The route's static endpoints are
// then read back from the snapshot's route definitions.
func routeItem(attributed model.RouteState, routes []model.RouteState, events []model.Event) model.EvidenceItem {
	if attributed.ID != "" {
		return model.EvidenceItem{
			Category:    model.CategoryRoute,
			Status:      model.EvidenceRecorded,
			Summary:     fmt.Sprintf("route %s from %s to %s", attributed.ID, attributed.FromStation, attributed.ToStation),
			Detail:      attributed,
			RecordCount: 1,
		}
	}
	routeID := routeIDFromEvents(events)
	if routeID == "" {
		return model.EvidenceItem{
			Category:  model.CategoryRoute,
			Status:    model.EvidenceMissing,
			Summary:   "no route recorded for this run",
			GapReason: "no live lease and no launch/complete journal event names a route",
		}
	}
	for _, route := range routes {
		if route.ID == routeID {
			return model.EvidenceItem{
				Category:    model.CategoryRoute,
				Status:      model.EvidenceRecorded,
				Summary:     fmt.Sprintf("route %s from %s to %s (lease released)", route.ID, route.FromStation, route.ToStation),
				Detail:      route,
				RecordCount: 1,
			}
		}
	}
	return model.EvidenceItem{
		Category:    model.CategoryRoute,
		Status:      model.EvidenceRecorded,
		Summary:     fmt.Sprintf("route %s (definition no longer present)", routeID),
		Detail:      map[string]string{"id": routeID},
		RecordCount: 1,
	}
}

// routeIDFromEvents recovers the route id a run used from its journal events.
// Launch and complete events store the route id as their payload.
func routeIDFromEvents(events []model.Event) string {
	for _, event := range events {
		if (event.Kind == "launch" || event.Kind == "complete") && event.Payload != "" {
			return event.Payload
		}
	}
	return ""
}

func pressureItem(pressure model.PressureState) model.EvidenceItem {
	if pressure.StationID == "" || pressure.RunID == "" {
		return model.EvidenceItem{
			Category:  model.CategoryPressure,
			Status:    model.EvidenceMissing,
			Summary:   "no pressure record attributed to this run",
			GapReason: "no station pressure state carries this run id",
		}
	}
	return model.EvidenceItem{
		Category:    model.CategoryPressure,
		Status:      model.EvidenceRecorded,
		Summary:     fmt.Sprintf("station %s at %.2f (target %.2f, %s)", pressure.StationID, pressure.Value, pressure.Target, pressure.Mode),
		Detail:      pressure,
		RecordCount: 1,
	}
}

func trackingItem(tracking []model.TrackingState, generation int64) model.EvidenceItem {
	if len(tracking) == 0 {
		return model.EvidenceItem{
			Category:  model.CategoryTracking,
			Status:    model.EvidenceMissing,
			Summary:   "no tracking observations recorded",
			GapReason: "the run produced no passage tracking points",
		}
	}
	return model.EvidenceItem{
		Category: model.CategoryTracking,
		Status:   model.EvidenceRecorded,
		Summary:  fmt.Sprintf("%d tracking observations at generation %d", len(tracking), generation),
		Detail: struct {
			Generation int64                 `json:"generation"`
			Points     []model.TrackingState `json:"points"`
		}{Generation: generation, Points: tracking},
		RecordCount: len(tracking),
	}
}

func permitsItem(permits []model.PermitState) model.EvidenceItem {
	if len(permits) == 0 {
		return model.EvidenceItem{
			Category:  model.CategoryPermits,
			Status:    model.EvidenceMissing,
			Summary:   "no passage permits granted",
			GapReason: "no repressurization permit was granted for this run",
		}
	}
	return model.EvidenceItem{
		Category:    model.CategoryPermits,
		Status:      model.EvidenceRecorded,
		Summary:     fmt.Sprintf("%d permit(s) granted", len(permits)),
		Detail:      permits,
		RecordCount: len(permits),
	}
}

func incidentsItem(incidents []model.Incident) model.EvidenceItem {
	// Zero incidents is a legitimate clean-run outcome, not a gap.
	return model.EvidenceItem{
		Category:    model.CategoryIncidents,
		Status:      model.EvidenceRecorded,
		Summary:     fmt.Sprintf("%d incident(s) recorded", len(incidents)),
		Detail:      incidents,
		RecordCount: len(incidents),
	}
}

func journalItem(events []model.Event) model.EvidenceItem {
	if len(events) == 0 {
		return model.EvidenceItem{
			Category:  model.CategoryJournal,
			Status:    model.EvidenceMissing,
			Summary:   "no journal events for this run",
			GapReason: "the durable journal holds no events for this run",
		}
	}
	kinds := make([]string, 0, len(events))
	seen := make(map[string]bool)
	first, last := events[0].At, events[0].At
	for _, event := range events {
		if !seen[event.Kind] {
			seen[event.Kind] = true
			kinds = append(kinds, event.Kind)
		}
		if event.At.Before(first) {
			first = event.At
		}
		if event.At.After(last) {
			last = event.At
		}
	}
	return model.EvidenceItem{
		Category:    model.CategoryJournal,
		Status:      model.EvidenceRecorded,
		Summary:     fmt.Sprintf("%d journal events from %s to %s", len(events), first.Format(time.RFC3339), last.Format(time.RFC3339)),
		Detail:      journalSummary{EventCount: len(events), FirstAt: first, LastAt: last, Kinds: kinds},
		RecordCount: len(events),
	}
}
