package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/cry-96/internal/model"
	"github.com/wyw14/cry-96/internal/water"
)

type geometryInput struct {
	LengthM        float64 `json:"length_m"`
	WidthM         float64 `json:"width_m"`
	SillElevationM float64 `json:"sill_elevation_m"`
}

func configureGeometryHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input geometryInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		geometry, err := runtime.Water.ConfigureGeometry(
			r.Context(), chi.URLParam(r, "chamberID"),
			input.LengthM, input.WidthM, input.SillElevationM,
		)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, geometry)
	}
}

func geometryHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		geometry, ok := runtime.Water.Geometry(chi.URLParam(r, "chamberID"))
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("chamber geometry not configured"))
			return
		}
		writeJSON(w, http.StatusOK, geometry)
	}
}

type observationInput struct {
	CycleID    string   `json:"cycle_id"`
	Generation uint64   `json:"generation"`
	Phase      string   `json:"phase"`
	Meters     *float64 `json:"meters"`
	ObservedAt string   `json:"observed_at"`
}

func waterObservationHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input observationInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cycleID, err := parseUUID(input.CycleID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if input.Phase != model.ObservationPhaseStart && input.Phase != model.ObservationPhaseEnd {
			writeError(w, http.StatusBadRequest, errors.New("phase must be start or end"))
			return
		}
		observedAt := runtime.Now()
		if input.ObservedAt != "" {
			parsed, err := time.Parse(time.RFC3339, input.ObservedAt)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("observed_at must be RFC3339"))
				return
			}
			observedAt = parsed
		}
		observation, err := runtime.Water.RecordObservation(
			r.Context(), chi.URLParam(r, "chamberID"), cycleID, input.Generation,
			input.Phase, input.Meters, observedAt,
		)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusAccepted, observation)
	}
}

type accountInput struct {
	Generation uint64 `json:"generation"`
}

func accountCycleHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input accountInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		cycleID, err := parseUUID(chi.URLParam(r, "cycleID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		account, err := runtime.Water.AccountCycle(
			r.Context(), chi.URLParam(r, "chamberID"), cycleID, input.Generation,
		)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, account)
	}
}

func waterAccountsHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter := water.AccountFilter{ChamberID: r.URL.Query().Get("chamber_id")}
		if filter.ChamberID == "" {
			writeError(w, http.StatusBadRequest, errors.New("chamber_id query parameter is required"))
			return
		}
		if value := r.URL.Query().Get("from"); value != "" {
			from, err := time.Parse(time.RFC3339, value)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("from must be RFC3339"))
				return
			}
			filter.From = &from
		}
		if value := r.URL.Query().Get("to"); value != "" {
			to, err := time.Parse(time.RFC3339, value)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("to must be RFC3339"))
				return
			}
			filter.To = &to
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"chamber_id": filter.ChamberID,
			"from":       optionalTime(filter.From),
			"to":         optionalTime(filter.To),
			"accounts":   runtime.Water.Accounts(filter),
		})
	}
}

func optionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}
