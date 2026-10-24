package controller

import (
	"bufio"
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// Every settings annotation on a Deployment, StatefulSet or Namespace in the
// docs is one Napwake knows, with a value it can read: a reader who copies
// one gets the setting the page describes, not a warning event.
func TestDocsAnnotationsAreReadable(t *testing.T) {
	known := knownAnnotations(t)
	checked := 0
	for _, obj := range docsObjects(t) {
		var annotations map[string]string
		for key, value := range obj.Metadata.Annotations {
			if !strings.HasPrefix(key, "napwake.dev/") {
				continue
			}
			assert.True(t, known[key], "%s: %s isn't an annotation Napwake reads", obj.source, key)
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations[key] = value
		}
		if annotations == nil {
			continue
		}
		checked++
		target := v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKind(obj.Kind), Name: obj.Metadata.Name}
		workload, namespace := annotations, map[string]string(nil)
		if obj.Kind == kindNamespace {
			target = v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: "app"}
			workload, namespace = nil, annotations
		}
		_, problems := optInSpec(obj.Metadata.Namespace, target, workload, namespace, OptInDefaults{})
		assert.Empty(t, problems, "%s", obj.source)
	}
	assert.NotZero(t, checked, "the docs have annotated workloads to check")
}

const kindNamespace = "Namespace"

type docsObject struct {
	source   string
	Kind     string `json:"kind"`
	Metadata struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
}

// The docs' kustomizations never put the opt-in label in a selector or a
// pod template: Kubernetes doesn't let a workload's selector change, so
// copying one onto a workload that exists would fail to apply.
func TestDocsKustomizationsLeaveSelectorsAlone(t *testing.T) {
	kustomizations := kustomizationsIn(docsDocuments(t))
	require.NotEmpty(t, kustomizations, "the docs have kustomizations to check")
	for _, k := range kustomizations {
		for key := range k.CommonLabels {
			assert.False(t, strings.HasPrefix(key, "napwake.dev/"),
				"%s: commonLabels puts %s in the workloads' selectors", k.source, key)
		}
		for _, l := range k.Labels {
			for key := range l.Pairs {
				if !strings.HasPrefix(key, "napwake.dev/") {
					continue
				}
				assert.False(t, l.IncludeSelectors, "%s: includeSelectors puts %s in the workloads' selectors", k.source, key)
				assert.False(t, l.IncludeTemplates, "%s: includeTemplates puts %s on the pods", k.source, key)
			}
		}
	}
}

// docsObjects are the Deployments, StatefulSets and Namespaces in the docs'
// yaml blocks, including those a kustomization's patches describe.
func docsObjects(t *testing.T) []docsObject {
	t.Helper()
	docs := docsDocuments(t)
	objs := make([]docsObject, 0, len(docs))
	for _, doc := range docs {
		objs = append(objs, objectsIn(t, doc.source, doc.data)...)
	}
	for _, k := range kustomizationsIn(docs) {
		for _, p := range k.Patches {
			objs = append(objs, objectsIn(t, k.source+" patch", []byte(p.Patch))...)
		}
	}
	return objs
}

// objectsIn picks the Deployments, StatefulSets and Namespaces out of a
// stream of YAML documents.
func objectsIn(t *testing.T, source string, data []byte) []docsObject {
	t.Helper()
	var objs []docsObject
	for _, doc := range splitYAML(t, source, data) {
		var obj docsObject
		if yaml.Unmarshal(doc, &obj) != nil {
			continue
		}
		switch obj.Kind {
		case string(v1alpha1.TargetKindDeployment), string(v1alpha1.TargetKindStatefulSet), kindNamespace:
			obj.source = source
			objs = append(objs, obj)
		}
	}
	return objs
}

type kustomization struct {
	source       string
	APIVersion   string            `json:"apiVersion"`
	Kind         string            `json:"kind"`
	Resources    []string          `json:"resources"`
	CommonLabels map[string]string `json:"commonLabels"`
	Labels       []struct {
		Pairs            map[string]string `json:"pairs"`
		IncludeSelectors bool              `json:"includeSelectors"`
		IncludeTemplates bool              `json:"includeTemplates"`
	} `json:"labels"`
	Patches []struct {
		Patch string `json:"patch"`
	} `json:"patches"`
}

// kustomizationsIn picks the kustomization.yaml files out of the docs'
// documents: a Kustomization, or a document with no kind that lists
// resources, as most are written.
func kustomizationsIn(docs []docsDocument) []kustomization {
	var out []kustomization
	for _, doc := range docs {
		var k kustomization
		if yaml.Unmarshal(doc.data, &k) != nil || len(k.Resources) == 0 {
			continue
		}
		if k.Kind != "" && (k.Kind != "Kustomization" || !strings.HasPrefix(k.APIVersion, "kustomize.config.k8s.io/")) {
			continue
		}
		k.source = doc.source
		out = append(out, k)
	}
	return out
}

type docsDocument struct {
	source string
	data   []byte
}

// docsDocuments are the YAML documents in the yaml blocks of the docs'
// pages, Markdown and MDX, in Starlight's content folder.
func docsDocuments(t *testing.T) []docsDocument {
	t.Helper()
	var docs []docsDocument
	root := filepath.Join("..", "..", "docs", "src", "content", "docs")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || (filepath.Ext(path) != ".md" && filepath.Ext(path) != ".mdx") {
			return err
		}
		page, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, block := range markdownYAML(page) {
			for _, doc := range splitYAML(t, path, block) {
				docs = append(docs, docsDocument{source: path, data: doc})
			}
		}
		return nil
	})
	require.NoError(t, err)
	return docs
}

func splitYAML(t *testing.T, source string, data []byte) [][]byte {
	t.Helper()
	var docs [][]byte
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	for {
		doc, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return docs
		}
		require.NoError(t, err, "reading %s", source)
		docs = append(docs, doc)
	}
}

// markdownYAML returns the bodies of a page's fenced yaml blocks, dedented
// by their fence's indentation, as blocks inside list steps and tabs are.
func markdownYAML(page []byte) [][]byte {
	var (
		blocks  [][]byte
		current *bytes.Buffer
		indent  string
	)
	scanner := bufio.NewScanner(bytes.NewReader(page))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		lang, _, _ := strings.Cut(trimmed, " ")
		switch {
		case current == nil && (lang == "```yaml" || lang == "```yml"):
			current = &bytes.Buffer{}
			indent = line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		case current != nil && trimmed == "```":
			blocks = append(blocks, current.Bytes())
			current = nil
		case current != nil:
			current.WriteString(strings.TrimPrefix(line, indent))
			current.WriteByte('\n')
		}
	}
	return blocks
}

// knownAnnotations are the napwake.dev annotations the API declares.
// They're read from labels.go's source, so a new one is known without
// listing it twice.
func knownAnnotations(t *testing.T) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "..", "api", "v1alpha1", "labels.go"), nil, 0)
	require.NoError(t, err)
	known := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			if !strings.HasPrefix(name.Name, "Annotation") || i >= len(spec.Values) {
				continue
			}
			if lit, ok := spec.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if value, err := strconv.Unquote(lit.Value); err == nil {
					known[value] = true
				}
			}
		}
		return true
	})
	require.NotEmpty(t, known)
	return known
}
