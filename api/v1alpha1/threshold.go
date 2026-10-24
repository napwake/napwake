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

package v1alpha1

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/intstr"
)

// MinCPUThreshold and MaxCPUThreshold bound idlePolicy.activity.cpuThreshold
// and the napwake.dev/cpu-threshold annotation, as percentages of the
// workload's CPU requests.
const (
	MinCPUThreshold = 0.1
	MaxCPUThreshold = 100
)

// cpuThresholdPattern is how a CPU threshold is written: a whole number, or
// a decimal with at most two places, as the CRD's pattern also says.
var cpuThresholdPattern = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,2})?$`)

// ParseCPUThreshold reads a CPU threshold written as the annotation and the
// field's string form take it, such as "10" or "0.5": a percentage from
// MinCPUThreshold to MaxCPUThreshold, to at most two decimal places.
func ParseCPUThreshold(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if !cpuThresholdPattern.MatchString(s) {
		return 0, fmt.Errorf("%q isn't a percentage such as 10 or 0.5", s)
	}
	percent, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("reading %q: %w", s, err)
	}
	if percent < MinCPUThreshold || percent > MaxCPUThreshold {
		return 0, fmt.Errorf("%s isn't from %s to %s", s, FormatCPUThreshold(MinCPUThreshold),
			FormatCPUThreshold(MaxCPUThreshold))
	}
	return percent, nil
}

// FormatCPUThreshold writes a CPU threshold as the annotation takes it, and
// as messages give it: "10", "0.5", to at most two decimal places.
func FormatCPUThreshold(percent float64) string {
	return strconv.FormatFloat(math.Round(percent*100)/100, 'f', -1, 64)
}

// CPUThresholdValue is a CPU threshold as the field holds it: a whole number
// as an integer, and anything else as a string, since Kubernetes APIs don't
// carry floating-point numbers.
func CPUThresholdValue(percent float64) *intstr.IntOrString {
	written := FormatCPUThreshold(percent)
	if whole, err := strconv.ParseInt(written, 10, 32); err == nil {
		v := intstr.FromInt32(int32(whole))
		return &v
	}
	v := intstr.FromString(written)
	return &v
}

// CPUThresholdPercent is the CPU threshold set, as a percentage, and false
// when none is, or one that can't be read is, which the API server's
// validation rules out.
func (a *ActivitySpec) CPUThresholdPercent() (float64, bool) {
	if a == nil || a.CPUThreshold == nil {
		return 0, false
	}
	if a.CPUThreshold.Type == intstr.Int {
		percent := float64(a.CPUThreshold.IntVal)
		return percent, percent >= MinCPUThreshold && percent <= MaxCPUThreshold
	}
	percent, err := ParseCPUThreshold(a.CPUThreshold.StrVal)
	return percent, err == nil
}

// MaxIdleWaitAfterWrongPauses is how much longer than idleAfter the idle
// clock waits, at most, after pauses made while the workload was in use.
const MaxIdleWaitAfterWrongPauses = time.Hour

// IdleWait is how long a workload's idle clock waits without activity
// before a pause: idleAfter, doubled for each of wrongPauses, the pauses in
// a row something undid soon after they began, and never more than
// idleAfter plus MaxIdleWaitAfterWrongPauses. It's never less than
// idleAfter.
func IdleWait(idleAfter time.Duration, wrongPauses int32) time.Duration {
	limit := idleAfter + MaxIdleWaitAfterWrongPauses
	wait := idleAfter
	for n := int32(0); n < wrongPauses && wait > 0 && wait < limit; n++ {
		wait *= 2
	}
	return min(wait, limit)
}

// MaxWrongPauseWindow caps how soon after a pause began the workload's own
// use makes it wrong. Steady light use wakes a wrong pause within seconds
// or a minute or two; a pause that saved a quarter of an hour paid for its
// cold start, and a wake after that is the next use, which idleAfter
// allows for. Under a short idleAfter, the window is idleAfter itself: the
// user said that's how long without use is idle.
const MaxWrongPauseWindow = 15 * time.Minute

// WrongPauseWindow is how soon after a pause began the workload's own use
// makes the pause wrong, and lengthens IdleWait: idleAfter, or
// MaxWrongPauseWindow if that's shorter. A pause that lasts it held.
func WrongPauseWindow(idleAfter time.Duration) time.Duration {
	return min(idleAfter, MaxWrongPauseWindow)
}
