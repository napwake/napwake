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
	"maps"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/autoscaler"
	"github.com/napwake/napwake/internal/doorman"
	"github.com/napwake/napwake/internal/forecast"
	"github.com/napwake/napwake/internal/lifecycle"
)

// ingressEnv is an API server with a Ready doorman pod, in which each case
// gets a namespace with the Deployment api, the Service api selecting it,
// the Ingresses it asks for, and a ManagedWorkload that can be asked to
// pause.
type ingressEnv struct {
	ctx       context.Context
	admin     client.Client
	doormanNS string
}

func startIngressEnv(t *testing.T) *ingressEnv {
	t.Helper()
	cfg := startEnvtest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	admin, err := client.New(cfg, client.Options{Scheme: testScheme(t)})
	require.NoError(t, err)
	e := &ingressEnv{ctx: ctx, admin: admin, doormanNS: "napwake-system"}
	require.NoError(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: e.doormanNS}}))
	require.NoError(t, admin.Create(ctx, &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "napwake-doorman-abc", Namespace: e.doormanNS,
			Labels: map[string]string{discoveryv1.LabelServiceName: "napwake-doorman"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{"10.0.0.7"},
			Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)},
		}},
	}))
	return e
}

// ingressCase is one workload, and the operator's clock, which the case
// moves, starting at the real clock's reading, monotonic and all.
type ingressCase struct {
	env      *ingressEnv
	key      client.ObjectKey
	now      time.Time
	forecast *stubForecaster
	pauses   int
}

func (e *ingressEnv) start(t *testing.T, ns string, setup func(*v1alpha1.ManagedWorkload),
	ingresses ...networkingv1.Ingress) *ingressCase {
	t.Helper()
	ctx := e.ctx
	require.NoError(t, e.admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	target := envtestDeployment(ns, 1)
	target.Spec.Template.Spec.Containers[0].ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
		HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(8080)}}}
	require.NoError(t, e.admin.Create(ctx, target))
	require.NoError(t, e.admin.Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: ns},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "api"},
			Ports:    []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080)}},
		},
	}))
	for i := range ingresses {
		ingresses[i].Namespace = ns
		require.NoError(t, e.admin.Create(ctx, &ingresses[i]))
	}
	w := envtestWorkload(ns)
	if setup != nil {
		setup(w)
	}
	require.NoError(t, createTakenOn(ctx, e.admin, w))
	return &ingressCase{env: e, key: client.ObjectKeyFromObject(w), now: time.Now(), forecast: &stubForecaster{}}
}

// reconciler is an operator as it starts: nothing in memory, its own
// events.
func (c *ingressCase) reconciler(t *testing.T) *Reconciler {
	t.Helper()
	cl := client.WithFieldOwner(indexedClient{c.env.admin}, v1alpha1.FieldManager)
	finder := autoscaler.NewFinder(cl)
	return &Reconciler{
		Client: cl, Scheme: testScheme(t), Recorder: events.NewFakeRecorder(1000),
		pauser: lifecycle.NewPauser(cl, finder), autoscalers: finder,
		metrics:        &stubMetrics{cpuPerReplica: 100, memoryPerReplica: 1 << 30, replicas: 1},
		engines:        newEngineRegistry(func() forecaster { return c.forecast }),
		DoormanService: "napwake-doorman", DoormanNamespace: c.env.doormanNS,
		clock: func() time.Time { return c.now },
	}
}

func (c *ingressCase) workload(t *testing.T) *v1alpha1.ManagedWorkload {
	t.Helper()
	var w v1alpha1.ManagedWorkload
	require.NoError(t, c.env.admin.Get(c.env.ctx, c.key, &w))
	return &w
}

func (c *ingressCase) reconcile(t *testing.T, r *Reconciler) {
	t.Helper()
	_, err := r.Reconcile(c.env.ctx, ctrl.Request{NamespacedName: c.key})
	require.NoError(t, err)
}

// settle stands in for the Deployment controller: the pods asked for are
// Ready at once.
func (c *ingressCase) settle(t *testing.T) {
	t.Helper()
	c.ready(t, -1)
}

