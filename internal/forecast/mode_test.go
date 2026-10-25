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

package forecast

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMode(t *testing.T) {
	for _, tt := range []struct {
		name string
		want int
	}{{"eager", 60}, {"balanced", 75}, {"cautious", 85}} {
		m, ok := ParseMode(tt.name)
		require.True(t, ok, tt.name)
		assert.Equal(t, tt.want, m.Threshold(), tt.name)
	}
	_, ok := ParseMode("Eager")
	assert.False(t, ok, "names are lowercase, as the annotation and the spec spell them")
	assert.Equal(t, 75, Mode("").Threshold())
}

func TestThreshold(t *testing.T) {
	assert.Equal(t, 60, Threshold(Eager, 0))
	assert.Equal(t, 90, Threshold(Eager, 90), "a confidence overrides the mode")
	assert.Equal(t, 75, Threshold("", 0))
}
