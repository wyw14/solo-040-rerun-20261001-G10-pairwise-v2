package water

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestService(t *testing.T) (*Service, *journal.Recorder, *clock) {
	t.Helper()
	store := journal.NewMemoryStore()
	c := &clock{t: time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)}
	recorder := journal.NewRecorder(store, c.now)
	return NewService(store, recorder, c.now), recorder, c
}

// recordCycle emits a cycle.created event so the projection knows the transit.
func recordCycle(t *testing.T, ctx context.Context, recorder *journal.Recorder, chamberID string, direction model.Direction, at time.Time) model.TransitCycle {
	t.Helper()
	cycleValue, err := model.NewTransitCycle(chamberID, uuid.New(), direction, 1, at)
	if err != nil {
		t.Fatalf("new cycle: %v", err)
	}
	if _, err := recorder.Record(ctx, chamberID, model.EventCycleCreated, cycleValue.ID, cycleValue.Generation, cycleValue); err != nil {
		t.Fatalf("record cycle: %v", err)
	}
	return cycleValue
}

func configure(t *testing.T, ctx context.Context, service *Service, chamberID string) {
	t.Helper()
	geometry := model.ChamberGeometry{
		ChamberID: chamberID, LengthM: 200, WidthM: 24,
		SillElevationM: 0, DeckElevationM: 15,
	}
	if _, err := service.ConfigureGeometry(ctx, geometry); err != nil {
		t.Fatalf("configure geometry: %v", err)
	}
}

func observeBoundary(t *testing.T, ctx context.Context, service *Service, cycleValue model.TransitCycle, boundary model.LevelBoundary, meters float64, at time.Time) {
	t.Helper()
	if _, err := service.Observe(ctx, cycleValue.ChamberID, cycleValue.ID, cycleValue.Generation, boundary, meters, at); err != nil {
		t.Fatalf("observe %s: %v", boundary, err)
	}
}

func recordValveStep(t *testing.T, ctx context.Context, recorder *journal.Recorder, cycleValue model.TransitCycle, device model.DeviceKind, target float64, epoch uint64, at time.Time) {
	t.Helper()
	command, err := model.NewDeviceCommand(cycleValue, epoch, device, "set_opening", target, at)
	if err != nil {
		t.Fatalf("new command: %v", err)
	}
	if _, err := recorder.Record(ctx, cycleValue.ChamberID, model.EventCommandQueued, cycleValue.ID, cycleValue.Generation, command); err != nil {
		t.Fatalf("record command: %v", err)
	}
}

func TestAccountableFillTransitVolume(t *testing.T) {
	ctx := context.Background()
	service, recorder, clk := newTestService(t)
	configure(t, ctx, service, "chamber-1")
	cycleValue := recordCycle(t, ctx, recorder, "chamber-1", model.DirectionUpstream, clk.now())

	t0 := clk.now()
	recordValveStep(t, ctx, recorder, cycleValue, model.DeviceFillValve, 0.4, 1, t0.Add(time.Minute))
	recordValveStep(t, ctx, recorder, cycleValue, model.DeviceFillValve, 0.8, 2, t0.Add(2*time.Minute))

	observeBoundary(t, ctx, service, cycleValue, model.LevelStart, 4.0, t0.Add(30*time.Second))
	observeBoundary(t, ctx, service, cycleValue, model.LevelEnd, 7.0, t0.Add(10*time.Minute))

	account, err := service.Account(ctx, "chamber-1", cycleValue.ID)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.Status != model.WaterAccountable {
		t.Fatalf("status = %s, want accountable; notes=%v", account.Status, account.Quality.Notes)
	}
	if !account.Fill {
		t.Error("rising level should be a fill")
	}
	if account.EffectiveAreaM2 == nil || math.Abs(*account.EffectiveAreaM2-4800) > 1e-6 {
		t.Errorf("area = %v, want 4800", account.EffectiveAreaM2)
	}
	if account.LevelChangeM == nil || math.Abs(*account.LevelChangeM-3.0) > 1e-6 {
		t.Errorf("level change = %v, want 3.0", account.LevelChangeM)
	}
	if account.EstimatedVolumeM3 == nil || math.Abs(*account.EstimatedVolumeM3-14400) > 1e-3 {
		t.Errorf("volume = %v, want 14400", account.EstimatedVolumeM3)
	}
	if account.Quality.Confidence != "high" {
		t.Errorf("confidence = %q, want high", account.Quality.Confidence)
	}
	if account.Quality.Method != methodPlanAreaDelta {
		t.Errorf("method = %q", account.Quality.Method)
	}
	if len(account.ValveTrace) != 2 {
		t.Fatalf("valve trace length = %d, want 2", len(account.ValveTrace))
	}
	for _, step := range account.ValveTrace {
		if step.Device != model.DeviceFillValve {
			t.Errorf("trace device = %s", step.Device)
		}
	}
	// Every account must be traceable to the transit and its samples.
	kinds := map[string]bool{}
	for _, ref := range account.Quality.Trace {
		kinds[ref.Kind] = true
		if ref.ID == uuid.Nil {
			t.Errorf("trace ref %s has nil id", ref.Kind)
		}
	}
	for _, want := range []string{"level.start", "level.end", "geometry.configured", "valve.command"} {
		if !kinds[want] {
			t.Errorf("trace missing %s, got %v", want, kinds)
		}
	}
}

