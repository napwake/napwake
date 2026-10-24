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

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/controller"
	"github.com/napwake/napwake/internal/doorman"
	"github.com/napwake/napwake/internal/forwarder"
	"github.com/napwake/napwake/internal/gitops"
)

// The image is built from a context .dockerignore keeps to what the build
// needs, so every file the manager embeds has to be let back in by name,
// or the image doesn't build.
func TestImageBuildContext_KeepsEveryEmbeddedFile(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	out, err := exec.Command("go", "list", "-deps",
		"-f", "{{.Dir}}{{range .EmbedFiles}} {{.}}{{end}}", ".").Output()
	require.NoError(t, err)
	ignore, err := os.ReadFile(filepath.Join(root, ".dockerignore"))
	require.NoError(t, err)
	kept := strings.Split(string(ignore), "\n")

	var embedded []string
	for line := range strings.Lines(string(out)) {
		dir, files, _ := strings.Cut(strings.TrimSpace(line), " ")
		rel, err := filepath.Rel(root, dir)
		if err != nil || strings.HasPrefix(rel, "..") || files == "" {
			continue
		}
		for _, f := range strings.Fields(files) {
			path := filepath.ToSlash(filepath.Join(rel, f))
			embedded = append(embedded, path)
			assert.Contains(t, kept, "!"+path, ".dockerignore leaves %s out of the image's build context", path)
		}
	}
	assert.True(t, slices.Contains(embedded, "internal/doorman/page.html"), "found the manager's embedded files")
}

