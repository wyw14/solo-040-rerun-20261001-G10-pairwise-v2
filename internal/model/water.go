package model

import (
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
)

// WaterAccountStatus marks whether a transit's water use can be estimated.
type WaterAccountStatus string

const (
	// WaterAccountable means start and end observations are present and valid.
	WaterAccountable WaterAccountStatus = "accountable"
	// WaterNotAccountable means required measurements are missing or invalid.
	WaterNotAccountable WaterAccountStatus = "not_accountable"
)

// LevelBoundary identifies the two operator observations that bracket a leveling period.
type LevelBoundary string

const (
	LevelStart LevelBoundary = "start"
	LevelEnd   LevelBoundary = "end"
)

func (b LevelBoundary) Valid() bool { return b == LevelStart || b == LevelEnd }

// ChamberGeometry describes the effective water body of one lock chamber.
// Every volume is derived from observed level change x plan area;
// the valve opening is a control signal and is never used as a flow value.
type ChamberGeometry struct {
	ChamberID      string    `json:"chamber_id"`
	LengthM        float64   `json:"length_m"`
	WidthM         float64   `json:"width_m"`
	PlanAreaM2     float64   `json:"plan_area_m2"`
	SillElevationM float64   `json:"sill_elevation_m"`
	DeckElevationM float64   `json:"deck_elevation_m"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func NewChamberGeometry(chamberID string, lengthM, widthM, sillM, deckM float64, now time.Time) (ChamberGeometry, error) {
	if chamberID == "" {
		return ChamberGeometry{}, errors.New("chamber id is required")
	}
	if !(lengthM > 0) || math.IsNaN(lengthM) || math.IsInf(lengthM, 0) {
		return ChamberGeometry{}, errors.New("chamber length must be a positive number")
	}
	if !(widthM > 0) || math.IsNaN(widthM) || math.IsInf(widthM, 0) {
		return ChamberGeometry{}, errors.New("chamber width must be a positive number")
	}
	if sillM < 0 || deckM <= sillM {
		return ChamberGeometry{}, errors.New("elevations require 0 <= sill < deck")
	}
	return ChamberGeometry{
		ChamberID: chamberID, LengthM: lengthM, WidthM: widthM,
		PlanAreaM2: lengthM * widthM, SillElevationM: sillM, DeckElevationM: deckM,
		UpdatedAt: now.UTC(),
	}, nil
}

// ContainsLevel reports whether an observed surface elevation is inside the chamber envelope.
func (g ChamberGeometry) ContainsLevel(meters float64) bool {
	return meters+1e-9 >= g.SillElevationM && meters-1e-9 <= g.DeckElevationM
}

// WaterObservation is one start/end level reading attached to a specific transit.
type WaterObservation struct {
	ID         uuid.UUID     `json:"id"`
	ChamberID  string        `json:"chamber_id"`
	CycleID    uuid.UUID     `json:"cycle_id"`
	Generation uint64        `json:"generation"`
	Boundary   LevelBoundary `json:"boundary"`
	Meters     float64       `json:"meters"`
	ObservedAt time.Time     `json:"observed_at"`
	RecordedAt time.Time     `json:"recorded_at"`
}

func NewWaterObservation(cycleValue TransitCycle, boundary LevelBoundary, meters float64, observedAt, now time.Time) (WaterObservation, error) {
	if cycleValue.ID == uuid.Nil || cycleValue.ChamberID == "" || cycleValue.Generation == 0 {
		return WaterObservation{}, errors.New("observation requires a transit identity")
	}
	if !boundary.Valid() {
		return WaterObservation{}, errors.New("boundary must be start or end")
	}
	if math.IsNaN(meters) || math.IsInf(meters, 0) {
		return WaterObservation{}, errors.New("level must be a finite number")
	}
	if meters < 0 {
		return WaterObservation{}, errors.New("level cannot be negative")
	}
	if observedAt.IsZero() {
		observedAt = now
	}
	return WaterObservation{
		ID: uuid.New(), ChamberID: cycleValue.ChamberID, CycleID: cycleValue.ID,
		Generation: cycleValue.Generation, Boundary: boundary, Meters: meters,
		ObservedAt: observedAt.UTC(), RecordedAt: now.UTC(),
	}, nil
}

// ValveTraceStep summarizes one issued valve ramp step for the accounting record.
// Opening is a dimensionless control position (0..1 fraction for fill valves,
// signed target magnitude for drain valves); it is explicitly NOT a flow value.
type ValveTraceStep struct {
	Device    DeviceKind `json:"device"`
	Opening   float64    `json:"opening"`
	CommandID uuid.UUID  `json:"command_id"`
	IssuedAt  time.Time  `json:"issued_at"`
}

// WaterAccount is the per-transit accounting result.
type WaterAccount struct {
	ChamberID         string             `json:"chamber_id"`
	CycleID           uuid.UUID          `json:"cycle_id"`
	Generation        uint64             `json:"generation"`
	Direction         Direction          `json:"direction"`
	Stage             TransitStage       `json:"stage"`
	Status            WaterAccountStatus `json:"status"`
	CreatedAt         time.Time          `json:"created_at"`
	StartLevelM       *float64           `json:"start_level_m,omitempty"`
	EndLevelM         *float64           `json:"end_level_m,omitempty"`
	LevelChangeM      *float64           `json:"level_change_m,omitempty"`
	EffectiveAreaM2   *float64           `json:"effective_area_m2,omitempty"`
	EstimatedVolumeM3 *float64           `json:"estimated_volume_m3,omitempty"`
	Fill              bool               `json:"fill"`
	StartObservedAt   *time.Time         `json:"start_observed_at,omitempty"`
	EndObservedAt     *time.Time         `json:"end_observed_at,omitempty"`
	ValveTrace        []ValveTraceStep   `json:"valve_trace,omitempty"`
	Quality           WaterQuality       `json:"quality"`
}

// WaterQuality carries the data-quality statement accompanying an account.
type WaterQuality struct {
	Method     string      `json:"method"`
	Confidence string      `json:"confidence"`
	Notes      []string    `json:"notes"`
	Trace      []SampleRef `json:"trace"`
}

// SampleRef points back to the observation/event a figure was derived from.
type SampleRef struct {
	Kind       string    `json:"kind"`
	ID         uuid.UUID `json:"id"`
	Sequence   uint64    `json:"sequence,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}
