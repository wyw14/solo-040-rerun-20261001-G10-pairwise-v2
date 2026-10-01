// Package water accounts for the water used by each lock transit (闸次).
//
// The estimate is deliberately simple and auditable:
//
//	estimated volume = chamber plan area x |end level - start level|
//
// Valve ramp steps are kept as a trace of control opening positions only;
// they are never interpreted as flow, because opening is not a flow signal.
// When start or end level measurements are missing or invalid the transit is
// explicitly marked "not_accountable" rather than assigned a guessed volume.
package water

import (
	"context"
	"errors"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

const methodPlanAreaDelta = "plan_area_level_difference"

// CycleLookup provides an optional fast path to the live transit coordinator.
type CycleLookup interface {
	Current(chamberID string) (model.TransitCycle, bool)
}

// Filter narrows account queries by chamber and observation time window.
type Filter struct {
	ChamberID string
	From      time.Time
	To        time.Time
}

// Service owns water-accounting inputs and derives per-transit accounts.
type Service struct {
	mu       sync.RWMutex
	store    journal.Store
	recorder *journal.Recorder
	cycles   CycleLookup
	now      func() time.Time
}

func NewService(store journal.Store, recorder *journal.Recorder, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, recorder: recorder, now: now}
}

// BindCycleLookup attaches the live transit coordinator after wiring.
func (s *Service) BindCycleLookup(cycles CycleLookup) {
	s.mu.Lock()
	s.cycles = cycles
	s.mu.Unlock()
}

// ConfigureGeometry validates and journals chamber geometry parameters.
func (s *Service) ConfigureGeometry(ctx context.Context, input model.ChamberGeometry) (model.ChamberGeometry, error) {
	geometry, err := model.NewChamberGeometry(
		input.ChamberID, input.LengthM, input.WidthM,
		input.SillElevationM, input.DeckElevationM, s.now(),
	)
	if err != nil {
		return model.ChamberGeometry{}, err
	}
	if _, err := s.recorder.Record(ctx, geometry.ChamberID, model.EventGeometryConfigured, uuid.Nil, 0, geometry); err != nil {
		return model.ChamberGeometry{}, err
	}
	return geometry, nil
}

// Geometry returns the latest configured geometry for a chamber.
func (s *Service) Geometry(ctx context.Context, chamberID string) (model.ChamberGeometry, bool, error) {
	projection, err := s.projectChamber(ctx, chamberID)
	if err != nil {
		return model.ChamberGeometry{}, false, err
	}
	if projection.geometry == nil {
		return model.ChamberGeometry{}, false, nil
	}
	return *projection.geometry, true, nil
}

// Observe records a start/end level observation against a specific transit.
func (s *Service) Observe(ctx context.Context, chamberID string, cycleID uuid.UUID, generation uint64, boundary model.LevelBoundary, meters float64, observedAt time.Time) (model.WaterObservation, error) {
	if chamberID == "" {
		return model.WaterObservation{}, errors.New("chamber id is required")
	}
	cycleValue, err := s.resolveCycle(ctx, chamberID, cycleID, generation)
	if err != nil {
		return model.WaterObservation{}, err
	}
	observation, err := model.NewWaterObservation(cycleValue, boundary, meters, observedAt, s.now())
	if err != nil {
		return model.WaterObservation{}, err
	}
	if _, err := s.recorder.Record(ctx, chamberID, model.EventLevelObserved, cycleValue.ID, cycleValue.Generation, observation); err != nil {
		return model.WaterObservation{}, err
	}
	return observation, nil
}

func (s *Service) resolveCycle(ctx context.Context, chamberID string, cycleID uuid.UUID, generation uint64) (model.TransitCycle, error) {
	s.mu.RLock()
	cycles := s.cycles
	s.mu.RUnlock()
	if cycles != nil {
		if current, ok := cycles.Current(chamberID); ok && current.IdentityMatches(cycleID, generation) {
			return current, nil
		}
	}
	projection, err := s.projectChamber(ctx, chamberID)
	if err != nil {
		return model.TransitCycle{}, err
	}
	if cycleValue, ok := projection.cycles[cycleID]; ok && cycleValue.Generation == generation {
		return cycleValue, nil
	}
	return model.TransitCycle{}, errors.New("observation does not match a known transit for this chamber")
}

