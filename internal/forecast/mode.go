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
// seeds each (see internal/forecast/sim). Below 75, a steady office
// workload starts acting no sooner but begins waking on its first
// weekends; 60 has the first ready mornings of 50, the minimum, on every
// pattern but the most irregular, for about half the hours woken for
// nothing. Above 75 a steady workload starts a week later, and 85 avoids
// most of what waking for nothing is left.
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
