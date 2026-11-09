//go:build sizing

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

// Package sizing_test measures what the operator and the doorman hold in
// memory for each pod, Service and ManagedWorkload in a cluster, so the
// chart's sizing advice is measured rather than guessed. Each test fills
// an API server with realistic objects, then starts the cache the operator
// or the doorman starts, with its own selectors and trimming, and reads how
// far the live heap grew once it holds them all. TestOperatorBinary runs
// the operator binary itself against such a cluster, as the chart runs it,
// and reads its runtime's memory. The chart's values.yaml, and the Helm
// values and cost tracking pages, quote what it measures.
//
// The caches' tests create up to 100,000 pods, as many endpoints and
// 50,000 ManagedWorkloads, and cache each count several times, which takes
// about four minutes. The binary's takes about half an hour. So CI builds
// them, in make vet and the linter, but doesn't run them. Run them with:
//
//	go test -tags sizing -count=1 -timeout 120m -v ./test/sizing
//
// Without -race: the race detector turns off the runtime's packing of small
// objects, so the doorman's EndpointSlices and ManagedWorkloads read up to a
// tenth more than they cost it.
package sizing_test

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/uuid"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/yaml"

	"github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/capacity"
	"github.com/napwake/napwake/internal/doorman"
	"github.com/napwake/napwake/internal/forecast"
)

// What the chart's values.yaml, and the Helm values and cost tracking
// pages, say each object costs: the largest median below, from 10,000 up,
// rounded up to two figures. Below 10,000, an empty cache's own cost,
// which varies by tens of kilobytes from one cache to the next, still
// shows in the result.
const (
	// The operator, for each pod bound to a node.
	podBytes int64 = 2300
	// The doorman, for each pod behind a Service, and each Service.
	endpointBytes int64 = 320
	serviceBytes  int64 = 1200
	// The doorman, for each ManagedWorkload.
	managedWorkloadBytes int64 = 9400
	// A figure may round up this far, and no further.
	roundedUp = 1.25
	// The counts below this aren't held to it.
	heldFrom = 10_000
)

var (
	// The counts of pods each pod and EndpointSlice cache is measured at.
	podCounts = []int{1_000, 10_000, 50_000, 100_000}
	// The counts of ManagedWorkloads.
	workloadCounts = []int{1_000, 10_000, 50_000}
	// How often each count is cached.
	runs = 5
)

const (
	namespaces = 50
	nodes      = 200
	// Pods are made a Deployment's at a time, so a Service's endpoints
	// share a namespace.
	replicas = 10
	// How many objects are created at once.
	writers = 64
	// What the pods, EndpointSlices and ManagedWorkloads are written as.
	writer = client.FieldOwner("kube-controller-manager")
)

// The operator caches what each pod bound to a node requests of it, cut
// down by capacity.PodCache. Its trimming builds a new pod, with no managed
// fields or annotations, so the transform every other cached kind gets
// (trimForCache in cmd/main.go) would find nothing left to drop. A
// ByObject transform replaces the default one anyway.
func TestOperatorPodCache(t *testing.T) {
	api := startAPIServer(t)
	perPod := api.measure(t, kind{
		name:   "pods",
		obj:    &corev1.Pod{},
		list:   &corev1.PodList{},
		cache:  capacity.PodCache(),
		counts: podCounts,
		create: func(ctx context.Context, c client.Client, i int) error {
			return c.Create(ctx, pod(i), writer)
		},
		cached: func(pods int) int { return pods },
		trimmed: func(t *testing.T, list client.ObjectList) {
			p := list.(*corev1.PodList).Items[0]
			assert.Empty(t, p.Labels, "a cached pod keeps no labels")
			assert.Empty(t, p.OwnerReferences, "or owners")
			assert.Empty(t, p.Spec.Containers, "or containers")
			assert.Equal(t, "750m", p.Spec.Overhead.Cpu().String(), "its requests are collapsed into its overhead: "+
				"what the scheduler counts, the app, the log shipper and the mesh proxy, more than the init container")
		},
	})
	for _, n := range podCounts {
		t.Logf("the operator, %d pods: %s a pod", n, perPod[n])
	}
	for _, n := range podCounts {
		if n >= heldFrom {
			heldTo(t, perPod[n].median, podBytes, fmt.Sprintf("the operator's bytes a pod, at %d pods", n))
		}
	}
}

