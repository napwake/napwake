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

package sizing_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/napwake/napwake/api/v1alpha1"
)

// What the chart's values.yaml and the Helm values page say the operator's
// default memory limit covers, each held to the operator binary's peak
// resident memory, which the container's limit counts: so many workloads,
// each a Deployment with a pod, a Service and a ManagedWorkload, or so many
// pods, behind a tenth as many Deployments and Services. Covered means the
// peak leaves a fifth of the limit to spare.
const (
	coveredWorkloads  = 10_000
	coveredPods       = 100_000
	spare             = 0.2
	operatorWorkloads = "the operator's default limit covers some 10,000 ManagedWorkloads, or 100,000 pods behind " +
		"10,000 Deployments, with a fifth to spare"
)

// operatorLimit is the operator's memory limit as the chart sets it by
// default, which the binary is measured running under.
func operatorLimit(t *testing.T) int64 {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "charts", "napwake", "values.yaml"))
	require.NoError(t, err)
	var values struct {
		Resources corev1.ResourceRequirements `json:"resources"`
	}
	require.NoError(t, yaml.Unmarshal(data, &values))
	limit, ok := values.Resources.Limits[corev1.ResourceMemory]
	require.True(t, ok, "values.yaml sets resources.limits.memory")
	return limit.Value()
}

// operatorCluster is a cluster the operator binary is measured holding:
// deployments, each with its replicas' pods and a Service, the first
// managed of them each with a ManagedWorkload that has weeks of use behind
// it, on 200 nodes across 50 namespaces.
type operatorCluster struct {
	name        string
	deployments int
	replicas    int
	managed     int
	// covered says the chart's limit is quoted as covering it.
	covered bool
}

var operatorClusters = []operatorCluster{
	{name: "1,000 workloads", deployments: 1_000, replicas: 1, managed: 1_000},
	{name: "2,500 workloads", deployments: 2_500, replicas: 1, managed: 2_500},
	{name: "4,000 workloads", deployments: 4_000, replicas: 1, managed: 4_000},
	{name: "5,000 workloads", deployments: 5_000, replicas: 1, managed: 5_000},
	{name: "7,500 workloads", deployments: 7_500, replicas: 1, managed: 7_500},
	{name: "10,000 workloads", deployments: coveredWorkloads, replicas: 1, managed: coveredWorkloads, covered: true},
	{name: "40,000 pods, 1,000 managed", deployments: 4_000, replicas: replicas, managed: 1_000},
	{name: "100,000 pods, 1,000 managed", deployments: coveredPods / replicas, replicas: replicas, managed: 1_000,
		covered: true},
}

// The operator binary, as the chart runs it, with MEMORY_LIMIT at its
// default, holding each cluster, as it starts, takes on every
// ManagedWorkload and rechecks each every minute: its peak resident
// memory, which the container's limit kills at, from the kernel's account
// once it exits, and as /metrics reports it; and beside it, the heap in
// use, the live heap from the runtime's own GC trace, and what the runtime
// holds from the OS. Each cluster gets an API server of its own, since the
// operator caches everything in it.
func TestOperatorBinary(t *testing.T) {
	bin := buildOperator(t)
	for _, c := range operatorClusters {
		t.Run(c.name, func(t *testing.T) {
			api := startAPIServer(t, filepath.Join("..", "..", "config", "crd", "bases"))
			start := time.Now()
			api.fillOperatorCluster(t, c)
			t.Logf("%s: filled in %s", c.name, time.Since(start).Round(time.Second))

			r := runOperator(t, bin, api, c)
			t.Logf("%s: peak resident memory %s (%s at most in /metrics, %s at the end; soft limit %s); "+
				"heap in use %s at most, %s at the end; live heap %s at most, %s at the end; the runtime held %s at "+
				"most, and took at most %s from the OS", c.name, mib(r.maxRSS), mib(r.peak.rss), mib(r.end.rss),
				mib(operatorLimit(t)/10*9), mib(r.peakHeapInUse), mib(r.end.heapInUse), mib(r.peakLive), mib(r.live),
				mib(r.peakHeld), mib(r.end.obtained))
			t.Logf("%s: over the last two minutes, %.0f reconciles a second, %.0f queued at the end, where "+
				"rechecking each ManagedWorkload every minute takes %.0f a second", c.name, r.rate, r.depth,
				float64(c.managed)/60)
			if r.squeezed > 0 {
				t.Logf("%s: in %d samples the resident memory was less than the heap in use: the OS compressed or "+
					"swapped the operator's memory, so these figures read low", c.name, r.squeezed)
			}
			if c.covered {
				assert.Zero(t, r.squeezed, "%s: measure what the chart quotes with the machine's memory to spare", c.name)
				assert.LessOrEqual(t, float64(r.maxRSS), float64(operatorLimit(t))*(1-spare),
					"%s, as values.yaml says, but at %s its peak resident memory was %s of %s", operatorWorkloads,
					c.name, mib(r.maxRSS), mib(operatorLimit(t)))
			}
		})
	}
}

