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

package discovery

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// officeCores is a pod requesting 100m busy at 40m on weekdays from 09:10
// to 17:30, and at 1m otherwise.
func officeCores(t time.Time) float64 {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	since := t.Sub(day)
	if t.Weekday() != time.Saturday && t.Weekday() != time.Sunday &&
		since >= 9*time.Hour+10*time.Minute && since < 17*time.Hour+30*time.Minute {
		return 0.04
	}
	return 0.001
}

func forecastScan(t *testing.T, objs ...client.Object) Workload {
	t.Helper()
	return forecastScanOf(t, 28*24*time.Hour, objs...)
}

// forecastScanOf scans the api Deployment, with history of office hours
// for as long as since, and objs beside it.
func forecastScanOf(t *testing.T, since time.Duration, objs ...client.Object) Workload {
	t.Helper()
	return byName(forecastReportOf(t, since, interceptor.Funcs{}, objs...))["api"]
}

// forecastReportOf is the scan forecastScanOf makes, its client's calls
// through funcs, and the whole report.
func forecastReportOf(t *testing.T, since time.Duration, funcs interceptor.Funcs, objs ...client.Object) *ClusterReport {
	t.Helper()
	window := 28 * 24 * time.Hour
	f := &fakePrometheus{from: scanTime.Add(-since), byNamespace: map[string][]series{testNamespace: {
		{pod: "api-7d9f8c6b5-abcde", container: "main", cores: officeCores},
	}}}
	opts := scanOptions(testNamespace)
	opts.History, opts.Window, opts.IdleAfter = newFakePrometheus(t, f), window, time.Hour
	opts.Forecast = &ForecastOptions{Location: time.UTC}
	runtimeObjs := deploymentWithRollout("api", testNamespace, 1, 60*24*time.Hour)
	c := interceptor.NewClient(fake.NewClientBuilder().WithScheme(newScheme()).WithRuntimeObjects(runtimeObjs...).
		WithObjects(objs...).Build(), funcs)

	report, err := NewScanner(c, c).ScanCluster(context.Background(), opts)
	require.NoError(t, err)
	return report
}

// Four weeks of office hours: opted in now, the forecast acts at once, and
// over the last two weeks, having learned the first two, it would have had
// the workload up ahead of people every weekday morning.
func TestScanCluster_ReplaysTheForecast(t *testing.T) {
	w := forecastScan(t)

	require.NotNil(t, w.Forecast)
	f := w.Forecast
	assert.Equal(t, 75, f.Threshold)
	assert.True(t, f.IfOptedIn.Acting)
	assert.Nil(t, f.Live)
	require.NotNil(t, f.Replay)
	assert.Equal(t, 10, f.Replay.UsedDays)
	assert.Equal(t, 10, f.Replay.Ready, "%+v", f.Replay)
	assert.Zero(t, f.Replay.Missed, "the forecast acting never sleeps through a morning")
	assert.Zero(t, f.Replay.WastedWakes)
	assert.Equal(t, 25*time.Minute, f.Replay.MedianLead, "woken at 08:45, people at 09:10")
	for _, d := range f.Replay.Days {
		if d.WokeAt != nil {
			assert.Equal(t, "08:45", d.WokeAt.Format("15:04"))
		}
	}
}

// A workload Napwake manages is replayed with its own settings, and its
// forecast's own outlook is shown beside the replay.
func TestScanCluster_ReplaysAManagedWorkloadsForecast(t *testing.T) {
	starts := metav1.NewTime(scanTime.Add(48 * time.Hour))
	mw := &v1alpha1.ManagedWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: testNamespace},
		Spec: v1alpha1.ManagedWorkloadSpec{
			Target:     v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: "api"},
			Prediction: v1alpha1.PredictionSpec{Mode: v1alpha1.PredictionCautious},
		},
		Status: v1alpha1.ManagedWorkloadStatus{Phase: v1alpha1.PhaseRunning, Prediction: &v1alpha1.PredictionStatus{
			DailyConfidence: 62, Reason: "EarningConfidence", Message: "learning", StartsActing: &starts}},
	}

	w := forecastScan(t, mw)

	require.NotNil(t, w.Forecast)
	assert.Equal(t, 85, w.Forecast.Threshold, "its own mode's")
	require.NotNil(t, w.Forecast.Live)
	assert.Equal(t, "EarningConfidence", w.Forecast.Live.Waiting)
	assert.Equal(t, 62, w.Forecast.Live.Confidence)
	require.NotNil(t, w.Forecast.Live.StartsActing)
	assert.True(t, starts.Time.Equal(*w.Forecast.Live.StartsActing))
}