// The doorman caches every EndpointSlice the EndpointSlice controller
// writes, cut down by doorman.EndpointSliceCache. A slice holds up to 100
// endpoints, one a pod, so its cost is one for its Service and one for each
// pod behind it. Measured with one pod behind each Service, as most are in
// a non-production cluster, and with ten, the two give each part.
func TestDoormanEndpointSliceCache(t *testing.T) {
	perSlice := map[int]map[int]result{}
	for _, endpoints := range []int{1, 10} {
		t.Run(fmt.Sprintf("%d endpoints a slice", endpoints), func(t *testing.T) {
			api := startAPIServer(t)
			perSlice[endpoints] = api.measure(t, kind{
				name:   "endpointslices",
				obj:    &discoveryv1.EndpointSlice{},
				list:   &discoveryv1.EndpointSliceList{},
				cache:  doorman.EndpointSliceCache(),
				counts: podCounts,
				create: func(ctx context.Context, c client.Client, i int) error {
					if i%endpoints != 0 {
						return nil
					}
					return c.Create(ctx, endpointSlice(i/endpoints, endpoints), writer)
				},
				cached: func(pods int) int { return pods / endpoints },
				trimmed: func(t *testing.T, list client.ObjectList) {
					s := list.(*discoveryv1.EndpointSliceList).Items[0]
					assert.Len(t, s.Labels, 2, "a cached slice keeps only its Service's name and its writer")
					assert.Empty(t, s.OwnerReferences, "and no owner")
					require.Len(t, s.Endpoints, endpoints)
					assert.Nil(t, s.Endpoints[0].NodeName, "an endpoint keeps no node")
				},
			})
		})
	}
	require.Len(t, perSlice, 2, "both measured")
	for _, n := range podCounts {
		one, ten := perSlice[1][n].median, perSlice[10][n].median
		endpoint := (ten - one) / 9
		service := one - endpoint
		t.Logf("the doorman, %d pods: %s a slice with 1 endpoint, %s a slice with 10; "+
			"so %d bytes a pod and %d a Service", n, perSlice[1][n], perSlice[10][n], endpoint, service)
		if n >= heldFrom {
			heldTo(t, endpoint, endpointBytes, fmt.Sprintf("the doorman's bytes a pod, at %d pods", n))
			heldTo(t, service, serviceBytes, fmt.Sprintf("the doorman's bytes a Service, at %d pods", n))
		}
	}
}

// The doorman caches every ManagedWorkload in the watched namespaces,
// without its managed fields: of a ManagedWorkload, trimForCache drops only
// those, as cache.TransformStripManagedFields does. The operator caches
// them the same way, and indexes them too, which this leaves out. Measured
// with what a paused workload's status holds after weeks of use, the
// forecast's state among it.
func TestManagedWorkloadCache(t *testing.T) {
	api := startAPIServer(t, filepath.Join("..", "..", "config", "crd", "bases"))
	template := managedWorkload(t)
	perWorkload := api.measure(t, kind{
		name:   "managedworkloads",
		obj:    &v1alpha1.ManagedWorkload{},
		list:   &v1alpha1.ManagedWorkloadList{},
		cache:  cache.ByObject{Transform: cache.TransformStripManagedFields()},
		counts: workloadCounts,
		create: func(ctx context.Context, c client.Client, i int) error {
			w := template.DeepCopy()
			w.Name, w.Namespace = app(i), podNamespace(i*replicas)
			w.Spec.Target.Name = w.Name
			w.OwnerReferences[0].Name, w.OwnerReferences[0].UID = w.Name, uuid.NewUUID()
			if err := c.Create(ctx, w, writer); err != nil {
				return err
			}
			w.Status = *template.Status.DeepCopy()
			return c.Status().Update(ctx, w, writer)
		},
		cached: func(workloads int) int { return workloads },
		trimmed: func(t *testing.T, list client.ObjectList) {
			w := list.(*v1alpha1.ManagedWorkloadList).Items[0]
			assert.Empty(t, w.ManagedFields, "a cached ManagedWorkload keeps no managed fields")
			require.NotNil(t, w.Status.Prediction)
			assert.Equal(t, template.Status.Prediction.State, w.Status.Prediction.State, "but its whole status")
		},
	})
	for _, n := range workloadCounts {
		t.Logf("the doorman, %d ManagedWorkloads: %s a ManagedWorkload", n, perWorkload[n])
		if n >= heldFrom {
			heldTo(t, perWorkload[n].median, managedWorkloadBytes,
				fmt.Sprintf("the doorman's bytes a ManagedWorkload, at %d", n))
		}
	}
}