// Account derives the water account for one transit, replaying the journal.
func (s *Service) Account(ctx context.Context, chamberID string, cycleID uuid.UUID) (model.WaterAccount, error) {
	projection, err := s.projectChamber(ctx, chamberID)
	if err != nil {
		return model.WaterAccount{}, err
	}
	cycleValue, ok := projection.cycles[cycleID]
	if !ok {
		return model.WaterAccount{}, errors.New("transit not found for chamber")
	}
	return projection.accountFor(cycleValue), nil
}

// ListAccounts returns accounts matching the chamber/time filter, newest first.
func (s *Service) ListAccounts(ctx context.Context, filter Filter) ([]model.WaterAccount, error) {
	if filter.ChamberID == "" {
		return nil, errors.New("chamber id is required")
	}
	return s.collectAccounts(ctx, []string{filter.ChamberID}, filter)
}

// ListAllAccounts returns accounts across every known chamber within the time window.
func (s *Service) ListAllAccounts(ctx context.Context, from, to time.Time) ([]model.WaterAccount, error) {
	chambers, err := s.store.Chambers(ctx)
	if err != nil {
		return nil, err
	}
	return s.collectAccounts(ctx, chambers, Filter{From: from, To: to})
}

func (s *Service) collectAccounts(ctx context.Context, chamberIDs []string, filter Filter) ([]model.WaterAccount, error) {
	accounts := make([]model.WaterAccount, 0)
	for _, chamberID := range chamberIDs {
		projection, err := s.projectChamber(ctx, chamberID)
		if err != nil {
			return nil, err
		}
		cycles := make([]model.TransitCycle, 0, len(projection.cycles))
		for _, cycleValue := range projection.cycles {
			cycles = append(cycles, cycleValue)
		}
		sort.Slice(cycles, func(i, j int) bool { return cycles[i].CreatedAt.After(cycles[j].CreatedAt) })
		for _, cycleValue := range cycles {
			account := projection.accountFor(cycleValue)
			if withinWindow(anchorTime(account), filter.From, filter.To) {
				accounts = append(accounts, account)
			}
		}
	}
	sort.Slice(accounts, func(i, j int) bool { return anchorTime(accounts[i]).After(anchorTime(accounts[j])) })
	return accounts, nil
}

// anchorTime defines the time used for period filtering: last measurement,
// falling back to the start measurement and finally the transit creation time.
func anchorTime(account model.WaterAccount) time.Time {
	if account.EndObservedAt != nil {
		return *account.EndObservedAt
	}
	if account.StartObservedAt != nil {
		return *account.StartObservedAt
	}
	return account.CreatedAt
}

func withinWindow(stamp, from, to time.Time) bool {
	if !from.IsZero() && stamp.Before(from) {
		return false
	}
	if !to.IsZero() && stamp.After(to) {
		return false
	}
	return true
}

type chamberProjection struct {
	geometry     *model.ChamberGeometry
	geometryRef  geometryRef
	cycles       map[uuid.UUID]model.TransitCycle
	observations map[uuid.UUID]boundaryPair
	valves       map[uuid.UUID][]model.ValveTraceStep
}

type geometryRef struct {
	eventID  uuid.UUID
	sequence uint64
}

type boundaryPair struct {
	start *observedPoint
	end   *observedPoint
}

type observedPoint struct {
	observation model.WaterObservation
	sequence    uint64
}

