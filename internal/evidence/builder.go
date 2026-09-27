package evidence

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wyw14/solo-036/internal/emergency"
	"github.com/wyw14/solo-036/internal/model"
	"github.com/wyw14/solo-036/internal/tracker"
)

// Inputs 是构建证据包所需的全部采集输入。这些值由 service 层在
// 各子系统锁外取好后传入，Build 本身是不持锁的纯函数。
type Inputs struct {
	PackageID       string
	Version         int
	Run             model.Run
	Snapshot        model.ControlSnapshot
	RunEvents       []model.Event
	Tail            JournalTail
	ControlRevision string
	Lineage         []tracker.SourceObservation // 可空：重启后血缘为空，不影响主判定
	Parent          *Package                    // 非 nil 表示构建修订版
	RevisionReason  string
	Now             time.Time
}

// journalEvent 包装"某类事件是否存在"，便于在构建器间传递可选事件。
type journalEvent struct {
	event model.Event
	found bool
}

// Build 汇集一次作业的八类证据，逐项标注 adopted/incomplete/missing，
// 修订版额外标注相对父包的变化，并计算内容指纹。
func Build(in Inputs) *Package {
	events := indexEvents(in.RunEvents)
	stations := in.Snapshot.StationIndex()
	launch := lookupEvent(events, "launch")
	receiving := lookupEvent(events, "receiving")
	complete := lookupEvent(events, "complete")
	loaded := lookupEvent(events, "state")

	builders := []func() Item{
		func() Item { return buildRunItem(in.Run, loaded, complete) },
		func() Item { return buildRouteItem(in.Snapshot, launch, complete, loaded) },
		func() Item { return buildPressureItem(in.Run.ID, in.Snapshot, stations) },
		func() Item { return buildValvesItem(in.Run.ID, in.Snapshot, stations, launch, receiving) },
		func() Item { return buildClosuresItem(in.Run.ID, in.Snapshot, stations) },
		func() Item { return buildTrackingItem(in.Run.ID, in.Snapshot, events, in.Lineage) },
		func() Item { return buildPermitsItem(in.Run.ID, in.Snapshot) },
		func() Item { return buildIncidentsItem(in.Run.ID, in.Snapshot, events) },
	}

	items := make([]Item, 0, len(CategoryOrder))
	for _, build := range builders {
		items = append(items, build())
	}

	pkg := &Package{
		PackageID:       in.PackageID,
		RunID:           in.Run.ID,
		Version:         in.Version,
		RevisionNo:      in.Version,
		Status:          StatusDraft,
		PreparedAt:      in.Now,
		JournalTail:     in.Tail,
		ControlRevision: in.ControlRevision,
		Items:           items,
	}
	if in.Parent != nil {
		pkg.RevisionOf = in.Parent.PackageID
		pkg.Supersedes = in.Parent.PackageID
		pkg.RevisionReason = strings.TrimSpace(in.RevisionReason)
		markChanged(pkg, in.Parent)
	}
	for _, item := range pkg.Items {
		switch item.Status {
		case ItemAdopted:
			pkg.Summary.Adopted++
		case ItemMissing:
			pkg.Summary.Missing++
		case ItemIncomplete:
			pkg.Summary.Incomplete++
		}
	}
	pkg.Fingerprint = ComputeFingerprint(pkg)
	return pkg
}

func indexEvents(events []model.Event) map[string][]model.Event {
	out := make(map[string][]model.Event)
	for _, event := range events {
		out[event.Kind] = append(out[event.Kind], event)
	}
	return out
}

func lookupEvent(events map[string][]model.Event, kind string) journalEvent {
	if list := events[kind]; len(list) > 0 {
		return journalEvent{event: list[0], found: true}
	}
	return journalEvent{}
}

func sourceRef(event model.Event, detail string) SourceRef {
	return SourceRef{Kind: "journal_event", Detail: detail, EventID: event.ID}
}

func gaps(messages ...string) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		if strings.TrimSpace(message) != "" {
			out = append(out, message)
		}
	}
	return out
}

