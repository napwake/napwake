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

package cost

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The default rates are an m6i.large's on-demand price in us-east-1, $0.096
// an hour for 2 vCPUs and 8 GiB, split 7.75 to 1 between a vCPU-hour and a
// GiB-hour, each rounded down to a tenth of a cent, as their comment says.
func TestDefaultRates_AreAnM6iLarge(t *testing.T) {
	const hourly, vcpu, memoryGiB, split = 0.096, 2, 8, 7.75
	require.InDelta(t, split, cpuToMemory, 1e-9, "the split every list price is given")

	got := splitHourly(hourly, vcpu, memoryGiB)

	assert.InDelta(t, 0.0317, got.CPUPerHour, 0.00005)
	assert.InDelta(t, 0.0041, got.MemoryPerHour, 0.00005)
	assert.InDelta(t, DefaultRates.CPUPerHour, math.Floor(got.CPUPerHour*1000)/1000, 1e-12, "rounded down")
	assert.InDelta(t, DefaultRates.MemoryPerHour, math.Floor(got.MemoryPerHour*1000)/1000, 1e-12, "rounded down")
}

func TestHoursPrice(t *testing.T) {
	h := Hours{CPU: 100, Memory: 400, Storage: 200}

	assert.InDelta(t, 100*0.031+400*0.004+200*(0.08/730), h.Price(DefaultRates), 1e-12)
}

func TestComputeHourly(t *testing.T) {
	assert.InDelta(t, 2*0.031+8*0.004, ComputeHourly(2, 8, DefaultRates), 1e-12)
}

func TestCounted(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{name: "a flush interval counts in full", in: 5 * time.Minute, want: 5 * time.Minute},
		{name: "up to the limit", in: MaxInterval, want: MaxInterval},
		{name: "downtime counts as the limit", in: 10 * time.Hour, want: MaxInterval},
		{name: "the clock going back counts as nothing", in: -5 * time.Minute, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Counted(tt.in))
		})
	}
}

func TestMonthOf(t *testing.T) {
	tokyo := time.FixedZone("JST", 9*60*60)

	assert.Equal(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), MonthOf(time.Date(2026, 3, 31, 23, 59, 0, 0, time.UTC)))
	assert.Equal(t, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), MonthOf(time.Date(2026, 3, 1, 8, 0, 0, 0, tokyo)),
		"months are UTC months wherever the operator runs")
}

// A workload created late in the month is projected from the time it was
// tracked, not from the day of the month.
func TestProject(t *testing.T) {
	march20 := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	const hourly = 0.05

	tests := []struct {
		name    string
		tracked time.Duration
		want    float64
		ok      bool
	}{
		{name: "less than a day is pending", tracked: 23 * time.Hour},
		{name: "a day tracked from the 19th", tracked: 24 * time.Hour, want: hourly * 31 * 24, ok: true},
		{name: "the month so far", tracked: (19*24 + 12) * time.Hour, want: hourly * 31 * 24, ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Project(hourly*tt.tracked.Hours(), tt.tracked, march20)

			assert.Equal(t, tt.ok, ok)
			assert.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

// An amount above zero that rounds to nothing is <$0.01, never $0.00,
// which is for none. A negative amount is written as it always was.
func TestFormatDollars(t *testing.T) {
	tests := []struct {
		amount float64
		want   string
	}{
		{0, "$0.00"},
		{math.SmallestNonzeroFloat64, "<$0.01"},
		{0.001, "<$0.01"},
		{0.004, "<$0.01"},
		{0.0049, "<$0.01"},
		{0.005, "$0.01"},
		{0.01, "$0.01"},
		{1.5, "$1.50"},
		{123.456, "$123.46"},
		{-0.001, "$-0.00"},
		{-1.5, "$-1.50"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, FormatDollars(tt.amount), "%g", tt.amount)
	}
}

func TestParseDollars(t *testing.T) {
	tests := []struct {
		s    string
		want float64
	}{
		{"$0.00", 0},
		{"$0.01", 0.01},
		{"$12.40", 12.40},
		{"$-1.50", -1.5},
	}
	for _, tt := range tests {
		got, err := ParseDollars(tt.s)
		require.NoError(t, err, tt.s)
		assert.InDelta(t, tt.want, got, 1e-12, tt.s)
	}

	under, err := ParseDollars("<$0.01")
	require.NoError(t, err)
	assert.Greater(t, under, 0.0, "under a cent is above zero")
	assert.Less(t, under, 0.005, "and rounds to nothing")

	for _, s := range []string{"", "pending", "unknown", "$", "<$1"} {
		_, err := ParseDollars(s)
		assert.Error(t, err, "%q", s)
	}
}

// What FormatDollars writes reads back above zero exactly when the amount
// was, and written again is the same, for every amount to a millionth of a
// dollar up to ten cents and beyond.
func TestParseDollars_ReadsWhatFormatDollarsWrites(t *testing.T) {
	const steps = 100_000
	amounts := make([]float64, 0, steps+6)
	amounts = append(amounts, math.SmallestNonzeroFloat64, 1e-9, 0.0049999, 1234.5, 1e6)
	for i := range steps + 1 {
		amounts = append(amounts, float64(i)/1e6)
	}
	for _, amount := range amounts {
		written := FormatDollars(amount)
		read, err := ParseDollars(written)
		require.NoError(t, err, written)
		if (read > 0) != (amount > 0) {
			t.Fatalf("%g written as %s reads as %g", amount, written, read)
		}
		if again := FormatDollars(read); again != written {
			t.Fatalf("%g written as %s reads as %g, written again as %s", amount, written, read, again)
		}
	}
}
