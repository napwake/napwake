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

// Command prices regenerates the list prices Hybernate prices nodes with,
// on-demand and spot, from the instance data published by
// instances.vantage.sh (github.com/vantage-sh/ec2instances.info, MIT), which
// collects each provider's public price lists.
//
//	go run ./hack/prices internal/cost/prices.csv.gz
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type source struct {
	provider string
	url      string
	// spotKey is the field holding the Linux spot price. AWS's moves with
	// demand, so it's the average across the region's zones when the table
	// is generated; Azure and Google Cloud publish one per region.
	spotKey string
}

var sources = []source{
	{"aws", "https://instances.vantage.sh/instances.json", "spot_avg"},
	{"azure", "https://instances.vantage.sh/azure/instances.json", "spot_min"},
	{"gcp", "https://instances.vantage.sh/gcp/instances.json", "spot"},
}

// instance is the part of each provider's records the table needs. AWS and
// GCP spell vCPUs "vCPU", Azure "vcpu"; prices are strings or numbers.
type instance struct {
	InstanceType string                     `json:"instance_type"`
	VCPU         json.Number                `json:"vCPU"`
	VCPULower    json.Number                `json:"vcpu"`
	Memory       json.Number                `json:"memory"`
	Pricing      map[string]json.RawMessage `json:"pricing"`
	Regions      map[string]string          `json:"regions"`
}

type osPrices struct {
	Linux map[string]json.RawMessage `json:"linux"`
}

type row struct {
	provider, instanceType string
	vcpu, memoryGiB        float64
	prices                 []regionPrice
}

// regionPrice is an instance type's hourly price in a region, on-demand and
// spot, which is zero where the provider publishes none.
type regionPrice struct {
	region         string
	onDemand, spot float64
}

// A feed that's emptied or renamed would otherwise drop every price for its
// provider without an error, and every node there would go unpriced; one
// whose spot field is renamed would price every spot node on-demand.
const (
	minRows      = 100
	maxDropRate  = 0.2
	minSpotShare = 0.5
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: prices OUTPUT.csv.gz")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(out string) error {
	var rows []row
	for _, s := range sources {
		instances, err := fetch(s.url)
		if err != nil {
			return fmt.Errorf("fetching %s prices: %w", s.provider, err)
		}
		for _, in := range instances {
			if r, ok := rowFor(s, in); ok {
				rows = append(rows, r)
			}
		}
	}
	previous, err := countsIn(out)
	if err != nil {
		return err
	}
	if err := checkCounts(previous, countsOf(rows)); err != nil {
		return err
	}
	slices.SortFunc(rows, func(a, b row) int {
		return strings.Compare(a.provider+" "+a.instanceType, b.provider+" "+b.instanceType)
	})

	var gz bytes.Buffer
	// No name or modification time in the header, so the same prices
	// always produce the same file.
	w, err := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("compressing prices: %w", err)
	}
	if _, err := w.Write(table(rows)); err != nil {
		return fmt.Errorf("compressing prices: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("compressing prices: %w", err)
	}
	if err := os.WriteFile(out, gz.Bytes(), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", out, err)
	}
	fmt.Printf("wrote %d instance types to %s\n", len(rows), out)
	return nil
}

// table writes the rows as internal/cost reads them: a line per instance
// type, then each region's on-demand price, and its spot price after a
// slash where there is one.
func table(rows []row) []byte {
	var csv bytes.Buffer
	csv.WriteString("# provider,instance type,vCPUs,memory GiB,region=on-demand[/spot] USD per hour...\n")
	for _, r := range rows {
		fmt.Fprintf(&csv, "%s,%s,%s,%s", r.provider, r.instanceType, number(r.vcpu), number(r.memoryGiB))
		for _, p := range r.prices {
			fmt.Fprintf(&csv, ",%s=%s", p.region, number(p.onDemand))
			if p.spot > 0 {
				fmt.Fprintf(&csv, "/%s", number(p.spot))
			}
		}
		csv.WriteByte('\n')
	}
	return csv.Bytes()
}

// counts are how many instance types a provider has in a table, and how
// many of those have a spot price in at least one region.
type counts struct {
	types, spot int
}

