package water

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

func testCycle(t *testing.T, now time.Time) model.TransitCycle {
	t.Helper()
	cycle, err := model.NewTransitCycle("chamber-1", uuid.New(), model.DirectionUpstream, 1, now)
	if err != nil {
		t.Fatalf("new cycle: %v", err)
	}
	return cycle
}

func observation(t *testing.T, cycle model.TransitCycle, phase string, meters *float64, at time.Time) model.WaterObservation {
	t.Helper()
	obs, err := model.NewWaterObservation(cycle.ChamberID, phase, cycle.ID, cycle.Generation, meters, at, at)
	if err != nil {
		t.Fatalf("new observation: %v", err)
	}
	return obs
}

func ptr(v float64) *float64 { return &v }

func TestEstimateUsesLevelChangeNotValveOpening(t *testing.T) {
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	cycle := testCycle(t, now)
	geometry, err := model.NewChamberGeometry(cycle.ChamberID, 100, 10, 0, now)
	if err != nil {
		t.Fatalf("geometry: %v", err)
	}
	actions := []model.ValveAction{
		{Device: model.DeviceFillValve, Target: 0.25, Action: "set_opening", IssuedAt: now.Add(time.Minute)},
		{Device: model.DeviceFillValve, Target: 0.75, Action: "set_opening", IssuedAt: now.Add(2 * time.Minute)},
	}
	account := Estimate(model.AccountInput{
		Geometry: &geometry,
		Start:    ptrObs(observation(t, cycle, model.ObservationPhaseStart, ptr(4.0), now)),
		End:      ptrObs(observation(t, cycle, model.ObservationPhaseEnd, ptr(6.5), now.Add(30*time.Minute))),
		Cycle:    &cycle,
		Actions:  actions,
	}, now.Add(time.Hour))

	if account.Status != model.AccountEstimated {
		t.Fatalf("status = %s, want estimated", account.Status)
	}
	// 100m x 10m x 2.5m = 2500 m3 regardless of the 0.25/0.75 valve openings.
	if account.LevelChangeM != 2.5 {
		t.Fatalf("level change = %v, want 2.5", account.LevelChangeM)
	}
	if account.EstimatedVolumeM3 != 2500 {
		t.Fatalf("volume = %v, want 2500 (must not derive from valve opening)", account.EstimatedVolumeM3)
	}
	if account.NetVolumeM3 != 2500 {
		t.Fatalf("net volume = %v, want 2500", account.NetVolumeM3)
	}
	if len(account.ValveActions) != 2 {
		t.Fatalf("valve actions = %d, want 2 attached for traceability", len(account.ValveActions))
	}
	if !hasNote(account, "valve_actions_excluded") {
		t.Fatalf("expected valve_actions_excluded quality note, got %v", account.QualityNotes)
	}
}

func TestEstimateDrainHasNegativeNetVolume(t *testing.T) {
	now := time.Now().UTC()
	cycle := testCycle(t, now)
	geometry, _ := model.NewChamberGeometry(cycle.ChamberID, 100, 10, 2, now)
	account := Estimate(model.AccountInput{
		Geometry: &geometry,
		Start:    ptrObs(observation(t, cycle, model.ObservationPhaseStart, ptr(6.0), now)),
		End:      ptrObs(observation(t, cycle, model.ObservationPhaseEnd, ptr(3.0), now.Add(time.Minute))),
		Cycle:    &cycle,
	}, now.Add(2*time.Minute))
	if account.EstimatedVolumeM3 != 3000 || account.NetVolumeM3 != -3000 {
		t.Fatalf("got gross=%v net=%v, want 3000 / -3000", account.EstimatedVolumeM3, account.NetVolumeM3)
	}
}

func TestMissingObservationsAreNotAccountable(t *testing.T) {
	now := time.Now().UTC()
	cycle := testCycle(t, now)
	geometry, _ := model.NewChamberGeometry(cycle.ChamberID, 100, 10, 0, now)

	cases := []struct {
		name    string
		start   *model.WaterObservation
		end     *model.WaterObservation
		missing string
	}{
		{"missing start", nil, ptrObs(observation(t, cycle, model.ObservationPhaseEnd, ptr(6.0), now)), "start"},
		{"missing end", ptrObs(observation(t, cycle, model.ObservationPhaseStart, ptr(4.0), now)), nil, "end"},
		{"explicit absent reading", ptrObs(observation(t, cycle, model.ObservationPhaseStart, ptr(4.0), now)),
			ptrObs(observation(t, cycle, model.ObservationPhaseEnd, nil, now)), "end"},
		{"missing geometry too", nil, nil, "start"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := model.AccountInput{Start: tc.start, End: tc.end, Cycle: &cycle}
			if tc.name != "missing geometry too" {
				input.Geometry = &geometry
			}
			account := Estimate(input, now.Add(time.Minute))
			if account.Status != model.AccountNotAccountable {
				t.Fatalf("status = %s, want not_accountable", account.Status)
			}
			if account.EstimatedVolumeM3 != 0 || account.NetVolumeM3 != 0 {
				t.Fatalf("missing measurements must not yield a volume, got %v/%v", account.EstimatedVolumeM3, account.NetVolumeM3)
			}
			if !contains(account.MissingPhases, tc.missing) {
				t.Fatalf("missing phases %v, want %q", account.MissingPhases, tc.missing)
			}
		})
	}
}