func mib(b int64) string { return fmt.Sprintf("%.0f MiB", float64(b)/(1<<20)) }

func buildOperator(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "manager")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./cmd")
	build.Dir = filepath.Join("..", "..")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	return bin
}

// fillOperatorCluster creates the nodes, Deployments, pods, Services and
// ManagedWorkloads of c.
func (a *apiServer) fillOperatorCluster(t *testing.T, c operatorCluster) {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, a.client.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "napwake-system"}}))
	for i := range nodes {
		n := node(i)
		status := n.Status
		require.NoError(t, a.client.Create(ctx, n))
		n.Status = status
		require.NoError(t, a.client.Status().Update(ctx, n))
	}
	template := managedWorkload(t)
	template.Labels, template.OwnerReferences, template.Finalizers = nil, nil, nil
	template.Status.Phase = v1alpha1.PhaseRunning
	template.Status.Pause, template.Status.Doorman = nil, nil
	a.fill(ctx, t, kind{name: "workloads", create: func(ctx context.Context, cl client.Client, d int) error {
		if err := cl.Create(ctx, deployment(d, c.replicas), writer); err != nil {
			return err
		}
		if err := cl.Create(ctx, service(d), writer); err != nil {
			return err
		}
		for k := range c.replicas {
			if err := cl.Create(ctx, pod(d*replicas+k), writer); err != nil {
				return err
			}
		}
		if d >= c.managed {
			return nil
		}
		w := template.DeepCopy()
		w.Name, w.Namespace = app(d), podNamespace(d*replicas)
		w.Spec.Target.Name = w.Name
		if err := cl.Create(ctx, w); err != nil {
			return err
		}
		w.Status = *template.Status.DeepCopy()
		return cl.Status().Update(ctx, w)
	}}, 0, c.deployments)
}

// node is the i-th node, an m6i.xlarge, Ready.
func node(i int) *corev1.Node {
	capacity := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("4"),
		corev1.ResourceMemory: resource.MustParse("16Gi"),
		corev1.ResourcePods:   resource.MustParse("110"),
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName(i), Labels: map[string]string{
			"node.kubernetes.io/instance-type": "m6i.xlarge",
			"topology.kubernetes.io/region":    "us-east-1",
			"topology.kubernetes.io/zone":      zone(i),
			"eks.amazonaws.com/nodegroup":      "staging",
			"kubernetes.io/os":                 "linux",
		}},
		Status: corev1.NodeStatus{
			Capacity: capacity, Allocatable: capacity,
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue,
				Reason: "KubeletReady", LastHeartbeatTime: metav1.Now(), LastTransitionTime: metav1.Now()}},
		},
	}
}

// deployment is the d-th Deployment, whose pods pod makes.
func deployment(d, replicaCount int) *appsv1.Deployment {
	p := pod(d * replicas)
	spec := p.Spec.DeepCopy()
	spec.NodeName = ""
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: app(d), Namespace: p.Namespace, Labels: map[string]string{
			"app.kubernetes.io/name": app(d), "app.kubernetes.io/instance": app(d) + "-staging",
			"app.kubernetes.io/managed-by": "Helm", "helm.sh/chart": "shop-1.42.0",
		}},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(replicaCount)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"app.kubernetes.io/instance": app(d) + "-staging"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: p.Labels, Annotations: p.Annotations},
				Spec:       *spec,
			},
		},
	}
}

