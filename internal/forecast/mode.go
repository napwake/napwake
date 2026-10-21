/*
Copyright 2026.

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

package forecast

// Mode says, in plain words, how soon the forecast acts on what it has
// learned: the confidence threshold it acts at. A wrong forecast only wakes
// a workload early or keeps it up a while longer, costing a little money,
// never availability, since a request still wakes it through the doorman.
type Mode string

const (
	// Eager acts sooner, and on less regular patterns, and wakes a workload
	// for nothing more often.
	Eager Mode = "eager"
	// Balanced is the default.
	Balanced Mode = "balanced"
	// Cautious acts only on patterns it forecasts well, for workloads that
	// are costly to keep awake for nothing.
	Cautious Mode = "cautious"
)

// Modes are the modes, most eager first.
var Modes = []Mode{Eager, Balanced, Cautious}

// The thresholds were chosen by simulating eight weeks of office-hours
// traffic, as kept and with people arriving up to 90 minutes either side of
// their usual time and staying away up to one weekday in three, over twenty
// seeds each (see internal/forecast/sim). On steadily kept hours every
// threshold from 50 to 95 acts on the same day and never wakes for
// nothing, so the thresholds trade only on irregular patterns: 60 keeps
// most of the ready mornings of 50, the minimum, for less waking for
// nothing; 75 roughly halves 60's hours woken for nothing, for a quarter to
// a half fewer ready mornings; and 85 roughly halves them again.
const (
	eagerThreshold    = 60
	balancedThreshold = 75
	cautiousThreshold = 85
)

// ParseMode reads a mode's name.
func ParseMode(s string) (Mode, bool) {
	switch m := Mode(s); m {
	case Eager, Balanced, Cautious:
		return m, true
	}
	return "", false
}

// Threshold is the confidence a workload's forecast acts at: confidence,
// when set, or mode's.
func Threshold(mode Mode, confidence int) int {
	if confidence > 0 {
		return confidence
	}
	return mode.Threshold()
}

// Threshold is the confidence percentage the mode acts at; a mode it
// doesn't know acts as Balanced.
func (m Mode) Threshold() int {
	switch m {
	case Eager:
		return eagerThreshold
	case Cautious:
		return cautiousThreshold
	default:
		return balancedThreshold
	}
}
