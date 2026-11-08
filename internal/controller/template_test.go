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
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/discovery"
	"github.com/napwake/napwake/internal/metrics"
)

// fullTemplate is a pod template as a chart with a service mesh writes it,
// with something in nearly every field the operator never reads.
func fullTemplate() corev1.PodTemplateSpec {
	requests := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("600m"),
			corev1.ResourceMemory: resource.MustParse("512Mi")},
		Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
	}
	probe := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz",
		Port: intstr.FromString("web")}}, PeriodSeconds: 10}
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{"app": "api", "version": "1.42.0"},
			Annotations: map[string]string{"sidecar.istio.io/status": `{"containers":null}`},
		},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{
				Name: "migrate", Image: "shop/api-migrate:1.42.0", Args: []string{"migrate", "up"},
				Env:       []corev1.EnvVar{{Name: "DATABASE_HOST", Value: "postgres.data.svc.cluster.local"}},
				Resources: requests,
			}, {
				Name: "istio-proxy", Image: "istio/proxyv2:1.24.0", RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways),
				Ports:     []corev1.ContainerPort{{Name: "http-envoy-prom", ContainerPort: 15090}},
				Resources: requests,
			}},
			Containers: []corev1.Container{{
				Name: "app", Image: "shop/api:1.42.0", Command: []string{"/api"}, WorkingDir: "/srv",
				Ports: []corev1.ContainerPort{{Name: "web", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}},
				Env: []corev1.EnvVar{
					{Name: "CACHE_URL", Value: "redis://cache:6379"},
					{Name: "SETTING", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}, Key: "setting"}}},
				},
				EnvFrom:        []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "api-env"}}}},
				Resources:      requests,
				ReadinessProbe: probe, LivenessProbe: probe,
				VolumeMounts:    []corev1.VolumeMount{{Name: "config", MountPath: "/etc/api"}},
				SecurityContext: &corev1.SecurityContext{RunAsNonRoot: ptr.To(true)},
				ImagePullPolicy: corev1.PullIfNotPresent,
			}, {
				Name: "log-shipper", Image: "fluent-bit:3.2", StartupProbe: probe, Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
					Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m"),
						corev1.ResourceMemory: resource.MustParse("128Mi")},
				},
			}},
			Volumes: []corev1.Volume{
				{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}}}},
				{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: "api-data"}}},
			},
			TerminationGracePeriodSeconds: ptr.To[int64](45),
			ReadinessGates:                []corev1.PodReadinessGate{{ConditionType: "example.com/ready"}},
			Resources:                     &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}},
			Overhead:                      corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi")},
			ServiceAccountName:            "api",
			NodeSelector:                  map[string]string{"pool": "general"},
			Tolerations:                   []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists}},
			SecurityContext:               &corev1.PodSecurityContext{RunAsUser: ptr.To[int64](1000)},
		},
	}
}

// The cache keeps of a pod template exactly what the operator reads, and
// the hash of the whole: drop any of it from TrimPodTemplate and this
// fails.
func TestTrimPodTemplate_KeepsWhatTheOperatorReads(t *testing.T) {
	full := fullTemplate()
	trimmed := fullTemplate()

	TrimPodTemplate(&trimmed)

	app, sidecar, migrate, shipper := full.Spec.Containers[0], full.Spec.InitContainers[1],
		full.Spec.InitContainers[0], full.Spec.Containers[1]
	requested := func(c corev1.Container) corev1.ResourceRequirements {
		return corev1.ResourceRequirements{Requests: c.Resources.Requests}
	}
	assert.Equal(t, corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      full.Labels,
			Annotations: map[string]string{cachedTemplateHash: hashTemplate(full)},
		},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{Name: migrate.Name, Env: migrate.Env, Resources: requested(migrate)},
				{Name: sidecar.Name, Ports: sidecar.Ports, Resources: requested(sidecar), RestartPolicy: sidecar.RestartPolicy},
			},
			Containers: []corev1.Container{
				{Name: app.Name, Ports: app.Ports, Env: app.Env, EnvFrom: app.EnvFrom, Resources: requested(app),
					ReadinessProbe: &corev1.Probe{}},
				{Name: shipper.Name, StartupProbe: &corev1.Probe{}, Resources: corev1.ResourceRequirements{
					Requests: shipper.Resources.Requests,
					Limits:   corev1.ResourceList{corev1.ResourceCPU: shipper.Resources.Limits[corev1.ResourceCPU]},
				}},
			},
			Volumes:                       full.Spec.Volumes[1:],
			TerminationGracePeriodSeconds: full.Spec.TerminationGracePeriodSeconds,
			ReadinessGates:                full.Spec.ReadinessGates,
			Resources:                     full.Spec.Resources,
			Overhead:                      full.Spec.Overhead,
		},
	}, trimmed)

	again := *trimmed.DeepCopy()
	TrimPodTemplate(&again)
	assert.Equal(t, trimmed, again, "trimming what's trimmed changes nothing")

	var empty corev1.PodTemplateSpec
	TrimPodTemplate(&empty)
	assert.Equal(t, hashTemplate(corev1.PodTemplateSpec{}), empty.Annotations[cachedTemplateHash])
}

