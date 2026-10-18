package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/carbon/provider"
)

const (
	headerDataAge = "X-Carbon-Data-Age-Seconds"
	headerQuality = "X-Carbon-Quality"
	headerSource  = "X-Carbon-Source"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_ = r
	body := s.cache.Health(s.mode())
	s.writeJSON(w, body.HTTPStatusCode(), body)
}

func (s *Server) handleZones(w http.ResponseWriter, r *http.Request) {
	_ = r
	snap, err := s.cache.Load()
	if err != nil {
		s.writeError(w, err)
		return
	}
	items := make([]v1alpha1.ZoneProfile, 0, len(snap.Zones))
	for id, st := range snap.Zones {
		rec, ok := s.cache.Get(string(id))
		if !ok {
			continue
		}
		items = append(items, v1alpha1.ZoneProfile{
			APIVersion:          v1alpha1.GroupVersion,
			Kind:                v1alpha1.KindZoneProfile,
			Zone:                id,
			SourceZone:          rec.SourceZone,
			Quality:             rec.Quality,
			Source:              rec.Source,
			TemporalGranularity: rec.TemporalGranularity,
			IntervalSeconds:     rec.IntervalSeconds,
			HasCurrent:          true,
			HasForecast:         len(st.Forecast) > 0,
			FreshnessSeconds:    rec.FreshnessSeconds,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Zone < items[j].Zone })
	s.writeJSON(w, http.StatusOK, v1alpha1.ZoneProfileList{
		APIVersion:        v1alpha1.GroupVersion,
		Kind:              v1alpha1.KindZoneProfileList,
		SnapshotFetchedAt: snap.FetchedAt,
		Items:             items,
	})
}

func (s *Server) handleCurrent(w http.ResponseWriter, r *http.Request) {
	zone := v1alpha1.ZoneID(r.PathValue("zone"))
	if err := zone.Validate(); err != nil {
		s.writeStatus(w, http.StatusBadRequest, v1alpha1.ReasonBadRequest, err.Error(), zone)
		return
	}
	if _, err := s.cache.Load(); err != nil {
		s.writeError(w, err)
		return
	}
	rec, ok := s.cache.Get(string(zone))
	if !ok {
		s.writeStatus(w, http.StatusNotFound, v1alpha1.ReasonNotFound,
			fmt.Sprintf("zone %q is not in the carbon snapshot", zone), zone)
		return
	}
	rec.APIVersion = v1alpha1.GroupVersion
	rec.Kind = v1alpha1.KindCarbonRecord
	w.Header().Set(headerDataAge, strconv.FormatInt(rec.FreshnessSeconds, 10))
	w.Header().Set(headerQuality, string(rec.Quality))
	w.Header().Set(headerSource, string(rec.Source))
	s.writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleForecast(w http.ResponseWriter, r *http.Request) {
	zone := v1alpha1.ZoneID(r.PathValue("zone"))
	if err := zone.Validate(); err != nil {
		s.writeStatus(w, http.StatusBadRequest, v1alpha1.ReasonBadRequest, err.Error(), zone)
		return
	}
	horizonSec := v1alpha1.DefaultForecastHorizonSeconds
	raw := r.URL.Query().Get(v1alpha1.QueryDuration)
	if raw != "" {
		sec, err := v1alpha1.ParseDurationSeconds(raw)
		if err != nil {
			s.writeStatus(w, http.StatusBadRequest, v1alpha1.ReasonBadRequest, err.Error(), zone)
			return
		}
		if sec > v1alpha1.MaxForecastHorizonSeconds {
			s.writeStatus(w, http.StatusBadRequest, v1alpha1.ReasonBadRequest,
				fmt.Sprintf("duration %s exceeds maximum %s", raw, v1alpha1.FormatDurationSeconds(v1alpha1.MaxForecastHorizonSeconds)), zone)
			return
		}
		horizonSec = sec
	}
	snap, err := s.cache.Load()
	if err != nil {
		s.writeError(w, err)
		return
	}
	st, ok := s.cache.State(zone)
	if !ok {
		s.writeStatus(w, http.StatusNotFound, v1alpha1.ReasonNotFound,
			fmt.Sprintf("zone %q is not in the carbon snapshot", zone), zone)
		return
	}
	issuedAt := snap.FetchedAt
	if issuedAt.IsZero() {
		issuedAt = v1alpha1.RFC3339Time{Time: s.cache.Clock().Now().UTC()}
	}
	end := issuedAt.UTC().Add(time.Duration(horizonSec) * time.Second)
	filtered := make([]v1alpha1.ForecastInterval, 0, len(st.Forecast))
	for _, iv := range st.Forecast {
		ts := iv.Timestamp.UTC()
		if !ts.Before(issuedAt.UTC()) && ts.Before(end) {
			if iv.Unit == "" {
				iv.Unit = v1alpha1.IntensityUnitGCO2ePerKWh
			}
			filtered = append(filtered, iv)
		}
	}
	mode := v1alpha1.ForecastModeNone
	if len(filtered) > 0 {
		mode = v1alpha1.ForecastModeRecorded
	}
	body := v1alpha1.CarbonForecast{
		APIVersion:          v1alpha1.GroupVersion,
		Kind:                v1alpha1.KindCarbonForecast,
		Zone:                zone,
		Unit:                v1alpha1.IntensityUnitGCO2ePerKWh,
		IssuedAt:            issuedAt,
		Source:              st.Current.Source,
		EmissionFactorType:  st.Current.EmissionFactorType,
		TemporalGranularity: st.Current.TemporalGranularity,
		ForecastMode:        mode,
		Horizon:             v1alpha1.FormatDurationSeconds(horizonSec),
		FreshnessSeconds:    st.Current.FreshnessSeconds,
		Intervals:           filtered,
	}
	s.writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	_ = r
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	s.cache.WriteMetrics(w)
	if err := s.metrics.WriteText(w); err != nil {
		s.logger.Error("encode metrics", "err", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, v1alpha1.ErrEmptyCache), errors.Is(err, provider.ErrNotImplemented),
		errors.Is(err, provider.ErrTimeout), errors.Is(err, provider.ErrInvalidResponse):
		s.writeStatus(w, http.StatusServiceUnavailable, v1alpha1.ReasonServiceUnavailable, err.Error(), "")
	case errors.Is(err, v1alpha1.ErrUnknownZone):
		s.writeStatus(w, http.StatusNotFound, v1alpha1.ReasonNotFound, err.Error(), "")
	case errors.Is(err, v1alpha1.ErrInvalidDuration):
		s.writeStatus(w, http.StatusBadRequest, v1alpha1.ReasonBadRequest, err.Error(), "")
	default:
		s.writeStatus(w, http.StatusServiceUnavailable, v1alpha1.ReasonServiceUnavailable, err.Error(), "")
	}
}

func (s *Server) writeStatus(w http.ResponseWriter, code int, reason v1alpha1.StatusReason, message string, zone v1alpha1.ZoneID) {
	st := v1alpha1.NewStatus(code, reason, message)
	if zone != "" {
		st.Details = &v1alpha1.StatusDetails{Name: string(zone), Kind: "Zone"}
	}
	s.writeJSON(w, code, st)
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	if err := enc.Encode(body); err != nil {
		s.logger.Error("encode response", "err", err)
	}
}

func (s *Server) mode() v1alpha1.ProviderMode {
	if s.provider == nil {
		return v1alpha1.ProviderModeMock
	}
	return s.provider.Mode()
}
