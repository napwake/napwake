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

// Package chartguard holds the chart's refusal of an upgrade as it
// renders to the upgrade guard's: for any ManagedWorkloads and
// watchNamespaces, the chart refuses exactly when the guard refuses
// ManagedWorkloads it would leave without an operator, naming them in the
// guard's own first line, and leaves those being deleted to the guard,
// which waits for them. It renders the chart's helpers with helm template,
// so make check-chart runs it with HELM set to a Helm binary; without, it's
// skipped.
package chartguard

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
	"github.com/napwake/napwake/internal/guard"
)

const install = "napwake-system"

// The guard's lead when it refuses ManagedWorkloads it would leave, rather
// than only those still being deleted after its wait.
const keptLead = "Napwake refuses this upgrade: it stops watching"

// testTemplate renders each case of the values below through the chart's
// helpers, one key a case.
const testTemplate = `{{- range $name, $c := .Values.refusalTest }}
{{ $name }}: {{ include "napwake.upgradeRefusal" $c | quote }}
{{- end }}
{{- range $name, $c := .Values.leftOutTest }}
{{ $name }}: {{ include "napwake.leftOut" $c | fromYaml | toJson | quote }}
{{- end }}
`

// refusalCase is one set of ManagedWorkloads, as the API server returns
// them, and the watchNamespaces an upgrade sets.
type refusalCase struct {
	Items   []v1alpha1.ManagedWorkload `json:"items"`
	Watched []string                   `json:"watched"`
	Install string                     `json:"install"`
}

// leftOutCase is the running operator's Deployment, as the API server
// returns it, or empty when there's none, and the watchNamespaces an
// upgrade sets.
type leftOutCase struct {
	Running map[string]any `json:"running"`
	Watched []string       `json:"watched"`
}

type leftOut struct {
	All        bool     `json:"all"`
	Namespaces []string `json:"namespaces"`
}

// render renders the cases through a copy of the chart with testTemplate
// added, and returns what each case's key renders to.
func render(t *testing.T, refusals map[string]refusalCase, leftOuts map[string]leftOutCase) map[string]string {
	t.Helper()
	helm := os.Getenv("HELM")
	if helm == "" {
		t.Skip("HELM isn't set; run make check-chart")
	}
	dir := t.TempDir()
	chart := filepath.Join(dir, "napwake")
	require.NoError(t, os.CopyFS(chart, os.DirFS(filepath.Join("..", "..", "charts", "napwake"))))
	// The test's values aren't the chart's, which its schema would refuse.
	require.NoError(t, os.Remove(filepath.Join(chart, "values.schema.json")))
	require.NoError(t, os.WriteFile(filepath.Join(chart, "templates", "refusal-test.yaml"), []byte(testTemplate), 0o600))
	values, err := json.Marshal(map[string]any{"refusalTest": refusals, "leftOutTest": leftOuts})
	require.NoError(t, err)
	valuesFile := filepath.Join(dir, "values.json")
	require.NoError(t, os.WriteFile(valuesFile, values, 0o600))

	out, err := exec.CommandContext(t.Context(), helm, "template", "napwake", chart, "--namespace", install,
		"--show-only", "templates/refusal-test.yaml", "-f", valuesFile).CombinedOutput()
	require.NoError(t, err, string(out))
	rendered := map[string]string{}
	require.NoError(t, yaml.Unmarshal(out, &rendered), string(out))
	return rendered
}

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

// guardLead is the first line the upgrade guard prints of the case's
// ManagedWorkloads when it refuses those it would leave, and "" when it
// goes ahead or refuses only those being deleted. It waits for none.
func guardLead(t *testing.T, c refusalCase) string {
	t.Helper()
	objs := make([]client.Object, len(c.Items))
	for i := range c.Items {
		objs[i] = &c.Items[i]
	}
	var out strings.Builder
	g := &guard.Guard{Reader: fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).Build(),
		Out: &out, Install: c.Install, Watched: c.Watched, Poll: time.Hour}
	err := g.Check(context.Background(), guard.Upgrade)
	if err == nil {
		return ""
	}
	require.ErrorIs(t, err, guard.ErrRefused)
	lead, _, _ := strings.Cut(out.String(), "\n")
	if !strings.HasPrefix(lead, keptLead) {
		return ""
	}
	return lead
}

func managedWorkload(namespace, name string) v1alpha1.ManagedWorkload {
	return v1alpha1.ManagedWorkload{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ManagedWorkload"},
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
	}
}

// randomCase is a cluster of up to 14 ManagedWorkloads across four
// namespaces, each with the cleanup finalizer or not, another's or none,
// this install's operator annotation, another's or none, and some being
// deleted, and an upgrade to watch some of those namespaces.
func randomCase(r *rand.Rand) refusalCase {
	namespaces := []string{"shop", "blog", "preview-1", "preview-2"}
	c := refusalCase{Install: install}
	for _, ns := range namespaces {
		if r.IntN(2) == 0 {
			c.Watched = append(c.Watched, ns)
		}
	}
	if len(c.Watched) == 0 {
		c.Watched = []string{namespaces[r.IntN(len(namespaces))]}
	}
	for i := range r.IntN(15) {
		mw := managedWorkload(namespaces[r.IntN(len(namespaces))], fmt.Sprintf("app-%02d", i))
		switch r.IntN(4) {
		case 0:
		case 1:
			mw.Finalizers = []string{"example.com/other"}
		default:
			mw.Finalizers = []string{v1alpha1.FinalizerCleanup}
		}
		switch r.IntN(4) {
		case 0:
			mw.Annotations = map[string]string{v1alpha1.AnnotationOperator: "team-b-system"}
		case 1:
			mw.Annotations = map[string]string{v1alpha1.AnnotationOperator: install}
		}
		if len(mw.Finalizers) > 0 && r.IntN(5) == 0 {
			mw.DeletionTimestamp = &metav1.Time{Time: time.Now().Truncate(time.Second)}
		}
		c.Items = append(c.Items, mw)
	}
	return c
}