func countsOf(rows []row) map[string]counts {
	out := make(map[string]counts)
	for _, r := range rows {
		c := out[r.provider]
		c.types++
		if slices.ContainsFunc(r.prices, func(p regionPrice) bool { return p.spot > 0 }) {
			c.spot++
		}
		out[r.provider] = c
	}
	return out
}

// countsIn counts the instance types per provider in the committed table at
// path; there's none to compare against the first time it's written.
func countsIn(path string) (map[string]counts, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = f.Close() }() // read-only, nothing to do if closing fails
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("decompressing %s: %w", path, err)
	}
	c, err := countRows(gz)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return c, nil
}

func countRows(r io.Reader) (map[string]counts, error) {
	out := make(map[string]counts)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		provider, rest, _ := strings.Cut(line, ",")
		c := out[provider]
		c.types++
		if strings.Contains(rest, "/") {
			c.spot++
		}
		out[provider] = c
	}
	return out, scanner.Err()
}

// checkCounts fails if any provider has fewer than minRows instance types,
// or spot prices for fewer than minSpotShare of them, or lost more than
// maxDropRate of either since the previous table.
func checkCounts(previous, current map[string]counts) error {
	var errs []error
	for _, s := range sources {
		n, was := current[s.provider], previous[s.provider]
		switch {
		case n.types < minRows:
			errs = append(errs, fmt.Errorf("%s has %d instance types, fewer than %d; is %s still the feed?",
				s.provider, n.types, minRows, s.url))
		case float64(n.types) < float64(was.types)*(1-maxDropRate):
			errs = append(errs, fmt.Errorf("%s has %d instance types, down from %d; is %s still the feed?",
				s.provider, n.types, was.types, s.url))
		case float64(n.spot) < float64(n.types)*minSpotShare:
			errs = append(errs, fmt.Errorf("%s has spot prices for %d of %d instance types; is %q still the spot "+
				"field of %s?", s.provider, n.spot, n.types, s.spotKey, s.url))
		case float64(n.spot) < float64(was.spot)*(1-maxDropRate):
			errs = append(errs, fmt.Errorf("%s has spot prices for %d instance types, down from %d; is %q still "+
				"the spot field of %s?", s.provider, n.spot, was.spot, s.spotKey, s.url))
		}
	}
	return errors.Join(errs...)
}

func fetch(url string) ([]instance, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }() // read-only, nothing to do if closing fails
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	var instances []instance
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&instances); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", url, err)
	}
	return instances, nil
}

var space = regexp.MustCompile(`\s+`)

func rowFor(s source, in instance) (row, bool) {
	vcpu, _ := in.VCPU.Float64()
	if vcpu == 0 {
		vcpu, _ = in.VCPULower.Float64()
	}
	memory, _ := in.Memory.Float64()
	if vcpu <= 0 || memory <= 0 {
		return row{}, false
	}
	r := row{provider: s.provider, instanceType: strings.ToLower(in.InstanceType), vcpu: vcpu, memoryGiB: memory}
	for region, raw := range in.Pricing {
		var p osPrices
		if json.Unmarshal(raw, &p) != nil {
			continue
		}
		onDemand := hourly(p.Linux["ondemand"])
		if onDemand <= 0 {
			continue
		}
		// Azure's records key regions by a slug of their display name;
		// nodes are labelled with the ARM name, which is that display name
		// lowercased without spaces ("East US 2" is eastus2).
		if s.provider == "azure" {
			name, ok := in.Regions[region]
			if !ok {
				continue
			}
			region = strings.ToLower(space.ReplaceAllString(name, ""))
		}
		// Spot is never billed above on-demand, so a feed's figure over it
		// is a stale reading, not a price anyone pays.
		spot := max(min(hourly(p.Linux[s.spotKey]), onDemand), 0)
		r.prices = append(r.prices, regionPrice{region: region, onDemand: onDemand, spot: spot})
	}
	if len(r.prices) == 0 {
		return row{}, false
	}
	slices.SortFunc(r.prices, func(a, b regionPrice) int { return strings.Compare(a.region, b.region) })
	return r, true
}

// hourly reads a price the feeds write as a string or a number, or 0 for
// one that's missing or isn't a number.
func hourly(raw json.RawMessage) float64 {
	price, err := strconv.ParseFloat(strings.Trim(string(raw), `"`), 64)
	if err != nil {
		return 0
	}
	return price
}

func number(f float64) string {
	return strconv.FormatFloat(f, 'g', 6, 64)
}
