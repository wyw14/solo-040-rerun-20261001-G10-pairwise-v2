package water

import (
	"time"

	"github.com/google/uuid"
	"github.com/wyw14/cry-96/internal/model"
)

// Estimate turns geometry, start/end level observations and valve ramp actions
// into a per-transit account. It is a pure function so the rule "valve
// openings are never a flow or volume" lives in one testable place: the volume
// is chamber plan area multiplied by the measured level change, and ramp
// actions are attached only as data-quality / provenance context.
//
// If a required measurement is missing the account is explicitly
// not_accountable and no volume is produced, rather than guessing a value.
func Estimate(input model.AccountInput, accountedAt time.Time) model.WaterAccount {
	account := model.WaterAccount{
		ChamberID:    chamberOf(input),
		CycleID:      cycleIDOf(input),
		Generation:   generationOf(input),
		Status:       model.AccountNotAccountable,
		QualityNotes: []model.QualityNote{},
		AccountedAt:  accountedAt.UTC(),
	}
	if input.Cycle != nil {
		account.Direction = input.Cycle.Direction
	}

	var missing []string
	if input.Geometry == nil {
		missing = append(missing, "geometry")
		account.QualityNotes = append(account.QualityNotes, model.QualityNote{
			Code:    "missing_geometry",
			Message: "chamber geometry is not configured; level change cannot be converted to volume",
		})
	}
	if input.Start == nil || !input.Start.Present {
		missing = append(missing, model.ObservationPhaseStart)
		account.QualityNotes = append(account.QualityNotes, model.QualityNote{
			Code:    "missing_start_level",
			Message: "start-of-leveling water level observation is missing",
		})
	}
	if input.End == nil || !input.End.Present {
		missing = append(missing, model.ObservationPhaseEnd)
		account.QualityNotes = append(account.QualityNotes, model.QualityNote{
			Code:    "missing_end_level",
			Message: "end-of-leveling water level observation is missing",
		})
	}
	account.MissingPhases = missing
	if len(missing) > 0 {
		attachValveContext(&account, input.Actions)
		return account
	}

	start, end := input.Start.Meters, input.End.Meters
	account.StartMeters = start
	account.EndMeters = end
	account.StartObservationID = input.Start.ID
	account.EndObservationID = input.End.ID
	account.StartEventSeq = input.Start.SourceEventSeq
	account.EndEventSeq = input.End.SourceEventSeq
	account.GeometryEventSeq = input.Geometry.SourceEventSeq
	account.PlanAreaM2 = input.Geometry.PlanAreaM2()

	change := end - start
	account.LevelChangeM = change
	account.EstimatedVolumeM3 = account.PlanAreaM2 * abs(change)
	account.NetVolumeM3 = account.PlanAreaM2 * change
	if change >= 0 {
		account.QualityNotes = append(account.QualityNotes, model.QualityNote{
			Code:    "estimated_fill",
			Message: "volume estimated from measured level rise and chamber plan area",
		})
	} else {
		account.QualityNotes = append(account.QualityNotes, model.QualityNote{
			Code:    "estimated_drain",
			Message: "volume estimated from measured level drawdown and chamber plan area",
		})
	}

	if !input.End.ObservedAt.After(input.Start.ObservedAt) {
		account.QualityNotes = append(account.QualityNotes, model.QualityNote{
			Code:    "observation_order",
			Message: "end observation is not later than start observation; verify sampling times",
		})
	}
	if len(input.Actions) == 0 {
		account.QualityNotes = append(account.QualityNotes, model.QualityNote{
			Code:    "no_valve_actions",
			Message: "no valve ramp commands were recorded for this transit; estimate is level-based only",
		})
	} else {
		account.QualityNotes = append(account.QualityNotes, model.QualityNote{
			Code:    "valve_actions_excluded",
			Message: "valve ramp openings are recorded for traceability only and were not used as flow",
		})
	}
	attachValveContext(&account, input.Actions)
	account.Status = model.AccountEstimated
	return account
}

func attachValveContext(account *model.WaterAccount, actions []model.ValveAction) {
	if len(actions) == 0 {
		return
	}
	account.ValveActions = append([]model.ValveAction(nil), actions...)
}

func chamberOf(input model.AccountInput) string {
	switch {
	case input.Cycle != nil:
		return input.Cycle.ChamberID
	case input.Start != nil:
		return input.Start.ChamberID
	case input.End != nil:
		return input.End.ChamberID
	case input.Geometry != nil:
		return input.Geometry.ChamberID
	}
	return ""
}

func cycleIDOf(input model.AccountInput) uuid.UUID {
	switch {
	case input.Start != nil:
		return input.Start.CycleID
	case input.End != nil:
		return input.End.CycleID
	case input.Cycle != nil:
		return input.Cycle.ID
	}
	return uuid.Nil
}

func generationOf(input model.AccountInput) uint64 {
	switch {
	case input.Start != nil:
		return input.Start.Generation
	case input.End != nil:
		return input.End.Generation
	case input.Cycle != nil:
		return input.Cycle.Generation
	}
	return 0
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