// heldTo checks a figure the chart and docs quote against what was
// measured: at least what was measured, rounded up no further than
// roundedUp.
func heldTo(t *testing.T, measured, quoted int64, what string) {
	t.Helper()
	assert.LessOrEqual(t, measured, quoted, "%s: measured %d, but the chart says %d", what, measured, quoted)
	assert.LessOrEqual(t, float64(quoted), float64(measured)*roundedUp,
		"%s: the chart says %d, more than %d measured rounds up to", what, quoted, measured)
}

// kind is a cache to measure: the kind of object, how the operator or the
// doorman caches it, and how to fill the cluster with ones that look real.
type kind struct {
	name  string
	obj   client.Object
	list  client.ObjectList
	cache cache.ByObject
	// counts are the counts of things, pods or ManagedWorkloads, the cache
	// is measured at.
	counts []int
	// create creates what the i-th thing brings to the cluster, if
	// anything.
	create func(ctx context.Context, c client.Client, i int) error
	// cached is how many objects the cache holds for that many things.
	cached func(n int) int
	// trimmed checks the cache holds what the trimming leaves.
	trimmed func(t *testing.T, list client.ObjectList)
}

// result is what one object cost in each run at one count, in bytes: the
// heap's growth, less an empty cache's, over the objects it held.
type result struct {
	median, min, max int64
	runs             []int64
}

func (r result) String() string {
	return fmt.Sprintf("%d bytes (median of %d runs, %d to %d)", r.median, len(r.runs), r.min, r.max)
}

func summarise(each []int64) result {
	sorted := slices.Sorted(slices.Values(each))
	return result{median: sorted[len(sorted)/2], min: sorted[0], max: sorted[len(sorted)-1], runs: each}
}

// apiServer is an envtest API server, the client that fills it, and the
// HTTP client and REST mapper each measured cache shares, as every cache
// in one manager does, so their cost isn't counted as the cache's.
type apiServer struct {
	cfg        *rest.Config
	kubeconfig []byte
	scheme     *k8sruntime.Scheme
	client     client.Client
	httpClient *http.Client
	mapper     meta.RESTMapper
}

func startAPIServer(t *testing.T, crds ...string) *apiServer {
	t.Helper()
	logf.SetLogger(zap.New(zap.WriteTo(io.Discard)))
	s := k8sruntime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	env := &envtest.Environment{CRDDirectoryPaths: crds, ErrorIfCRDPathMissing: true, Scheme: s}
	// A cluster's range, with room for a Service for each of 20,000
	// Deployments. envtest's own holds 256.
	env.ControlPlane.GetAPIServer().Configure().Set("service-cluster-ip-range", "10.96.0.0/12")
	cfg, err := env.Start()
	require.NoError(t, err, "starting envtest; set KUBEBUILDER_ASSETS to an absolute path, as `make setup-envtest` prints")
	t.Cleanup(func() { assert.NoError(t, env.Stop()) })

	fill := rest.CopyConfig(cfg)
	fill.QPS = -1 // no client-side rate limit, to create 100,000 objects in a minute or two
	c, err := client.New(fill, client.Options{Scheme: s})
	require.NoError(t, err)
	httpClient, err := rest.HTTPClientFor(cfg)
	require.NoError(t, err)
	mapper, err := apiutil.NewDynamicRESTMapper(cfg, httpClient)
	require.NoError(t, err)
	for i := range namespaces {
		require.NoError(t, c.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace(i)}}))
	}
	return &apiServer{cfg: cfg, kubeconfig: env.KubeConfig, scheme: s, client: c, httpClient: httpClient, mapper: mapper}
}

