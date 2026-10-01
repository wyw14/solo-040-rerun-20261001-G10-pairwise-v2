package model

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// AccountStatus describes whether a transit's water use can be estimated.
type AccountStatus string

const (
	// AccountEstimated means start/end levels and chamber geometry were present.
	AccountEstimated AccountStatus = "estimated"
	// AccountNotAccountable means a required measurement was missing.
	AccountNotAccountable AccountStatus = "not_accountable"
)

// ChamberGeometry holds the static lock chamber dimensions used to convert a
// level change into a water volume. Only geometric/level measurements are used;
// valve openings are deliberately not part of this computation.
type ChamberGeometry struct {
	ChamberID      string    `json:"chamber_id"`
	LengthM        float64   `json:"length_m"`
	WidthM         float64   `json:"width_m"`
	SillElevationM float64   `json:"sill_elevation_m"`
	UpdatedAt      time.Time `json:"updated_at"`
	SourceEventSeq uint64    `json:"source_event_seq,omitempty"`
}

func NewChamberGeometry(chamberID string, lengthM, widthM, sillElevationM float64, now time.Time) (ChamberGeometry, error) {
	if chamberID == "" {
		return ChamberGeometry{}, errors.New("chamber id is required")
	}
	if lengthM <= 0 || widthM <= 0 {
		return ChamberGeometry{}, errors.New("chamber length and width must be positive")
	}
	if sillElevationM < 0 {
		return ChamberGeometry{}, errors.New("sill elevation must not be negative")
	}
	return ChamberGeometry{
		ChamberID: chamberID, LengthM: lengthM, WidthM: widthM,
		SillElevationM: sillElevationM, UpdatedAt: now.UTC(),
	}, nil
}

// PlanAreaM2 returns the chamber's horizontal prism area.
func (g ChamberGeometry) PlanAreaM2() float64 { return g.LengthM * g.WidthM }

// WaterObservation is a start/end level reading for one transit. Present is
// explicit so a missing measurement is distinguishable from a zero reading.
type WaterObservation struct {
	ID             uuid.UUID `json:"id"`
	ChamberID      string    `json:"chamber_id"`
	CycleID        uuid.UUID `json:"cycle_id"`
	Generation     uint64    `json:"generation"`
	Phase          string    `json:"phase"`
	Present        bool      `json:"present"`
	Meters         float64   `json:"meters,omitempty"`
	ObservedAt     time.Time `json:"observed_at"`
	RecordedAt     time.Time `json:"recorded_at"`
	SourceEventSeq uint64    `json:"source_event_seq,omitempty"`
}

// NewWaterObservation builds one reading. Pass meters=nil to record an
// explicitly missing measurement, which forces the transit to be
// non-accountable rather than silently treated as zero.
func NewWaterObservation(chamberID, phase string, cycleID uuid.UUID, generation uint64, meters *float64, observedAt, now time.Time) (WaterObservation, error) {
	if chamberID == "" {
		return WaterObservation{}, errors.New("chamber id is required")
	}
	if phase != ObservationPhaseStart && phase != ObservationPhaseEnd {
		return WaterObservation{}, errors.New("observation phase must be start or end")
	}
	if cycleID == uuid.Nil {
		return WaterObservation{}, errors.New("observation requires a transit cycle")
	}
	if observedAt.IsZero() {
		return WaterObservation{}, errors.New("observation time is required")
	}
	obs := WaterObservation{
		ID: uuid.New(), ChamberID: chamberID, CycleID: cycleID,
		Generation: generation, Phase: phase,
		Present:    meters != nil,
		ObservedAt: observedAt.UTC(), RecordedAt: now.UTC(),
	}
	if meters != nil {
		if *meters < 0 {
			return WaterObservation{}, errors.New("level reading must not be negative")
		}
		obs.Meters = *meters
	}
	return obs, nil
}

const (
	ObservationPhaseStart = "start"
	ObservationPhaseEnd   = "end"
)

// ValveAction is a traceability view of a valve ramp command. Opening fractions
// are valve actions only; they are never converted into a flow or volume.
type ValveAction struct {
	Device    DeviceKind `json:"device"`
	Target    float64    `json:"target"`
	Action    string     `json:"action"`
	IssuedAt  time.Time  `json:"issued_at"`
	EventSeq  uint64     `json:"event_seq,omitempty"`
	CommandID uuid.UUID  `json:"command_id,omitempty"`
}

// QualityNote explains one data-quality fact about an account.
type QualityNote struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WaterAccount is the per-transit water-use result. When Status is
// not_accountable the volume fields stay zero and MissingPhases lists the
// measurements that prevented estimation.
type WaterAccount struct {
	ChamberID          string        `json:"chamber_id"`
	CycleID            uuid.UUID     `json:"cycle_id"`
	Generation         uint64        `json:"generation"`
	Status             AccountStatus `json:"status"`
	Direction          Direction     `json:"direction,omitempty"`
	StartMeters        float64       `json:"start_meters,omitempty"`
	EndMeters          float64       `json:"end_meters,omitempty"`
	LevelChangeM       float64       `json:"level_change_m,omitempty"`
	PlanAreaM2         float64       `json:"plan_area_m2,omitempty"`
	EstimatedVolumeM3  float64       `json:"estimated_volume_m3,omitempty"`
	NetVolumeM3        float64       `json:"net_volume_m3,omitempty"`
	MissingPhases      []string      `json:"missing_phases,omitempty"`
	QualityNotes       []QualityNote `json:"quality_notes"`
	ValveActions       []ValveAction `json:"valve_actions,omitempty"`
	StartObservationID uuid.UUID     `json:"start_observation_id,omitempty"`
	EndObservationID   uuid.UUID     `json:"end_observation_id,omitempty"`
	StartEventSeq      uint64        `json:"start_event_seq,omitempty"`
	EndEventSeq        uint64        `json:"end_event_seq,omitempty"`
	GeometryEventSeq   uint64        `json:"geometry_event_seq,omitempty"`
	AccountedAt        time.Time     `json:"accounted_at"`
}

// AccountInput carries everything the estimator needs for one transit.
type AccountInput struct {
	Geometry *ChamberGeometry
	Start    *WaterObservation
	End      *WaterObservation
	Cycle    *TransitCycle
	Actions  []ValveAction
}
