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

package human

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{in: 0, want: "0 seconds"},
		{in: -time.Minute, want: "0 seconds"},
		{in: 300 * time.Millisecond, want: "under a second"},
		{in: time.Second, want: "1 second"},
		{in: 45 * time.Second, want: "45 seconds"},
		{in: time.Minute, want: "1 minute"},
		{in: 90 * time.Second, want: "90 seconds"},
		{in: 119*time.Second + 600*time.Millisecond, want: "2 minutes"},
		{in: 2*time.Minute + 20*time.Second, want: "2 minutes"},
		{in: 5 * time.Minute, want: "5 minutes"},
		{in: 59*time.Minute + 40*time.Second, want: "1 hour"},
		{in: time.Hour, want: "1 hour"},
		{in: 90 * time.Minute, want: "1 hour 30 minutes"},
		{in: 3*time.Hour + 12*time.Minute, want: "3 hours 12 minutes"},
		{in: 47*time.Hour + 59*time.Minute + 50*time.Second, want: "2 days"},
		{in: 96*time.Hour + 12*time.Minute, want: "4 days"},
		{in: 98*time.Hour + 40*time.Minute, want: "4 days 3 hours"},
	}
	for _, tt := range tests {
		t.Run(tt.in.String(), func(t *testing.T) {
			assert.Equal(t, tt.want, Duration(tt.in))
		})
	}
}