// measure fills the cluster up to each of k's counts, and at each, and
// first with none, caches k runs times and reads what each object cost.
// The first cache of each is left uncounted: it sets up what every later
// one shares, such as the decoder's view of the kind.
func (a *apiServer) measure(t *testing.T, k kind) map[int]result {
	t.Helper()
	ctx := t.Context()
	a.cacheGrowth(t, k, 0)
	empty := make([]int64, runs)
	for run := range runs {
		empty[run] = a.cacheGrowth(t, k, 0)
	}
	fixed := summarise(empty).median
	t.Logf("%s: an empty cache grows the heap by %d bytes (%d to %d)", k.name, fixed, slices.Min(empty), slices.Max(empty))

	out := map[int]result{}
	created := 0
	for _, n := range k.counts {
		a.fill(ctx, t, k, created, n)
		objects := k.cached(n)
		if created == 0 {
			a.cacheGrowth(t, k, objects)
		}
		created = n
		each := make([]int64, runs)
		for run := range runs {
			growth := a.cacheGrowth(t, k, objects)
			each[run] = (growth - fixed) / int64(objects)
			t.Logf("%s: %d, run %d: %d objects grew the heap by %d bytes, %d an object",
				k.name, n, run+1, objects, growth, each[run])
		}
		out[n] = summarise(each)
	}
	return out
}

// fill creates what things from to to bring to the cluster.
func (a *apiServer) fill(ctx context.Context, t *testing.T, k kind, from, to int) {
	t.Helper()
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(writers)
	for i := from; i < to; i++ {
		g.Go(func() error {
			if err := k.create(ctx, a.client, i); err != nil {
				return fmt.Errorf("creating %s for %d: %w", k.name, i, err)
			}
			return nil
		})
	}
	require.NoError(t, g.Wait())
}

// cacheGrowth starts the cache k describes, cluster-wide, as an install
// that watches every namespace does, waits until it holds every object,
// and returns how far that grew the live heap.
func (a *apiServer) cacheGrowth(t *testing.T, k kind, want int) int64 {
	t.Helper()
	before := liveHeap()
	c, err := cache.New(a.cfg, cache.Options{
		HTTPClient: a.httpClient,
		Scheme:     a.scheme,
		Mapper:     a.mapper,
		ByObject:   map[client.Object]cache.ByObject{k.obj: k.cache},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	var g errgroup.Group
	g.Go(func() error { return c.Start(ctx) })
	defer func() {
		cancel()
		require.NoError(t, g.Wait())
	}()
	_, err = c.GetInformer(ctx, k.obj)
	require.NoError(t, err)
	require.True(t, c.WaitForCacheSync(ctx), "the cache syncs")
	growth := liveHeap() - before

	list := k.list.DeepCopyObject().(client.ObjectList)
	require.NoError(t, c.List(ctx, list, client.UnsafeDisableDeepCopy))
	require.Equal(t, want, meta.LenList(list), "the cache holds every %s", k.name)
	if want > 0 {
		k.trimmed(t, list)
	}
	return growth
}

// liveHeap is what the heap holds once a full collection has freed all it
// can. The second frees what the first left for finalizers.
func liveHeap() int64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.HeapAlloc)
}

func namespace(i int) string { return fmt.Sprintf("team-%02d-staging", i) }

func app(deployment int) string { return fmt.Sprintf("service-%05d", deployment) }

func podName(i int) string { return fmt.Sprintf("%s-7d9f8b6c5d-%05d", app(i/replicas), i) }

func podNamespace(i int) string { return namespace((i / replicas) % namespaces) }

func nodeName(i int) string {
	n := i % nodes
	return fmt.Sprintf("ip-10-0-%d-%d.ec2.internal", n/100, n%100+10)
}

func podIP(i int) string { return fmt.Sprintf("10.%d.%d.%d", 64+i>>16&63, i>>8&255, i&255) }

func zone(i int) string { return "us-east-1" + string(rune('a'+i%3)) }

func requests(cpu, memory string) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(cpu),
		corev1.ResourceMemory: resource.MustParse(memory),
	}
}