// The replay wakes a paused workload as Napwake would: through the doorman
// when one of its Services can be routed there, with its activity query
// when it has one, and by hand otherwise, the hours it's paused then going
// unseen. Two weeks of history leave the forecast a week to learn before it
// acts, so what it sees of the workload's nights decides the mornings ready.
func TestScanCluster_ReplaysWithTheWorkloadsOwnWakes(t *testing.T) {
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: testNamespace},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "api"}, ClusterIP: "10.96.0.10",
			Ports: []corev1.ServicePort{{Port: 80}}}}
	worker := &v1alpha1.ManagedWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: testNamespace},
		Spec: v1alpha1.ManagedWorkloadSpec{
			Target: v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: "api"},
			IdlePolicy: &v1alpha1.IdlePolicySpec{Activity: &v1alpha1.ActivitySpec{Prometheus: []v1alpha1.PrometheusActivity{
				{PromQL: `sum(rabbitmq_queue_messages{queue="tasks"})`}}}},
		},
		Status: v1alpha1.ManagedWorkloadStatus{Phase: v1alpha1.PhaseRunning},
	}
	tests := []struct {
		name      string
		objs      []client.Object
		want      ReplayWakes
		wantReady int
	}{
		{name: "behind the doorman", objs: []client.Object{service}, want: ReplayWakes{Request: true}, wantReady: 5},
		{name: "a queue worker its query wakes", objs: []client.Object{worker},
			want: ReplayWakes{Query: true, NoRequest: "no Service routes requests to it"}, wantReady: 5},
		{name: "with nothing to wake it", want: ReplayWakes{NoRequest: "no Service routes requests to it"}, wantReady: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := forecastScanOf(t, 14*24*time.Hour, tt.objs...)

			require.NotNil(t, w.Forecast)
			assert.Equal(t, tt.want, w.Forecast.Wakes)
			require.NotNil(t, w.Forecast.Replay)
			assert.Equal(t, 5, w.Forecast.Replay.UsedDays)
			assert.Equal(t, tt.wantReady, w.Forecast.Replay.Ready)
		})
	}
}

