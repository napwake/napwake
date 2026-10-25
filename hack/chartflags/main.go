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

// Command chartflags fails when the operator would refuse the arguments
// the rendered chart gives it, as it would with a value rendered as <nil>.
// It reads the rendered chart from stdin and runs the operator binary it's
// given with each /manager container's arguments and -help: the flags are
// all parsed before -help stops the binary, so it exits 0 only if every
// one is accepted.
//
//	go build -o bin/manager ./cmd
//	helm template napwake charts/napwake | go run ./hack/chartflags bin/manager
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

const operatorCommand = "/manager"

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: helm template ... | chartflags OPERATOR-BINARY")
		os.Exit(2)
	}
	rendered, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reading the rendered chart:", err)
		os.Exit(1)
	}
	if err := check(rendered, binaryParses(os.Args[1])); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// container is one container that runs the operator binary, and where.
type container struct {
	source string
	args   []string
}

// check runs parse on the arguments of every container that runs the
// operator binary, and reports each it refuses.
func check(rendered []byte, parse func(args []string) error) error {
	containers, err := operatorContainers(rendered)
	if err != nil {
		return err
	}
	if len(containers) == 0 {
		return errors.New("the rendered chart runs no " + operatorCommand + " container")
	}
	var refused []string
	for _, c := range containers {
		if err := parse(c.args); err != nil {
			refused = append(refused, fmt.Sprintf("  %s: %v\n    args: %s", c.source, err, strings.Join(c.args, " ")))
		}
	}
	if len(refused) > 0 {
		return fmt.Errorf("the operator refuses the arguments the chart gives it:\n%s", strings.Join(refused, "\n"))
	}
	return nil
}

var documentSeparator = regexp.MustCompile(`(?m)^---\s*$`)

// operatorContainers finds the containers and init containers, in every
// pod template the chart renders, whose command is the operator binary.
func operatorContainers(rendered []byte) ([]container, error) {
	var out []container
	for _, doc := range documentSeparator.Split(string(rendered), -1) {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Template struct {
					Spec corev1.PodSpec `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			return nil, fmt.Errorf("parsing a manifest: %w", err)
		}
		pod := obj.Spec.Template.Spec
		for _, c := range slices.Concat(pod.InitContainers, pod.Containers) {
			if slices.Equal(c.Command, []string{operatorCommand}) {
				out = append(out, container{source: fmt.Sprintf("%s/%s container %s", obj.Kind, obj.Metadata.Name, c.Name),
					args: c.Args})
			}
		}
	}
	return out, nil
}

// binaryParses runs the operator binary with the arguments and -help. A
// binary that doesn't stop within a minute, having started rather than
// printed its help, counts as refusing them.
func binaryParses(binary string) func(args []string) error {
	return func(args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, binary, append(slices.Clone(args), "-help")...).CombinedOutput()
		if err == nil {
			return nil
		}
		// The flag package says what it refused first, then prints the usage.
		first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
		return fmt.Errorf("%w: %s", err, first)
	}
}