func TestMissingEndObservationIsNotAccountable(t *testing.T) {
	ctx := context.Background()
	service, recorder, clk := newTestService(t)
	configure(t, ctx, service, "chamber-1")
	cycleValue := recordCycle(t, ctx, recorder, "chamber-1", model.DirectionUpstream, clk.now())
	t0 := clk.now()
	recordValveStep(t, ctx, recorder, cycleValue, model.DeviceFillValve, 0.6, 1, t0.Add(time.Minute))
	observeBoundary(t, ctx, service, cycleValue, model.LevelStart, 4.0, t0)

	account, err := service.Account(ctx, "chamber-1", cycleValue.ID)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.Status != model.WaterNotAccountable {
		t.Fatalf("status = %s, want not_accountable", account.Status)
	}
	if account.EstimatedVolumeM3 != nil {
		t.Errorf("volume must be absent without an end level, got %v", *account.EstimatedVolumeM3)
	}
	if account.EndLevelM != nil {
		t.Errorf("end level must be absent, got %v", *account.EndLevelM)
	}
	if !containsNote(account, "end level observation is missing") {
		t.Errorf("notes should explain missing end level: %v", account.Quality.Notes)
	}
	if !containsNote(account, "not flow measurements") {
		t.Errorf("notes must state openings are not flow: %v", account.Quality.Notes)
	}
	if len(account.ValveTrace) != 1 {
		t.Errorf("valve trace retained for audit = %d steps", len(account.ValveTrace))
	}
}

func TestMissingBothObservationsIsNotAccountable(t *testing.T) {
	ctx := context.Background()
	service, recorder, clk := newTestService(t)
	configure(t, ctx, service, "chamber-1")
	cycleValue := recordCycle(t, ctx, recorder, "chamber-1", model.DirectionUpstream, clk.now())

	account, err := service.Account(ctx, "chamber-1", cycleValue.ID)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.Status != model.WaterNotAccountable {
		t.Fatalf("status = %s", account.Status)
	}
	if !containsNote(account, "start level observation is missing") || !containsNote(account, "end level observation is missing") {
		t.Errorf("notes should name both missing levels: %v", account.Quality.Notes)
	}
	if account.EstimatedVolumeM3 != nil || account.LevelChangeM != nil {
		t.Error("no numeric estimate may be produced")
	}
}

func TestMissingGeometryIsNotAccountable(t *testing.T) {
	ctx := context.Background()
	service, recorder, clk := newTestService(t)
	cycleValue := recordCycle(t, ctx, recorder, "chamber-1", model.DirectionUpstream, clk.now())
	t0 := clk.now()
	observeBoundary(t, ctx, service, cycleValue, model.LevelStart, 4.0, t0)
	observeBoundary(t, ctx, service, cycleValue, model.LevelEnd, 7.0, t0.Add(5*time.Minute))

	account, err := service.Account(ctx, "chamber-1", cycleValue.ID)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.Status != model.WaterNotAccountable {
		t.Fatalf("status = %s", account.Status)
	}
	if !containsNote(account, "geometry is not configured") {
		t.Errorf("notes should explain missing geometry: %v", account.Quality.Notes)
	}
	if account.EffectiveAreaM2 != nil {
		t.Errorf("area must be unknown, got %v", *account.EffectiveAreaM2)
	}
}