// What would wake a paused workload is the operator's verdict on its
// routing where it has one, and otherwise judged from its namespace's
// Services as the doorman routes them.
func TestReplayWakes(t *testing.T) {
	pods := map[string]string{"app": "api", "tier": "web"}
	service := func(mutate func(*corev1.Service)) map[string]corev1.Service {
		svc := corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api"}, Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "api"}, ClusterIP: "10.96.0.10", Ports: []corev1.ServicePort{{Port: 80}}}}
		if mutate != nil {
			mutate(&svc)
		}
		return map[string]corev1.Service{svc.Name: svc}
	}
	managed := func(mutate func(*v1alpha1.ManagedWorkload)) *v1alpha1.ManagedWorkload {
		mw := &v1alpha1.ManagedWorkload{}
		mutate(mw)
		return mw
	}
	routing := func(status metav1.ConditionStatus, reason string) func(*v1alpha1.ManagedWorkload) {
		return func(mw *v1alpha1.ManagedWorkload) {
			mw.Status.Conditions = []metav1.Condition{{Type: "WakeOnRequest", Status: status, Reason: reason}}
		}
	}
	local := corev1.ServiceInternalTrafficPolicyLocal
	neg := func(s *corev1.Service) {
		s.Annotations = map[string]string{"cloud.google.com/neg-status": `{"network_endpoint_groups":{"80":"k8s1-neg"}}`}
	}
	tests := []struct {
		name     string
		mw       *v1alpha1.ManagedWorkload
		asks     bool
		services map[string]corev1.Service
		install  Install
		want     ReplayWakes
	}{
		{name: "a Service selects its pods", services: service(nil), want: ReplayWakes{Request: true}},
		{name: "no Service", services: map[string]corev1.Service{},
			want: ReplayWakes{NoRequest: noRequestNoService}},
		{name: "a Service selecting other pods", services: service(func(s *corev1.Service) {
			s.Spec.Selector = map[string]string{"app": "web"}
		}), want: ReplayWakes{NoRequest: noRequestNoService}},
		{name: "a headless Service", services: service(func(s *corev1.Service) { s.Spec.ClusterIP = "None" }),
			want: ReplayWakes{NoRequest: noRequestNoService}},
		{name: "only UDP", services: service(func(s *corev1.Service) {
			s.Spec.Ports[0].Protocol = corev1.ProtocolUDP
		}), want: ReplayWakes{NoRequest: noRequestNoService}},
		{name: "a Service in GKE NEGs", services: service(neg), want: ReplayWakes{NoRequest: noRequestNEGs}},
		{name: "a Service in GKE NEGs, with the doorman's forwarder on", services: service(neg),
			install: Install{NEGForwarder: true}, want: ReplayWakes{Request: true}},
		{name: "managed and awake, in GKE NEGs, with the forwarder on, as routed at its last pause",
			services: service(neg), install: Install{NEGForwarder: true},
			mw: managed(routing(metav1.ConditionFalse, "NotPaused")), want: ReplayWakes{Request: true}},
		{name: "the doorman turned off for the cluster", services: service(nil), install: Install{DoormanOff: true},
			want: ReplayWakes{NoRequest: noRequestDoormanOff}},
		{name: "the doorman turned off, and an activity query", services: service(nil),
			install: Install{DoormanOff: true}, mw: managed(func(mw *v1alpha1.ManagedWorkload) {
				mw.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{Activity: &v1alpha1.ActivitySpec{
					Prometheus: []v1alpha1.PrometheusActivity{{PromQL: "queue_length"}}}}
			}), want: ReplayWakes{Query: true, NoRequest: noRequestDoormanOff}},
		{name: "a node-local Service", services: service(func(s *corev1.Service) {
			s.Spec.InternalTrafficPolicy = &local
		}), want: ReplayWakes{NoRequest: noRequestNodeLocal}},
		{name: "Services not read, as Napwake's default", want: ReplayWakes{Request: true}},
		{name: "asking, by the annotation it would be opted in with", services: service(nil), asks: true,
			want: ReplayWakes{People: true, NoRequest: noRequestAsks}},
		{name: "managed, asking", services: service(nil), mw: managed(func(mw *v1alpha1.ManagedWorkload) {
			mw.Spec.Wake = &v1alpha1.WakeSpec{Mode: v1alpha1.WakeOnAsk}
		}), want: ReplayWakes{People: true, NoRequest: noRequestAsks}},
		{name: "asking, with the doorman turned off, so nobody can click", services: service(nil), asks: true,
			install: Install{DoormanOff: true}, want: ReplayWakes{NoRequest: noRequestDoormanOff}},
		{name: "asking, with no Service the doorman could route", services: map[string]corev1.Service{},
			asks: true, want: ReplayWakes{NoRequest: noRequestNoService}},
		{name: "managed, asking, its Services unroutable at its last pause", services: service(nil),
			mw: managed(func(mw *v1alpha1.ManagedWorkload) {
				mw.Spec.Wake = &v1alpha1.WakeSpec{Mode: v1alpha1.WakeOnAsk}
				routing(metav1.ConditionFalse, "NoServices")(mw)
			}), want: ReplayWakes{NoRequest: noRequestNoService}},
		{name: "managed, waking on request, whatever it would be opted in with", services: service(nil), asks: true,
			mw: managed(func(mw *v1alpha1.ManagedWorkload) {
				mw.Spec.Wake = &v1alpha1.WakeSpec{Mode: v1alpha1.WakeOnRequest}
			}), want: ReplayWakes{Request: true}},
		{name: "managed, asking, with an activity query", services: service(nil),
			mw: managed(func(mw *v1alpha1.ManagedWorkload) {
				mw.Spec.Wake = &v1alpha1.WakeSpec{Mode: v1alpha1.WakeOnAsk}
				mw.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{Activity: &v1alpha1.ActivitySpec{
					Prometheus: []v1alpha1.PrometheusActivity{{PromQL: "queue_length"}}}}
			}), want: ReplayWakes{Query: true, People: true, NoRequest: noRequestAsks}},
		{name: "managed and routed while paused", services: map[string]corev1.Service{},
			mw: managed(routing(metav1.ConditionTrue, "DoormanRouted")), want: ReplayWakes{Request: true}},
		{name: "managed, its Services in NEGs at its last pause", services: service(nil),
			mw:   managed(routing(metav1.ConditionFalse, "UnsupportedLoadBalancer")),
			want: ReplayWakes{NoRequest: noRequestNEGs}},
		{name: "managed and awake, judged from its Services", services: service(nil),
			mw: managed(routing(metav1.ConditionFalse, "NotPaused")), want: ReplayWakes{Request: true}},
		{name: "managed with an activity query and no Service", services: map[string]corev1.Service{},
			mw: managed(func(mw *v1alpha1.ManagedWorkload) {
				mw.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{Activity: &v1alpha1.ActivitySpec{
					Prometheus: []v1alpha1.PrometheusActivity{{PromQL: "queue_length"}}}}
			}), want: ReplayWakes{Query: true, NoRequest: noRequestNoService}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, replayWakes(tt.mw, tt.asks, pods, tt.services, tt.install))
		})
	}
}