// The garbage collector aims below the container's limit, leaving room for
// memory outside the heap, rather than at it, where the kernel kills first.
func TestSoftMemoryLimit(t *testing.T) {
	tests := []struct {
		name       string
		gomemlimit string
		container  string
		want       int64
		wantOK     bool
	}{
		{name: "90% of the container's limit", container: "268435456", want: 241591905, wantOK: true},
		{name: "GOMEMLIMIT set by hand wins", gomemlimit: "200MiB", container: "268435456"},
		{name: "no limit", container: ""},
		{name: "unparseable", container: "256Mi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := softMemoryLimit(tt.gomemlimit, tt.container)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidatePrometheusURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "empty is allowed", url: ""},
		{name: "in-cluster service", url: "http://prometheus.monitoring.svc:9090"},
		{name: "https with path prefix", url: "https://mimir.example.com/prometheus"},
		{name: "missing scheme", url: "prometheus.monitoring.svc:9090", wantErr: true},
		{name: "unsupported scheme", url: "ftp://prometheus:9090", wantErr: true},
		{name: "no host", url: "http://", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePrometheusURL(tt.url)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestParsePrometheusSelector(t *testing.T) {
	const url = "http://thanos.monitoring.svc:9090"
	tests := []struct {
		name    string
		raw     string
		url     string
		want    []string
		wantErr string
	}{
		{name: "none", url: url},
		{name: "one cluster", raw: `cluster="prelaunch"`, url: url, want: []string{`cluster="prelaunch"`}},
		{name: "braces and a regex", raw: `{cluster=~"pre.*", replica!="b"}`, url: url,
			want: []string{`cluster=~"pre.*"`, `replica!="b"`}},
		{name: "unquoted value", raw: `cluster=prelaunch`, url: url, wantErr: "double-quoted"},
		{name: "a query smuggled in", raw: `cluster="a"} or vector(1)`, url: url, wantErr: "expected a comma"},
		{name: "no Prometheus to narrow", raw: `cluster="prelaunch"`, wantErr: "--prometheus-url, which isn't set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePrometheusSelector(tt.raw, tt.url)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, []string(got))
		})
	}
}

func TestValidateOptInDefaults(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*controller.OptInDefaults)
		wantErr bool
	}{
		{name: "built-in defaults", mutate: func(*controller.OptInDefaults) {}},
		{name: "threshold of 100", mutate: func(d *controller.OptInDefaults) { d.CPUThreshold = 100 }},
		{name: "threshold of 1", mutate: func(d *controller.OptInDefaults) { d.CPUThreshold = 1 }},
		{name: "a decimal threshold", mutate: func(d *controller.OptInDefaults) { d.CPUThreshold = 0.5 }},
		{name: "threshold below the CRD's minimum", mutate: func(d *controller.OptInDefaults) { d.CPUThreshold = 0.05 },
			wantErr: true},
		{name: "threshold with more places than the CRD takes",
			mutate: func(d *controller.OptInDefaults) { d.CPUThreshold = 0.125 }, wantErr: true},
		{name: "threshold above the CRD's maximum", mutate: func(d *controller.OptInDefaults) { d.CPUThreshold = 150 },
			wantErr: true},
		{name: "threshold of 0, which the CRD turns into 10",
			mutate: func(d *controller.OptInDefaults) { d.CPUThreshold = 0 }, wantErr: true},
		{name: "negative threshold", mutate: func(d *controller.OptInDefaults) { d.CPUThreshold = -5 }, wantErr: true},
		{name: "idleAfter of a minute", mutate: func(d *controller.OptInDefaults) { d.IdleAfter = time.Minute }},
		{name: "idleAfter under a minute, below the CRD's minimum",
			mutate: func(d *controller.OptInDefaults) { d.IdleAfter = 59 * time.Second }, wantErr: true},
		{name: "zero idleAfter", mutate: func(d *controller.OptInDefaults) { d.IdleAfter = 0 }, wantErr: true},
		{name: "idleAfter of a minute", mutate: func(d *controller.OptInDefaults) { d.IdleAfter = time.Minute }},
		{name: "idleAfter under the CRD's minute, which would pause and wake in a loop",
			mutate: func(d *controller.OptInDefaults) { d.IdleAfter = 30 * time.Second }, wantErr: true},
		{name: "negative idleAfter", mutate: func(d *controller.OptInDefaults) { d.IdleAfter = -time.Minute },
			wantErr: true},
		{name: "an eager forecast", mutate: func(d *controller.OptInDefaults) { d.Forecast = v1alpha1.PredictionEager }},
		{name: "a forecast setting the CRD doesn't have",
			mutate: func(d *controller.OptInDefaults) { d.Forecast = "Eager" }, wantErr: true},
		{name: "no forecast setting", mutate: func(d *controller.OptInDefaults) { d.Forecast = "" }, wantErr: true},
		{name: "asking before waking", mutate: func(d *controller.OptInDefaults) { d.Wake = v1alpha1.WakeOnAsk }},
		{name: "a wake setting the CRD doesn't have",
			mutate: func(d *controller.OptInDefaults) { d.Wake = "ask" }, wantErr: true},
		{name: "no wake setting", mutate: func(d *controller.OptInDefaults) { d.Wake = "" }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := controller.DefaultOptInDefaults
			tt.mutate(&d)
			err := validateOptInDefaults(d)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestNapwakeNamespaces(t *testing.T) {
	tests := []struct {
		name                              string
		pod, doormanNamespace, doormanSvc string
		want                              []string
	}{
		{name: "operator and doorman together", pod: "napwake-system", doormanNamespace: "napwake-system",
			doormanSvc: "napwake-doorman", want: []string{"napwake-system"}},
		{name: "doorman elsewhere", pod: "platform", doormanNamespace: "edge", doormanSvc: "napwake-doorman",
			want: []string{"platform", "edge"}},
		{name: "doorman disabled", pod: "platform", doormanNamespace: "edge", want: []string{"platform"}},
		{name: "run outside the cluster", doormanNamespace: "napwake-system", doormanSvc: "napwake-doorman",
			want: []string{"napwake-system"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := napwakeNamespaces(tt.pod, tt.doormanNamespace, tt.doormanSvc)
			assert.Equal(t, tt.want, got)
			for _, ns := range got {
				assert.True(t, v1alpha1.Protected(ns, nil, got), "%s is protected", ns)
			}
		})
	}
}

func managedFields(t *testing.T) []metav1.ManagedFieldsEntry {
	t.Helper()
	at := func(minute int) *metav1.Time {
		return &metav1.Time{Time: time.Date(2026, 10, 5, 9, minute, 0, 0, time.UTC)}
	}
	return []metav1.ManagedFieldsEntry{
		{Manager: "argocd-controller", Operation: metav1.ManagedFieldsOperationApply, Time: at(1),
			FieldsV1: metav1.NewFieldsV1(`{"f:metadata":{"f:labels":{}},"f:spec":{"f:replicas":{},"f:template":{}}}`)},
		{Manager: "kube-controller-manager", Operation: metav1.ManagedFieldsOperationUpdate, Time: at(5),
			FieldsV1: metav1.NewFieldsV1(`{"f:status":{"f:replicas":{},"f:readyReplicas":{}}}`)},
		{Manager: "kubectl-edit", Operation: metav1.ManagedFieldsOperationUpdate, Time: at(3),
			FieldsV1: metav1.NewFieldsV1(`{"f:spec":{"f:template":{"f:spec":{}}}}`)},
	}
}

func TestTrimForCacheKeepsWhoSetReplicas(t *testing.T) {
	fields := managedFields(t)
	wantWriter, wantFound := gitops.ReplicasWriter(fields)
	require.True(t, wantFound)

	for _, obj := range []client.Object{&appsv1.Deployment{}, &appsv1.StatefulSet{}} {
		obj.SetManagedFields(managedFields(t))
		obj.SetAnnotations(map[string]string{corev1.LastAppliedConfigAnnotation: "{}", "team": "payments"})

		trimmed, err := trimForCache(obj)
		require.NoError(t, err)
		got := trimmed.(client.Object)

		writer, found := gitops.ReplicasWriter(got.GetManagedFields())
		assert.True(t, found)
		assert.Equal(t, wantWriter, writer)
		assert.Len(t, got.GetManagedFields(), 1, "only the entry that set spec.replicas is kept")
		assert.Equal(t, map[string]string{"team": "payments"}, got.GetAnnotations())
	}
}

func TestTrimForCacheDropsManagedFieldsAndLastApplied(t *testing.T) {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{
		ManagedFields: managedFields(t),
		Annotations:   map[string]string{corev1.LastAppliedConfigAnnotation: "{}"},
	}}

	trimmed, err := trimForCache(svc)
	require.NoError(t, err)

	got := trimmed.(*corev1.Service)
	assert.Empty(t, got.ManagedFields)
	assert.Empty(t, got.Annotations)
}

func TestTrimForCacheKeepsManagedWorkloadsLastApplied(t *testing.T) {
	mw := &v1alpha1.ManagedWorkload{ObjectMeta: metav1.ObjectMeta{
		ManagedFields: managedFields(t),
		Annotations:   map[string]string{corev1.LastAppliedConfigAnnotation: "{}"},
	}}

	trimmed, err := trimForCache(mw)
	require.NoError(t, err)

	got := trimmed.(*v1alpha1.ManagedWorkload)
	assert.Empty(t, got.ManagedFields)
	assert.Contains(t, got.Annotations, corev1.LastAppliedConfigAnnotation,
		"the operator updates ManagedWorkloads whole, which would delete kubectl's record")
}

// A stopping leader hands over its lease, so the next operator leads at
// once rather than after the lease expires.
func TestLeaderElectionHandsOverOnStop(t *testing.T) {
	cfg := startEnvtest(t)
	newManager := func() ctrl.Manager {
		opts := withLeaderElection(ctrl.Options{
			Metrics:                 metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress:  "0",
			LeaderElectionNamespace: "default",
		}, true)
		mgr, err := ctrl.NewManager(cfg, opts)
		require.NoError(t, err)
		return mgr
	}

	first := newManager()
	ctx, stop := context.WithCancel(context.Background())
	var running sync.WaitGroup
	running.Go(func() { assert.NoError(t, first.Start(ctx)) })
	select {
	case <-first.Elected():
	case <-time.After(30 * time.Second):
		t.Fatal("the first operator never led")
	}
	stop()
	running.Wait()

	direct, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	var lease coordinationv1.Lease
	require.NoError(t, direct.Get(context.Background(),
		client.ObjectKey{Namespace: "default", Name: withLeaderElection(ctrl.Options{}, true).LeaderElectionID}, &lease))
	assert.Empty(t, ptr.Deref(lease.Spec.HolderIdentity, ""), "the lease was handed over, not left to expire")

	second := newManager()
	ctx, stop = context.WithCancel(context.Background())
	defer func() {
		stop()
		running.Wait()
	}()
	running.Go(func() { assert.NoError(t, second.Start(ctx)) })
	select {
	case <-second.Elected():
	case <-time.After(30 * time.Second):
		t.Fatal("the second operator never led")
	}
}

func startEnvtest(t *testing.T) *rest.Config {
	t.Helper()
	env := &envtest.Environment{}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		dirs, _ := filepath.Glob(filepath.Join("..", "bin", "k8s", "*"))
		if len(dirs) > 0 {
			env.BinaryAssetsDirectory = dirs[0]
		}
	}
	cfg, err := env.Start()
	require.NoError(t, err, "starting envtest; run make setup-envtest")
	t.Cleanup(func() { assert.NoError(t, env.Stop()) })
	return cfg
}

func endpointSlice(namespace, name string, labels map[string]string) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: labels},
		AddressType: discoveryv1.AddressTypeIPv4,
	}
}