func TestLedgerEndToEndAndReplay(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	store := journal.NewMemoryStore()
	recorder := journal.NewRecorder(store, now)
	ledger := NewLedger(recorder, store, now)

	cycle := testCycle(t, clock)
	if _, err := recorder.Record(ctx, cycle.ChamberID, model.EventCycleCreated, cycle.ID, cycle.Generation, cycle); err != nil {
		t.Fatalf("record cycle: %v", err)
	}
	// A valve ramp command: opening is an action, not a flow measurement.
	command, err := model.NewDeviceCommand(cycle, 1, model.DeviceFillValve, "set_opening", 0.5, clock)
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if _, err := recorder.Record(ctx, cycle.ChamberID, model.EventCommandQueued, cycle.ID, cycle.Generation, command); err != nil {
		t.Fatalf("record command: %v", err)
	}

	geometry, err := ledger.ConfigureGeometry(ctx, cycle.ChamberID, 120, 12, 1)
	if err != nil {
		t.Fatalf("configure geometry: %v", err)
	}
	if geometry.SourceEventSeq == 0 {
		t.Fatal("geometry should carry its journal event sequence")
	}
	startObs, err := ledger.RecordObservation(ctx, cycle.ChamberID, cycle.ID, cycle.Generation, model.ObservationPhaseStart, ptr(4.0), clock.Add(time.Minute))
	if err != nil {
		t.Fatalf("record start: %v", err)
	}
	endObs, err := ledger.RecordObservation(ctx, cycle.ChamberID, cycle.ID, cycle.Generation, model.ObservationPhaseEnd, ptr(7.0), clock.Add(20*time.Minute))
	if err != nil {
		t.Fatalf("record end: %v", err)
	}

	account, err := ledger.AccountCycle(ctx, cycle.ChamberID, cycle.ID, cycle.Generation)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.Status != model.AccountEstimated || account.EstimatedVolumeM3 != 120*12*3 {
		t.Fatalf("account = status %s volume %v", account.Status, account.EstimatedVolumeM3)
	}
	if account.StartEventSeq != startObs.SourceEventSeq || account.EndEventSeq != endObs.SourceEventSeq {
		t.Fatalf("account must trace back to sample events, got start=%d end=%d", account.StartEventSeq, account.EndEventSeq)
	}
	if account.GeometryEventSeq != geometry.SourceEventSeq {
		t.Fatalf("geometry provenance = %d, want %d", account.GeometryEventSeq, geometry.SourceEventSeq)
	}
	if len(account.ValveActions) != 1 || account.ValveActions[0].CommandID != command.ID {
		t.Fatalf("valve action must trace to ramp command, got %+v", account.ValveActions)
	}

	// Chamber + time-window query.
	all := ledger.Accounts(AccountFilter{ChamberID: cycle.ChamberID})
	if len(all) != 1 {
		t.Fatalf("accounts = %d, want 1", len(all))
	}
	windowed := ledger.Accounts(AccountFilter{
		ChamberID: cycle.ChamberID,
		From:      timePtr(clock.Add(time.Hour)),
	})
	if len(windowed) != 0 {
		t.Fatalf("future-bounded query should be empty, got %d", len(windowed))
	}
	otherChamber := ledger.Accounts(AccountFilter{ChamberID: "chamber-2"})
	if len(otherChamber) != 0 {
		t.Fatalf("other chamber should be isolated, got %d", len(otherChamber))
	}

	// Rebuild a fresh ledger from the journal: geometry, observations and the
	// accounted result must all be recoverable.
	rebuilt := NewLedger(recorder, store, now)
	if err := rebuilt.RestoreAll(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored, ok := rebuilt.Geometry(cycle.ChamberID); !ok || restored.PlanAreaM2() != 1440 {
		t.Fatalf("geometry not restored: %+v ok=%v", restored, ok)
	}
	reAccounts := rebuilt.Accounts(AccountFilter{ChamberID: cycle.ChamberID})
	if len(reAccounts) != 1 || reAccounts[0].EstimatedVolumeM3 != 120*12*3 {
		t.Fatalf("accounts not restored: %+v", reAccounts)
	}
}

func TestLedgerRecomputeAfterMissingMarkedNotAccountable(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	store := journal.NewMemoryStore()
	recorder := journal.NewRecorder(store, func() time.Time { return clock })
	ledger := NewLedger(recorder, store, func() time.Time { return clock })
	cycle := testCycle(t, clock)
	if _, err := recorder.Record(ctx, cycle.ChamberID, model.EventCycleCreated, cycle.ID, cycle.Generation, cycle); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ConfigureGeometry(ctx, cycle.ChamberID, 100, 10, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.RecordObservation(ctx, cycle.ChamberID, cycle.ID, cycle.Generation, model.ObservationPhaseStart, ptr(4.0), clock); err != nil {
		t.Fatal(err)
	}
	// End measurement explicitly absent.
	if _, err := ledger.RecordObservation(ctx, cycle.ChamberID, cycle.ID, cycle.Generation, model.ObservationPhaseEnd, nil, clock); err != nil {
		t.Fatal(err)
	}
	account, err := ledger.AccountCycle(ctx, cycle.ChamberID, cycle.ID, cycle.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if account.Status != model.AccountNotAccountable {
		t.Fatalf("status = %s, want not_accountable", account.Status)
	}
	if account.EstimatedVolumeM3 != 0 {
		t.Fatalf("not-accountable transit must report zero volume, got %v", account.EstimatedVolumeM3)
	}
}

func ptrObs(obs model.WaterObservation) *model.WaterObservation { return &obs }

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func hasNote(account model.WaterAccount, code string) bool {
	for _, note := range account.QualityNotes {
		if note.Code == code {
			return true
		}
	}
	return false
}

func timePtr(t time.Time) *time.Time { return &t }