func buildRunItem(run model.Run, loaded, complete journalEvent) Item {
	item := Item{
		Category: "run",
		Records: RunRecord{
			RunID:     run.ID,
			Station:   run.Station,
			State:     string(run.State),
			Epoch:     run.Epoch,
			CreatedAt: run.CreatedAt,
			UpdatedAt: run.UpdatedAt,
		},
		Sources: []SourceRef{},
	}
	if loaded.found {
		item.Sources = append(item.Sources, sourceRef(loaded.event, fmt.Sprintf("event kind=%s epoch=%d", loaded.event.Kind, loaded.event.Epoch)))
	}
	if complete.found {
		item.Sources = append(item.Sources, sourceRef(complete.event, fmt.Sprintf("event kind=complete epoch=%d", complete.event.Epoch)))
	}
	switch {
	case run.State == model.StateComplete && complete.found:
		item.Status = ItemAdopted
	case run.State == model.StateComplete:
		item.Status = ItemIncomplete
		item.Gaps = gaps("run is complete but no complete event was found in the journal")
	default:
		item.Status = ItemIncomplete
		item.Gaps = gaps(fmt.Sprintf("run state is %q, want %q", run.State, model.StateComplete))
	}
	return item
}

func buildRouteItem(snapshot model.ControlSnapshot, launch, complete, loaded journalEvent) Item {
	item := Item{Category: "route", Sources: []SourceRef{}}
	launchRoute := ""
	if launch.found {
		launchRoute = launch.event.Payload
		item.Sources = append(item.Sources, sourceRef(launch.event, fmt.Sprintf("event kind=launch epoch=%d payload=%s", launch.event.Epoch, launchRoute)))
	}
	completeRoute := ""
	if complete.found {
		completeRoute = complete.event.Payload
		item.Sources = append(item.Sources, sourceRef(complete.event, fmt.Sprintf("event kind=complete payload=%s", completeRoute)))
	}
	routeID := launchRoute
	if routeID == "" {
		routeID = completeRoute
	}
	if routeID == "" {
		item.Status = ItemMissing
		item.Gaps = gaps("route cannot be identified: neither launch nor complete event carries a route id")
		return item
	}

	routeState, topologyKnown := routeByID(snapshot, routeID)
	if topologyKnown {
		item.Sources = append(item.Sources, SourceRef{
			Kind:   "control_snapshot",
			Detail: fmt.Sprintf("control:routes/%s", routeID),
		})
	}
	leaseEpoch := int64(0)
	if loaded.found {
		leaseEpoch = loaded.event.Epoch
	}
	item.Records = RouteRecord{
		RouteID:     routeID,
		FromStation: routeState.FromStation,
		ToStation:   routeState.ToStation,
		LeaseEpoch:  leaseEpoch,
		Released:    topologyKnown && !routeState.Reserved,
	}

	messages := []string{}
	if launchRoute != "" && completeRoute != "" && launchRoute != completeRoute {
		messages = append(messages, fmt.Sprintf("route id mismatch: launch=%s complete=%s", launchRoute, completeRoute))
	}
	if !topologyKnown {
		messages = append(messages, fmt.Sprintf("route %s topology is unavailable in the control snapshot", routeID))
	} else if routeState.Reserved {
		messages = append(messages, "route remains reserved after completion; release was not recorded")
	}
	if len(messages) > 0 {
		item.Status = ItemIncomplete
		item.Gaps = gaps(messages...)
	} else {
		item.Status = ItemAdopted
	}
	return item
}

func routeByID(snapshot model.ControlSnapshot, routeID string) (model.RouteState, bool) {
	for _, routeState := range snapshot.Routes {
		if routeState.ID == routeID {
			return routeState, true
		}
	}
	return model.RouteState{}, false
}

