package water

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

// Ledger owns chamber geometry, start/end level observations and the resulting
// per-transit water accounts. Everything authoritative is appended to the
// journal first; in-memory maps are read models that can be rebuilt by replay.
type Ledger struct {
	mu           sync.RWMutex
	store        journal.Store
	recorder     *journal.Recorder
	now          func() time.Time
	geometry     map[string]model.ChamberGeometry
	observations map[string]map[cycleKey]map[string]model.WaterObservation
	accounts     map[string][]model.WaterAccount
}

type cycleKey struct {
	id         uuid.UUID
	generation uint64
}

func NewLedger(recorder *journal.Recorder, store journal.Store, now func() time.Time) *Ledger {
	if now == nil {
		now = time.Now
	}
	return &Ledger{
		store:        store,
		recorder:     recorder,
		now:          now,
		geometry:     make(map[string]model.ChamberGeometry),
		observations: make(map[string]map[cycleKey]map[string]model.WaterObservation),
		accounts:     make(map[string][]model.WaterAccount),
	}
}

// ConfigureGeometry stores the lock chamber dimensions used for estimation.
func (l *Ledger) ConfigureGeometry(ctx context.Context, chamberID string, lengthM, widthM, sillElevationM float64) (model.ChamberGeometry, error) {
	geometry, err := model.NewChamberGeometry(chamberID, lengthM, widthM, sillElevationM, l.now())
	if err != nil {
		return model.ChamberGeometry{}, err
	}
	event, err := l.recorder.Record(ctx, chamberID, model.EventChamberGeometry, uuid.Nil, 0, geometry)
	if err != nil {
		return model.ChamberGeometry{}, err
	}
	geometry.SourceEventSeq = event.Sequence
	l.mu.Lock()
	l.geometry[chamberID] = geometry
	l.mu.Unlock()
	return geometry, nil
}

func (l *Ledger) Geometry(chamberID string) (model.ChamberGeometry, bool) {
	l.mu.RLock()
	geometry, ok := l.geometry[chamberID]
	l.mu.RUnlock()
	return geometry, ok
}

// RecordObservation accepts a start/end level reading. Pass meters=nil to
// record that the measurement is missing: the transit then stays
// non-accountable instead of being treated as a zero level.
func (l *Ledger) RecordObservation(ctx context.Context, chamberID string, cycleID uuid.UUID, generation uint64, phase string, meters *float64, observedAt time.Time) (model.WaterObservation, error) {
	if observedAt.IsZero() {
		observedAt = l.now()
	}
	observation, err := model.NewWaterObservation(chamberID, phase, cycleID, generation, meters, observedAt, l.now())
	if err != nil {
		return model.WaterObservation{}, err
	}
	event, err := l.recorder.Record(ctx, chamberID, model.EventWaterObserved, cycleID, generation, observation)
	if err != nil {
		return model.WaterObservation{}, err
	}
	observation.SourceEventSeq = event.Sequence
	l.mu.Lock()
	l.putObservationLocked(observation)
	l.mu.Unlock()
	return observation, nil
}

func (l *Ledger) putObservationLocked(observation model.WaterObservation) {
	key := cycleKey{id: observation.CycleID, generation: observation.Generation}
	byCycle := l.observations[observation.ChamberID]
	if byCycle == nil {
		byCycle = make(map[cycleKey]map[string]model.WaterObservation)
		l.observations[observation.ChamberID] = byCycle
	}
	byPhase := byCycle[key]
	if byPhase == nil {
		byPhase = make(map[string]model.WaterObservation)
		byCycle[key] = byPhase
	}
	byPhase[observation.Phase] = observation
}

