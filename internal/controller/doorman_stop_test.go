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
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// A reconcile stopped as the operator stops, its context cancelled, isn't
// a routing failure: it's logged at info, with no error, no RoutingFailed
// condition or event, and no back-off, so a rollout logs no errors for
// what was only cut short. Any other error, a cancelled call within a
// reconcile that goes on included, is still a failure.
func TestRouteDoorman_AStoppedReconcileIsNoFailure(t *testing.T) {
	tests := []struct {
		name     string
		cancel   bool
		err      func(ctx context.Context) error
		failure  bool
		logLevel string
	}{
		{name: "the reconcile stopped", cancel: true, err: func(ctx context.Context) error { return ctx.Err() },
			logLevel: `"level":"info"`},
		{name: "a call cancelled within a reconcile that goes on",
			err: func(context.Context) error { return context.Canceled }, failure: true, logLevel: `"level":"error"`},
		{name: "a call that timed out", err: func(context.Context) error { return context.DeadlineExceeded },
			failure: true, logLevel: `"level":"error"`},
		{name: "a refusal while stopping", cancel: true,
			err: func(context.Context) error { return errors.New("endpointslices is forbidden") }, failure: true,
			logLevel: `"level":"error"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workload := lifecycleWorkload("api", v1alpha1.PhasePaused)
			r := doormanReconciler(t, workload, doormanEndpoints("10.0.0.7"),
				service("api", map[string]string{"app": "api"}, corev1.ServicePort{Name: "http", Port: 80}))
			fail := func(ctx context.Context) error { return tt.err(ctx) }
			r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
				Create: func(ctx context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
					return fail(ctx)
				}})
			var logs bytes.Buffer
			ctx, cancel := context.WithCancel(log.IntoContext(context.Background(), zap.New(zap.WriteTo(&logs))))
			defer cancel()
			if tt.cancel {
				cancel()
			}

			recheck := r.routeDoorman(ctx, workload, appTarget())

			require.Contains(t, logs.String(), tt.logLevel, logs.String())
			routingFailed := conditionFalseWith(workload, conditionWakeOnRequest, reasonRoutingFailed)
			assert.Equal(t, tt.failure, routingFailed, "the RoutingFailed condition")
			recorder := r.Recorder.(*events.FakeRecorder)
			announced := false
			for len(recorder.Events) > 0 {
				announced = announced || strings.Contains(<-recorder.Events, reasonRoutingFailed)
			}
			assert.Equal(t, tt.failure, announced, "the RoutingFailed event")
			if tt.failure {
				assert.Equal(t, doormanRetryMin, recheck, "retried after a failure")
				return
			}
			assert.Zero(t, recheck)
			assert.NotContains(t, logs.String(), `"level":"error"`)
		})
	}
}