// The replay reads the install from its operator's Deployment: one run with
// doorman.enabled: false wakes nothing on request, however its workloads'
// Services are set up.
func TestScanCluster_ReplaysWithTheInstallsDoorman(t *testing.T) {
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: testNamespace},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "api"}, ClusterIP: "10.96.0.10",
			Ports: []corev1.ServicePort{{Port: 80}}}}

	w := forecastScanOf(t, 14*24*time.Hour, service, operatorDeployment("--doorman-service="))

	require.NotNil(t, w.Forecast)
	assert.Equal(t, ReplayWakes{NoRequest: noRequestDoormanOff}, w.Forecast.Wakes)
	require.NotNil(t, w.Forecast.Replay)
	assert.Equal(t, 2, w.Forecast.Replay.Ready, "as with nothing to wake it")
}

// A workload not yet managed would have the doorman ask before waking it
// as the ManagedWorkload opting it in would: by its own napwake.dev/wake,
// then its namespace's, then its install's default, where a value that
// can't be read asks.
func TestOptedInAsks(t *testing.T) {
	ask := map[string]string{v1alpha1.AnnotationWake: "ask"}
	request := map[string]string{v1alpha1.AnnotationWake: "request"}
	invalid := map[string]string{v1alpha1.AnnotationWake: "no"}
	tests := []struct {
		name                string
		workload, namespace map[string]string
		install             Install
		want                bool
	}{
		{name: "nothing says"},
		{name: "the install's default", install: Install{Asks: true}, want: true},
		{name: "the workload asks", workload: ask, want: true},
		{name: "its namespace asks", namespace: ask, want: true},
		{name: "the workload's own wins", workload: request, namespace: ask, install: Install{Asks: true}},
		{name: "a value that can't be read asks, whatever its namespace says", workload: invalid, namespace: request,
			want: true},
		{name: "spaces around it are ignored", workload: map[string]string{v1alpha1.AnnotationWake: " ask\n"},
			want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, optedInAsks(tt.workload, tt.namespace, tt.install))
		})
	}
}

// A namespace that has the doorman ask before waking the workloads it opts
// in has the replay wake its workloads only for people, as they come. Only
// people can wake it, so the forecast learns the hours it's paused, as it
// does behind the doorman: having learned only the first week, it acts in
// the second, and has the workload ready on the same mornings.
func TestScanCluster_ReplaysWithTheNamespacesWake(t *testing.T) {
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: testNamespace},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "api"}, ClusterIP: "10.96.0.10",
			Ports: []corev1.ServicePort{{Port: 80}}}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace,
		Annotations: map[string]string{v1alpha1.AnnotationWake: "ask"}}}

	w := forecastScanOf(t, 14*24*time.Hour, service, ns)
	onRequest := forecastScanOf(t, 14*24*time.Hour, service)

	require.NotNil(t, w.Forecast)
	assert.Equal(t, ReplayWakes{People: true, NoRequest: noRequestAsks}, w.Forecast.Wakes)
	require.NotNil(t, w.Forecast.Replay)
	require.NotNil(t, onRequest.Forecast.Replay)
	assert.Positive(t, w.Forecast.Replay.Ready, "%+v", w.Forecast.Replay)
	assert.Equal(t, onRequest.Forecast.Replay.Ready, w.Forecast.Replay.Ready)
}

