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

package controller

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/doorman"
)

// On GKE every Service carries the NEG annotation, which mustn't keep it off
// the doorman. A GKE Ingress taking the Service on, or the NEG controller
// reporting NEGs for it, takes it off while the workload stays paused, and
// it's routed again once neither holds, without waiting for a recheck.
func TestGKENEGRoutingFollowsIngressesAndServices(t *testing.T) {
	cfg := startEnvtest(t)
	opScheme := testScheme(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := client.New(cfg, client.Options{Scheme: opScheme})
	require.NoError(t, err)

	const ns, doormanNS = "shop", "hybernate-system"
	for _, name := range []string{ns, doormanNS} {
		require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))
	}
	require.NoError(t, c.Create(ctx, &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "hybernate-doorman-abc", Namespace: doormanNS,
			Labels: map[string]string{discoveryv1.LabelServiceName: "hybernate-doorman"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"10.0.0.7"},
			Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)},
		}},
	}))
	require.NoError(t, c.Create(ctx, envtestDeployment(ns, 2)))
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: ns,
			Annotations: map[string]string{annotationGKENEG: gkeAutoNEG}},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "api"},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 80}},
		},
	}
	require.NoError(t, c.Create(ctx, svc))
	require.NoError(t, createTakenOn(ctx, c, withPauseRequested(envtestWorkload(ns))))

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: opScheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)}})
	require.NoError(t, err)
	r := &Reconciler{Client: mgr.GetClient(), Scheme: opScheme, Recorder: events.NewFakeRecorder(1000),
		PodReader: mgr.GetAPIReader(), DoormanService: "hybernate-doorman", DoormanNamespace: doormanNS}
	require.NoError(t, r.SetupWithManager(mgr))
	mgrCtx, stop := context.WithCancel(context.Background())
	var running sync.WaitGroup
	running.Go(func() { assert.NoError(t, mgr.Start(mgrCtx)) })
	t.Cleanup(func() {
		stop()
		running.Wait()
	})

	workload := func() *v1alpha1.ManagedWorkload {
		var w v1alpha1.ManagedWorkload
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "api"}, &w))
		return &w
	}
	routed := func() bool {
		var list discoveryv1.EndpointSliceList
		require.NoError(t, c.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{
			discoveryv1.LabelManagedBy: doorman.ManagedBy, discoveryv1.LabelServiceName: "api"}))
		return len(list.Items) > 0
	}
	refused := func() bool {
		w := workload()
		return w.Status.Phase == v1alpha1.PhasePaused && !routed() &&
			conditionFalseWith(w, conditionWakeOnRequest, "UnsupportedLoadBalancer")
	}
	const wait, tick = 30 * time.Second, 100 * time.Millisecond

	require.Eventually(t, func() bool { return workload().Status.Phase == v1alpha1.PhasePaused && routed() },
		wait, tick, "GKE's own annotation doesn't keep the Service off the doorman")

	ingress := gceIngressTo("api")
	ingress.Namespace = ns
	require.NoError(t, c.Create(ctx, &ingress))
	require.Eventually(t, refused, wait, tick, "a GKE Ingress puts the Service in NEGs")

	ingress.Annotations[annotationIngressClass] = "nginx"
	require.NoError(t, c.Update(ctx, &ingress))
	require.Eventually(t, routed, wait, tick, "an nginx Ingress reads the doorman's EndpointSlices")

	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(svc), svc))
	svc.Annotations[annotationGKENEGStatus] = `{"network_endpoint_groups":{"80":"k8s1-abc-shop-api-80-1"}}`
	require.NoError(t, c.Update(ctx, svc))
	require.Eventually(t, refused, wait, tick, "the NEG controller reports NEGs for the Service")
}
