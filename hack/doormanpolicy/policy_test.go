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

// Package doormanpolicy tests the ValidatingAdmissionPolicies around the
// doorman's request stamps, as the chart, the kustomize manifests, and an
// overlay on them that renames them install them, against a real API
// server. make test-doorman-policy renders them and runs it; with none of
// CHART_MANIFEST, KUSTOMIZE_MANIFEST and KUSTOMIZE_OVERLAY_MANIFEST set
// it's skipped.
package doormanpolicy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/napwake/napwake/api/v1alpha1"
)

const workloadNS = "shop"

var appliedKinds = map[string]bool{
	"ClusterRole":                      true,
	"ClusterRoleBinding":               true,
	"ValidatingAdmissionPolicy":        true,
	"ValidatingAdmissionPolicyBinding": true,
}

func TestDoormanMayOnlyStampRequests(t *testing.T) {
	manifests := []struct{ name, env string }{
		{name: "chart", env: "CHART_MANIFEST"},
		{name: "kustomize", env: "KUSTOMIZE_MANIFEST"},
		{name: "kustomize overlay", env: "KUSTOMIZE_OVERLAY_MANIFEST"},
	}
	ran := false
	for _, m := range manifests {
		path := os.Getenv(m.env)
		if path == "" {
			continue
		}
		ran = true
		t.Run(m.name, func(t *testing.T) { testPolicy(t, path) })
	}
	if !ran {
		t.Skip("no manifest is set; run make test-doorman-policy")
	}
}

func testPolicy(t *testing.T, manifest string) {
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")}}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() }) // best-effort; the process exits next

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	scheme := clientScheme(t)
	admin, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	doormanUser := apply(ctx, t, admin, manifest)

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

	patchAs := func(c client.Client, p string) error {
		web := &v1alpha1.ManagedWorkload{ObjectMeta: metav1.ObjectMeta{Name: workload.Name, Namespace: workloadNS}}
		return c.Patch(ctx, web, client.RawPatch(types.MergePatchType, []byte(p)))
	}
	patch := func(p string) error { return patchAs(doorman, p) }

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
		{"stamps a request", `{"metadata":{"annotations":{` +
			`"napwake.dev/last-request":"2026-10-17T00:00:00Z","napwake.dev/last-request-from":"10.0.0.1"}}}`, true},
		{"removes its own stamp", `{"metadata":{"annotations":{"napwake.dev/last-request-from":null}}}`, true},
		{"counts requests turned away", `{"metadata":{"annotations":{` +
			`"napwake.dev/turned-away":"{\"pausedAt\":\"2026-10-17T00:00:00Z\",\"requests\":3}"}}}`, true},
		{"changes the spec", `{"spec":{"dryRun":true}}`, false},
		{"adds a settings annotation", `{"metadata":{"annotations":{"napwake.dev/idle-after":"1m"}}}`, false},
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
		assert.NoError(t, patchAs(admin, `{"spec":{"dryRun":false}}`))
	})

	t.Run("a ServiceAccount named like the doorman elsewhere isn't restricted", func(t *testing.T) {
		name := doormanUser[strings.LastIndex(doormanUser, ":")+1:]
		otherCfg := rest.CopyConfig(cfg)
		otherCfg.Impersonate = rest.ImpersonationConfig{
			UserName: "system:serviceaccount:" + workloadNS + ":" + name,
			Groups:   []string{"system:masters"},
		}
		other, err := client.New(otherCfg, client.Options{Scheme: scheme})
		require.NoError(t, err)
		assert.NoError(t, patchAs(other, `{"spec":{"dryRun":true}}`))
	})

	testOnlyTheDoormanStamps(ctx, t, cfg, admin, doorman, patchAs)
	testWithoutTheDoormansServiceAccount(ctx, t, cfg, admin, doormanUser, patchAs)
}

// testWithoutTheDoormansServiceAccount checks what the kustomize policies,
// which take the doorman's ServiceAccount as their parameter, do once it's
// gone: they let every write through, since Deny would refuse every write
// to every ManagedWorkload, as the last subtest shows.
func testWithoutTheDoormansServiceAccount(ctx context.Context, t *testing.T, cfg *rest.Config, admin client.Client,
	doormanUser string, patchAs func(client.Client, string) error) {
	var bindings admissionregistrationv1.ValidatingAdmissionPolicyBindingList
	require.NoError(t, admin.List(ctx, &bindings))
	i := slices.IndexFunc(bindings.Items, func(b admissionregistrationv1.ValidatingAdmissionPolicyBinding) bool {
		return strings.HasSuffix(b.Spec.PolicyName, "request-stamps")
	})
	require.GreaterOrEqual(t, i, 0, "the manifest has no request-stamps binding")
	stamps := bindings.Items[i]
	if stamps.Spec.ParamRef == nil {
		return
	}
	parts := strings.Split(doormanUser, ":")
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: parts[2], Name: parts[3]}}
	require.NoError(t, admin.Delete(ctx, sa))

	userCfg := rest.CopyConfig(cfg)
	userCfg.Impersonate = rest.ImpersonationConfig{UserName: "alice"}
	alice, err := client.New(userCfg, client.Options{Scheme: clientScheme(t)})
	require.NoError(t, err)

	t.Run("without the doorman's ServiceAccount, stamps are left to the operator's own check", func(t *testing.T) {
		n := 0
		assert.Eventually(t, func() bool {
			n++
			stamp := fmt.Sprintf(`{"metadata":{"annotations":{"napwake.dev/last-request-from":"10.0.1.%d"}}}`, n)
			return patchAs(alice, stamp) == nil
		}, 30*time.Second, 200*time.Millisecond)
		assert.NoError(t, patchAs(admin, `{"metadata":{"annotations":{"example.com/note":"still writable"}}}`))
	})

	t.Run("Deny would refuse every write while it's missing", func(t *testing.T) {
		deny := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "request-stamps-deny"},
			Spec:       *stamps.Spec.DeepCopy(),
		}
		deny.Spec.ParamRef.ParameterNotFoundAction = ptr.To(admissionregistrationv1.DenyAction)
		require.NoError(t, admin.Create(ctx, deny))
		t.Cleanup(func() { _ = admin.Delete(context.Background(), deny) }) // best-effort; the envtest stops next

		n := 0
		assert.Eventually(t, func() bool {
			n++
			return patchAs(admin, fmt.Sprintf(`{"metadata":{"annotations":{"example.com/note":"%d"}}}`, n)) != nil
		}, 30*time.Second, 200*time.Millisecond, "a write that sets no stamp went through")
	})
}