// pod is the i-th pod as a ReplicaSet makes it in a non-production cluster
// with a service mesh: its chart's and the mesh's labels and annotations,
// its owner, a migration that runs first, the mesh proxy as a sidecar, the
// app and a log shipper, each with its requests, bound to one of the
// cluster's nodes. The API server sets it Pending, which the operator
// caches as it would a Running pod.
func pod(i int) *corev1.Pod {
	name := app(i / replicas)
	env := []corev1.EnvVar{
		{Name: "SERVICE_NAME", Value: name},
		{Name: "LOG_LEVEL", Value: "info"},
		{Name: "DATABASE_HOST", Value: name + "-postgresql." + podNamespace(i) + ".svc.cluster.local"},
		{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "http://otel-collector.observability.svc:4317"},
		{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
		}},
	}
	probe := func(path string) *corev1.Probe {
		return &corev1.Probe{
			ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromInt32(8080)}},
			PeriodSeconds: 10,
		}
	}
	mounts := []corev1.VolumeMount{
		{Name: "config", MountPath: "/etc/" + name},
		{Name: "kube-api-access", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount", ReadOnly: true},
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName(i),
			Namespace: podNamespace(i),
			Labels: map[string]string{
				"app.kubernetes.io/name":          name,
				"app.kubernetes.io/instance":      name + "-staging",
				"app.kubernetes.io/version":       "1.42.0",
				"app.kubernetes.io/part-of":       "shop",
				"pod-template-hash":               "7d9f8b6c5d",
				"security.istio.io/tlsMode":       "istio",
				"service.istio.io/canonical-name": name,
			},
			Annotations: map[string]string{
				"kubectl.kubernetes.io/default-container": "app",
				"prometheus.io/scrape":                    "true",
				"prometheus.io/port":                      "9090",
				"istio.io/rev":                            "default",
				"sidecar.istio.io/status": `{"initContainers":["istio-proxy"],"containers":null,` +
					`"volumes":["workload-socket","istio-envoy","istio-data","istio-podinfo","istio-token"],"revision":"default"}`,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1", Kind: "ReplicaSet", Name: name + "-7d9f8b6c5d", UID: uuid.NewUUID(),
				Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true),
			}},
		},
		Spec: corev1.PodSpec{
			NodeName:           nodeName(i),
			ServiceAccountName: name,
			InitContainers: []corev1.Container{{
				Name:      "migrate",
				Image:     "registry.example.com/shop/" + name + "-migrate:1.42.0",
				Args:      []string{"migrate", "up"},
				Env:       env[:3],
				Resources: corev1.ResourceRequirements{Requests: requests("100m", "128Mi")},
			}, {
				Name:          "istio-proxy",
				Image:         "docker.io/istio/proxyv2:1.24.0",
				Args:          []string{"proxy", "sidecar", "--domain", "$(POD_NAMESPACE).svc.cluster.local"},
				RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways),
				Ports:         []corev1.ContainerPort{{Name: "http-envoy-prom", ContainerPort: 15090}},
				Resources: corev1.ResourceRequirements{
					Requests: requests("100m", "128Mi"),
					Limits:   requests("2", "1Gi"),
				},
			}},
			Containers: []corev1.Container{{
				Name:  "app",
				Image: "registry.example.com/shop/" + name + ":1.42.0",
				Ports: []corev1.ContainerPort{
					{Name: "web", ContainerPort: 8080},
					{Name: "prometheus", ContainerPort: 9090},
				},
				Env: env,
				Resources: corev1.ResourceRequirements{
					Requests: requests("600m", "512Mi"),
					Limits:   requests("1", "1Gi"),
				},
				LivenessProbe:  probe("/healthz"),
				ReadinessProbe: probe("/readyz"),
				VolumeMounts:   mounts,
			}, {
				Name:         "log-shipper",
				Image:        "cr.fluentbit.io/fluent/fluent-bit:3.2",
				Resources:    corev1.ResourceRequirements{Requests: requests("50m", "64Mi")},
				VolumeMounts: mounts[1:],
			}},
			Volumes: []corev1.Volume{
				{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: name + "-config"},
				}}},
				{Name: "kube-api-access", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{
						{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
							Path: "token", ExpirationSeconds: ptr.To[int64](3607),
						}},
						{ConfigMap: &corev1.ConfigMapProjection{
							LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"},
							Items:                []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}},
						}},
					},
				}}},
			},
			Tolerations: []corev1.Toleration{
				{Key: "node.kubernetes.io/not-ready", Operator: corev1.TolerationOpExists,
					Effect: corev1.TaintEffectNoExecute, TolerationSeconds: ptr.To[int64](300)},
				{Key: "node.kubernetes.io/unreachable", Operator: corev1.TolerationOpExists,
					Effect: corev1.TaintEffectNoExecute, TolerationSeconds: ptr.To[int64](300)},
			},
		},
	}
}