// The chart refuses at render exactly what the upgrade guard refuses at
// once, in its words, across random clusters and upgrades, and the cases
// they might miss: none left, every one being deleted, another install's,
// and more than the lead names.
func TestChartRefusesWhatTheGuardDoes(t *testing.T) {
	cases := map[string]refusalCase{}
	r := rand.New(rand.NewPCG(1, 2))
	for i := range 300 {
		cases[fmt.Sprintf("random-%03d", i)] = randomCase(r)
	}
	held := func(namespace, name string) v1alpha1.ManagedWorkload {
		mw := managedWorkload(namespace, name)
		mw.Finalizers = []string{v1alpha1.FinalizerCleanup}
		return mw
	}
	deleting := held("blog", "web")
	deleting.DeletionTimestamp = &metav1.Time{Time: time.Now().Truncate(time.Second)}
	theirs := held("blog", "web")
	theirs.Annotations = map[string]string{v1alpha1.AnnotationOperator: "team-b-system"}
	many := refusalCase{Install: install, Watched: []string{"shop"}}
	for i := range 12 {
		many.Items = append(many.Items, held(fmt.Sprintf("preview-%02d", 12-i), "web"))
	}
	cases["none"] = refusalCase{Install: install, Watched: []string{"shop"}}
	cases["only being deleted"] = refusalCase{Install: install, Watched: []string{"shop"},
		Items: []v1alpha1.ManagedWorkload{deleting}}
	cases["another install's"] = refusalCase{Install: install, Watched: []string{"shop"},
		Items: []v1alpha1.ManagedWorkload{theirs}}
	cases["inside watchNamespaces"] = refusalCase{Install: install, Watched: []string{"blog"},
		Items: []v1alpha1.ManagedWorkload{held("blog", "web")}}
	cases["more than ten"] = many

	rendered := render(t, cases, nil)

	refused := 0
	for name, c := range cases {
		want := guardLead(t, c)
		assert.Equal(t, want, rendered[name], "%s: %+v", name, c)
		if want != "" {
			refused++
		}
	}
	assert.Greater(t, refused, 50, "the random cases refuse often enough to say something")
	assert.Less(t, refused, 250, "and go ahead often enough")
	assert.Contains(t, rendered["more than ten"], "preview-01/web, ")
	assert.Contains(t, rendered["more than ten"], " and 2 more.")
	for _, name := range []string{"none", "only being deleted", "another install's", "inside watchNamespaces"} {
		assert.Empty(t, rendered[name], name)
	}
}

// The chart looks only where the running operator watches and the upgrade
// won't, read from its Deployment's --watch-namespaces, and everywhere
// when it watches every namespace or there's no Deployment to read.
func TestChartLooksWhereTheUpgradeStopsWatching(t *testing.T) {
	deployment := func(containers ...map[string]any) map[string]any {
		return map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": containers}}}}
	}
	manager := func(args ...string) map[string]any {
		return map[string]any{"name": "manager", "args": args}
	}
	cases := map[string]struct {
		leftOutCase
		want leftOut
	}{
		"no Deployment": {leftOutCase{Running: map[string]any{}, Watched: []string{"shop"}},
			leftOut{All: true, Namespaces: []string{}}},
		"watching every namespace": {leftOutCase{Running: deployment(manager("--leader-elect")),
			Watched: []string{"shop"}}, leftOut{All: true, Namespaces: []string{}}},
		"narrowed": {leftOutCase{Running: deployment(manager("--leader-elect", "--watch-namespaces=shop,blog,preview-1")),
			Watched: []string{"blog"}}, leftOut{Namespaces: []string{"shop", "preview-1"}}},
		"widened": {leftOutCase{Running: deployment(manager("--watch-namespaces=shop")),
			Watched: []string{"shop", "blog"}}, leftOut{Namespaces: []string{}}},
		"moved": {leftOutCase{Running: deployment(manager("--watch-namespaces=shop")),
			Watched: []string{"blog"}}, leftOut{Namespaces: []string{"shop"}}},
		"only the manager's flag counts": {leftOutCase{Running: deployment(
			map[string]any{"name": "sidecar", "args": []string{"--watch-namespaces=other"}},
			manager("--watch-namespaces=shop")), Watched: []string{"shop"}}, leftOut{Namespaces: []string{}}},
		"no manager container": {leftOutCase{Running: deployment(map[string]any{"name": "app"}),
			Watched: []string{"shop"}}, leftOut{All: true, Namespaces: []string{}}},
	}
	inputs := map[string]leftOutCase{}
	for name, c := range cases {
		inputs[name] = c.leftOutCase
	}

	rendered := render(t, nil, inputs)

	for name, c := range cases {
		var got leftOut
		require.NoError(t, json.Unmarshal([]byte(rendered[name]), &got), name)
		assert.Equal(t, c.want, got, name)
	}
}