// service is the d-th Deployment's Service.
func service(d int) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: app(d), Namespace: podNamespace(d * replicas)},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app.kubernetes.io/instance": app(d) + "-staging"},
			Ports: []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromString("web"),
				Protocol: corev1.ProtocolTCP}},
		},
	}
}

// memory is what the operator's /metrics reported of its memory at once.
type memory struct {
	// rss is its resident memory, process_resident_memory_bytes.
	rss       int64
	heapInUse int64
	// held is what the runtime holds from the OS, less what it has
	// returned: what GOMEMLIMIT counts.
	held int64
	// obtained is the most it ever took from the OS, held or since
	// returned, which sampling held can miss the peak of.
	obtained int64
}

// operatorRun is the operator's memory over a run: its peak resident
// memory, as the kernel accounts it when the process exits; the sample
// with the most resident memory, and the last; the most heap in use and
// the most held of any sample; the live heap the GC marked, at most and in
// its last cycle; and, over the steady last two minutes, how many
// reconciles it made a second, and its queue at the end.
type operatorRun struct {
	maxRSS                  int64
	peak, end               memory
	peakHeapInUse, peakHeld int64
	peakLive, live          int64
	rate, depth             float64
	// squeezed counts the samples whose resident memory was less than the
	// heap in use, which only the OS compressing or swapping the
	// operator's memory explains: its resident figures then read low.
	squeezed int
}

// gcTrace is a GODEBUG=gctrace=1 line's heap sizes: at the cycle's start,
// at its end, and the live heap it marked, in MB.
var gcTrace = regexp.MustCompile(`^gc \d+ @.* (\d+)->(\d+)->(\d+) MB, `)

// runOperator runs the operator binary against api, as the chart runs it
// with its default memory limit, until it has reconciled each
// ManagedWorkload three times, as it takes each on and writes its status,
// then two minutes more, as it rechecks each every minute.
func runOperator(t *testing.T, bin string, api *apiServer, c operatorCluster) operatorRun {
	t.Helper()
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, api.kubeconfig, 0o600))
	logs, err := os.Create(filepath.Join(dir, "operator.log"))
	require.NoError(t, err)
	metricsAddr, probeAddr := freeAddr(t), freeAddr(t)

	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, bin, "--metrics-bind-address="+metricsAddr, "--metrics-secure=false",
		"--health-probe-bind-address="+probeAddr, "--zap-log-level=error")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig, "POD_NAMESPACE=napwake-system",
		fmt.Sprintf("MEMORY_LIMIT=%d", operatorLimit(t)), "GODEBUG=gctrace=1")
	cmd.Stdout, cmd.Stderr = logs, logs
	require.NoError(t, cmd.Start())
	stopped := make(chan struct{})
	var exitErr error
	go func() {
		exitErr = cmd.Wait()
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
		_ = logs.Close() // best effort, the test is over
	})

	var r operatorRun
	var steady time.Time
	var steadyFrom float64
	deadline, logged := time.Now().Add(30*time.Minute), time.Now()
	for {
		select {
		case <-stopped:
			out, _ := os.ReadFile(logs.Name()) // for the failure message only
			t.Fatalf("the operator exited, %v:\n%s", exitErr, out)
		case <-time.After(scrapeEvery):
		}
		m, ok := scrape(t, metricsAddr)
		if !ok {
			continue
		}
		mem := m.memory()
		if mem.rss > r.peak.rss {
			r.peak = mem
		}
		r.peakHeapInUse, r.peakHeld = max(r.peakHeapInUse, mem.heapInUse), max(r.peakHeld, mem.held)
		if mem.rss > 0 && mem.rss < mem.heapInUse {
			r.squeezed++
		}
		r.end = mem
		reconciled, depth := m.sum("controller_runtime_reconcile_total", `controller="managedworkload"`),
			m.sum("workqueue_depth", `name="managedworkload"`)
		if time.Since(logged) > time.Minute {
			t.Logf("%s: %.0f reconciles, %.0f queued, resident memory %s, heap in use %s", c.name, reconciled, depth,
				mib(mem.rss), mib(mem.heapInUse))
			logged = time.Now()
		}
		switch {
		case steady.IsZero() && reconciled >= 3*float64(c.managed):
			steady, steadyFrom = time.Now(), reconciled
		case !steady.IsZero() && time.Since(steady) >= 2*time.Minute:
			r.rate, r.depth = (reconciled-steadyFrom)/time.Since(steady).Seconds(), depth
			require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
			select {
			case <-stopped:
			case <-time.After(2 * time.Minute):
				t.Fatalf("%s: the operator didn't stop within two minutes of SIGTERM", c.name)
			}
			r.maxRSS = maxRSS(t, cmd.ProcessState)
			r.peakLive, r.live = readLog(t, c.name, logs.Name())
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: after 30 minutes the operator has made %.0f reconciles for %d ManagedWorkloads, with %.0f queued",
				c.name, reconciled, c.managed, depth)
		}
	}
}

