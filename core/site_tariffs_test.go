package core

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForecastSlotEnergy(t *testing.T) {
	slot := time.Unix(1735689600, 0).Truncate(tariff.SlotDuration)
	rate := func(i int, power float64) api.Rate {
		return api.Rate{
			Start: slot.Add(time.Duration(i) * tariff.SlotDuration),
			End:   slot.Add(time.Duration(i+1) * tariff.SlotDuration),
			Value: power,
		}
	}

	rr := api.Rates{rate(0, 4000), rate(1, 8000)}

	// trapezoidal like the published curve, (4kW + 8kW) / 2 * 15min,
	// and identical anywhere inside the slot
	assert.Equal(t, 1.5, forecastSlotEnergy(rr, slot))
	assert.Equal(t, 1.5, forecastSlotEnergy(rr, slot.Add(time.Minute)))

	// the last sample has no successor to integrate towards, as for the daily totals
	assert.Equal(t, 0.0, forecastSlotEnergy(rr, slot.Add(tariff.SlotDuration)))

	// beyond the forecast horizon
	assert.Equal(t, 0.0, forecastSlotEnergy(rr, slot.Add(2*tariff.SlotDuration)))
}

func TestForecastRates(t *testing.T) {
	start := time.Unix(1735689600, 0)

	for _, tc := range []struct {
		desc  string
		rates api.Rates
		want  string
	}{
		{desc: "nil", rates: nil, want: "null"},
		{desc: "empty", rates: api.Rates{}, want: "null"},
		{
			desc: "slots",
			rates: api.Rates{
				{Start: start, End: start.Add(time.Hour), Value: 0.25},
				{Start: start.Add(time.Hour), End: start.Add(2 * time.Hour), Value: -0.1},
			},
			want: "[[1735689600,1735693200,0.25],[1735693200,1735696800,-0.1]]",
		},
		{
			desc: "estimate",
			rates: api.Rates{
				{Start: start, End: start.Add(time.Hour), Value: 0.25},
				{Start: start.Add(time.Hour), End: start.Add(2 * time.Hour), Value: 0.3, Estimate: true},
			},
			want: "[[1735689600,1735693200,0.25],[1735693200,1735696800,0.3,1]]",
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			b, err := json.Marshal(forecastRates(tc.rates))
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(b))
		})
	}
}

func TestTimeseriesMarshal(t *testing.T) {
	start := time.Unix(1735689600, 0)

	for _, tc := range []struct {
		desc string
		ts   timeseries
		want string
	}{
		{desc: "nil", ts: nil, want: "null"},
		{desc: "empty", ts: timeseries{}, want: "[]"},
		{
			desc: "entries",
			ts: timeseries{
				{Timestamp: start, Value: 1000},
				{Timestamp: start.Add(time.Hour), Value: 0},
			},
			want: "[[1735689600,1000],[1735693200,0]]",
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			b, err := json.Marshal(tc.ts)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(b))

			b, err = tc.ts.MarshalBytes()
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(b))
		})
	}
}

func TestPercentileOf(t *testing.T) {
	// n values of v
	fill := func(n int, v float64) []float64 {
		s := make([]float64, n)
		for i := range s {
			s[i] = v
		}
		return s
	}

	t.Run("too few samples returns false", func(t *testing.T) {
		_, ok := percentileOf(nil, 0.5, solarScaleMinSamples)
		assert.False(t, ok)

		_, ok = percentileOf(fill(solarScaleMinSamples-1, 0.9), 0.5, solarScaleMinSamples)
		assert.False(t, ok)
	})

	t.Run("stable cluster", func(t *testing.T) {
		v, ok := percentileOf(fill(20, 0.9), 0.5, solarScaleMinSamples)
		assert.True(t, ok)
		assert.InDelta(t, 0.9, v, 0.001)
	})

	// P50 rejects outlier days for free: a broken forecast feed (recent ratio
	// ~2.3) and a metering outage (ratio ~0.16) do not move the result as
	// long as they stay a minority of the window.
	t.Run("outlier days do not move P50", func(t *testing.T) {
		ratios := fill(20, 0.9)                   // healthy installation bias
		ratios = append(ratios, fill(4, 2.3)...)  // broken forecast feed
		ratios = append(ratios, fill(8, 0.16)...) // metering outage

		v, ok := percentileOf(ratios, 0.5, solarScaleMinSamples)
		assert.True(t, ok)
		assert.InDelta(t, 0.9, v, 0.001)
	})

	t.Run("higher percentile shifts toward the upper tail", func(t *testing.T) {
		ratios := append(fill(15, 0.8), fill(15, 1.2)...)

		p50, _ := percentileOf(ratios, 0.5, solarScaleMinSamples)
		p90, _ := percentileOf(ratios, 0.9, solarScaleMinSamples)
		assert.Less(t, p50, p90)
	})
}

