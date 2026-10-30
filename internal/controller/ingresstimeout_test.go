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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// Which controller an IngressClass names is cluster-scoped, which the
// operator can't read, so what counts as ingress-nginx's is told from the
// Ingress alone.
func TestServedByNginx(t *testing.T) {
	tests := []struct {
		name        string
		className   *string
		annotations map[string]string
		want        bool
	}{
		{name: "ingressClassName nginx", className: ptr.To("nginx"), want: true},
		{name: "a class with nginx in its name", className: ptr.To("internal-NGINX"), want: true},
		{name: "the class annotation", annotations: map[string]string{annotationIngressClass: "nginx"}, want: true},
		{name: "an nginx annotation under another class name, as MicroK8s's public",
			className: ptr.To("public"), annotations: map[string]string{"nginx.ingress.kubernetes.io/ssl-redirect": "false"},
			want: true},
		{name: "another controller's class", className: ptr.To("traefik")},
		{name: "GKE's class annotation", annotations: map[string]string{annotationIngressClass: "gce"}},
		{name: "no class and no nginx annotation, though ingress-nginx may be the default"},
		{name: "another controller's annotations", annotations: map[string]string{"nginx.org/proxy-read-timeout": "120s"}},
		{name: "class nginx, but F5's NGINX Ingress Controller's annotations",
			className: ptr.To("nginx"), annotations: map[string]string{"nginx.org/proxy-read-timeout": "120s"}},
		{name: "class nginx, but F5's NGINX Plus annotations",
			annotations: map[string]string{annotationIngressClass: "nginx", "nginx.com/sticky-cookie-services": "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, servedByNginx(ingressTo("api", tt.annotations, tt.className)))
		})
	}
}