func (s *Service) projectChamber(ctx context.Context, chamberID string) (chamberProjection, error) {
	events, err := s.store.Events(ctx, chamberID, 0)
	if err != nil {
		return chamberProjection{}, err
	}
	result := chamberProjection{
		cycles:       make(map[uuid.UUID]model.TransitCycle),
		observations: make(map[uuid.UUID]boundaryPair),
		valves:       make(map[uuid.UUID][]model.ValveTraceStep),
	}
	for _, event := range events {
		switch event.Type {
		case model.EventGeometryConfigured:
			var geometry model.ChamberGeometry
			if err := event.Decode(&geometry); err != nil {
				return chamberProjection{}, err
			}
			configured := geometry
			result.geometry = &configured
			result.geometryRef = geometryRef{eventID: event.ID, sequence: event.Sequence}
		case model.EventCycleCreated, model.EventCycleAdvanced:
			var cycleValue model.TransitCycle
			if err := event.Decode(&cycleValue); err != nil {
				return chamberProjection{}, err
			}
			if cycleValue.ChamberID == chamberID {
				result.cycles[cycleValue.ID] = cycleValue
			}
		case model.EventLevelObserved:
			var observation model.WaterObservation
			if err := event.Decode(&observation); err != nil {
				return chamberProjection{}, err
			}
			if observation.ChamberID != chamberID {
				continue
			}
			pair := result.observations[observation.CycleID]
			point := observedPoint{observation: observation, sequence: event.Sequence}
			switch observation.Boundary {
			case model.LevelStart:
				pair.start = &point
			case model.LevelEnd:
				pair.end = &point
			}
			result.observations[observation.CycleID] = pair
		case model.EventCommandQueued:
			var command model.DeviceCommand
			if err := event.Decode(&command); err != nil {
				return chamberProjection{}, err
			}
			if command.ChamberID != chamberID || command.Action != "set_opening" {
				continue
			}
			if command.Device != model.DeviceFillValve && command.Device != model.DeviceDrainValve {
				continue
			}
			result.valves[command.CycleID] = append(result.valves[command.CycleID], model.ValveTraceStep{
				Device: command.Device, Opening: command.Target,
				CommandID: command.ID, IssuedAt: command.CreatedAt,
			})
		}
	}
	return result, nil
}

