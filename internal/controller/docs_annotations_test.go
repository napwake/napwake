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

	v1alpha1 "github.com/okedeji/hybernate/api/v1alpha1"
)

// Every settings annotation on a Deployment, StatefulSet or Namespace in the
// docs is one Hybernate knows, with a value it can read: a reader who copies
// one gets the setting the page describes, not a warning event.
func TestDocsAnnotationsAreReadable(t *testing.T) {
	known := knownAnnotations(t)
	checked := 0
	for _, obj := range docsObjects(t) {
		var annotations map[string]string
		for key, value := range obj.Metadata.Annotations {
			if !strings.HasPrefix(key, "hybernate.io/") {
				continue
			}
			assert.True(t, known[key], "%s: %s isn't an annotation Hybernate reads", obj.source, key)
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
		if obj.Kind == "Namespace" {
			target = v1alpha1.WorkloadRef{Kind: v1alpha1.TargetKindDeployment, Name: "app"}
			workload, namespace = nil, annotations
		}
		_, problems := optInSpec(obj.Metadata.Namespace, target, workload, namespace, OptInDefaults{})
		assert.Empty(t, problems, "%s", obj.source)
	}
	assert.NotZero(t, checked, "the docs have annotated workloads to check")
}

type docsObject struct {
	source   string
	Kind     string `json:"kind"`
	Metadata struct {
		Name        string            `json:"name"`
		Namespace   string            `json:"namespace"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
}

// docsObjects are the Deployments, StatefulSets and Namespaces in the docs'
// yaml blocks.
func docsObjects(t *testing.T) []docsObject {
	t.Helper()
	var objs []docsObject
	root := filepath.Join("..", "..", "docs")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		page, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, block := range markdownYAML(page) {
			reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(block)))
			for {
				doc, err := reader.Read()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return err
				}
				var obj docsObject
				if yaml.Unmarshal(doc, &obj) != nil {
					continue
				}
				switch obj.Kind {
				case "Deployment", "StatefulSet", "Namespace":
					obj.source = path
					objs = append(objs, obj)
				}
			}
		}
		return nil
	})
	require.NoError(t, err)
	return objs
}

// markdownYAML returns the bodies of a page's fenced yaml blocks, dedented
// by their fence's indentation, as blocks inside admonitions and tabs are.
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

// knownAnnotations are the hybernate.io annotations the API declares.
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