// An install whose operator asks by default has the replay ask for the
// workloads it would opt in that don't say otherwise.
func TestScanCluster_ReplaysWithTheInstallsWake(t *testing.T) {
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: testNamespace},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "api"}, ClusterIP: "10.96.0.10",
			Ports: []corev1.ServicePort{{Port: 80}}}}

	w := forecastScanOf(t, 14*24*time.Hour, service, operatorDeployment("--default-wake=ask"))

	require.NotNil(t, w.Forecast)
	assert.Equal(t, ReplayWakes{People: true, NoRequest: noRequestAsks}, w.Forecast.Wakes)
}

// deploymentsOnlyIn has the API server refuse lists of Deployments outside
// allowed, as for a user who may list them only in their own namespaces.
func deploymentsOnlyIn(allowed ...string) interceptor.Funcs {
	return interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList,
		opts ...client.ListOption) error {
		var o client.ListOptions
		o.ApplyOptions(opts)
		if _, ok := list.(*appsv1.DeploymentList); ok && !slices.Contains(allowed, o.Namespace) {
			return apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "",
				errors.New("not allowed in "+cmp.Or(o.Namespace, "the cluster")))
		}
		return c.List(ctx, list, opts...)
	}}
}

// A user who can't list Deployments across the cluster still has the
// operator read in its own namespace. One who can't list them there either
// is told the doorman's defaults were assumed, why, and what access reads
// them, in forecast's notes, and scan, which doesn't read the operator,
// says nothing of it.
func TestScanCluster_SaysWhenTheInstallCouldntBeRead(t *testing.T) {
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: testNamespace},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "api"}, ClusterIP: "10.96.0.10",
			Ports: []corev1.ServicePort{{Port: 80}}}}
	operator := operatorDeployment("--doorman-service=")
	installNote := func(notes []string) string {
		for _, n := range notes {
			if strings.Contains(n, "doorman") {
				return n
			}
		}
		return ""
	}

	inItsNamespace := forecastReportOf(t, 14*24*time.Hour, deploymentsOnlyIn(testNamespace, "napwake-system"),
		service, operator)
	assert.Equal(t, ReplayWakes{NoRequest: noRequestDoormanOff}, byName(inItsNamespace)["api"].Forecast.Wakes,
		"read in the operator's own namespace")
	assert.Empty(t, installNote(inItsNamespace.Notes))

	unread := forecastReportOf(t, 14*24*time.Hour, deploymentsOnlyIn(testNamespace), service, operator)
	assert.Equal(t, ReplayWakes{Request: true}, byName(unread)["api"].Forecast.Wakes, "Napwake's defaults")
	note := installNote(unread.Notes)
	assert.Contains(t, note, "couldn't read Napwake's operator to see whether its doorman and NEG forwarder are on")
	assert.Contains(t, note, "not allowed in the cluster", "why")
	assert.Contains(t, note, "so the replay assumes Napwake's defaults, the doorman on and the forwarder off")
	assert.Contains(t, note, "let your user list Deployments in the operator's namespace, napwake-system by "+
		"default, or across the cluster")

	c := interceptor.NewClient(fake.NewClientBuilder().WithScheme(newScheme()).
		WithRuntimeObjects(deploymentWithRollout("api", testNamespace, 1, 60*24*time.Hour)...).
		WithObjects(service, operator).Build(), deploymentsOnlyIn(testNamespace))
	scan, err := NewScanner(c, c).ScanCluster(context.Background(), scanOptions(testNamespace))
	require.NoError(t, err)
	assert.Empty(t, installNote(scan.Notes), "scan doesn't read the operator")
}