func (p chamberProjection) accountFor(cycleValue model.TransitCycle) model.WaterAccount {
	account := model.WaterAccount{
		ChamberID:  cycleValue.ChamberID,
		CycleID:    cycleValue.ID,
		Generation: cycleValue.Generation,
		Direction:  cycleValue.Direction,
		Stage:      cycleValue.Stage,
		Status:     model.WaterNotAccountable,
		CreatedAt:  cycleValue.CreatedAt,
		Quality: model.WaterQuality{
			Method:     methodPlanAreaDelta,
			Confidence: "none",
			Notes:      []string{},
			Trace:      []model.SampleRef{},
		},
	}
	pair := p.observations[cycleValue.ID]
	if pair.start != nil {
		start := pair.start.observation.Meters
		account.StartLevelM = &start
		startAt := pair.start.observation.ObservedAt
		account.StartObservedAt = &startAt
		account.Quality.Trace = append(account.Quality.Trace, model.SampleRef{
			Kind: "level.start", ID: pair.start.observation.ID,
			Sequence: pair.start.sequence, ObservedAt: pair.start.observation.ObservedAt,
		})
	}
	if pair.end != nil {
		end := pair.end.observation.Meters
		account.EndLevelM = &end
		endAt := pair.end.observation.ObservedAt
		account.EndObservedAt = &endAt
		account.Quality.Trace = append(account.Quality.Trace, model.SampleRef{
			Kind: "level.end", ID: pair.end.observation.ID,
			Sequence: pair.end.sequence, ObservedAt: pair.end.observation.ObservedAt,
		})
	}

	trace := p.valves[cycleValue.ID]
	account.ValveTrace = trace

	missing := p.geometry == nil || pair.start == nil || pair.end == nil
	if p.geometry == nil {
		account.Quality.Notes = append(account.Quality.Notes,
			"chamber geometry is not configured; plan area unknown")
	}
	if pair.start == nil {
		account.Quality.Notes = append(account.Quality.Notes,
			"start level observation is missing; transit is marked not accountable")
	}
	if pair.end == nil {
		account.Quality.Notes = append(account.Quality.Notes,
			"end level observation is missing; transit is marked not accountable")
	}
	if missing {
		account.Quality.Notes = append(account.Quality.Notes,
			"valve ramp openings are control positions, not flow measurements, and cannot substitute for missing levels")
		return account
	}

	geometry := *p.geometry
	account.Quality.Trace = append(account.Quality.Trace, model.SampleRef{
		Kind: "geometry.configured", ID: p.geometryRef.eventID,
		Sequence: p.geometryRef.sequence, ObservedAt: geometry.UpdatedAt,
	})

	startMeters := pair.start.observation.Meters
	endMeters := pair.end.observation.Meters
	change := endMeters - startMeters
	notes := []string{
		"estimate = chamber plan area x observed level difference",
		"valve ramp openings are control positions, not flow measurements, and were not converted to flow",
	}

	switch {
	case !geometry.ContainsLevel(startMeters):
		account.Quality.Notes = append(notes,
			"start level lies outside the configured sill/deck envelope; transit is marked not accountable")
		return account
	case !geometry.ContainsLevel(endMeters):
		account.Quality.Notes = append(notes,
			"end level lies outside the configured sill/deck envelope; transit is marked not accountable")
		return account
	case pair.end.observation.ObservedAt.Before(pair.start.observation.ObservedAt):
		account.Quality.Notes = append(notes,
			"end observation predates start observation; transit is marked not accountable")
		return account
	}

	account.Status = model.WaterAccountable
	account.Fill = change >= 0
	account.LevelChangeM = floatPtr(roundTo(change, 4))
	account.EffectiveAreaM2 = floatPtr(roundTo(geometry.PlanAreaM2, 3))
	account.EstimatedVolumeM3 = floatPtr(roundTo(geometry.PlanAreaM2*math.Abs(change), 3))

	confidence := "high"
	if len(trace) == 0 {
		confidence = "medium"
		notes = append(notes, "no valve ramp steps recorded for the transit; estimate relies on level observations alone")
	} else if directionMismatch(change, trace) {
		confidence = "low"
		notes = append(notes, "valve ramp direction disagrees with the observed level change; verify observations")
	}
	if cycleValue.Direction == model.DirectionUpstream && change < 0 {
		confidence = "low"
		notes = append(notes, "upstream transit shows a falling chamber level; verify observations")
	}
	if cycleValue.Direction == model.DirectionDownstream && change > 0 {
		confidence = "low"
		notes = append(notes, "downstream transit shows a rising chamber level; verify observations")
	}
	if math.Abs(change) < 1e-9 {
		if confidence == "high" {
			confidence = "medium"
		}
		notes = append(notes, "start and end levels are equal; estimated water use is zero")
	}
	if pair.end.observation.ObservedAt.Equal(pair.start.observation.ObservedAt) {
		if confidence == "high" {
			confidence = "medium"
		}
		notes = append(notes, "start and end observations share a timestamp; leveling duration is unknown")
	}

	account.Quality.Confidence = confidence
	account.Quality.Notes = notes
	for _, step := range trace {
		account.Quality.Trace = append(account.Quality.Trace, model.SampleRef{
			Kind: "valve.command", ID: step.CommandID, ObservedAt: step.IssuedAt,
		})
	}
	return account
}

func directionMismatch(change float64, trace []model.ValveTraceStep) bool {
	fillSteps, drainSteps := 0, 0
	for _, step := range trace {
		switch step.Device {
		case model.DeviceFillValve:
			fillSteps++
		case model.DeviceDrainValve:
			drainSteps++
		}
	}
	if fillSteps > drainSteps && change < 0 {
		return true
	}
	if drainSteps > fillSteps && change > 0 {
		return true
	}
	return false
}

func floatPtr(value float64) *float64 { return &value }

func roundTo(value float64, decimals int) float64 {
	scale := math.Pow(10, float64(decimals))
	return math.Round(value*scale) / scale
}