// ready stands in for the Deployment controller with n of the pods asked
// for Ready, or all of them for -1.
func (c *ingressCase) ready(t *testing.T, n int32) {
	t.Helper()
	var d appsv1.Deployment
	require.NoError(t, c.env.admin.Get(c.env.ctx, c.key, &d))
	if n < 0 {
		n = *d.Spec.Replicas
	}
	d.Status.ObservedGeneration = d.Generation
	d.Status.Replicas, d.Status.ReadyReplicas = *d.Spec.Replicas, n
	require.NoError(t, c.env.admin.Status().Update(c.env.ctx, &d))
}

// addIgnoredPort gives the Service api a port the doorman doesn't route,
// listed in its napwake.dev/doorman-ignore-ports.
func (c *ingressCase) addIgnoredPort(t *testing.T, port int32) {
	t.Helper()
	var svc corev1.Service
	require.NoError(t, c.env.admin.Get(c.env.ctx, client.ObjectKey{Namespace: c.key.Namespace, Name: "api"}, &svc))
	name := "port-" + strconv.Itoa(int(port))
	svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{Name: name, Port: port})
	svc.Annotations = map[string]string{doorman.AnnotationIgnorePorts: strconv.Itoa(int(port))}
	require.NoError(t, c.env.admin.Update(c.env.ctx, &svc))
}

// annotate sets annotations on the ManagedWorkload, as kubectl napwake and
// the doorman do.
func (c *ingressCase) annotate(t *testing.T, annotations map[string]string) {
	t.Helper()
	w := c.workload(t)
	original := w.DeepCopy()
	if w.Annotations == nil {
		w.Annotations = map[string]string{}
	}
	maps.Copy(w.Annotations, annotations)
	require.NoError(t, c.env.admin.Patch(c.env.ctx, w, client.MergeFrom(original)))
}

// pause asks for a pause, and lets it go through the doorman's takeover, a
// few seconds at a time, until the workload is Paused and routed.
func (c *ingressCase) pause(t *testing.T, r *Reconciler) {
	t.Helper()
	c.pauses++
	c.annotate(t, map[string]string{v1alpha1.AnnotationPauseRequested: "pause-" + strconv.Itoa(c.pauses)})
	for range 30 {
		c.settle(t)
		c.reconcile(t, r)
		c.now = c.now.Add(3 * time.Second)
		if w := c.workload(t); w.Status.Phase == v1alpha1.PhasePaused && len(w.Status.Doorman) > 0 {
			c.reconcile(t, r)
			return
		}
	}
	w := c.workload(t)
	t.Fatalf("never paused and routed: phase %s, routes %v", w.Status.Phase, w.Status.Doorman)
}

// wake wakes the paused workload as stamp has it, then has its first pod
// Ready took later, and reconciles until it's Running.
func (c *ingressCase) wake(t *testing.T, r *Reconciler, stamp func(), took time.Duration) {
	t.Helper()
	stamp()
	c.reconcile(t, r)
	require.Equal(t, v1alpha1.PhaseResuming, c.workload(t).Status.Phase)
	c.now = c.now.Add(took)
	c.settle(t)
	c.reconcile(t, r)
	require.Equal(t, v1alpha1.PhaseRunning, c.workload(t).Status.Phase)
}

// byRequest stamps a request the doorman holds, as it does, at the
// operator's clock, to the second.
func (c *ingressCase) byRequest(t *testing.T) func() {
	return func() {
		c.annotate(t, map[string]string{v1alpha1.AnnotationLastRequest: c.now.UTC().Format(time.RFC3339)})
	}
}

// nginxIngress is an Ingress of class nginx named name, sending / to
// service, with the proxy-read-timeout annotation given, unless it's empty.
func nginxIngress(name, service, readTimeout string) networkingv1.Ingress {
	var annotations map[string]string
	if readTimeout != "" {
		annotations = map[string]string{annotationNginxReadTimeout: readTimeout}
	}
	ing := ingressTo(service, annotations, ptr.To("nginx"))
	ing.Name = name
	return ing
}