func TestLevelOutsideEnvelopeIsNotAccountable(t *testing.T) {
	ctx := context.Background()
	service, recorder, clk := newTestService(t)
	configure(t, ctx, service, "chamber-1")
	cycleValue := recordCycle(t, ctx, recorder, "chamber-1", model.DirectionUpstream, clk.now())
	t0 := clk.now()
	observeBoundary(t, ctx, service, cycleValue, model.LevelStart, 4.0, t0)
	observeBoundary(t, ctx, service, cycleValue, model.LevelEnd, 18.0, t0.Add(5*time.Minute)) // above deck 15

	account, err := service.Account(ctx, "chamber-1", cycleValue.ID)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.Status != model.WaterNotAccountable {
		t.Fatalf("status = %s", account.Status)
	}
	if !containsNote(account, "outside the configured sill/deck envelope") {
		t.Errorf("notes should flag envelope breach: %v", account.Quality.Notes)
	}
	if account.EstimatedVolumeM3 != nil {
		t.Errorf("no volume for out-of-envelope level, got %v", *account.EstimatedVolumeM3)
	}
}

func TestEndBeforeStartIsNotAccountable(t *testing.T) {
	ctx := context.Background()
	service, recorder, clk := newTestService(t)
	configure(t, ctx, service, "chamber-1")
	cycleValue := recordCycle(t, ctx, recorder, "chamber-1", model.DirectionUpstream, clk.now())
	t0 := clk.now()
	observeBoundary(t, ctx, service, cycleValue, model.LevelStart, 7.0, t0.Add(10*time.Minute))
	observeBoundary(t, ctx, service, cycleValue, model.LevelEnd, 4.0, t0)

	account, err := service.Account(ctx, "chamber-1", cycleValue.ID)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.Status != model.WaterNotAccountable || !containsNote(account, "predates start observation") {
		t.Fatalf("expected reordered-observation rejection, status=%s notes=%v", account.Status, account.Quality.Notes)
	}
}

func TestValveDirectionDisagreementLowersConfidence(t *testing.T) {
	ctx := context.Background()
	service, recorder, clk := newTestService(t)
	configure(t, ctx, service, "chamber-1")
	cycleValue := recordCycle(t, ctx, recorder, "chamber-1", model.DirectionUpstream, clk.now())
	t0 := clk.now()
	// Drain valves operated while the observed level rose: contradictory evidence.
	recordValveStep(t, ctx, recorder, cycleValue, model.DeviceDrainValve, 0.5, 1, t0.Add(time.Minute))
	observeBoundary(t, ctx, service, cycleValue, model.LevelStart, 4.0, t0)
	observeBoundary(t, ctx, service, cycleValue, model.LevelEnd, 7.0, t0.Add(5*time.Minute))

	account, err := service.Account(ctx, "chamber-1", cycleValue.ID)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.Status != model.WaterAccountable {
		t.Fatalf("status = %s", account.Status)
	}
	if account.Quality.Confidence != "low" {
		t.Errorf("confidence = %q, want low", account.Quality.Confidence)
	}
	if !containsNote(account, "valve ramp direction disagrees") {
		t.Errorf("notes should flag disagreement: %v", account.Quality.Notes)
	}
}

func TestNoValveTraceStillAccountableFromLevels(t *testing.T) {
	ctx := context.Background()
	service, recorder, clk := newTestService(t)
	configure(t, ctx, service, "chamber-1")
	cycleValue := recordCycle(t, ctx, recorder, "chamber-1", model.DirectionUpstream, clk.now())
	t0 := clk.now()
	observeBoundary(t, ctx, service, cycleValue, model.LevelStart, 4.0, t0)
	observeBoundary(t, ctx, service, cycleValue, model.LevelEnd, 7.0, t0.Add(5*time.Minute))

	account, err := service.Account(ctx, "chamber-1", cycleValue.ID)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.Status != model.WaterAccountable {
		t.Fatalf("levels alone must still be accountable, got %s", account.Status)
	}
	if account.Quality.Confidence != "medium" {
		t.Errorf("confidence = %q, want medium without valve trace", account.Quality.Confidence)
	}
	if len(account.ValveTrace) != 0 {
		t.Errorf("expected empty valve trace")
	}
}