// testOnlyTheDoormanStamps proves no one but the doorman can set the
// request annotations, not even under Napwake's field manager, which the
// operator would otherwise take as the doorman's.
func testOnlyTheDoormanStamps(ctx context.Context, t *testing.T, cfg *rest.Config, admin, doorman client.Client,
	patchAs func(client.Client, string) error) {
	const user = "alice"
	editor := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "managedworkload-editor"},
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{v1alpha1.GroupVersion.Group},
			Resources: []string{"managedworkloads"}, Verbs: []string{"get", "create", "update", "patch"}}},
	}
	require.NoError(t, admin.Create(ctx, editor))
	require.NoError(t, admin.Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "managedworkload-editor"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: editor.Name},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: rbacv1.UserKind, Name: user}},
	}))
	userCfg := rest.CopyConfig(cfg)
	userCfg.Impersonate = rest.ImpersonationConfig{UserName: user}
	alice, err := client.New(userCfg, client.Options{Scheme: clientScheme(t)})
	require.NoError(t, err)
	asNapwake := client.WithFieldOwner(alice, v1alpha1.FieldManager)

	forged := `{"metadata":{"annotations":{"napwake.dev/last-request-from":"10.0.0.9"}}}`
	require.Eventually(t, func() bool {
		return apierrors.IsInvalid(patchAs(asNapwake, forged))
	}, 30*time.Second, 200*time.Millisecond, "the policy never denied a user the doorman's stamp")

	tests := []struct {
		name    string
		as      client.Client
		patch   string
		allowed bool
	}{
		{"the doorman can stamp a request", doorman, `{"metadata":{"annotations":{` +
			`"napwake.dev/last-request":"2026-10-18T00:00:00Z","napwake.dev/last-request-from":"10.0.0.2"}}}`, true},
		{"a user can't set where a request came from", alice, forged, false},
		{"a user can't set it under Napwake's field manager", asNapwake, forged, false},
		{"a user can't set a request time", alice,
			`{"metadata":{"annotations":{"napwake.dev/last-request":"2026-10-18T01:00:00Z"}}}`, false},
		{"the operator can't set where a request came from", admin, forged, false},
		{"a user can change other annotations", asNapwake,
			`{"metadata":{"annotations":{"napwake.dev/last-activity":"2026-10-18T00:00:00Z"}}}`, true},
		{"a user can remove the doorman's stamp", alice,
			`{"metadata":{"annotations":{"napwake.dev/last-request":null}}}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := patchAs(tt.as, tt.patch)
			if tt.allowed {
				assert.NoError(t, err)
				return
			}
			assert.True(t, apierrors.IsInvalid(err), "want the policy to deny it, got %v", err)
		})
	}

	t.Run("a user can't create a ManagedWorkload with a stamp", func(t *testing.T) {
		mw := &v1alpha1.ManagedWorkload{
			ObjectMeta: metav1.ObjectMeta{Name: "forged", Namespace: workloadNS,
				Annotations: map[string]string{v1alpha1.AnnotationLastRequestFrom: "10.0.0.9"}},
			Spec: v1alpha1.ManagedWorkloadSpec{Target: v1alpha1.WorkloadRef{Kind: "Deployment", Name: "forged"}},
		}
		err := asNapwake.Create(ctx, mw)
		assert.True(t, apierrors.IsInvalid(err), "want the policy to deny it, got %v", err)
	})
}

func clientScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, rbacv1.AddToScheme(s))
	require.NoError(t, admissionregistrationv1.AddToScheme(s))
	return s
}

// apply creates the manifest's cluster-wide RBAC and admission policy, and
// the doorman's ServiceAccount, which the kustomize policy takes as its
// parameter, and returns its username, wherever and whatever the manifest
// named it.
func apply(ctx context.Context, t *testing.T, c client.Client, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	applied := 0
	var doormanUser string
	for {
		var obj unstructured.Unstructured
		err := decoder.Decode(&obj.Object)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		if obj.GetKind() == "ServiceAccount" && strings.HasSuffix(obj.GetName(), "doorman") {
			doormanUser = "system:serviceaccount:" + obj.GetNamespace() + ":" + obj.GetName()
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: obj.GetNamespace()}}
			if err := c.Create(ctx, ns); !apierrors.IsAlreadyExists(err) {
				require.NoError(t, err, "creating namespace %s", ns.Name)
			}
			require.NoError(t, c.Create(ctx, &obj), "creating ServiceAccount %s", obj.GetName())
			continue
		}
		if !appliedKinds[obj.GetKind()] {
			continue
		}
		// Kustomize gives cluster-scoped objects its namespace too.
		obj.SetNamespace("")
		require.NoError(t, c.Create(ctx, &obj), "creating %s %s", obj.GetKind(), obj.GetName())
		applied++
	}
	require.Positive(t, applied, "the manifest has nothing to apply")
	require.NotEmpty(t, doormanUser, "the manifest has no doorman ServiceAccount")
	return doormanUser
}