func timeoutCondition(w *v1alpha1.ManagedWorkload) *metav1.Condition {
	return meta.FindStatusCondition(w.Status.Conditions, conditionIngressTimeoutShort)
}

func warnings(r *Reconciler) []string {
	return recorded(r.Recorder.(*events.FakeRecorder), "Warning "+ReasonIngressTimeoutShort)
}

// storefront is the Ingress of class nginx in front of the Service api.
const storefront = "storefront"

// recordedStorefront is storefront as status records it, with its
// proxy-read-timeout annotation and how long that has ingress-nginx wait.
func recordedStorefront(readTimeout string, timeout time.Duration) v1alpha1.NginxIngress {
	return v1alpha1.NginxIngress{Name: storefront, Services: []string{"api"}, ProxyReadTimeout: readTimeout,
		ReadTimeout: metav1.Duration{Duration: timeout}}
}

// The doorman holds a request for up to wake.maxWait while a workload wakes,
// but ingress-nginx waits for an answer only its proxy-read-timeout, 60
// seconds by default, and answers a write it gives up on with a 504. Once a
// wake by a request has taken longer than that, under maxWait, the workload
// says so, until every ingress-nginx Ingress in front of it waits at least
// maxWait.
func TestIngressTimeoutShort(t *testing.T) {
	env := startIngressEnv(t)

	t.Run("a slow wake by a request behind ingress-nginx's default warns, once", func(t *testing.T) {
		c := env.start(t, "slow", nil, nginxIngress(storefront, "api", ""))
		r := c.reconciler(t)
		c.pause(t, r)
		require.Equal(t, []v1alpha1.NginxIngress{recordedStorefront("", time.Minute)},
			c.workload(t).Status.NginxIngresses, "the pause records the Ingress in front, and its timeout")

		c.wake(t, r, c.byRequest(t), 85*time.Second)

		w := c.workload(t)
		cond := timeoutCondition(w)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Equal(t, reasonWakeOutlastedTimeout, cond.Reason)
		assert.Equal(t, "a wake by a request took 85 seconds to its first Ready replica, longer than ingress-nginx "+
			"waits for an answer through Ingress storefront (60 seconds, its default), so a write held that long gets a 504 "+
			`from ingress-nginx. Set nginx.ingress.kubernetes.io/proxy-read-timeout: "120" on it, its maxWait (2 `+
			"minutes, the default) in seconds. ingress-nginx's ConfigMap may already set a longer proxy-read-timeout, which Napwake can't "+
			"see: the annotation settles it. If F5's NGINX Ingress Controller, which also takes the class nginx, "+
			`serves it, set nginx.org/proxy-read-timeout: "120s" instead`, cond.Message)
		require.NotNil(t, w.Status.LastSlowWake)
		assert.Equal(t, 85*time.Second, w.Status.LastSlowWake.Took.Duration)
		assert.Len(t, warnings(r), 1)

		c.now = c.now.Add(time.Minute)
		c.reconcile(t, r)
		assert.True(t, meta.IsStatusConditionTrue(c.workload(t).Status.Conditions, conditionIngressTimeoutShort))
		c.pause(t, r)
		c.wake(t, r, c.byRequest(t), 95*time.Second)
		assert.Contains(t, timeoutCondition(c.workload(t)).Message, "took 95 seconds", "it says what the last took")
		assert.Empty(t, warnings(r), "the warning is announced only as it's set")
	})

	t.Run("measured to the first Ready replica, not the last", func(t *testing.T) {
		c := env.start(t, "replicas", func(w *v1alpha1.ManagedWorkload) {
			w.Spec.Wake = &v1alpha1.WakeSpec{MaxWait: &metav1.Duration{Duration: 5 * time.Minute}}
		}, nginxIngress(storefront, "api", "90"))
		var d appsv1.Deployment
		require.NoError(t, env.admin.Get(env.ctx, c.key, &d))
		d.Spec.Replicas = ptr.To(int32(3))
		require.NoError(t, env.admin.Update(env.ctx, &d))
		r := c.reconciler(t)
		c.pause(t, r)
		c.byRequest(t)()
		c.reconcile(t, r)
		require.Equal(t, v1alpha1.PhaseResuming, c.workload(t).Status.Phase)

		c.now = c.now.Add(100 * time.Second)
		c.ready(t, 1)
		c.reconcile(t, r)
		w := c.workload(t)
		require.Equal(t, v1alpha1.PhaseResuming, w.Status.Phase, "it waits for the rest")
		require.True(t, meta.IsStatusConditionTrue(w.Status.Conditions, conditionIngressTimeoutShort),
			"a request is let through once one is Ready")
		assert.Equal(t, "a wake by a request took 100 seconds to its first Ready replica, longer than ingress-nginx "+
			"waits for an answer through Ingress storefront (90 seconds), so a write held that long gets a 504 from "+
			`ingress-nginx. Set nginx.ingress.kubernetes.io/proxy-read-timeout: "300" on it, wake.maxWait in seconds`,
			timeoutCondition(w).Message)
		assert.Len(t, warnings(r), 1)

		c.now = c.now.Add(time.Minute)
		c.settle(t)
		c.reconcile(t, r)
		w = c.workload(t)
		require.Equal(t, v1alpha1.PhaseRunning, w.Status.Phase)
		assert.Contains(t, timeoutCondition(w).Message, "took 100 seconds")
		assert.Empty(t, warnings(r))
	})

	t.Run("not with the annotation at maxWait, even for a wake past maxWait", func(t *testing.T) {
		c := env.start(t, "at-max-wait", nil, nginxIngress(storefront, "api", "120"))
		r := c.reconciler(t)
		for _, took := range []time.Duration{85 * time.Second, 150 * time.Second} {
			c.pause(t, r)
			c.wake(t, r, c.byRequest(t), took)

			w := c.workload(t)
			assert.Nil(t, timeoutCondition(w), "after a wake of %s", took)
			assert.Nil(t, w.Status.LastSlowWake)
			assert.Equal(t, []v1alpha1.NginxIngress{recordedStorefront("120", 2*time.Minute)}, w.Status.NginxIngresses)
		}
		assert.Empty(t, warnings(r))
	})

	t.Run("a timeout ingress-nginx ignores is its default", func(t *testing.T) {
		c := env.start(t, "ignored", nil, nginxIngress(storefront, "api", "120s"))
		r := c.reconciler(t)
		c.pause(t, r)
		require.Equal(t, []v1alpha1.NginxIngress{recordedStorefront("120s", time.Minute)},
			c.workload(t).Status.NginxIngresses, "recorded as written, waiting ingress-nginx's default")

		c.wake(t, r, c.byRequest(t), 85*time.Second)

		cond := timeoutCondition(c.workload(t))
		require.NotNil(t, cond)
		assert.Equal(t, "a wake by a request took 85 seconds to its first Ready replica, longer than ingress-nginx "+
			"waits for an answer through Ingress storefront (60 seconds, its default), so a write held that long gets "+
			`a 504 from ingress-nginx. Ingress storefront's proxy-read-timeout "120s" isn't a whole number of seconds, `+
			`so ingress-nginx ignores it. Set nginx.ingress.kubernetes.io/proxy-read-timeout: "120" on it, its maxWait `+
			"(2 minutes, the default) in seconds. ingress-nginx's ConfigMap may already set a longer proxy-read-timeout, which Napwake can't "+
			"see: the annotation settles it. If F5's NGINX Ingress Controller, which also takes the class nginx, "+
			`serves it, set nginx.org/proxy-read-timeout: "120s" instead`, cond.Message)
		assert.Len(t, warnings(r), 1)
	})

	t.Run("a workload made from a label, on the default maxWait, isn't told of an annotation it never set",
		func(t *testing.T) {
			c := env.start(t, "from-label", func(w *v1alpha1.ManagedWorkload) {
				w.Labels = map[string]string{v1alpha1.LabelFromLabel: v1alpha1.True}
				w.Spec.Wake = &v1alpha1.WakeSpec{MaxWait: &metav1.Duration{Duration: v1alpha1.DefaultWakeMaxWait}}
			}, nginxIngress(storefront, "api", ""))
			r := c.reconciler(t)
			c.pause(t, r)
			c.wake(t, r, c.byRequest(t), 85*time.Second)

			cond := timeoutCondition(c.workload(t))
			require.NotNil(t, cond)
			assert.Contains(t, cond.Message, `"120" on it, its maxWait (2 minutes, the default) in seconds`)
			assert.NotContains(t, cond.Message, v1alpha1.AnnotationWakeMaxWait)
		})

	t.Run("timed from the request, not from the scale-up", func(t *testing.T) {
		c := env.start(t, "from-request", nil, nginxIngress(storefront, "api", ""))
		r := c.reconciler(t)
		c.pause(t, r)
		c.wake(t, r, func() {
			c.byRequest(t)()
			c.now = c.now.Add(30 * time.Second)
		}, 45*time.Second)

		cond := timeoutCondition(c.workload(t))
		require.NotNil(t, cond, "held 75 seconds, though Ready 45 seconds after the scale-up")
		assert.Contains(t, cond.Message, "a wake by a request took 75 seconds to its first Ready replica")
		assert.Len(t, warnings(r), 1)
	})

	t.Run("not after a fast wake, though status has the timeout", func(t *testing.T) {
		c := env.start(t, "fast", nil, nginxIngress(storefront, "api", ""))
		r := c.reconciler(t)
		c.pause(t, r)
		c.wake(t, r, c.byRequest(t), 20*time.Second)

		w := c.workload(t)
		assert.Nil(t, timeoutCondition(w))
		assert.Equal(t, []v1alpha1.NginxIngress{recordedStorefront("", time.Minute)}, w.Status.NginxIngresses)
		assert.Empty(t, warnings(r))
	})

	t.Run("not after a slow wake the forecast began, which no request times", func(t *testing.T) {
		c := env.start(t, "forecast", func(w *v1alpha1.ManagedWorkload) {
			w.Spec.IdlePolicy = &v1alpha1.IdlePolicySpec{AutoResume: true}
		}, nginxIngress(storefront, "api", ""))
		r := c.reconciler(t)
		c.pause(t, r)
		c.wake(t, r, func() {
			c.forecast.phase, c.forecast.predictValue = forecast.DailyActive, 100
		}, 85*time.Second)

		w := c.workload(t)
		assert.Nil(t, timeoutCondition(w))
		assert.Equal(t, []v1alpha1.NginxIngress{recordedStorefront("", time.Minute)}, w.Status.NginxIngresses)
		assert.Empty(t, warnings(r))
	})

	t.Run("only ingress-nginx's Ingresses in front of the workload's Services count", func(t *testing.T) {
		traefik := ingressTo("api", map[string]string{"nginx.ingress.kubernetes.io/ssl-redirect": "false"},
			ptr.To("traefik"))
		traefik.Name = "traefik"
		unclassed := ingressTo("api", nil, nil)
		unclassed.Name = "unclassed"
		f5 := nginxIngress("f5", "api", "")
		f5.Annotations = map[string]string{"nginx.org/proxy-read-timeout": "60s"}
		metrics := nginxIngress("metrics", "api", "")
		metrics.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port = networkingv1.ServiceBackendPort{Number: 9090}
		byName := nginxIngress("by-name", "api", "120")
		byName.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port = networkingv1.ServiceBackendPort{Name: "http"}
		c := env.start(t, "others", nil, nginxIngress(storefront, "api", "120"), traefik, unclassed,
			nginxIngress("elsewhere", "other", ""), f5, metrics, byName)
		c.addIgnoredPort(t, 9090)
		r := c.reconciler(t)
		c.pause(t, r)
		c.wake(t, r, c.byRequest(t), 85*time.Second)

		w := c.workload(t)
		named := recordedStorefront("120", 2*time.Minute)
		named.Name = "by-name"
		assert.Equal(t, []v1alpha1.NginxIngress{named, recordedStorefront("120", 2*time.Minute)}, w.Status.NginxIngresses,
			"by the routed port's name or number, and not to a port the doorman doesn't route")
		assert.Nil(t, timeoutCondition(w))
		assert.Empty(t, warnings(r))
	})

	t.Run("an Ingress moved to a port the doorman doesn't route no longer counts", func(t *testing.T) {
		c := env.start(t, "moved", nil, nginxIngress(storefront, "api", ""))
		c.addIgnoredPort(t, 9090)
		r := c.reconciler(t)
		c.pause(t, r)
		c.wake(t, r, c.byRequest(t), 85*time.Second)
		require.True(t, meta.IsStatusConditionTrue(c.workload(t).Status.Conditions, conditionIngressTimeoutShort))

		var ing networkingv1.Ingress
		require.NoError(t, env.admin.Get(env.ctx, client.ObjectKey{Namespace: c.key.Namespace, Name: storefront}, &ing))
		ing.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port = networkingv1.ServiceBackendPort{Number: 9090}
		require.NoError(t, env.admin.Update(env.ctx, &ing))
		c.now = c.now.Add(time.Minute)
		c.reconcile(t, r)

		w := c.workload(t)
		assert.Empty(t, w.Status.NginxIngresses)
		assert.True(t, conditionFalseWith(w, conditionIngressTimeoutShort, reasonNoNginxIngress))
	})

	t.Run("raising the timeout to maxWait clears it, and lowering it again removes it", func(t *testing.T) {
		c := env.start(t, "raised", nil, nginxIngress(storefront, "api", ""))
		r := c.reconciler(t)
		c.pause(t, r)
		c.wake(t, r, c.byRequest(t), 85*time.Second)
		require.True(t, meta.IsStatusConditionTrue(c.workload(t).Status.Conditions, conditionIngressTimeoutShort))

		var ing networkingv1.Ingress
		require.NoError(t, env.admin.Get(env.ctx, client.ObjectKey{Namespace: c.key.Namespace, Name: storefront}, &ing))
		ing.Annotations = map[string]string{annotationNginxReadTimeout: "120"}
		require.NoError(t, env.admin.Update(env.ctx, &ing))
		c.now = c.now.Add(time.Minute)
		c.reconcile(t, r)

		w := c.workload(t)
		cond := timeoutCondition(w)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, reasonTimeoutsCoverMaxWait, cond.Reason)
		assert.Nil(t, w.Status.LastSlowWake)
		assert.Equal(t, []v1alpha1.NginxIngress{recordedStorefront("120", 2*time.Minute)}, w.Status.NginxIngresses)

		require.NoError(t, env.admin.Get(env.ctx, client.ObjectKey{Namespace: c.key.Namespace, Name: storefront}, &ing))
		ing.Annotations = nil
		require.NoError(t, env.admin.Update(env.ctx, &ing))
		c.now = c.now.Add(time.Minute)
		c.reconcile(t, r)

		w = c.workload(t)
		assert.Nil(t, timeoutCondition(w), "lowered again with no slow wake since, it's as before any")
		assert.Equal(t, []v1alpha1.NginxIngress{recordedStorefront("", time.Minute)}, w.Status.NginxIngresses)
		assert.Len(t, warnings(r), 1, "one warning, as it was set")
	})

	t.Run("an operator that restarts keeps it, and doesn't warn again", func(t *testing.T) {
		c := env.start(t, "restart", nil, nginxIngress(storefront, "api", ""))
		r := c.reconciler(t)
		c.pause(t, r)
		c.wake(t, r, c.byRequest(t), 85*time.Second)
		before := timeoutCondition(c.workload(t))
		require.NotNil(t, before)
		require.Len(t, warnings(r), 1)

		restarted := c.reconciler(t)
		c.now = c.now.Add(time.Minute)
		c.reconcile(t, restarted)

		after := timeoutCondition(c.workload(t))
		require.NotNil(t, after)
		assert.Equal(t, metav1.ConditionTrue, after.Status)
		assert.Equal(t, before.Message, after.Message)
		assert.True(t, before.LastTransitionTime.Equal(&after.LastTransitionTime))
		assert.Empty(t, warnings(restarted))
	})
}
