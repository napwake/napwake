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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// syncFromGit has Argo CD set the Deployment's replicas back from Git, after
// the pause's scale to zero, as its sync does.
func (tc *undoneCase) syncFromGit(t *testing.T, after time.Duration) {
	t.Helper()
	tc.afterThePause(t, after)
	d := (&faultEnv{ctx: tc.ctx, admin: tc.admin}).deployment(t, tc.key)
	d.Spec.Replicas = ptr.To(int32(1))
	require.NoError(t, tc.admin.Update(tc.ctx, d, client.FieldOwner("argocd-controller")))
}

// notes are the notes of the events of reason on the ManagedWorkload, as
// kubectl describe would show them.
func (tc *undoneCase) notes(t *testing.T, reason string) []string {
	t.Helper()
	var out []string
	for _, e := range tc.sent.list(t, tc.key.Namespace) {
		if e.Regarding.Kind == "ManagedWorkload" && e.Reason == reason {
			out = append(out, e.Type+" "+e.Note)
		}
	}
	return out
}

// P96: Argo CD undoes a pause, so GitOpsConflict names it, and so does
// lastScaledUp. Before a pause holds, something else wakes the workload
// through the scale subresource, as kubectl scale does, and lastScaledUp is
// that scale-up instead, with no tool, while the conflict stands. The pause
// that then held an hour announced "a pause held, so  leaves the replicas to
// Napwake now", naming no tool. It says plainly which tool it means once the
// record of its undo is gone, and names the tool while it's there.
func TestGitOpsConflictResolved_AfterAScaleUpNotFromGit(t *testing.T) {
	env := startUndoneEnv(t)
	for i, tt := range []struct {
		name        string
		scaledSince bool
		want        string
	}{
		{name: "the tool's undo the last scale-up",
			want: "a pause held, so Argo CD leaves the replicas to Napwake now"},
		{name: "scaled up since through the scale subresource", scaledSince: true,
			want: "a pause held, so the GitOps tool leaves the replicas to Napwake now"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tc := env.create(t, fmt.Sprintf("gitops-%d", i), false)
			requirePauserOnItsClock(t, tc.r, tc.key.Namespace)

			tc.pause(t)
			tc.syncFromGit(t, 30*time.Second)
			w := tc.awake(t)
			require.Equal(t, "Argo CD", w.Status.LastScaledUp.GitOps)
			require.True(t, meta.IsStatusConditionTrue(w.Status.Conditions, conditionGitOpsConflict),
				"GitOpsConflict, with conditions %v", conditionTypes(w))
			tc.pausesAt(t, w.Status.LastScaledUp.At.Add(time.Hour))

			if tt.scaledSince {
				tc.pause(t)
				tc.scaleBack(t, 10*time.Minute)
				w := tc.awake(t)
				require.Equal(t, "unknown", w.Status.LastScaledUp.By)
				require.Empty(t, w.Status.LastScaledUp.GitOps, "lastScaledUp is a scale-up Git didn't make")
				require.True(t, meta.IsStatusConditionTrue(w.Status.Conditions, conditionGitOpsConflict),
					"the conflict stands until a pause holds, with conditions %v", conditionTypes(w))
				tc.pausesAt(t, w.Status.LastScaledUp.At.Add(time.Hour))
			}

			w = tc.pause(t)
			tc.now = w.Status.Pause.PausedAt.Add(time.Hour + requeueLag)
			w = tc.reconcile(t)
			require.Equal(t, v1alpha1.PhasePaused, w.Status.Phase)
			require.Nil(t, meta.FindStatusCondition(w.Status.Conditions, conditionGitOpsConflict), "cleared")
			tc.warned(t, "Normal", ReasonGitOpsConflictResolved, tt.want)
			assert.Equal(t, []string{"Normal api: " + tt.want}, tc.notes(t, ReasonGitOpsConflictResolved))
		})
	}
}