// AccountCycle estimates the water used by one transit from its measured level
// change. Valve ramp commands are read from the journal and attached as
// provenance only; they never enter the volume calculation.
func (l *Ledger) AccountCycle(ctx context.Context, chamberID string, cycleID uuid.UUID, generation uint64) (model.WaterAccount, error) {
	if chamberID == "" || cycleID == uuid.Nil || generation == 0 {
		return model.WaterAccount{}, errors.New("chamber, cycle id and positive generation are required")
	}
	l.mu.RLock()
	var geometry *model.ChamberGeometry
	if configured, ok := l.geometry[chamberID]; ok {
		copy := configured
		geometry = &copy
	}
	var start, end *model.WaterObservation
	if byCycle, ok := l.observations[chamberID]; ok {
		if byPhase, ok := byCycle[cycleKey{id: cycleID, generation: generation}]; ok {
			if observed, ok := byPhase[model.ObservationPhaseStart]; ok {
				copy := observed
				start = &copy
			}
			if observed, ok := byPhase[model.ObservationPhaseEnd]; ok {
				copy := observed
				end = &copy
			}
		}
	}
	l.mu.RUnlock()

	events, err := l.store.Events(ctx, chamberID, 0)
	if err != nil {
		return model.WaterAccount{}, err
	}
	var cycle *model.TransitCycle
	var actions []model.ValveAction
	for _, event := range events {
		if event.CycleID != cycleID {
			continue
		}
		switch event.Type {
		case model.EventCycleCreated:
			var decoded model.TransitCycle
			if err := event.Decode(&decoded); err != nil {
				return model.WaterAccount{}, err
			}
			if decoded.Generation == generation {
				cycle = &decoded
			}
		case model.EventCommandQueued:
			var command model.DeviceCommand
			if err := event.Decode(&command); err != nil {
				return model.WaterAccount{}, err
			}
			if command.Generation != generation || !isValve(command.Device) {
				continue
			}
			actions = append(actions, model.ValveAction{
				Device:    command.Device,
				Target:    command.Target,
				Action:    command.Action,
				IssuedAt:  command.CreatedAt,
				EventSeq:  event.Sequence,
				CommandID: command.ID,
			})
		}
	}
	if cycle == nil {
		return model.WaterAccount{}, errors.New("no recorded transit found for cycle")
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].EventSeq < actions[j].EventSeq })

	account := Estimate(model.AccountInput{
		Geometry: geometry, Start: start, End: end, Cycle: cycle, Actions: actions,
	}, l.now())
	if _, err := l.recorder.Record(ctx, chamberID, model.EventWaterAccounted, cycleID, generation, account); err != nil {
		return model.WaterAccount{}, err
	}
	l.mu.Lock()
	l.upsertAccountLocked(chamberID, account)
	l.mu.Unlock()
	return account, nil
}

func isValve(device model.DeviceKind) bool {
	return device == model.DeviceFillValve || device == model.DeviceDrainValve
}

func (l *Ledger) upsertAccountLocked(chamberID string, account model.WaterAccount) {
	existing := l.accounts[chamberID]
	for i := range existing {
		if existing[i].CycleID == account.CycleID {
			existing[i] = account
			l.accounts[chamberID] = existing
			return
		}
	}
	l.accounts[chamberID] = append(existing, account)
}

// AccountFilter narrows account queries by chamber and accounting time window.
type AccountFilter struct {
	ChamberID string
	From      *time.Time
	To        *time.Time
}

// Accounts returns computed water accounts for a chamber, optionally bounded
// by [From, To] on the accounting time. Newest first.
func (l *Ledger) Accounts(filter AccountFilter) []model.WaterAccount {
	l.mu.RLock()
	source := l.accounts[filter.ChamberID]
	result := make([]model.WaterAccount, 0, len(source))
	for _, account := range source {
		if filter.From != nil && account.AccountedAt.Before(filter.From.UTC()) {
			continue
		}
		if filter.To != nil && account.AccountedAt.After(filter.To.UTC()) {
			continue
		}
		result = append(result, account)
	}
	l.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].AccountedAt.After(result[j].AccountedAt) })
	return result
}

// RestoreAll rebuilds read models from the durable journal, e.g. after restart.
func (l *Ledger) RestoreAll(ctx context.Context) error {
	chambers, err := l.store.Chambers(ctx)
	if err != nil {
		return err
	}
	for _, chamberID := range chambers {
		events, err := l.store.Events(ctx, chamberID, 0)
		if err != nil {
			return err
		}
		if err := l.restore(chamberID, events); err != nil {
			return err
		}
	}
	return nil
}

func (l *Ledger) restore(chamberID string, events []model.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, event := range events {
		switch event.Type {
		case model.EventChamberGeometry:
			var geometry model.ChamberGeometry
			if err := event.Decode(&geometry); err != nil {
				return err
			}
			geometry.ChamberID = chamberID
			geometry.SourceEventSeq = event.Sequence
			l.geometry[chamberID] = geometry
		case model.EventWaterObserved:
			var observation model.WaterObservation
			if err := event.Decode(&observation); err != nil {
				return err
			}
			observation.ChamberID = chamberID
			observation.SourceEventSeq = event.Sequence
			l.putObservationLocked(observation)
		case model.EventWaterAccounted:
			var account model.WaterAccount
			if err := event.Decode(&account); err != nil {
				return err
			}
			account.ChamberID = chamberID
			l.upsertAccountLocked(chamberID, account)
		}
	}
	return nil
}