func TestSolarCorrectionScale(t *testing.T) {
	now := time.Date(2026, 11, 28, 14, 0, 0, 0, time.UTC)
	eod := time.Date(2026, 11, 29, 0, 0, 0, 0, time.UTC)

	nowcast := solarCorrection{ratio: 0.1, anchor: now, fade: solarNowcastHorizon, until: eod}
	hold := solarCorrection{ratio: 0.02, anchor: now, plateau: solarHoldPlateau, fade: solarHoldFade}

	tc := []struct {
		name  string
		c     solarCorrection
		start time.Time
		want  float64
	}{
		{"nowcast current slot", nowcast, now, 0.1},
		{"nowcast already started", nowcast, now.Add(-10 * time.Minute), 0.1},
		{"nowcast half way", nowcast, now.Add(solarNowcastHorizon / 2), 0.4},
		{"nowcast horizon", nowcast, now.Add(solarNowcastHorizon), 0.7},
		{"nowcast not applied tomorrow", nowcast, eod.Add(time.Hour), 0.7},
		{"hold next morning", hold, now.Add(18 * time.Hour), 0.02},
		{"hold end of plateau", hold, now.Add(solarHoldPlateau), 0.02},
		{"hold fading", hold, now.Add(solarHoldPlateau + solarHoldFade/2), 0.36},
		{"hold faded", hold, now.Add(solarHoldPlateau + solarHoldFade), 0.7},
	}
	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			assert.InDelta(t, tc.want, tc.c.scale(0.7, tc.start), 1e-9)
		})
	}
}

func TestUpdateSolarHold(t *testing.T) {
	now := time.Date(2026, 11, 28, 14, 0, 0, 0, time.UTC)
	site := &Site{log: util.NewLogger("foo")}

	// production is as expected, nothing to hold
	assert.Nil(t, site.updateSolarHold(solarNowcastResult{day: 0.6, dayOk: true, ratio: 0.7, ok: true}, now))

	// snow day: almost no production despite forecast
	hold := site.updateSolarHold(solarNowcastResult{day: 0.01, dayOk: true, ratio: 0.01, ok: true}, now)
	require.NotNil(t, hold)
	assert.Equal(t, 0.01, hold.Ratio)

	// night: no data, the hold stays
	night := now.Add(10 * time.Hour)
	assert.Equal(t, hold, site.updateSolarHold(solarNowcastResult{}, night))

	// next morning, production resumes slowly: not released yet
	assert.NotNil(t, site.updateSolarHold(solarNowcastResult{ratio: 0.2, ok: true}, now.Add(20*time.Hour)))

	// modules are clear again
	assert.Nil(t, site.updateSolarHold(solarNowcastResult{ratio: 0.8, ok: true}, now.Add(21*time.Hour)))

	// expires without new observation
	site.updateSolarHold(solarNowcastResult{day: 0.01, dayOk: true}, now)
	assert.Nil(t, site.updateSolarHold(solarNowcastResult{}, now.Add(solarHoldPlateau+solarHoldFade+time.Minute)))
}

func TestNowcastRatio(t *testing.T) {
	tc := []struct {
		name     string
		pv, fcst float64
		want     float64
		wantOk   bool
	}{
		{"night", 0, 0, 0, false},
		{"too little forecast", 0.01, 0.2, 0, false},
		{"snow", 0.02, 1.5, 0.02 / 1.5, true},
		{"on forecast", 1.5, 1.5, 1, true},
		{"above forecast capped", 2.5, 1.5, 1, true},
		{"zero production", 0, 1, 0, true},
	}
	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := nowcastRatio(tc.pv, tc.fcst)
			assert.Equal(t, tc.wantOk, ok)
			assert.InDelta(t, tc.want, got, 1e-9)
		})
	}
}
