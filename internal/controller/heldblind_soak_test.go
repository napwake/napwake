//go:build soak

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

package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The test cluster's orders/api through 8 weeks, at its settings: its CPU,
// its only activity source, 6 to 9.9% of its 10% threshold through its load
// hours, idleAfter 5m, autoResume on and the balanced confidence, 75, with
// about a fifth of its pauses in its load hours scaled to zero before a
// call comes. Measured over weeks 4 to 8, as the review of P71 did. Before
// a turned-back pause taught the forecast its hour, it was paused wrongly
// about 10 times a load day, woken ahead 7 of 25 mornings, and the
// forecast left acting 11 times; with that alone, about 8 times a day, 8
// of 25 and 21 times. The hours it's held in being unseen while a request
// undid a pause within 7 days, the forecast holds its pauses off through
// the load days, wakes it ahead most mornings, and stops acting about once
// a week, as that lapses.
//
// It runs 8 weeks of reconciles, over a minute and a half under the race
// detector, so it's left out of CI. Run it with:
//
//	go test -race -tags soak -run TestHeldHours_TheSoaksOrdersAPI ./internal/controller
func TestHeldHours_TheSoaksOrdersAPI(t *testing.T) {
	monday := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	run := soakWeeks(t, monday, soak{idleAfter: 5 * time.Minute, weeks: 8, cpu: variedCPU, zero: 0.2})

	from, until := monday.AddDate(0, 0, 21), monday.AddDate(0, 0, 56)
	wrong := run.wrongPerDay(from, until)
	woken, days := run.wokenAhead(from, until)
	left := run.leftActing(from, until)
	t.Logf("weeks 4 to 8: %.2f wrong pauses a load day, %d of %d mornings woken ahead, left acting %d times, at %v",
		wrong, woken, days, len(left), left)
	assert.LessOrEqual(t, wrong, 2.5, "wrong pauses a load day, well under the 10.4 before")
	assert.Equal(t, 25, days)
	assert.GreaterOrEqual(t, woken, 20, "mornings woken ahead")
	assert.LessOrEqual(t, len(left), 6, "the forecast leaves acting about once a week at most")
}
