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

package guard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

const install = "napwake-system"

func managed(namespace, name string, finalizers ...string) *v1alpha1.ManagedWorkload {
	return &v1alpha1.ManagedWorkload{ObjectMeta: metav1.ObjectMeta{
		Namespace: namespace, Name: name, Finalizers: finalizers,
	}}
}

func takenOnBy(install, namespace, name string) *v1alpha1.ManagedWorkload {
	mw := managed(namespace, name, v1alpha1.FinalizerCleanup)
	mw.Annotations = map[string]string{v1alpha1.AnnotationOperator: install}
	return mw
}

func beingDeleted(namespace, name string) *v1alpha1.ManagedWorkload {
	mw := managed(namespace, name, v1alpha1.FinalizerCleanup)
	mw.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	return mw
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name    string
		op      Operation
		watched []string
		objs    []client.Object
		refused bool
		names   []string
		absent  []string
	}{
		{
			name:    "uninstall refused while any ManagedWorkload is left",
			op:      Uninstall,
			objs:    []client.Object{managed("shop", "api", v1alpha1.FinalizerCleanup), managed("blog", "web")},
			refused: true,
			names:   []string{"shop/api", "blog/web", "refuses to uninstall"},
		},
		{
			name: "uninstall goes ahead with none left",
			op:   Uninstall,
		},
		{
			name:    "uninstall ignores unwatched ManagedWorkloads the operator never took on",
			op:      Uninstall,
			watched: []string{"shop"},
			objs:    []client.Object{managed("blog", "web")},
		},
		{
			name:    "uninstall refused for one in a watched namespace",
			op:      Uninstall,
			watched: []string{"shop", "blog"},
			objs:    []client.Object{managed("blog", "web"), managed("other", "x")},
			refused: true,
			names:   []string{"blog/web"},
			absent:  []string{"other/x"},
		},
		{
			name:    "uninstall after a refused upgrade still refused for one the operator took on",
			op:      Uninstall,
			watched: []string{"shop"},
			objs:    []client.Object{managed("blog", "web", v1alpha1.FinalizerCleanup)},
			refused: true,
			names:   []string{"blog/web"},
		},
		{
			name:    "uninstall refused for one still being deleted after the wait",
			op:      Uninstall,
			objs:    []client.Object{beingDeleted("shop", "api")},
			refused: true,
			names:   []string{"were being deleted", "shop/api", "--no-hooks"},
			absent:  []string{"would stay paused"},
		},
		{
			name:    "upgrade refused when it stops watching a namespace the operator manages workloads in",
			op:      Upgrade,
			watched: []string{"shop"},
			objs: []client.Object{
				managed("shop", "api", v1alpha1.FinalizerCleanup),
				managed("blog", "web", v1alpha1.FinalizerCleanup),
			},
			refused: true,
			names:   []string{"blog/web", "refuses this upgrade", "watchNamespaces"},
			absent:  []string{"shop/api"},
		},
		{
			name:    "upgrade ignores ManagedWorkloads the operator never took on",
			op:      Upgrade,
			watched: []string{"shop"},
			objs:    []client.Object{managed("blog", "web")},
		},
		{
			name: "upgrade to every namespace strands nothing",
			op:   Upgrade,
			objs: []client.Object{managed("blog", "web", v1alpha1.FinalizerCleanup)},
		},
		{
			name:    "uninstall ignores ManagedWorkloads another install took on",
			op:      Uninstall,
			watched: []string{"shop"},
			objs:    []client.Object{takenOnBy("team-b-system", "blog", "web")},
		},
		{
			name:    "uninstall ignores another install's ManagedWorkloads even where it watches",
			op:      Uninstall,
			watched: []string{"shop"},
			objs:    []client.Object{takenOnBy("team-b-system", "shop", "api")},
		},
		{
			name:    "uninstall refused for one it took on, outside the namespaces it watches",
			op:      Uninstall,
			watched: []string{"shop"},
			objs: []client.Object{
				takenOnBy(install, "blog", "web"),
				takenOnBy("team-b-system", "other", "x"),
			},
			refused: true,
			names:   []string{"blog/web"},
			absent:  []string{"other/x"},
		},
		{
			name:    "upgrade ignores ManagedWorkloads another install took on",
			op:      Upgrade,
			watched: []string{"shop"},
			objs:    []client.Object{takenOnBy("team-b-system", "blog", "web")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(tt.objs...).Build()
			var out strings.Builder
			g := &Guard{Reader: c, Out: &out, Install: install, Watched: tt.watched,
				Wait: 20 * time.Millisecond, Poll: 5 * time.Millisecond}

			err := g.Check(context.Background(), tt.op)

			if tt.refused {
				require.ErrorIs(t, err, ErrRefused)
			} else {
				require.NoError(t, err, out.String())
			}
			for _, s := range tt.names {
				assert.Contains(t, out.String(), s)
			}
			for _, s := range tt.absent {
				assert.NotContains(t, out.String(), s)
			}
		})
	}
}

