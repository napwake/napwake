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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
	"github.com/okedeji/hybernate/internal/doorman"
)

// The API server validates the forwarder as the operator builds it, and
// keeps the owner references that have it collected with its workload.
// envtest runs no controllers, so the forwarder's pods are written here,
// and the collection itself is left to a real cluster.
var _ = ginkgo.Describe("The forwarder for Services behind GKE NEGs", func() {
	var (
		ns  string
		r   *Reconciler
		now time.Time
	)

	ginkgo.BeforeEach(func() {
		ns = envtestNamespace()
		doormanNS := envtestNamespace()
		now = time.Now()
		gomega.Expect(k8sClient.Create(ctx, &discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name: "hybernate-doorman-abc", Namespace: doormanNS,
				Labels: map[string]string{discoveryv1.LabelServiceName: "hybernate-doorman"},
			},
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints: []discoveryv1.Endpoint{{
				Addresses:  []string{"10.0.0.7"},
				Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)},
			}},
		})).To(gomega.Succeed())

		operatorCache, err := cache.New(cfg, cache.Options{
			Scheme: scheme.Scheme,
			ByObject: map[client.Object]cache.ByObject{
				&discoveryv1.EndpointSlice{}: EndpointSliceCache(nil, doormanNS),
			},
		})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cacheCtx, stop := context.WithCancel(ctx)
		ginkgo.DeferCleanup(stop)
		go func() {
			defer ginkgo.GinkgoRecover()
			gomega.Expect(operatorCache.Start(cacheCtx)).To(gomega.Succeed())
		}()
		gomega.Expect(operatorCache.WaitForCacheSync(ctx)).To(gomega.BeTrue())
		cached, err := client.New(cfg, client.Options{Scheme: scheme.Scheme, Cache: &client.CacheOptions{Reader: operatorCache}})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		r = &Reconciler{
			Client:            cached,
			PodReader:         k8sClient,
			Scheme:            scheme.Scheme,
			Recorder:          events.NewFakeRecorder(100),
			DoormanService:    "hybernate-doorman",
			DoormanNamespace:  doormanNS,
			OperatorNamespace: thisInstall,
			NEGForwarder: &ForwarderOptions{
				Image:    "ghcr.io/okedeji/hybernate:test",
				Replicas: 2,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
				},
				Doorman: "hybernate-doorman-pods." + doormanNS + ".svc",
			},
			clock: func() time.Time { return now },
		}
	})

	pausedBehindNEG := func(targetPort int32) (*v1alpha1.ManagedWorkload, *appsv1.Deployment) {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: ns, Annotations: map[string]string{
				annotationGKENEG:              `{"ingress":true}`,
				"cloud.google.com/neg-status": `{"network_endpoint_groups":{"80":"k8s1-neg"},"zones":["europe-west1-b"]}`,
			}},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": "api"},
				Ports:    []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromInt32(targetPort)}},
			},
		}
		gomega.Expect(k8sClient.Create(ctx, svc)).To(gomega.Succeed())
		w := envtestWorkload(ns)
		gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
		w.Status.Phase = v1alpha1.PhasePaused
		return w, envtestDeployment(ns, 0)
	}
	forwarder := func() (*appsv1.Deployment, error) {
		var d appsv1.Deployment
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: "hybernate-forwarder-api"}, &d)
		return &d, err
	}
	routedSlices := func() []discoveryv1.EndpointSlice {
		var list discoveryv1.EndpointSliceList
		gomega.Expect(k8sClient.List(ctx, &list, client.InNamespace(ns),
			client.MatchingLabels{discoveryv1.LabelManagedBy: doorman.ManagedBy})).To(gomega.Succeed())
		return list.Items
	}
	readyPod := func(d *appsv1.Deployment, name, ip string) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: d.Spec.Template.Labels},
			Spec:       *d.Spec.Template.Spec.DeepCopy(),
		}
		pod.Spec.NodeName = "node-1"
		gomega.Expect(k8sClient.Create(ctx, pod)).To(gomega.Succeed())
		pod.Status.PodIP = ip
		pod.Status.PodIPs = []corev1.PodIP{{IP: ip}}
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		gomega.Expect(k8sClient.Status().Update(ctx, pod)).To(gomega.Succeed())
	}

	ginkgo.It("runs one the API server accepts, owned by the workload, and points the Service at its Ready pods", func() {
		w, target := pausedBehindNEG(8080)

		r.routeDoorman(ctx, w, target)

		d, err := forwarder()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		owner := metav1.GetControllerOf(d)
		gomega.Expect(owner).NotTo(gomega.BeNil())
		gomega.Expect(owner.Kind).To(gomega.Equal("ManagedWorkload"))
		gomega.Expect(owner.UID).To(gomega.Equal(w.UID))
		gomega.Expect(owner.BlockOwnerDeletion).To(gomega.Equal(ptr.To(true)))
		gomega.Expect(routedSlices()).To(gomega.BeEmpty())
		gomega.Expect(conditionFalseWith(w, conditionWakeOnRequest, reasonForwarderNotReady)).To(gomega.BeTrue())

		readyPod(d, "fwd-a", "10.244.2.5")
		gomega.Expect(r.routeDoorman(ctx, w, target)).To(gomega.BeZero())

		slices := routedSlices()
		gomega.Expect(slices).To(gomega.HaveLen(1))
		gomega.Expect(slices[0].Endpoints).To(gomega.HaveLen(1))
		gomega.Expect(slices[0].Endpoints[0].Addresses).To(gomega.Equal([]string{"10.244.2.5"}))
		gomega.Expect(slices[0].Endpoints[0].TargetRef.Namespace).To(gomega.Equal(ns))
		gomega.Expect(*slices[0].Ports[0].Port).To(gomega.Equal(int32(8080)))
		gomega.Expect(metav1.IsControlledBy(&slices[0], w)).To(gomega.BeTrue())
		gomega.Expect(w.Status.Doorman).To(gomega.HaveLen(1))
		gomega.Expect(w.Status.Doorman[0].Forwarded).To(gomega.BeTrue())
		gomega.Expect(conditionFalseWith(w, conditionWakeOnRequest, reasonForwarderNotReady)).To(gomega.BeFalse())
	})

	ginkgo.It("runs one that listens below 1024 for a workload that serves port 80", func() {
		w, target := pausedBehindNEG(80)

		r.routeDoorman(ctx, w, target)

		d, err := forwarder()
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "the API server refused the forwarder")
		gomega.Expect(d.Spec.Template.Spec.SecurityContext.Sysctls).To(gomega.ContainElement(
			corev1.Sysctl{Name: sysctlUnprivilegedPorts, Value: "0"}))
	})

	ginkgo.It("removes it once the workload is awake and its routes have drained", func() {
		w, target := pausedBehindNEG(8080)
		r.routeDoorman(ctx, w, target)
		d, err := forwarder()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		readyPod(d, "fwd-a", "10.244.2.5")
		r.routeDoorman(ctx, w, target)
		gomega.Expect(routedSlices()).To(gomega.HaveLen(1))

		w.Status.Phase = v1alpha1.PhaseRunning
		gomega.Expect(r.routeDoorman(ctx, w, target)).To(gomega.Equal(portDrain))

		gomega.Expect(routedSlices()).To(gomega.BeEmpty())
		_, err = forwarder()
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "removed before its routes drained")

		now = now.Add(portDrain)
		gomega.Expect(r.routeDoorman(ctx, w, target)).To(gomega.BeZero())

		_, err = forwarder()
		gomega.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue(), "got %v", err)
	})
})