func stationsByRole(stations map[string]model.Station, role model.StationRole) []model.Station {
	out := make([]model.Station, 0)
	for _, station := range stations {
		if station.Role == role && station.Enabled {
			out = append(out, station)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func buildPressureItem(runID string, snapshot model.ControlSnapshot, stations map[string]model.Station) Item {
	records := make([]PressureRecord, 0)
	for _, pressure := range snapshot.Pressures {
		if pressure.RunID != runID {
			continue
		}
		records = append(records, PressureRecord{
			StationID:  pressure.StationID,
			Role:       string(stations[pressure.StationID].Role),
			Value:      pressure.Value,
			Target:     pressure.Target,
			Mode:       string(pressure.Mode),
			ObservedAt: pressure.ObservedAt,
		})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].StationID < records[j].StationID })

	item := Item{
		Category: "pressure",
		Sources:  []SourceRef{{Kind: "control_snapshot", Detail: "control:pressures filtered by run_id"}},
	}
	if len(records) == 0 {
		item.Status = ItemMissing
		item.Records = []PressureRecord{}
		item.Gaps = gaps("no pressure record tagged with this run; station state holds only the latest run and may have been overwritten")
		return item
	}
	item.Records = records
	present := make(map[string]bool, len(records))
	for _, record := range records {
		present[record.StationID] = true
	}
	messages := []string{}
	for _, role := range []model.StationRole{model.StationLauncher, model.StationReceiver} {
		for _, station := range stationsByRole(stations, role) {
			if !present[station.ID] {
				messages = append(messages, fmt.Sprintf("pressure record absent at %s station %s (station may have been reused by a later run or never tagged)", role, station.ID))
			}
		}
	}
	if len(messages) > 0 {
		item.Status = ItemIncomplete
		item.Gaps = gaps(messages...)
	} else {
		item.Status = ItemAdopted
	}
	return item
}

func buildValvesItem(runID string, snapshot model.ControlSnapshot, stations map[string]model.Station, launch, receiving journalEvent) Item {
	records := make([]ValveRecord, 0)
	for _, valve := range snapshot.Valves {
		if valve.RunID != runID {
			continue
		}
		records = append(records, ValveRecord{
			ValveID:      valve.ID,
			StationID:    valve.StationID,
			Position:     string(valve.Position),
			CommandEpoch: valve.CommandEpoch,
			ObservedAt:   valve.ObservedAt,
		})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ValveID < records[j].ValveID })

	item := Item{
		Category: "valves",
		Sources:  []SourceRef{{Kind: "control_snapshot", Detail: "control:valves filtered by run_id"}},
	}
	if len(records) == 0 {
		item.Status = ItemMissing
		item.Records = []ValveRecord{}
		item.Gaps = gaps("no valve record tagged with this run")
		return item
	}
	item.Records = records
	byStation := make(map[string]ValveRecord, len(records))
	for _, record := range records {
		byStation[record.StationID] = record
	}
	messages := []string{}
	for _, role := range []model.StationRole{model.StationLauncher, model.StationReceiver} {
		for _, station := range stationsByRole(stations, role) {
			record, ok := byStation[station.ID]
			if !ok {
				messages = append(messages, fmt.Sprintf("valve record absent at %s station %s", role, station.ID))
				continue
			}
			var expected int64
			eventKind := ""
			if role == model.StationLauncher && launch.found {
				expected, eventKind = launch.event.Epoch, "launch"
			}
			if role == model.StationReceiver && receiving.found {
				expected, eventKind = receiving.event.Epoch, "receiving"
			}
			if eventKind != "" && record.CommandEpoch != expected {
				messages = append(messages, fmt.Sprintf("valve %s command_epoch=%d does not match %s event epoch=%d", record.ValveID, record.CommandEpoch, eventKind, expected))
			}
		}
	}
	if len(messages) > 0 {
		item.Status = ItemIncomplete
		item.Gaps = gaps(messages...)
	} else {
		item.Status = ItemAdopted
	}
	return item
}