// Helm 4 prints a hook's output as one log message, with its line breaks
// escaped, so a refusal's first line must say on its own what's left, what
// to do, and where the docs are.
func TestRefusalLeadsWithOneLine(t *testing.T) {
	many := make([]client.Object, 0, 12)
	for i := range 12 {
		many = append(many, managed("shop", fmt.Sprintf("app-%02d", i), v1alpha1.FinalizerCleanup))
	}
	tests := []struct {
		name    string
		op      Operation
		watched []string
		objs    []client.Object
		want    string
	}{
		{
			name: "uninstall",
			op:   Uninstall,
			objs: []client.Object{managed("rc-demo", "web", v1alpha1.FinalizerCleanup), managed("blog", "api")},
			want: "Napwake refuses to uninstall: these ManagedWorkloads' workloads would stay paused with no " +
				"operator: blog/api, rc-demo/web. Remove the napwake.dev/managed label from them and their " +
				"namespaces, and delete ManagedWorkloads you created, then uninstall again; " +
				"see https://docs.napwake.dev/getting-started/installation/#uninstall",
		},
		{
			name:    "upgrade",
			op:      Upgrade,
			watched: []string{"shop"},
			objs:    []client.Object{managed("blog", "web", v1alpha1.FinalizerCleanup)},
			want: "Napwake refuses this upgrade: it stops watching namespaces where these ManagedWorkloads' " +
				"workloads would stay paused with no operator: blog/web. Keep their namespaces in " +
				"watchNamespaces, or remove the napwake.dev/managed label from them and their namespaces, " +
				"and delete ManagedWorkloads you created there, then upgrade again; " +
				"see https://docs.napwake.dev/operations/upgrading/#narrowing-watchnamespaces",
		},
		{
			name: "stuck being deleted",
			op:   Uninstall,
			objs: []client.Object{beingDeleted("shop", "api")},
			want: "Napwake refuses to uninstall: these ManagedWorkloads were still being deleted after 20ms, " +
				"so the operator may not be running: shop/api. If it isn't, skip this check with --no-hooks " +
				"and restore their workloads by hand; " +
				"see https://docs.napwake.dev/getting-started/installation/#uninstall",
		},
		{
			name: "many left",
			op:   Uninstall,
			objs: many,
			want: "Napwake refuses to uninstall: these ManagedWorkloads' workloads would stay paused with no " +
				"operator: shop/app-00, shop/app-01, shop/app-02, shop/app-03, shop/app-04, shop/app-05, " +
				"shop/app-06, shop/app-07, shop/app-08, shop/app-09 and 2 more. Remove the napwake.dev/managed " +
				"label from them and their namespaces, and delete ManagedWorkloads you created, then uninstall " +
				"again; see https://docs.napwake.dev/getting-started/installation/#uninstall",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(tt.objs...).Build()
			var out strings.Builder
			g := &Guard{Reader: c, Out: &out, Install: install, Watched: tt.watched,
				Wait: 20 * time.Millisecond, Poll: 5 * time.Millisecond}

			require.ErrorIs(t, g.Check(context.Background(), tt.op), ErrRefused)

			first, rest, _ := strings.Cut(out.String(), "\n")
			assert.Equal(t, tt.want, first)
			for _, obj := range tt.objs {
				assert.Contains(t, rest, "  "+obj.GetNamespace()+"/"+obj.GetName()+"\n", "the details list every one")
			}
		})
	}
}

func TestCheckWaitsForThoseBeingDeleted(t *testing.T) {
	var lists int
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(beingDeleted("shop", "api")).
		WithInterceptorFuncs(interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch,
			list client.ObjectList, opts ...client.ListOption) error {
			lists++
			if lists == 2 {
				var mw v1alpha1.ManagedWorkload
				require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "shop", Name: "api"}, &mw))
				mw.Finalizers = nil
				require.NoError(t, c.Update(ctx, &mw))
			}
			return c.List(ctx, list, opts...)
		}}).Build()
	var out strings.Builder
	g := &Guard{Reader: c, Out: &out, Wait: time.Minute, Poll: time.Millisecond}

	require.NoError(t, g.Check(context.Background(), Uninstall), out.String())
	assert.Contains(t, out.String(), "can go ahead")
}

func TestCheckWithoutTheCRD(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{List: func(context.Context, client.WithWatch,
			client.ObjectList, ...client.ListOption) error {
			return &meta.NoKindMatchError{GroupKind: v1alpha1.GroupVersion.WithKind("ManagedWorkload").GroupKind()}
		}}).Build()
	var out strings.Builder
	g := &Guard{Reader: c, Out: &out, Wait: time.Minute, Poll: time.Millisecond}

	require.NoError(t, g.Check(context.Background(), Uninstall))
	assert.Contains(t, out.String(), "CRD isn't installed")
}

func TestCheckFailsClosedWhenItCantList(t *testing.T) {
	forbidden := errors.New("forbidden")
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).
		WithInterceptorFuncs(interceptor.Funcs{List: func(context.Context, client.WithWatch,
			client.ObjectList, ...client.ListOption) error {
			return forbidden
		}}).Build()
	g := &Guard{Reader: c, Out: &strings.Builder{}, Watched: []string{"shop"}, Wait: time.Minute, Poll: time.Millisecond}

	err := g.Check(context.Background(), Upgrade)

	require.ErrorIs(t, err, forbidden)
	assert.NotErrorIs(t, err, ErrRefused)
}