// ingress-nginx reads proxy-read-timeout as whole seconds. Anything else is
// reported as written, not guessed at.
func TestNginxReadTimeout(t *testing.T) {
	tests := []struct {
		annotation string
		want       time.Duration
		ok         bool
	}{
		{annotation: "", want: time.Minute, ok: true},
		{annotation: "120", want: 2 * time.Minute, ok: true},
		{annotation: "3600", want: time.Hour, ok: true},
		{annotation: "120s"},
		{annotation: "2m"},
		{annotation: "0"},
		{annotation: "-30"},
		{annotation: " 120"},
		{annotation: "99999999999"},
	}
	for _, tt := range tests {
		t.Run(tt.annotation, func(t *testing.T) {
			got, ok := nginxReadTimeout(tt.annotation)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIngressTimeoutMessage(t *testing.T) {
	timeout := func(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }
	shop := v1alpha1.NginxIngress{Name: "shop", Services: []string{"api"}, ReadTimeout: timeout(time.Minute)}
	admin := v1alpha1.NginxIngress{Name: "admin", Services: []string{"api"}, ProxyReadTimeout: "30",
		ReadTimeout: timeout(30 * time.Second)}
	api := v1alpha1.NginxIngress{Name: "api", Services: []string{"api"}, ProxyReadTimeout: "90",
		ReadTimeout: timeout(90 * time.Second)}
	unread := v1alpha1.NginxIngress{Name: "legacy", Services: []string{"api"}, ProxyReadTimeout: "120s"}
	slow := func(took time.Duration) v1alpha1.SlowWake {
		return v1alpha1.SlowWake{Took: metav1.Duration{Duration: took}}
	}
	tests := []struct {
		name    string
		slow    v1alpha1.SlowWake
		short   []v1alpha1.NginxIngress
		maxWait time.Duration
		setting string
		want    string
	}{
		{name: "one, on the annotation", slow: slow(45 * time.Second), short: []v1alpha1.NginxIngress{admin},
			maxWait: 2 * time.Minute, setting: "wake.maxWait",
			want: "a wake by a request took 45 seconds to its first Ready replica, longer than ingress-nginx waits " +
				"for an answer through Ingress admin (30 seconds), so a write held that long gets a 504 from " +
				`ingress-nginx. Set nginx.ingress.kubernetes.io/proxy-read-timeout: "120" on it, wake.maxWait in seconds`},
		{name: "several, one on the default, one unread, past maxWait, set by the annotation",
			slow: slow(150 * time.Second), short: []v1alpha1.NginxIngress{admin, shop, unread}, maxWait: 2 * time.Minute,
			setting: v1alpha1.AnnotationWakeMaxWait,
			want: "a wake by a request took 150 seconds to its first Ready replica, longer than ingress-nginx waits " +
				"for an answer through Ingresses admin (30 seconds) and shop (60 seconds, its default), so a write held " +
				"that long gets a 504 from ingress-nginx. Ingress legacy's proxy-read-timeout \"120s\" isn't a whole " +
				"number of seconds, so how long it waits can't be told. Set " +
				`nginx.ingress.kubernetes.io/proxy-read-timeout: "120" on each, napwake.dev/wake-max-wait in seconds. ` +
				"The wake took longer than napwake.dev/wake-max-wait (120 seconds) too: raise it as well, and the " +
				"annotation to match. ingress-nginx's ConfigMap may already set a longer proxy-read-timeout, which " +
				"Napwake can't see: the annotation settles it"},
		{name: "the one it outlasted raised, another still under maxWait", slow: slow(70 * time.Second),
			short: []v1alpha1.NginxIngress{api}, maxWait: 5 * time.Minute, setting: "wake.maxWait",
			want: "a wake by a request took 70 seconds to its first Ready replica, and ingress-nginx answers a write " +
				"held longer than it waits with a 504. Ingress api (90 seconds) also waits less than wake.maxWait " +
				`(300 seconds). Set nginx.ingress.kubernetes.io/proxy-read-timeout: "300" on it, wake.maxWait in seconds`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ingressTimeoutMessage(tt.slow, tt.short, tt.maxWait, tt.setting))
		})
	}
}

// A timeout raised on an Ingress clears the warning on the awake workloads
// it's recorded in front of, and is read by the paused ones, as any change
// to its backends is.
func TestFindWorkloadsBehindIngress(t *testing.T) {
	const ns = "retail"
	workload := func(name string, phase v1alpha1.WorkloadPhase, ingresses ...string) *v1alpha1.ManagedWorkload {
		w := depWorkload(ns, name, v1alpha1.TargetKindDeployment, phase)
		for _, ing := range ingresses {
			w.Status.NginxIngresses = append(w.Status.NginxIngresses, v1alpha1.NginxIngress{Name: ing})
		}
		return w
	}
	r := depReconciler(t, &stubPauser{}, workload("paused", v1alpha1.PhasePaused),
		workload("behind", v1alpha1.PhaseRunning, "web"), workload("elsewhere", v1alpha1.PhaseRunning, "admin"))
	ing := ingressTo("api", nil, ptr.To("nginx"))
	ing.Namespace = ns

	got := r.findWorkloadsBehindIngress(context.Background(), &ing)

	assert.ElementsMatch(t, []reconcile.Request{
		{NamespacedName: types.NamespacedName{Namespace: ns, Name: "behind"}},
		{NamespacedName: types.NamespacedName{Namespace: ns, Name: "paused"}},
	}, got)
}

// A workload whose doorman is turned off holds no request, so nothing an
// earlier wake found stands.
func TestReviewIngressTimeouts_DoormanOff(t *testing.T) {
	w := depWorkload("default", "api", v1alpha1.TargetKindDeployment, v1alpha1.PhaseRunning)
	w.Status.NginxIngresses = []v1alpha1.NginxIngress{{Name: "web"}}
	w.Status.LastSlowWake = &v1alpha1.SlowWake{Took: metav1.Duration{Duration: time.Minute}}
	w.Status.Conditions = []metav1.Condition{{Type: conditionIngressTimeoutShort, Status: metav1.ConditionTrue,
		Reason: reasonWakeOutlastedTimeout}}
	r := &Reconciler{Recorder: events.NewFakeRecorder(10), clock: func() time.Time { return fixedTime }}

	r.reviewIngressTimeouts(context.Background(), w)

	assert.Nil(t, w.Status.NginxIngresses)
	assert.Nil(t, w.Status.LastSlowWake)
	assert.True(t, conditionFalseWith(w, conditionIngressTimeoutShort, reasonDoormanDisabled))
}