// endpointSlice is the Service's slice as the EndpointSlice controller
// writes it, with the Service's labels, for its pods from the first, each
// Ready, on a node in a zone.
func endpointSlice(service, pods int) *discoveryv1.EndpointSlice {
	first := service * pods
	name := app(service)
	ns := podNamespace(first)
	endpoints := make([]discoveryv1.Endpoint, pods)
	for j := range endpoints {
		i := first + j
		endpoints[j] = discoveryv1.Endpoint{
			Addresses: []string{podIP(i)},
			Conditions: discoveryv1.EndpointConditions{
				Ready: ptr.To(true), Serving: ptr.To(true), Terminating: ptr.To(false),
			},
			NodeName:  ptr.To(nodeName(i)),
			Zone:      ptr.To(zone(i)),
			TargetRef: &corev1.ObjectReference{Kind: "Pod", Namespace: ns, Name: podName(i), UID: uuid.NewUUID()},
		}
	}
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: name + "-",
			Namespace:    ns,
			Labels: map[string]string{
				discoveryv1.LabelServiceName:   name,
				discoveryv1.LabelManagedBy:     "endpointslice-controller.k8s.io",
				"app.kubernetes.io/name":       name,
				"app.kubernetes.io/instance":   name + "-staging",
				"app.kubernetes.io/managed-by": "Helm",
				"helm.sh/chart":                "shop-1.42.0",
			},
			Annotations: map[string]string{"endpoints.kubernetes.io/last-change-trigger-time": "2026-10-30T09:12:44Z"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1", Kind: "Service", Name: name, UID: uuid.NewUUID(),
				Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true),
			}},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   endpoints,
		Ports: []discoveryv1.EndpointPort{
			{Name: ptr.To("http"), Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To[int32](8080)},
			{Name: ptr.To("metrics"), Protocol: ptr.To(corev1.ProtocolTCP), Port: ptr.To[int32](9090)},
		},
	}
}

