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

package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	v1alpha1 "github.com/napwake/napwake/api/v1alpha1"
)

// WorkloadCPU picks a workload's own containers out of CPU history: its
// pods by the names its kind gives them, and the containers of its pod
// template, so an injected sidecar's background work isn't counted.
type WorkloadCPU struct {
	Namespace  string
	Kind       v1alpha1.TargetKind
	Name       string
	Containers []string
}

// HourlyPeaks is, for each hour from start to end in which any of the
// workload's pods ran, the most CPU, in cores, its average pod used over
// five minutes in it: the hour's busiest moment, as the forecast learns
// it. Each is keyed by the Unix time its hour starts; start and end are
// the starts of the first hour and of the hour after the last. It's one
// range query a step of an hour apart, the five-minute rates taken by a
// subquery, so Prometheus does the work and answers with a point an hour.
func (p *Prometheus) HourlyPeaks(ctx context.Context, w WorkloadCPU, start, end time.Time) (map[int64]float64, error) {
	if len(w.Containers) == 0 {
		return nil, errors.New("the workload has no containers to read")
	}
	quoted := make([]string, 0, len(w.Containers))
	for _, c := range w.Containers {
		quoted = append(quoted, regexp.QuoteMeta(c))
	}
	matchers := append([]string{
		"namespace=" + strconv.Quote(w.Namespace),
		"pod=~" + strconv.Quote(podNamePattern(w.Kind, w.Name)),
		"container=~" + strconv.Quote(strings.Join(quoted, "|")),
	}, p.Selector...)
	window := promDuration(minStep)
	rates := fmt.Sprintf("max by (pod, container) (rate(container_cpu_usage_seconds_total{%s}[%s]))",
		strings.Join(matchers, ", "), window)
	query := fmt.Sprintf("max_over_time((sum(%s) / count(max by (pod) (%s)))[1h:%s])", rates, rates, window)
	params := url.Values{
		"query": {query},
		"start": {strconv.FormatInt(start.Add(time.Hour).Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.FormatInt(int64(time.Hour.Seconds()), 10)},
	}
	body, err := p.get(ctx, "api/v1/query_range", params)
	if err != nil {
		return nil, fmt.Errorf("querying %s: %w", p.Source, err)
	}
	var resp rangeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("reading %s's response: %w", p.Source, err)
	}
	if resp.Status != statusSuccess {
		return nil, fmt.Errorf("%s: query failed: %s", p.Source, resp.Error)
	}
	peaks := map[int64]float64{}
	for _, series := range resp.Data.Result {
		for _, v := range series.Values {
			if v.ok && v.value >= 0 {
				hour := v.at - int64(time.Hour.Seconds())
				peaks[hour] = max(peaks[hour], v.value)
			}
		}
	}
	return peaks, nil
}

// podNamePattern is a regular expression matching the names of a
// workload's pods, as PromQL anchors it: a StatefulSet's are its name and
// an ordinal; a Deployment's are named after a ReplicaSet, its name and a
// pod-template-hash, then a dash and a random suffix, with the base cut to
// leave room for the suffix in 63 characters. It matches what
// replicaSetPods and deploymentReplicaSet match, without the ReplicaSets'
// names, so old revisions' pods are in the history too.
func podNamePattern(kind v1alpha1.TargetKind, name string) string {
	if kind == v1alpha1.TargetKindStatefulSet {
		return regexp.QuoteMeta(name) + "-(0|[1-9][0-9]*)"
	}
	suffix := fmt.Sprintf("[%s]{%d}", generatedRunes, generatedSuffixLength)
	hash := "[" + templateHashRunes + "]"
	var alternatives []string
	if room := maxGeneratedNameBase - len(name) - 2; room >= 1 {
		longest := min(room, maxTemplateHashLength)
		alternatives = append(alternatives,
			fmt.Sprintf("%s-%s{1,%d}-%s", regexp.QuoteMeta(name), hash, longest, suffix))
	}
	switch kept := maxGeneratedNameBase - len(name) - 1; {
	case kept <= 0:
		alternatives = append(alternatives, regexp.QuoteMeta((name + "-")[:maxGeneratedNameBase])+suffix)
	case kept <= maxTemplateHashLength:
		alternatives = append(alternatives, fmt.Sprintf("%s-%s{%d}%s", regexp.QuoteMeta(name), hash, kept, suffix))
	}
	if len(alternatives) == 1 {
		return alternatives[0]
	}
	return "(" + strings.Join(slices.Compact(alternatives), "|") + ")"
}
