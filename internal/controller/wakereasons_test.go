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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// Each cause of a wake has a reason of its own, so what woke a workload is
// told by its events' reasons alone (P38): a request the doorman held, a
// person's click on one that asks first, a dependent's wake, and an
// activity annotation set by anything else. The forecast's AutoResume, a
// wake request's WakeRequested and a query's WokeByActivity are as before.
func TestWake_EachCauseHasItsOwnReason(t *testing.T) {
	dependent := func(phase v1alpha1.WorkloadPhase) client.Object {
		return depWorkload("default", "api", v1alpha1.TargetKindDeployment, phase,
			v1alpha1.DependencyRef{Kind: v1alpha1.TargetKindDeployment, Name: "db"})
	}
	activity := func(t *testing.T, r *Reconciler, now time.Time) {
		annotate(t, r, "db", v1alpha1.AnnotationLastActivity, now.UTC().Format(time.RFC3339))
	}
	tests := []struct {
		name      string
		asks      bool
		dependent client.Object
		wake      func(t *testing.T, r *Reconciler, now time.Time)
		want      string
	}{
		{name: "a request", wake: func(t *testing.T, r *Reconciler, now time.Time) { requestCame(t, r, "db", now) },
			want: "Normal WokenByRequest db: a request is waiting, waking"},
		{name: "a person's click", asks: true,
			wake: func(t *testing.T, r *Reconciler, now time.Time) { requestCame(t, r, "db", now) },
			want: "Normal WokenByClick db: a person asked to wake it, waking"},
		{name: "a dependent waking", dependent: dependent(v1alpha1.PhaseResuming), wake: activity,
			want: "Normal WokenByDependent db: its dependent default/api woke, waking"},
		{name: "an activity annotation, its dependent still paused", dependent: dependent(v1alpha1.PhasePaused),
			wake: activity, want: "Normal WokeByActivity db: activity annotation is newer than the pause, waking"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objs []client.Object
			if tt.dependent != nil {
				objs = append(objs, tt.dependent)
			}
			r, now := pausedFor(t, "db", 2*time.Hour, func(w *v1alpha1.ManagedWorkload) {
				if tt.asks {
					w.Spec.Wake = &v1alpha1.WakeSpec{Mode: v1alpha1.WakeOnAsk}
				}
			}, objs...)

			tt.wake(t, r, *now)
			reconcileNamed(t, r, "db")

			require.NotEqual(t, v1alpha1.PhasePaused, fetch(t, r, "db").Status.Phase, "it's woken")
			var woke []string
			for _, e := range recorded(r.Recorder.(*events.FakeRecorder), "Normal ") {
				if strings.Contains(e, "waking") {
					woke = append(woke, e)
				}
			}
			assert.Equal(t, []string{tt.want}, woke)
		})
	}
}
