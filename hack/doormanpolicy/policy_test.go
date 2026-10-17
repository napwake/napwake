// Package doormanpolicy tests the chart's ValidatingAdmissionPolicy for the
// doorman against a real API server. make test-doorman-policy renders the
// chart and runs it; without CHART_MANIFEST it's skipped.
package doormanpolicy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/okedeji/hybernate/api/v1alpha1"
)

const (
	releaseNamespace = "hybernate-system"
	doormanUser      = "system:serviceaccount:" + releaseNamespace + ":hybernate-doorman"
	workloadNS       = "shop"
)

var appliedKinds = map[string]bool{
	"ClusterRole":                      true,
	"ClusterRoleBinding":               true,
	"ValidatingAdmissionPolicy":        true,
	"ValidatingAdmissionPolicyBinding": true,
}

func TestDoormanMayOnlyStampRequests(t *testing.T) {
	manifest := os.Getenv("CHART_MANIFEST")
	if manifest == "" {
		t.Skip("CHART_MANIFEST isn't set; run make test-doorman-policy")
	}

	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() }) // best-effort; the process exits next

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	scheme := clientScheme(t)
	admin, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	applyChart(ctx, t, admin, manifest)

	require.NoError(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: workloadNS}}))
	workload := &v1alpha1.ManagedWorkload{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "web",
			Namespace:   workloadNS,
			Labels:      map[string]string{"team": "checkout"},
			Annotations: map[string]string{"keep": "me"},
		},
		Spec: v1alpha1.ManagedWorkloadSpec{
			Target:     v1alpha1.WorkloadRef{Kind: "Deployment", Name: "web"},
			Prediction: v1alpha1.PredictionSpec{Confidence: 75},
		},
	}
	require.NoError(t, admin.Create(ctx, workload))

	doormanCfg := rest.CopyConfig(cfg)
	doormanCfg.Impersonate = rest.ImpersonationConfig{UserName: doormanUser}
	doorman, err := client.New(doormanCfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	key := types.NamespacedName{Namespace: workloadNS, Name: "web"}
	patch := func(p string) error {
		return doorman.Patch(ctx, &v1alpha1.ManagedWorkload{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}},
			client.RawPatch(types.MergePatchType, []byte(p)))
	}

	// The policy is enforced once the API server has loaded it, a moment
	// after it's created.
	require.Eventually(t, func() bool {
		return apierrors.IsInvalid(patch(`{"spec":{"dryRun":true}}`))
	}, 30*time.Second, 200*time.Millisecond, "the policy never denied the doorman a spec change")

	tests := []struct {
		name    string
		patch   string
		allowed bool
	}{
		{"stamps a request", `{"metadata":{"annotations":{"hybernate.io/last-request":"2026-10-17T00:00:00Z","hybernate.io/last-request-from":"10.0.0.1"}}}`, true},
		{"removes its own stamp", `{"metadata":{"annotations":{"hybernate.io/last-request-from":null}}}`, true},
		{"changes the spec", `{"spec":{"dryRun":true}}`, false},
		{"adds a settings annotation", `{"metadata":{"annotations":{"hybernate.io/idle-after":"1m"}}}`, false},
		{"changes another annotation", `{"metadata":{"annotations":{"keep":"you"}}}`, false},
		{"removes another annotation", `{"metadata":{"annotations":{"keep":null}}}`, false},
		{"changes a label", `{"metadata":{"labels":{"team":"other"}}}`, false},
		{"adds a finalizer", `{"metadata":{"finalizers":["example.com/hold"]}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := patch(tt.patch)
			if tt.allowed {
				assert.NoError(t, err)
				return
			}
			assert.True(t, apierrors.IsInvalid(err), "want the policy to deny it, got %v", err)
		})
	}

	t.Run("the operator isn't restricted", func(t *testing.T) {
		assert.NoError(t, admin.Patch(ctx, &v1alpha1.ManagedWorkload{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}},
			client.RawPatch(types.MergePatchType, []byte(`{"spec":{"dryRun":true}}`))))
	})
}

func clientScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	return s
}

func applyChart(ctx context.Context, t *testing.T, c client.Client, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	applied := 0
	for {
		var obj unstructured.Unstructured
		err := decoder.Decode(&obj.Object)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		if !appliedKinds[obj.GetKind()] {
			continue
		}
		require.NoError(t, c.Create(ctx, &obj), "creating %s %s", obj.GetKind(), obj.GetName())
		applied++
	}
	require.Positive(t, applied, "the chart rendered nothing to apply")
}
