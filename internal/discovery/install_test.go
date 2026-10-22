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
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
)

// operatorDeployment is Hybernate's operator as the chart installs it in
// hybernate-system, with args for its manager.
func operatorDeployment(args ...string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "hybernate", Namespace: "hybernate-system", Labels: map[string]string{
			"control-plane": "controller-manager", "helm.sh/chart": "hybernate-0.2.0"}},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "kube-rbac-proxy", Args: []string{"--doorman-service=elsewhere"}},
			{Name: "manager", Command: []string{"/manager"}, Args: args},
		}}}},
	}
}

func TestInstallOf(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want Install
	}{
		{name: "the defaults, as the manifests run it", args: []string{"--leader-elect"}, want: Install{}},
		{name: "the doorman on, as the chart runs it", args: []string{"--doorman-service=hybernate-doorman"},
			want: Install{}},
		{name: "doorman.enabled: false", args: []string{"--doorman-service="}, want: Install{DoormanOff: true}},
		{name: "the value as the next argument", args: []string{"--doorman-service", ""},
			want: Install{DoormanOff: true}},
		{name: "the forwarder on", args: []string{"--neg-forwarder", "--neg-forwarder-image=ghcr.io/x"},
			want: Install{NEGForwarder: true}},
		{name: "the forwarder off by value", args: []string{"--neg-forwarder=false"}, want: Install{}},
		{name: "the last value given wins", args: []string{"--doorman-service=", "--doorman-service=d"},
			want: Install{}},
		{name: "watching namespaces", args: []string{"--watch-namespaces=shop, blog"},
			want: Install{Watches: []string{"shop", "blog"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.want.Namespace = "hybernate-system"
			assert.Equal(t, tt.want, installOf(operatorDeployment(tt.args...)))
		})
	}
}

// A workload's install is the one its ManagedWorkload names, then one that
// watches its namespace, then one that watches every namespace.
func TestInstallFor(t *testing.T) {
	everywhere := Install{Namespace: "hybernate-system"}
	shop := Install{Namespace: "hybernate-shop", Watches: []string{"shop"}, DoormanOff: true}
	installs := []Install{everywhere, shop}
	owned := &v1alpha1.ManagedWorkload{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{v1alpha1.AnnotationOperator: "hybernate-shop"}}}

	assert.Equal(t, shop, installFor(installs, "blog", owned), "the one its ManagedWorkload names")
	assert.Equal(t, shop, installFor(installs, "shop", nil), "the one watching its namespace")
	assert.Equal(t, everywhere, installFor(installs, "blog", nil), "the one watching every namespace")
	assert.Equal(t, Install{}, installFor([]Install{shop}, "blog", nil), "none: Hybernate's defaults")
	assert.Equal(t, Install{}, installFor(nil, "blog", owned))
}