func sliceNames(t *testing.T, ctx context.Context, c client.Reader, opts ...client.ListOption) []string {
	t.Helper()
	var list discoveryv1.EndpointSliceList
	require.NoError(t, c.List(ctx, &list, opts...))
	names := make([]string, 0, len(list.Items))
	for _, s := range list.Items {
		names = append(names, s.Namespace+"/"+s.Name)
	}
	return names
}

// The doorman caches only the slices it trusts, those the EndpointSlice
// controller writes, and only what it reads of them.
func TestDoormanCacheHoldsOnlyTrustedSlicesTrimmed(t *testing.T) {
	cfg := startEnvtest(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	direct, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	require.NoError(t, direct.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}}))
	own := endpointSlice("shop", "web-xyz", map[string]string{
		discoveryv1.LabelServiceName: "web", discoveryv1.LabelManagedBy: "endpointslice-controller.k8s.io",
	})
	own.Endpoints = []discoveryv1.Endpoint{{
		Addresses:  []string{"10.244.1.9"},
		Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)},
		NodeName:   ptr.To("node-1"),
		TargetRef:  &corev1.ObjectReference{Kind: "Pod", Namespace: "shop", Name: "web-0"},
	}}
	for _, s := range []*discoveryv1.EndpointSlice{
		own,
		endpointSlice("shop", "web-napwake-doorman",
			map[string]string{discoveryv1.LabelManagedBy: doorman.ManagedBy, discoveryv1.LabelServiceName: "web"}),
		endpointSlice("shop", "web-by-hand", map[string]string{discoveryv1.LabelServiceName: "web"}),
	} {
		require.NoError(t, direct.Create(ctx, s))
	}

	opts := doormanCacheOptions(nil)
	opts.Scheme = scheme
	c, err := cache.New(cfg, opts)
	require.NoError(t, err)
	cacheCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- c.Start(cacheCtx) }()
	defer func() {
		stop()
		assert.NoError(t, <-done)
	}()

	require.Eventually(t, func() bool {
		var list discoveryv1.EndpointSliceList
		return c.List(ctx, &list, client.InNamespace("shop")) == nil && len(list.Items) > 0
	}, 30*time.Second, 100*time.Millisecond)
	var list discoveryv1.EndpointSliceList
	require.NoError(t, c.List(ctx, &list, client.InNamespace("shop")))
	require.Len(t, list.Items, 1)
	got := list.Items[0]
	assert.Equal(t, "web-xyz", got.Name)
	require.Len(t, got.Endpoints, 1)
	assert.Equal(t, []string{"10.244.1.9"}, got.Endpoints[0].Addresses)
	assert.Equal(t, "Pod", got.Endpoints[0].TargetRef.Kind)
	assert.Nil(t, got.Endpoints[0].NodeName, "what the doorman doesn't read isn't kept")
	assert.Empty(t, got.ManagedFields)
}

