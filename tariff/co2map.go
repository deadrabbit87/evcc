package tariff

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/request"
)

type Co2Map struct {
	*request.Helper
	log     *util.Logger
	state   string
	country string
	data    *util.Monitor[api.Rates]
}

var _ api.Tariff = (*Co2Map)(nil)

func init() {
	registry.Add("co2map", NewCo2MapFromConfig)
}

func NewCo2MapFromConfig(other map[string]any) (api.Tariff, error) {
	cc := struct {
		State   string
		Country string
	}{
		State:   "DE",
		Country: "DE",
	}

	if err := util.DecodeOther(other, &cc); err != nil {
		return nil, err
	}

	t := &Co2Map{
		log:     util.NewLogger("co2map"),
		Helper:  request.NewHelper(util.NewLogger("co2map")),
		state:   cc.State,
		country: cc.Country,
		data:    util.NewMonitor[api.Rates](2 * time.Hour),
	}

	return runOrError(t)
}

func (t *Co2Map) run(done chan error) {
	var once sync.Once

	for tick := time.Tick(time.Hour); ; <-tick {
		// query from yesterday to tomorrow to ensure we always have valid rates
		yesterday := time.Now().Add(-24 * time.Hour).Format("2006-01-02")
		tomorrow := time.Now().Add(24 * time.Hour).Format("2006-01-02")

		uri := fmt.Sprintf("https://api.co2map.de/ConsumptionIntensityPreliminary/?state=%s&country=%s&start=%s&end=%s",
			t.state, t.country, yesterday, tomorrow)

		var res co2MapResponse

		if err := backoff.Retry(func() error {
			return backoffPermanentError(t.GetJSON(uri, &res))
		}, bo()); err != nil {
			once.Do(func() { done <- err })
			t.log.ERROR.Println(err)
			continue
		}

		data := make(api.Rates, 0, len(res.Intensity))
		for _, slot := range res.Intensity {
			if slot.Value == nil {
				continue
			}
			data = append(data, api.Rate{
				Start: slot.Timestamp.Local(),
				End:   slot.Timestamp.Add(time.Hour).Local(),
				Value: *slot.Value,
			})
		}

		// extend last known rate to cover the current hour,
		// since the API only provides data for completed hours
		if n := len(data); n > 0 {
			if now := time.Now(); data[n-1].End.Before(now) {
				data[n-1].End = now.Truncate(time.Hour).Add(time.Hour)
			}
		}

		mergeRates(t.data, data)
		once.Do(func() { close(done) })
	}
}

// Rates implements the api.Tariff interface
func (t *Co2Map) Rates() (api.Rates, error) {
	var res api.Rates
	err := t.data.GetFunc(func(val api.Rates) {
		res = slices.Clone(val)
	})
	return res, err
}

// Type implements the api.Tariff interface
func (t *Co2Map) Type() api.TariffType {
	return api.TariffTypeCo2
}

// co2MapResponse represents the API response
type co2MapResponse struct {
	State     string       `json:"state"`
	Start     string       `json:"start"`
	End       string       `json:"end"`
	Unit      string       `json:"unit"`
	Country   string       `json:"country"`
	Intensity []co2MapSlot `json:"-"`
}

// co2MapSlot represents a single [timestamp, value] pair
type co2MapSlot struct {
	Timestamp time.Time
	Value     *float64
}

// UnmarshalJSON handles the dynamic key name and [timestamp, value] array format
func (r *co2MapResponse) UnmarshalJSON(data []byte) error {
	// first unmarshal the known fields
	var aux struct {
		State   string `json:"state"`
		Start   string `json:"start"`
		End     string `json:"end"`
		Unit    string `json:"unit"`
		Country string `json:"country"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	r.State = aux.State
	r.Start = aux.Start
	r.End = aux.End
	r.Unit = aux.Unit
	r.Country = aux.Country

	// unmarshal into a raw map to find the intensity data key
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// find the intensity array — the key varies by endpoint
	for key, val := range raw {
		switch key {
		case "state", "start", "end", "unit", "country":
			continue
		}

		// this should be the intensity data array: [[timestamp, value], ...]
		var slots []json.RawMessage
		if err := json.Unmarshal(val, &slots); err != nil {
			continue
		}

		r.Intensity = make([]co2MapSlot, 0, len(slots))
		for _, slot := range slots {
			var pair []json.RawMessage
			if err := json.Unmarshal(slot, &pair); err != nil || len(pair) != 2 {
				continue
			}

			var ts string
			if err := json.Unmarshal(pair[0], &ts); err != nil {
				continue
			}

			t, err := time.Parse(time.RFC3339, ts)
			if err != nil {
				continue
			}

			s := co2MapSlot{Timestamp: t}

			if string(pair[1]) != "null" {
				var val float64
				if err := json.Unmarshal(pair[1], &val); err == nil {
					s.Value = &val
				}
			}

			r.Intensity = append(r.Intensity, s)
		}

		break
	}

	return nil
}