// Everything the operator reads of a template answers the same from the
// cache's trimmed copy as from the whole: the hash a deploy is told by,
// whether its pods have a readiness check, the ConfigMaps and dependencies
// its environment names, the containers whose CPU counts, a Service's
// named target port, and how long a pause waits for pods to stop.
func TestTrimPodTemplate_ReadersAgree(t *testing.T) {
	full := fullTemplate()
	trimmed := fullTemplate()
	TrimPodTemplate(&trimmed)
	deployment := func(template corev1.PodTemplateSpec) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop"},
			Spec: appsv1.DeploymentSpec{Template: template}}
	}
	statefulSet := func(template corev1.PodTemplateSpec) *appsv1.StatefulSet {
		return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop"},
			Spec: appsv1.StatefulSetSpec{Template: template}}
	}
	cache := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "shop"},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "cache"},
			Ports: []corev1.ServicePort{{Name: "redis", Port: 6379}}}}
	api := corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromString("web")}}}}
	services := map[string]map[string]corev1.Service{"shop": {"cache": *cache}}
	targets := func(template corev1.PodTemplateSpec) []discovery.Target {
		return []discovery.Target{{Namespace: "shop", Kind: v1alpha1.TargetKindDeployment, Name: "cache",
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "cache"}}}},
			{Namespace: "shop", Kind: v1alpha1.TargetKindDeployment, Name: "api", Template: template}}
	}
	containerNames := func(spec corev1.PodSpec) []string {
		var names []string
		for _, c := range metrics.WorkloadContainers(spec) {
			names = append(names, c.Name)
		}
		return names
	}
	r := &Reconciler{}

	for _, kind := range []string{"Deployment", "StatefulSet"} {
		hashOf := func(template corev1.PodTemplateSpec) string {
			if kind == "Deployment" {
				return podTemplateHash(deployment(template))
			}
			return podTemplateHash(statefulSet(template))
		}
		assert.Equal(t, hashOf(full), hashOf(trimmed), "%s: the template hash", kind)
	}
	assert.True(t, hasReadinessCheck(trimmed.Spec))
	assert.Equal(t, hasReadinessCheck(full.Spec), hasReadinessCheck(trimmed.Spec))
	assert.Equal(t, discovery.ConfigMapsReferenced(full.Spec), discovery.ConfigMapsReferenced(trimmed.Spec))
	fullDeps, fullSecrets := discovery.DependenciesOf("shop", v1alpha1.TargetKindDeployment, "api", full.Spec, nil,
		services, targets(full))
	trimmedDeps, trimmedSecrets := discovery.DependenciesOf("shop", v1alpha1.TargetKindDeployment, "api",
		trimmed.Spec, nil, services, targets(trimmed))
	assert.NotEmpty(t, fullDeps, "the environment names the cache")
	assert.Equal(t, fullDeps, trimmedDeps)
	assert.Equal(t, fullSecrets, trimmedSecrets)
	assert.Equal(t, r.namespacesNamed("shop", full.Spec, nil), r.namespacesNamed("shop", trimmed.Spec, nil))
	assert.Equal(t, []string{"app", "log-shipper", "istio-proxy"}, containerNames(trimmed.Spec))
	for i, c := range metrics.WorkloadContainers(trimmed.Spec) {
		assert.Equal(t, metrics.WorkloadContainers(full.Spec)[i].Resources.Requests, c.Resources.Requests, c.Name)
	}
	fullAppCPU, _ := metrics.AppCPURequest(nil, full.Spec)
	trimmedAppCPU, _ := metrics.AppCPURequest(nil, trimmed.Spec)
	assert.Equal(t, int64(600+200+600), trimmedAppCPU, "the shipper's CPU limit stands in for its request")
	assert.Equal(t, fullAppCPU, trimmedAppCPU)
	fullCPU, fullMemory := metrics.Requests(metrics.WorkloadContainers(full.Spec))
	trimmedCPU, trimmedMemory := metrics.Requests(metrics.WorkloadContainers(trimmed.Spec))
	assert.Equal(t, fullCPU, trimmedCPU)
	assert.Equal(t, fullMemory, trimmedMemory)
	fullPort, fullOK := targetPort(api, "http", full.Spec)
	trimmedPort, trimmedOK := targetPort(api, "http", trimmed.Spec)
	assert.True(t, trimmedOK)
	assert.Equal(t, fullPort, trimmedPort)
	assert.Equal(t, fullOK, trimmedOK)
	assert.Equal(t, full.Spec.TerminationGracePeriodSeconds, trimmed.Spec.TerminationGracePeriodSeconds)
	assert.Equal(t, full.Spec.Volumes[1], trimmed.Spec.Volumes[0], "the claim a StatefulSet's storage is priced by")
}