// Every other Service's slices are left out of the cache, so the operator
// reads them from the API server; the controller package's envtest routes a
// paused workload through this same configuration.
func TestOperatorCacheSeesOnlyDoormanSlices(t *testing.T) {
	cfg := startEnvtest(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	direct, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	for _, ns := range []string{"napwake-system", "shop", "other"} {
		require.NoError(t, direct.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	}
	routed := map[string]string{discoveryv1.LabelManagedBy: doorman.ManagedBy, discoveryv1.LabelServiceName: "web"}
	for _, s := range []*discoveryv1.EndpointSlice{
		endpointSlice("napwake-system", "napwake-doorman-abc",
			map[string]string{discoveryv1.LabelServiceName: "napwake-doorman"}),
		endpointSlice("shop", "web-napwake-doorman", routed),
		endpointSlice("shop", "web-xyz", map[string]string{discoveryv1.LabelServiceName: "web"}),
		endpointSlice("other", "api-napwake-doorman", routed),
		endpointSlice("other", "api-xyz", map[string]string{discoveryv1.LabelServiceName: "api"}),
	} {
		require.NoError(t, direct.Create(ctx, s))
	}

	tests := []struct {
		name    string
		watched []string
		want    map[string][]string
	}{
		{
			name:    "watching some namespaces",
			watched: []string{"shop"},
			want: map[string][]string{
				"napwake-system": {"napwake-system/napwake-doorman-abc"},
				"shop":           {"shop/web-napwake-doorman"},
			},
		},
		{
			name: "watching every namespace",
			want: map[string][]string{
				"napwake-system": {"napwake-system/napwake-doorman-abc"},
				"shop":           {"shop/web-napwake-doorman"},
				"other":          {"other/api-napwake-doorman"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := operatorCacheOptions(tt.watched, "napwake-system")
			opts.Scheme = scheme
			c, err := cache.New(cfg, opts)
			require.NoError(t, err)
			cacheCtx, stop := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- c.Start(cacheCtx) }()
			defer func() {
				stop()
				assert.NoError(t, <-done)
			}()

			for ns, want := range tt.want {
				require.Eventually(t, func() bool {
					var list discoveryv1.EndpointSliceList
					return c.List(ctx, &list, client.InNamespace(ns)) == nil && len(list.Items) == len(want)
				}, 30*time.Second, 100*time.Millisecond, "namespace %s", ns)
				assert.ElementsMatch(t, want, sliceNames(t, ctx, c, client.InNamespace(ns)))
			}
			assert.ElementsMatch(t, []string{"napwake-system/napwake-doorman-abc"},
				sliceNames(t, ctx, c, client.InNamespace("napwake-system"),
					client.MatchingLabels{discoveryv1.LabelServiceName: "napwake-doorman"}),
				"the operator finds the doorman's pods through these")
		})
	}
}

func requests(cpu, memory string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{Requests: corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)}}
}

