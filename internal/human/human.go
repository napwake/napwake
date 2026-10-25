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

// Package human writes values the way people say them, for the events and
// condition messages the operator and doorman show in kubectl describe.
package human

import (
	"fmt"
	"time"
)

const day = 24 * time.Hour

// Duration writes d as a person would say it, to the precision that
// matters at its size: seconds under 2 minutes ("90 seconds"), minutes
// under an hour ("5 minutes"), hours and minutes under 2 days ("1 hour 30
// minutes"), and days and hours beyond ("4 days 3 hours").
func Duration(d time.Duration) string {
	switch {
	case d <= 0:
		return "0 seconds"
	case d < 2*time.Minute:
		d = d.Round(time.Second)
	case d < 2*day:
		d = d.Round(time.Minute)
	default:
		d = d.Round(time.Hour)
	}
	switch {
	case d == 0:
		return "under a second"
	case d < 2*time.Minute && d%time.Minute != 0:
		return count(d/time.Second, "second")
	case d < time.Hour:
		return count(d/time.Minute, "minute")
	case d < 2*day:
		return both(count(d/time.Hour, "hour"), count(d%time.Hour/time.Minute, "minute"))
	default:
		return both(count(d/day, "day"), count(d%day/time.Hour, "hour"))
	}
}

func count(n time.Duration, unit string) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "1 " + unit
	default:
		return fmt.Sprintf("%d %ss", n, unit)
	}
}

func both(major, minor string) string {
	if minor == "" {
		return major
	}
	return major + " " + minor
}
