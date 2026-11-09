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
	"context"
	"errors"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/napwake/napwake/internal/metrics"
)

// stopped says whether err is only the reconcile being stopped, its own
// context cancelled, as every reconcile in flight is when the operator
// stops. That's no failure to report: the next reconcile goes on. A call
// cancelled within a reconcile that goes on, or one that timed out, is.
func stopped(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) && errors.Is(ctx.Err(), context.Canceled)
}

// endReconcile retries a write that lost to a newer one at the priority of
// what brought it, such as a wake, takes a reconcile stopped as the
// operator stops for no failure, and counts what's left as one.
func endReconcile(ctx context.Context, req ctrl.Request, res ctrl.Result, err error) (ctrl.Result, error) {
	res, err = retryIfStale(ctx, res, err)
	if err != nil && stopped(ctx, err) {
		log.FromContext(ctx).Info("stopped reconciling, as the reconcile was stopped",
			"workload", req.Name, "namespace", req.Namespace)
		return ctrl.Result{}, nil
	}
	if err != nil {
		metrics.ReconcileErrors.WithLabelValues("managedworkload").Inc()
	}
	return res, err
}