// The operator caches pods only to measure spare node capacity, so it holds
// only those bound to a node and not finished, as what they request, and
// nodes without their image lists. The cache is configured exactly as the
// operator's, against an API server that applies its field selector.
func TestOperatorCacheHoldsPodsAsWhatTheyRequest(t *testing.T) {
	cfg := startEnvtest(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	direct, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	require.NoError(t, direct.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}}))
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a",
		Labels: map[string]string{"karpenter.sh/nodepool": "general"}}}
	require.NoError(t, direct.Create(ctx, node))
	node.Status = corev1.NodeStatus{
		Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("16Gi"), corev1.ResourcePods: resource.MustParse("110")},
		Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue,
			Reason: "KubeletReady", Message: "kubelet is posting ready status"}},
		Images: []corev1.ContainerImage{{Names: []string{"registry.example/api:v1"}, SizeBytes: 1 << 30}},
	}
	require.NoError(t, direct.Status().Update(ctx, node))

	pod := func(name, nodeName string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", Labels: map[string]string{"app": "api"}},
			Spec: corev1.PodSpec{
				NodeName:   nodeName,
				Containers: []corev1.Container{{Name: "app", Image: "api:v1", Resources: requests("500m", "1Gi")}},
				InitContainers: []corev1.Container{{Name: "proxy", Image: "proxy:v1", Resources: requests("100m", "64Mi"),
					RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways)}},
			},
		}
	}
	running, pending, finished := pod("running", "node-a"), pod("pending", ""), pod("finished", "node-a")
	for _, p := range []*corev1.Pod{running, pending, finished} {
		require.NoError(t, direct.Create(ctx, p))
	}
	finished.Status.Phase = corev1.PodSucceeded
	require.NoError(t, direct.Status().Update(ctx, finished))

	opts := operatorCacheOptions(nil, "napwake-system")
	opts.Scheme = scheme
	c, err := cache.New(cfg, opts)
	require.NoError(t, err)
	cacheCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- c.Start(cacheCtx) }()
	defer func() {
		stop()
		assert.NoError(t, <-done)
	}()

	var pods corev1.PodList
	require.Eventually(t, func() bool {
		return c.List(ctx, &pods) == nil && len(pods.Items) > 0
	}, 30*time.Second, 100*time.Millisecond)
	require.Len(t, pods.Items, 1, "only the pod bound to a node and not finished")
	got := pods.Items[0]
	assert.Equal(t, "running", got.Name)
	assert.Equal(t, "node-a", got.Spec.NodeName)
	assert.Empty(t, got.Labels)
	require.Len(t, got.Spec.Containers, 1, "collapsed into one container")
	assert.Empty(t, got.Spec.Containers[0].Image)
	assert.Empty(t, got.Spec.InitContainers)
	assert.Equal(t, "600m", got.Spec.Containers[0].Resources.Requests.Cpu().String(), "the app and its sidecar")
	assert.Equal(t, "1088Mi", got.Spec.Containers[0].Resources.Requests.Memory().String())

	running.Status.Phase = corev1.PodFailed
	require.NoError(t, direct.Status().Update(ctx, running))
	require.Eventually(t, func() bool {
		return c.List(ctx, &pods) == nil && len(pods.Items) == 0
	}, 30*time.Second, 100*time.Millisecond, "a pod that finishes leaves the cache")

	var cached corev1.Node
	require.NoError(t, c.Get(ctx, client.ObjectKey{Name: "node-a"}, &cached))
	assert.Equal(t, "general", cached.Labels["karpenter.sh/nodepool"])
	assert.Empty(t, cached.Status.Images)
	assert.Equal(t, "4", cached.Status.Allocatable.Cpu().String())
	assert.Equal(t, []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		cached.Status.Conditions)
}

