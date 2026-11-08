//go:build sizing

/*
Copyright 2026 The Napwake Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sizing_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/napwake/napwake/internal/forecast"
)

// importLimit is the most ImportEngine takes of a state, decompressed, as
// internal/forecast's maxStateBytes: anything larger is refused as
// corrupt, and the engine learns again. Nothing else limits a state:
// status.prediction.state has no maxLength.
const importLimit = 64 << 10

// What a workload's forecast state comes to, as its status keeps it, from
// office hours to demand no hour can foretell, and near-zero demand, whose
// tiny figures take the most digits, and that each imports as it was
// saved. The state is the same shape whatever the demand, 864 single
// precision figures and a few counts, so only their digits vary: it stays
// near the 3.6 KB the cache measures above, and far below importLimit.
// internal/forecast's own test holds a trained office-hours engine's state
// under 4,096 bytes. Past that, nothing is refused: it costs only its own
// bytes wherever a ManagedWorkload is cached.
func TestForecastStateSize(t *testing.T) {
	noise := rand.New(rand.NewPCG(3, 4))
	start := time.Now().Add(-6 * 7 * 24 * time.Hour).Truncate(time.Hour)
	officeHours := func(at time.Time, spread float64) float64 {
		if wd := at.Weekday(); wd != time.Saturday && wd != time.Sunday && at.Hour() >= 9 && at.Hour() < 17 {
			return 30 + spread*noise.Float64()
		}
		return 0.5 * noise.Float64()
	}
	demands := []struct {
		name   string
		demand func(at time.Time) float64
	}{
		{name: "office hours", demand: func(at time.Time) float64 { return officeHours(at, 20) }},
		{name: "office hours, twice as noisy", demand: func(at time.Time) float64 { return officeHours(at, 40) }},
		{name: "any hour, 0 to 100% of requests", demand: func(time.Time) float64 { return 100 * noise.Float64() }},
		{name: "any hour, 0.001% to 1,000,000%", demand: func(time.Time) float64 {
			return math.Pow(10, -3+9*noise.Float64())
		}},
		{name: "any hour, near nothing and near the most", demand: func(time.Time) float64 {
			if noise.IntN(2) == 0 {
				return 1e-7 * noise.Float64()
			}
			return 1e9 * noise.Float64()
		}},
		{name: "near nothing, under a millionth of a percent", demand: func(time.Time) float64 {
			return 1e-6 * noise.Float64()
		}},
	}
	for _, d := range demands {
		e := forecast.NewEngine(forecast.DefaultParams(), forecast.Settings{Threshold: 75})
		for i := range 6 * forecast.WeeklySeason {
			at := start.Add(time.Duration(i) * time.Hour)
			_, err := e.Observe(d.demand(at), at)
			require.NoError(t, err, d.name)
		}
		state, err := e.Export()
		require.NoError(t, err, d.name)
		raw := decompressedState(t, state)
		t.Logf("%s: %d bytes, %d decompressed", d.name, len(state), raw)
		assert.Less(t, raw, importLimit/4, "%s: far below what ImportEngine takes", d.name)

		restored, err := forecast.ImportEngine(state, forecast.Settings{Threshold: 75})
		require.NoError(t, err, "%s: a state of %d bytes imports", d.name, len(state))
		again, err := restored.Export()
		require.NoError(t, err, d.name)
		assert.Equal(t, state, again, "%s: and exports as it was", d.name)
	}
}

func decompressedState(t *testing.T, state string) int {
	t.Helper()
	compressed, err := base64.StdEncoding.DecodeString(state)
	require.NoError(t, err)
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	require.NoError(t, err)
	raw, err := io.ReadAll(zr)
	require.NoError(t, err)
	return len(raw)
}