// pausedWorkload is a workload opted in with the label, paused for the
// evening after weeks of use, as its ManagedWorkload's status then holds
// it: the pause it will undo, its route through the doorman, its month's
// cost, and the forecast's phases, with its state added from a real one.
const pausedWorkload = `
metadata:
  name: service-00000
  namespace: team-00-staging
  labels:
    napwake.dev/from-label: "true"
  finalizers: [napwake.dev/cleanup]
  ownerReferences:
  - {apiVersion: apps/v1, kind: Deployment, name: service-00000, uid: 6f1c9a52-8d0e-4b7a-9c3e-2f5d1a7b8c90}
spec:
  target: {kind: Deployment, name: service-00000}
  idlePolicy:
    idleAfter: 30m
    activity: {cpuThreshold: 10}
    autoResume: true
  wake: {mode: Request}
  prediction: {mode: balanced}
status:
  phase: Paused
  conditions:
  - {type: TargetAvailable, status: "True", reason: TargetExists, lastTransitionTime: "2026-10-02T10:14:03Z",
     message: Deployment service-00000 exists and is managed}
  - {type: MetricsAvailable, status: "True", reason: MetricsReported, lastTransitionTime: "2026-10-30T07:58:41Z",
     message: CPU is measured from the Metrics API for 2 Ready pods}
  - {type: IdleVetoed, status: "False", reason: NotVetoed, lastTransitionTime: "2026-10-30T18:01:12Z",
     message: the forecast expects no demand in the coming hour}
  - {type: WakeOnRequest, status: "True", reason: DoormanRouted, lastTransitionTime: "2026-10-30T18:04:12Z",
     message: "1 Service is routed to the doorman: service-00000"}
  - {type: DependencyNotFound, status: "False", reason: NoDependencies, lastTransitionTime: "2026-10-02T10:14:03Z",
     message: it has no dependencies}
  - {type: Protected, status: "False", reason: NotProtected, lastTransitionTime: "2026-10-02T10:14:03Z",
     message: it can be paused}
  - {type: PausedWhileInUse, status: "False", reason: PauseHeld, lastTransitionTime: "2026-10-21T19:30:00Z",
     message: its last pause held}
  pause:
    previousReplicas: 2
    startedAt: "2026-10-30T18:04:10Z"
    routesWrittenAt: "2026-10-30T18:04:11Z"
    routedAt: "2026-10-30T18:04:12Z"
    pausedAt: "2026-10-30T18:04:40Z"
    resources:
      replicas: 2
      cpuMillis: 750
      memoryBytes: 738197504
      storageBytes: 0
      nodePools: [{name: general-purpose, pods: 2}]
    cpuBefore: "0.04"
    cpuJustBefore: "0.02"
  lastScaledUp: {at: "2026-10-30T07:58:02Z", by: napwake, replicas: 2}
  turnedAway: {pausedAt: "2026-10-29T18:12:40Z", requests: 3}
  prediction:
    dailyPhase: Active
    dailyConfidence: 84
    weeklyPhase: Suggesting
    weeklyConfidence: 71
    timeZone: Europe/London
    threshold: 75
    acting: true
    reason: ForecastQuiet
    message: the forecast expects no demand until 08:00
    history: {reason: Learned, hours: 672, from: "2026-09-04T10:00:00Z", to: "2026-10-02T10:00:00Z"}
  cost:
    tracked: 712h0m0s
    lastAccumulatedAt: "2026-10-30T18:00:00Z"
    awakeCPUHours: "160500m"
    awakeMemoryHours: "321"
    storageHours: "0"
    pausedCPUHours: "664500m"
    pausedMemoryHours: "1329"
    savedCPUHours: "398700m"
    savedMemoryHours: "0"
    costThisMonth: "$6.84"
    freedThisMonth: "$26.47"
    savedThisMonth: "$12.62"
    costWithoutNapwakeThisMonth: "$33.31"
    projectedMonthlyCost: "$7.15"
    projectedMonthlyFreed: "$27.68"
    projectedMonthlySavings: "$13.20"
    running: {replicas: 2, cpuMillis: 750, memoryBytes: 738197504, storageBytes: 0}
    pricedAt: "2026-10-30T07:58:00Z"
    pricedTemplateHash: 3f9c2a71d0e4b815
    listRates: {cpuPerHour: 31660u, memoryPerHour: 4085u}
    listPrice: OnDemand
  doorman: [{service: service-00000, portName: http, doormanPort: 20417}]
  activity:
    lastActivityTime: "2026-10-30T17:34:02Z"
    lastActivitySource: cpu
    lastEvaluatedTime: "2026-10-30T18:04:10Z"
    templateHash: 7d9f8b6c5d
  idleWait: {idleCPU: "0.02", idleCPUAt: "2026-10-29T18:30:00Z"}
`

func managedWorkload(t *testing.T) *v1alpha1.ManagedWorkload {
	t.Helper()
	var w v1alpha1.ManagedWorkload
	require.NoError(t, yaml.UnmarshalStrict([]byte(pausedWorkload), &w))
	w.Status.Prediction.State = forecastState(t)
	return &w
}

// forecastState is what five weeks of a weekday workload, busy from 9 to 5
// and using more or less from hour to hour, teach the forecast, as its
// status keeps it.
func forecastState(t *testing.T) string {
	t.Helper()
	e := forecast.NewEngine(forecast.DefaultParams(), forecast.Settings{Threshold: 75})
	noise := rand.New(rand.NewPCG(1, 2))
	start := time.Now().Add(-5 * 7 * 24 * time.Hour)
	for i := range 5 * forecast.WeeklySeason {
		at := start.Add(time.Duration(i) * time.Hour)
		demand := 0.5 * noise.Float64()
		if wd := at.Weekday(); wd != time.Saturday && wd != time.Sunday && at.Hour() >= 9 && at.Hour() < 17 {
			demand = 30 + 20*noise.Float64()
		}
		_, err := e.Observe(demand, at)
		require.NoError(t, err)
	}
	state, err := e.Export()
	require.NoError(t, err)
	t.Logf("the forecast's state is %d bytes", len(state))
	return state
}
