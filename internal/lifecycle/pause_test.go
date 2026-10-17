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

package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/okedeji/hybernate/internal/autoscaler"
)

func TestPause_ScalesToZero(t *testing.T) {
	c := newClient(t, deployment(3, 3))
	p := newTestPauser(c)
	workload := apiWorkload()

	require.NoError(t, p.Prepare(context.Background(), workload))
	assert.Equal(t, int32(3), replicas(t, c), "preparing changes nothing")
	assert.Nil(t, workload.Status.Pause.PausedAt)

	done, err := p.Pause(context.Background(), workload)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, int32(3), workload.Status.Pause.PreviousReplicas)
	assert.NotNil(t, workload.Status.Pause.PausedAt)
	assert.Equal(t, int32(0), replicas(t, c))
}

func TestPause_RequiresAPreparedRecord(t *testing.T) {
	c := newClient(t, deployment(3, 3))

	_, err := newTestPauser(c).Pause(context.Background(), apiWorkload())

	assert.ErrorIs(t, err, ErrNotPrepared)
	assert.Equal(t, int32(3), replicas(t, c))
}

func TestPause_Idempotent(t *testing.T) {
	c := newClient(t, deployment(0, 0))
	pausedAt := metav1.NewTime(time.Date(2026, 3, 14, 11, 0, 0, 0, time.UTC))
	workload := pausedWorkload(3)
	workload.Status.Pause.PausedAt = &pausedAt

	done, err := newTestPauser(c).Pause(context.Background(), workload)

	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, int32(3), workload.Status.Pause.PreviousReplicas)
	assert.Equal(t, &pausedAt, workload.Status.Pause.PausedAt)
}

// A pause interrupted after scaling to zero is retried from its record: the
// target, now at zero, must never become the count to restore.
func TestPause_RetryKeepsTheRecordedReplicas(t *testing.T) {
	c := newClient(t, deployment(3, 3))
	p := newTestPauser(c)
	workload := apiWorkload()

	require.NoError(t, p.Prepare(context.Background(), workload))
	_, err := p.Pause(context.Background(), workload)
	require.NoError(t, err)
	workload.Status.Pause.PausedAt = nil

	done, err := p.Pause(context.Background(), workload)

	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, int32(3), workload.Status.Pause.PreviousReplicas)
}

func TestResume_ScalesBackUp(t *testing.T) {
	c := newClient(t, deployment(0, 0))
	p := newTestPauser(c)
	workload := pausedWorkload(3)

	done, err := p.Resume(context.Background(), workload)
	require.NoError(t, err)
	assert.False(t, done, "its pods aren't Ready yet")
	assert.Equal(t, int32(3), replicas(t, c))

	settle(t, c, 3)
	done, err = p.Resume(context.Background(), workload)
	require.NoError(t, err)
	assert.True(t, done)
	assert.Equal(t, int32(3), replicas(t, c))
}

// Pods that exist but aren't Ready, such as a database replaying its log,
// mean the resume isn't done yet.
func TestResume_WaitsForReadyNotJustExisting(t *testing.T) {
	dep := deployment(3, 0)
	dep.Status.Replicas = 3
	c := newClient(t, dep)

	done, err := newTestPauser(c).Resume(context.Background(), pausedWorkload(3))

	require.NoError(t, err)
	assert.False(t, done)
}

// A status its controller wrote before seeing the scale-up still counts
// the pods from before it: right after a pause, those not yet stopped. A
// resume done by it would drop the doorman's routes before any pod of the
// resume is Ready.
func TestResume_WaitsForAStatusOfTheScaleUp(t *testing.T) {
	tests := []struct {
		name   string
		target func() *appsv1.Deployment
	}{
		{name: "status from before the scale-up", target: func() *appsv1.Deployment {
			d := deployment(3, 3)
			d.Generation = 2
			return d
		}},
		{name: "a read from before the scale-up", target: func() *appsv1.Deployment { return deployment(0, 3) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stale := tt.target()
			ready, err := checkReady(context.Background(), newClient(t, stale), stale.DeepCopy(), 3)
			require.NoError(t, err)
			assert.False(t, ready)
		})
	}
}

func TestResume_NoPauseStatusIsNoOp(t *testing.T) {
	done, err := newTestPauser(newClient(t)).Resume(context.Background(), apiWorkload())

	require.NoError(t, err)
	assert.True(t, done)
}

// Restore hands the workload back without waiting for it, never scales
// down what someone else has already scaled up, and never starts one that
// was at zero before its pause.
func TestRestore(t *testing.T) {
	tests := []struct {
		name     string
		previous int32
		current  int32
		want     int32
	}{
		{name: "still at zero", previous: 3, current: 0, want: 3},
		{name: "already scaled up by someone", previous: 3, current: 5, want: 5},
		{name: "at zero before the pause", previous: 0, current: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, deployment(tt.current, 0), scaledObjectFor(1))
			workload := pausedWorkload(tt.previous)
			workload.Status.Pause.ScaledObject = "api-scaler"
			require.NoError(t, autoscaler.HoldKEDA(context.Background(), c, "default", "api-scaler", 0))

			got, err := newTestPauser(c).Restore(context.Background(), workload)

			require.NoError(t, err)
			assert.Equal(t, tt.want, got, "says what it left the target at")
			assert.Equal(t, tt.want, replicas(t, c))
			_, held := pausedAnnotation(t, c)
			assert.False(t, held, "KEDA scales it again")
		})
	}
}

func TestRestore_TargetGoneStillReleasesKEDA(t *testing.T) {
	c := newClient(t, scaledObjectFor(1))
	workload := pausedWorkload(3)
	workload.Status.Pause.ScaledObject = "api-scaler"
	require.NoError(t, autoscaler.HoldKEDA(context.Background(), c, "default", "api-scaler", 0))

	got, err := newTestPauser(c).Restore(context.Background(), workload)
	require.NoError(t, err)
	assert.Zero(t, got)

	_, held := pausedAnnotation(t, c)
	assert.False(t, held)
}

func TestRestore_NoPauseIsNoOp(t *testing.T) {
	c := newClient(t, deployment(0, 0))

	got, err := newTestPauser(c).Restore(context.Background(), apiWorkload())

	require.NoError(t, err)
	assert.Zero(t, got)
	assert.Zero(t, replicas(t, c), "nothing Hybernate paused is started")
}