// maxRSS is the most resident memory an exited process had, as the kernel
// accounts it: in bytes on macOS, in kilobytes on Linux.
func maxRSS(t *testing.T, state *os.ProcessState) int64 {
	t.Helper()
	usage, ok := state.SysUsage().(*syscall.Rusage)
	require.True(t, ok, "the operator's resource usage is readable")
	if runtime.GOOS == "darwin" {
		return usage.Maxrss
	}
	return usage.Maxrss << 10
}

// readLog returns the most live heap the operator's GC trace marked, and
// the last, and says how many errors the operator logged, and the first,
// so what it was doing is on record with what it held.
func readLog(t *testing.T, name, file string) (peakLive, live int64) {
	t.Helper()
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	logged := map[string]int{}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if m := gcTrace.FindStringSubmatch(line); m != nil {
			mb, err := strconv.ParseInt(m[3], 10, 64)
			require.NoError(t, err)
			live = mb << 20
			peakLive = max(peakLive, live)
			continue
		}
		if line == "" {
			continue
		}
		var entry struct {
			Msg   string `json:"msg"`
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			entry.Msg = line
		}
		logged[fmt.Sprintf("%.200s: %.200s", entry.Msg, entry.Error)]++
	}
	require.NotZero(t, peakLive, "the operator traced its garbage collection")
	if len(logged) == 0 {
		t.Logf("%s: the operator logged no errors", name)
		return peakLive, live
	}
	for _, what := range slices.Sorted(maps.Keys(logged)) {
		t.Logf("%s: the operator logged %d times at error level %s", name, logged[what], what)
	}
	return peakLive, live
}

// scrapeEvery is how often the test reads the operator's metrics: as often
// as a Prometheus at half the chart's 30s interval would, since rendering
// tens of thousands of series costs the operator memory of its own.
const scrapeEvery = 15 * time.Second

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// metrics are the samples of one scrape, by their name and labels as
// written, such as workqueue_depth{controller="x",name="x"}.
type metrics map[string]float64

func scrape(t *testing.T, addr string) (metrics, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/metrics", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }() // best effort, read to the end below
	m := metrics{}
	lines := bufio.NewScanner(resp.Body)
	lines.Buffer(make([]byte, 1<<20), 1<<20)
	for lines.Scan() {
		line := lines.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		if v, err := strconv.ParseFloat(line[i+1:], 64); err == nil {
			m[line[:i]] = v
		}
	}
	return m, lines.Err() == nil
}

// sum adds every sample of the named metric whose labels include label.
func (m metrics) sum(name, label string) float64 {
	var total float64
	for series, v := range m {
		if (series == name || strings.HasPrefix(series, name+"{")) && strings.Contains(series, label) {
			total += v
		}
	}
	return total
}

func (m metrics) memory() memory {
	return memory{
		rss:       int64(m["process_resident_memory_bytes"]),
		heapInUse: int64(m["go_memstats_heap_inuse_bytes"]),
		held:      int64(m["go_memstats_sys_bytes"] - m["go_memstats_heap_released_bytes"]),
		obtained:  int64(m["go_memstats_sys_bytes"]),
	}
}