func buildClosuresItem(runID string, snapshot model.ControlSnapshot, stations map[string]model.Station) Item {
	records := make([]ClosureRecord, 0)
	for _, closure := range snapshot.Closures {
		if closure.RunID != runID {
			continue
		}
		records = append(records, ClosureRecord{
			StationID:    closure.StationID,
			Locked:       closure.Locked,
			CommandEpoch: closure.CommandEpoch,
			ObservedAt:   closure.ObservedAt,
		})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].StationID < records[j].StationID })

	item := Item{
		Category: "closures",
		Sources:  []SourceRef{{Kind: "control_snapshot", Detail: "control:closures filtered by run_id"}},
	}
	if len(records) == 0 {
		item.Status = ItemMissing
		item.Records = []ClosureRecord{}
		item.Gaps = gaps("no closure record tagged with this run")
		return item
	}
	item.Records = records
	messages := []string{}
	launcherLocked := false
	launcherSeen := false
	for _, record := range records {
		if stations[record.StationID].Role != model.StationLauncher {
			if !record.Locked {
				messages = append(messages, fmt.Sprintf("closure at station %s is not locked", record.StationID))
			}
			continue
		}
		launcherSeen = true
		if record.Locked {
			launcherLocked = true
		} else {
			messages = append(messages, fmt.Sprintf("launcher closure at station %s is not locked", record.StationID))
		}
	}
	if !launcherSeen {
		for _, station := range stationsByRole(stations, model.StationLauncher) {
			messages = append(messages, fmt.Sprintf("no launcher closure record at station %s", station.ID))
		}
	}
	if launcherSeen && !launcherLocked {
		// launcher 缺锁定的具体文案已在上面追加，这里不再重复。
	}
	if len(messages) > 0 {
		item.Status = ItemIncomplete
		item.Gaps = gaps(messages...)
	} else {
		item.Status = ItemAdopted
	}
	return item
}

func physicalSourcePrefix(sourceID string) string {
	return strings.TrimSpace(strings.SplitN(sourceID, "/", 2)[0])
}

func lineageKinds(observations []tracker.SourceObservation) map[string]string {
	out := make(map[string]string)
	for _, observation := range observations {
		key := fmt.Sprintf("%s|%d|%s", observation.StationID, observation.Generation, observation.PhysicalSourceID)
		if _, exists := out[key]; !exists {
			out[key] = string(observation.Kind)
		}
	}
	return out
}

func buildTrackingItem(runID string, snapshot model.ControlSnapshot, events map[string][]model.Event, lineage []tracker.SourceObservation) Item {
	records := make([]TrackingRecord, 0)
	maxGeneration := int64(0)
	for _, tracking := range snapshot.Tracking {
		if tracking.RunID != runID {
			continue
		}
		if tracking.Generation > maxGeneration {
			maxGeneration = tracking.Generation
		}
		records = append(records, TrackingRecord{
			StationID:  tracking.StationID,
			SourceID:   tracking.SourceID,
			Generation: tracking.Generation,
			Position:   tracking.Position,
			Online:     tracking.Online,
			ObservedAt: tracking.ObservedAt,
		})
	}
	kinds := lineageKinds(lineage)
	for i := range records {
		key := fmt.Sprintf("%s|%d|%s", records[i].StationID, records[i].Generation, records[i].SourceID)
		records[i].Kind = kinds[key]
		records[i].Superseded = records[i].Generation < maxGeneration
	}
	sort.Slice(records, func(i, j int) bool {
		if !records[i].ObservedAt.Equal(records[j].ObservedAt) {
			return records[i].ObservedAt.Before(records[j].ObservedAt)
		}
		if records[i].StationID != records[j].StationID {
			return records[i].StationID < records[j].StationID
		}
		return records[i].SourceID < records[j].SourceID
	})

	sources := []SourceRef{{Kind: "control_snapshot", Detail: "control:tracking filtered by run_id"}}
	if len(lineage) > 0 {
		sources = append(sources, SourceRef{Kind: "lineage", Detail: "source lineage observations"})
	}
	item := Item{Category: "tracking", Sources: sources}
	if len(records) == 0 {
		item.Status = ItemMissing
		item.Records = []TrackingRecord{}
		item.Gaps = gaps("no tracking observation recorded for this run")
		return item
	}
	item.Records = records

	currentSources := make(map[string]bool)
	currentCount := 0
	for _, record := range records {
		if record.Generation != maxGeneration {
			continue
		}
		currentCount++
		if prefix := physicalSourcePrefix(record.SourceID); prefix != "" {
			currentSources[prefix] = true
		}
	}
	messages := []string{}
	if currentCount == 0 {
		messages = append(messages, fmt.Sprintf("only superseded tracking generations present; %d tracking revision(s) occurred", len(events["tracking_revision"])))
	} else if len(currentSources) < 2 {
		messages = append(messages, fmt.Sprintf("latest tracking generation %d has %d independent physical source(s), need at least 2", maxGeneration, len(currentSources)))
	}
	if len(messages) > 0 {
		item.Status = ItemIncomplete
		item.Gaps = gaps(messages...)
	} else {
		item.Status = ItemAdopted
	}
	return item
}

