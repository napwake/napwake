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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestParseCPUThreshold(t *testing.T) {
	valid := map[string]float64{"10": 10, "0.5": 0.5, "0.1": 0.1, "100": 100, "12.25": 12.25, " 15 ": 15, "1": 1,
		"07": 7}
	for in, want := range valid {
		got, err := ParseCPUThreshold(in)
		require.NoError(t, err, in)
		assert.InDelta(t, want, got, 1e-9, in)
	}
	for _, in := range []string{"", "0", "0.05", "100.5", "101", "0.125", "-1", "1e1", "ten", ".5", "5.", "NaN"} {
		_, err := ParseCPUThreshold(in)
		assert.Error(t, err, in)
	}
}

func TestCPUThreshold_ReadsAndWritesBothForms(t *testing.T) {
	tests := []struct {
		value   *intstr.IntOrString
		percent float64
		written string
	}{
		{value: ptrTo(intstr.FromInt32(10)), percent: 10, written: "10"},
		{value: ptrTo(intstr.FromString("0.5")), percent: 0.5, written: "0.5"},
		{value: ptrTo(intstr.FromString("12.25")), percent: 12.25, written: "12.25"},
	}
	for _, tt := range tests {
		a := &ActivitySpec{CPUThreshold: tt.value}
		got, ok := a.CPUThresholdPercent()
		require.True(t, ok)
		assert.InDelta(t, tt.percent, got, 1e-9)
		assert.Equal(t, tt.written, FormatCPUThreshold(got))
		assert.Equal(t, *tt.value, *CPUThresholdValue(got), "a whole number is written as one, a decimal as a string")
	}

	var unset *ActivitySpec
	_, ok := unset.CPUThresholdPercent()
	assert.False(t, ok)
	_, ok = (&ActivitySpec{}).CPUThresholdPercent()
	assert.False(t, ok)
	_, ok = (&ActivitySpec{CPUThreshold: ptrTo(intstr.FromString("lots"))}).CPUThresholdPercent()
	assert.False(t, ok, "one the API server would have refused counts as unset")
	assert.Equal(t, "0.4", FormatCPUThreshold(0.40000000001), "to two places at most")
}

func ptrTo[T any](v T) *T { return &v }
