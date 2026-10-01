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
	DeckElevationM float64 `json:"deck_elevation_m"`
}

type observationInput struct {
	CycleID    string  `json:"cycle_id"`
	Generation uint64  `json:"generation"`
	Boundary   string  `json:"boundary"`
	Meters     float64 `json:"meters"`
	ObservedAt string  `json:"observed_at,omitempty"`
}

func getGeometryHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chamberID := chi.URLParam(r, "chamberID")
		geometry, ok, err := runtime.Water.Geometry(r.Context(), chamberID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("geometry is not configured for chamber"))
			return
		}
		writeJSON(w, http.StatusOK, geometry)
	}
}

func configureGeometryHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input geometryInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		geometry := model.ChamberGeometry{
			ChamberID:      chi.URLParam(r, "chamberID"),
			LengthM:        input.LengthM,
			WidthM:         input.WidthM,
			SillElevationM: input.SillElevationM,
			DeckElevationM: input.DeckElevationM,
		}
		saved, err := runtime.Water.ConfigureGeometry(r.Context(), geometry)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, saved)
	}
}

func levelObservationHandler(runtime *Runtime) http.HandlerFunc {
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
		boundary := model.LevelBoundary(input.Boundary)
		if !boundary.Valid() {
			writeError(w, http.StatusBadRequest, errors.New("boundary must be start or end"))
			return
		}
		var observedAt time.Time
		if input.ObservedAt != "" {
			observedAt, err = time.Parse(time.RFC3339, input.ObservedAt)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("observed_at must be RFC3339, e.g. 2026-09-29T08:30:00Z"))
				return
			}
		}
		observation, err := runtime.Water.Observe(
			r.Context(), chi.URLParam(r, "chamberID"),
			cycleID, input.Generation, boundary, input.Meters, observedAt,
		)
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusCreated, observation)
	}
}

func waterAccountsHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter := water.Filter{ChamberID: chi.URLParam(r, "chamberID")}
		if value := r.URL.Query().Get("from"); value != "" {
			from, err := time.Parse(time.RFC3339, value)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("from must be RFC3339"))
				return
			}
			filter.From = from
		}
		if value := r.URL.Query().Get("to"); value != "" {
			to, err := time.Parse(time.RFC3339, value)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("to must be RFC3339"))
				return
			}
			filter.To = to
		}
		accounts, err := runtime.Water.ListAccounts(r.Context(), filter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, accounts)
	}
}

func allWaterAccountsHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var from, to time.Time
		if value := r.URL.Query().Get("from"); value != "" {
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("from must be RFC3339"))
				return
			}
			from = parsed
		}
		if value := r.URL.Query().Get("to"); value != "" {
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("to must be RFC3339"))
				return
			}
			to = parsed
		}
		accounts, err := runtime.Water.ListAllAccounts(r.Context(), from, to)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, accounts)
	}
}

func waterAccountHandler(runtime *Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cycleID, err := parseUUID(chi.URLParam(r, "cycleID"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		account, err := runtime.Water.Account(r.Context(), chi.URLParam(r, "chamberID"), cycleID)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, account)
	}
}