func buildPermitsItem(runID string, snapshot model.ControlSnapshot) Item {
	records := make([]PermitRecord, 0)
	for _, permit := range snapshot.Permits {
		if permit.RunID != runID {
			continue
		}
		records = append(records, PermitRecord{
			Name:       permit.Name,
			Generation: permit.Generation,
			Granted:    permit.Granted,
			Reason:     permit.Reason,
			UpdatedAt:  permit.UpdatedAt,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Name != records[j].Name {
			return records[i].Name < records[j].Name
		}
		return records[i].Generation < records[j].Generation
	})

	item := Item{
		Category: "permits",
		Records:  records,
		Sources:  []SourceRef{{Kind: "control_snapshot", Detail: "control:permits filtered by run_id"}},
	}
	if len(records) == 0 {
		item.Status = ItemIncomplete
		item.Gaps = gaps("no repressurization permit recorded; passage grant may not have been performed")
		return item
	}
	messages := []string{}
	granted := false
	for _, record := range records {
		if record.Granted {
			granted = true
			continue
		}
		messages = append(messages, fmt.Sprintf("permit %s generation %d is not granted: %s", record.Name, record.Generation, record.Reason))
	}
	if !granted {
		messages = append(messages, "no granted permit recorded for this run")
	}
	if len(messages) > 0 {
		item.Status = ItemIncomplete
		item.Gaps = gaps(messages...)
	} else {
		item.Status = ItemAdopted
	}
	return item
}

func buildIncidentsItem(runID string, snapshot model.ControlSnapshot, events map[string][]model.Event) Item {
	records := make([]IncidentRecord, 0)
	for _, incident := range snapshot.Incidents {
		if incident.RunID != runID {
			continue
		}
		records = append(records, IncidentRecord{
			ID:       incident.ID,
			Severity: incident.Severity,
			Message:  incident.Message,
			At:       incident.At,
		})
	}
	sort.Slice(records, func(i, j int) bool {
		if !records[i].At.Equal(records[j].At) {
			return records[i].At.Before(records[j].At)
		}
		return records[i].ID < records[j].ID
	})

	sources := []SourceRef{{Kind: "control_snapshot", Detail: "control:incidents filtered by run_id"}}
	if incidentEvents := events["incident"]; len(incidentEvents) > 0 {
		sources = append(sources, sourceRef(incidentEvents[0], fmt.Sprintf("%d incident event(s) in journal", len(incidentEvents))))
	}
	item := Item{Category: "incidents", Records: records, Sources: sources}
	critical := 0
	for _, record := range records {
		if emergency.IsCritical(model.Incident{Severity: record.Severity}) {
			critical++
		}
	}
	if critical > 0 {
		item.Status = ItemIncomplete
		item.Gaps = gaps(fmt.Sprintf("%d critical/high severity incident(s) recorded", critical))
	} else {
		item.Status = ItemAdopted
	}
	return item
}

// markChanged 比较修订包与父包每个类别的规范化指纹，标记变化项。
func markChanged(pkg *Package, parent *Package) {
	parentItems := make(map[string]string, len(parent.Items))
	for _, item := range parent.Items {
		parentItems[item.Category] = itemFingerprint(item)
	}
	for i := range pkg.Items {
		parentFingerprint, exists := parentItems[pkg.Items[i].Category]
		pkg.Items[i].Changed = !exists || parentFingerprint != itemFingerprint(pkg.Items[i])
	}
}