// The forwarder's pod is Ready only once it reaches the doorman, so the
// operator doesn't point a Service at a forwarder that would drop its
// connections.
func TestForwarderProbes(t *testing.T) {
	f := forwarder.New(forwarder.Options{
		Routes:  map[int32]int32{8080: 23456},
		Doorman: "napwake-doorman-pods.napwake-system.svc",
	})
	probes := forwarderProbes(f)

	live := httptest.NewRecorder()
	probes.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	ready := httptest.NewRecorder()
	probes.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusOK, live.Code)
	assert.Equal(t, http.StatusServiceUnavailable, ready.Code, "Ready before it has reached the doorman")
	assert.Contains(t, ready.Body.String(), "doorman", "the probe says why it isn't Ready")
}

func TestForwarderOptions(t *testing.T) {
	valid := forwarderFlags{
		image:          "ghcr.io/napwake/napwake:v0.2.0",
		replicas:       2,
		resources:      defaultForwarderResources,
		doormanPods:    "napwake-doorman-pods",
		doormanService: "napwake-doorman",
		doormanNS:      "napwake-system",
	}
	tests := []struct {
		name    string
		change  func(*forwarderFlags)
		wantErr string
	}{
		{name: "valid"},
		{name: "no doorman", change: func(f *forwarderFlags) { f.doormanService = "" }, wantErr: "turns off"},
		{name: "no image", change: func(f *forwarderFlags) { f.image = "" }, wantErr: "--neg-forwarder-image"},
		{name: "no replicas", change: func(f *forwarderFlags) { f.replicas = 0 }, wantErr: "at least 1"},
		{name: "no headless Service", change: func(f *forwarderFlags) { f.doormanPods = "" }, wantErr: "doorman-service"},
		{name: "resources not in JSON", change: func(f *forwarderFlags) { f.resources = "cpu: 1" }, wantErr: "resources"},
		{
			name:    "a misspelt resources field",
			change:  func(f *forwarderFlags) { f.resources = `{"request":{"cpu":"10m"}}` },
			wantErr: "unknown field",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags := valid
			if tt.change != nil {
				tt.change(&flags)
			}

			opts, err := forwarderOptions(true, flags)

			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "napwake-doorman-pods.napwake-system.svc", opts.Doorman)
			assert.Equal(t, int32(2), opts.Replicas)
			assert.Equal(t, "10m", opts.Resources.Requests.Cpu().String())
			assert.Equal(t, "64Mi", opts.Resources.Limits.Memory().String())
		})
	}

	off, err := forwarderOptions(false, forwarderFlags{})
	require.NoError(t, err)
	assert.Nil(t, off, "off by default")
}