func TestOpeningsAreNeverMultipliedAsFlow(t *testing.T) {
	ctx := context.Background()
	service, recorder, clk := newTestService(t)
	configure(t, ctx, service, "chamber-1")
	cycleValue := recordCycle(t, ctx, recorder, "chamber-1", model.DirectionUpstream, clk.now())
	t0 := clk.now()
	recordValveStep(t, ctx, recorder, cycleValue, model.DeviceFillValve, 0.9, 1, t0.Add(time.Minute))
	observeBoundary(t, ctx, service, cycleValue, model.LevelStart, 4.0, t0)
	observeBoundary(t, ctx, service, cycleValue, model.LevelEnd, 7.0, t0.Add(5*time.Minute))

	account, err := service.Account(ctx, "chamber-1", cycleValue.ID)
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	// Volume derives from geometry x level delta only; the 0.9 opening must not appear in it.
	want := 4800.0 * 3.0
	if math.Abs(*account.EstimatedVolumeM3-want) > 1e-6 {
		t.Errorf("volume = %v, want %v (opening must not influence volume)", *account.EstimatedVolumeM3, want)
	}
}

func TestListAccountsByChamberAndPeriod(t *testing.T) {
	ctx := context.Background()
	service, recorder, clk := newTestService(t)
	configure(t, ctx, service, "chamber-1")
	configure(t, ctx, service, "chamber-2")
	cycle1 := recordCycle(t, ctx, recorder, "chamber-1", model.DirectionUpstream, clk.now())
	observeBoundary(t, ctx, service, cycle1, model.LevelStart, 4.0, clk.now())
	observeBoundary(t, ctx, service, cycle1, model.LevelEnd, 6.0, clk.now().Add(5*time.Minute))

	clk.t = clk.t.Add(2 * time.Hour)
	cycle2 := recordCycle(t, ctx, recorder, "chamber-2", model.DirectionUpstream, clk.now())
	observeBoundary(t, ctx, service, cycle2, model.LevelStart, 4.0, clk.now())
	observeBoundary(t, ctx, service, cycle2, model.LevelEnd, 5.0, clk.now().Add(5*time.Minute))

	// Chamber-scoped query.
	accounts, err := service.ListAccounts(ctx, Filter{ChamberID: "chamber-1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(accounts) != 1 || accounts[0].CycleID != cycle1.ID {
		t.Fatalf("chamber filter returned %d accounts", len(accounts))
	}

	// Period window that only covers the first transit's end observation.
	windowEnd := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	accounts, err = service.ListAllAccounts(ctx, time.Time{}, windowEnd)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ChamberID != "chamber-1" {
		t.Fatalf("period filter returned %d accounts: %+v", len(accounts), accounts)
	}

	// Unbounded query covers both chambers.
	accounts, err = service.ListAllAccounts(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(accounts))
	}
	if !accounts[0].CreatedAt.After(accounts[1].CreatedAt) && !anchorTime(accounts[0]).After(anchorTime(accounts[1])) {
		t.Error("accounts should be newest first")
	}
}

func TestInvalidGeometryRejected(t *testing.T) {
	ctx := context.Background()
	service, _, _ := newTestService(t)
	cases := []model.ChamberGeometry{
		{ChamberID: "chamber-1", LengthM: 0, WidthM: 24, SillElevationM: 0, DeckElevationM: 15},
		{ChamberID: "chamber-1", LengthM: 200, WidthM: -1, SillElevationM: 0, DeckElevationM: 15},
		{ChamberID: "chamber-1", LengthM: 200, WidthM: 24, SillElevationM: 10, DeckElevationM: 10},
		{ChamberID: "chamber-1", LengthM: 200, WidthM: 24, SillElevationM: -2, DeckElevationM: 15},
	}
	for i, geometry := range cases {
		if _, err := service.ConfigureGeometry(ctx, geometry); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestObserveRejectsUnknownTransit(t *testing.T) {
	ctx := context.Background()
	service, _, clk := newTestService(t)
	randomCycle := model.TransitCycle{ID: uuid.New(), ChamberID: "chamber-1", Generation: 9, Direction: model.DirectionUpstream}
	if _, err := service.Observe(ctx, "chamber-1", randomCycle.ID, 9, model.LevelStart, 4.0, clk.now()); err == nil {
		t.Fatal("observation for an unknown transit must be rejected")
	}
}

func containsNote(account model.WaterAccount, fragment string) bool {
	for _, note := range account.Quality.Notes {
		if strings.Contains(note, fragment) {
			return true
		}
	}
	return false
}
