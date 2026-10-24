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

package doorman

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

//go:embed page.html
var pageHTML string

var pageTemplate = template.Must(template.New("page").Parse(pageHTML))

// Mark is the Napwake doorman, his hat, his face and his shoulders, as an
// SVG path on a 24 grid, drawn on the waking-up and asleep pages and on the
// plugin's scan report and status page. Its eyes are counter-clockwise
// subpaths, so they cut out under the default nonzero fill rule as well as
// under evenodd.
const Mark = "M7.6 9.9L7.2 3.6Q7.2 2.6 8.2 2.6Q12 2.2 15.8 2.6Q16.8 2.6 16.8 3.6L16.4 9.9Z" +
	"M3.4 10.7Q3.4 9.8 4.6 9.9H19.4Q20.6 9.8 20.6 10.7Q20.6 11.7 19.4 11.7H4.6Q3.4 11.7 3.4 10.7Z" +
	"M6.6 19.2V13.8Q6.6 12.5 7.9 12.5H16.1Q17.4 12.5 17.4 13.8V19.2Z" +
	"M8 15.4A1.9 1.9 0 0 0 11.8 15.4Z" +
	"M12.2 15.4A1.9 1.9 0 0 0 16 15.4Z" +
	"M3 24V22.2Q3 20 5.2 20H18.8Q21 20 21 22.2V24Z"

// pageRefresh is how often the page reloads while the workload starts. The
// reload that finds it Ready is passed straight through to it.
const pageRefresh = 3 * time.Second

// pageData is all the page shows. It's served to whoever reaches the
// Service, so it names nothing inside the cluster: no namespace, workload,
// or Service name, and not when the workload was paused.
type pageData struct {
	// Address is the host the browser asked for, so the page reads as part
	// of what the person opened rather than as another site.
	Address string
	Elapsed string
	Refresh int
	Mark    string

	// Asleep shows the page that asks, whose Wake it button posts Token
	// back to the page's own address as the form field Field. Notice says
	// why an earlier click woke nothing, and NoticeDocs, if clicking again
	// won't help, where the docs say how else to wake it.
	Asleep     bool
	Field      string
	Token      string
	Notice     string
	NoticeDocs string
	// WakeRequestDocs is where the asleep page says tooling can wake the
	// workload instead.
	WakeRequestDocs string
}

// pageAddress is the host the browser asked for, without its port.
func pageAddress(req *http.Request) string {
	host := req.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host
}

// httpMethods are the request lines that mark a connection as HTTP/1.x, or
// as the HTTP/2 preface.
var httpMethods = []string{"GET ", "HEAD ", "POST ", "PUT ", "DELETE ", "OPTIONS ", "PATCH ", "CONNECT ", "TRACE ", "PRI "}

func looksLikeHTTP(head []byte) bool {
	for _, m := range httpMethods {
		if bytes.HasPrefix(head, []byte(m)) {
			return true
		}
	}
	return false
}

// startsMethod reports whether head is cut short of a method, as the first
// segment of a request split by a proxy or a slow link can be, so more must
// be read before the connection can be told apart.
func startsMethod(head []byte) bool {
	for _, m := range httpMethods {
		if len(head) > 0 && len(head) < len(m) && strings.HasPrefix(m, string(head)) {
			return true
		}
	}
	return false
}

// headersEnded reports whether head holds a whole request's headers. Lines
// may end in a bare LF, as net/http accepts and some probes and scripts
// send.
func headersEnded(head []byte) bool {
	return bytes.Contains(head, []byte("\r\n\r\n")) || bytes.Contains(head, []byte("\n\n"))
}

// parseRequest parses head, the first bytes of a connection, as an HTTP
// request. Headers longer than maxPeek, as single sign-on cookies can make
// them, are judged by the lines that fit: browsers send the ones the doorman
// reads before their cookies.
func parseRequest(head []byte) (*http.Request, bool) {
	if !looksLikeHTTP(head) {
		return nil, false
	}
	if !headersEnded(head) {
		lastLine := bytes.LastIndexByte(head, '\n')
		if lastLine < 0 {
			return nil, false
		}
		head = append(head[:lastLine+1:lastLine+1], "\r\n"...)
	}
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(head)))
	if err != nil {
		return nil, false
	}
	return req, true
}

// defaultHealthCheckAgents are the User-Agent prefixes of load balancer
// health checks, kubelet probes, metrics scrapers and uptime monitors. They
// reach a paused workload's Service on a schedule, so a request from one
// never wakes it. Options.HealthCheckAgents adds to them.
var defaultHealthCheckAgents = []string{
	"kube-probe/",
	"Prometheus/",
	"Blackbox Exporter/",
	"vm_promscrape",
	"GrafanaAgent/",
	"Alloy/",
	"OpenTelemetry Collector",
	"otelcol",
	"Datadog Agent/",
	"Datadog/Synthetics",
	"ELB-HealthChecker/",
	"Amazon-Route53-Health-Check-Service",
	"GoogleHC/",
	"GoogleStackdriverMonitoring-UptimeChecks",
	"Envoy/HC",
	"Mozilla/5.0+(compatible; UptimeRobot/",
	"Pingdom.com_bot",
	"NewRelicPinger/",
	"Uptime-Kuma/",
	"Gatus/",
}

// failClosedCheckAgents are the health checks of load balancers that send
// nothing to a backend failing them: Google Cloud's. Told a paused workload
// is down, they'd drop every request, so nothing would wake it; they're
// told it's up, and the doorman holds what they send. Others, such as
// AWS's, send to every target once all fail, so they hear the truth.
var failClosedCheckAgents = []string{"GoogleHC/"}

// isHealthCheck reports whether req comes from a health checker or scraper,
// one whose User-Agent starts with one of agents, or names it as
// "(compatible; agent", as monitors that present themselves as a browser
// do. No browser still in use sends "compatible;", so that can't match a
// person.
// Only a GET or HEAD can be one: the same agents also send real traffic,
// such as Prometheus remote write and Alertmanager notifications, which are
// POSTs and must reach the workload.
func isHealthCheck(req *http.Request, agents []string) bool {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}
	agent := req.UserAgent()
	for _, name := range agents {
		if strings.HasPrefix(agent, name) || strings.Contains(agent, "(compatible; "+name) {
			return true
		}
	}
	return false
}

// isHiddenFileRequest reports whether req asks for a hidden file, one with
// a path segment that starts with a dot, such as /.env or /.git/config.
// Scanners crawl the internet for these, hoping for leaked secrets; no app
// serves them to its users, so such a request never wakes a workload.
// /.well-known/ is the exception, since real clients use it, as ACME
// certificate checks do.
func isHiddenFileRequest(req *http.Request) bool {
	for i, segment := range strings.Split(req.URL.Path, "/") {
		if !strings.HasPrefix(segment, ".") || segment == "." || segment == ".." {
			continue
		}
		if i == 1 && segment == ".well-known" {
			continue
		}
		return true
	}
	return false
}

func writeNotFound(w io.Writer) error {
	_, err := io.WriteString(w, "HTTP/1.1 404 Not Found\r\n"+
		"Content-Length: 0\r\n"+
		"Cache-Control: no-store\r\n"+
		"Connection: close\r\n\r\n")
	return err
}

// writeUnavailable answers a health check: the workload is paused, so it
// isn't healthy, and it stays paused.
func writeUnavailable(w io.Writer) error {
	_, err := io.WriteString(w, "HTTP/1.1 503 Service Unavailable\r\n"+
		"Content-Length: 0\r\n"+
		"Cache-Control: no-store\r\n"+
		"Connection: close\r\n\r\n")
	return err
}

// writeHealthy answers a load balancer's check of a forwarder, which is up
// and holds requests for the paused workload behind it.
func writeHealthy(w io.Writer) error {
	_, err := io.WriteString(w, "HTTP/1.1 200 OK\r\n"+
		"Content-Length: 0\r\n"+
		"Cache-Control: no-store\r\n"+
		"Connection: close\r\n\r\n")
	return err
}

// isPageLoad reports whether req is a browser loading a page: a GET or HEAD
// for a document, not a fetch from a script, an API client, or a protocol
// upgrade. Those still get held, since a page is no answer to them.
func isPageLoad(req *http.Request) bool {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}
	if req.Header.Get("Upgrade") != "" {
		return false
	}
	// Browsers since 2020 send Sec-Fetch-Mode on every request, and only a
	// top-level or frame navigation is "navigate". Older ones are judged by
	// asking for HTML.
	if mode := req.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(req.Header.Get("Accept"), "text/html")
}

// writePage answers a page load with the waking-up page. It's a 503 so that
// nothing caches it or mistakes it for the app, and the connection closes
// after it.
func writePage(w io.Writer, req *http.Request, data pageData) error {
	data.Mark = Mark
	data.Refresh = int(pageRefresh.Seconds())
	var body bytes.Buffer
	if err := pageTemplate.Execute(&body, data); err != nil {
		return fmt.Errorf("rendering page: %w", err)
	}
	header := fmt.Sprintf("HTTP/1.1 503 Service Unavailable\r\n"+
		"Content-Type: text/html; charset=utf-8\r\n"+
		"Content-Length: %d\r\n"+
		"Cache-Control: no-store\r\n"+
		"Retry-After: %d\r\n"+
		"X-Robots-Tag: noindex\r\n"+
		"Connection: close\r\n\r\n", body.Len(), data.Refresh)
	if _, err := io.WriteString(w, header); err != nil {
		return fmt.Errorf("writing page header: %w", err)
	}
	if req.Method == http.MethodHead {
		return nil
	}
	if _, err := w.Write(body.Bytes()); err != nil {
		return fmt.Errorf("writing page: %w", err)
	}
	return nil
}

// asleepRefresh is how often the asleep page reloads itself. A tab left
// open then reaches the app once someone has woken it, rather than posting
// a stale click to the app, and its button's token never expires.
const asleepRefresh = time.Minute

// asleepPolicy keeps the asleep page to itself: no script runs on it, its
// button posts only back to the page's own address, and no other site can
// frame it, to trick someone into clicking it.
const asleepPolicy = "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; " +
	"frame-ancestors 'none'"

// writeAsleepPage answers a person's browser with the page that asks before
// waking the workload. It's a 200, as a page that works is, and like the
// waking-up page, nothing may cache or index it. Its referrer policy is
// same-origin, not no-referrer, under which a browser posts the click with
// Origin: null: over plain HTTP, its Origin is all that says the click came
// from the page, and other sites still get nothing.
func writeAsleepPage(w io.Writer, data pageData) error {
	data.Mark = Mark
	data.Refresh = int(asleepRefresh.Seconds())
	data.Asleep = true
	data.Field = wakeField
	data.WakeRequestDocs = wakeRequestDocs
	var body bytes.Buffer
	if err := pageTemplate.Execute(&body, data); err != nil {
		return fmt.Errorf("rendering page: %w", err)
	}
	header := fmt.Sprintf("HTTP/1.1 200 OK\r\n"+
		"Content-Type: text/html; charset=utf-8\r\n"+
		"Content-Length: %d\r\n"+
		"Cache-Control: no-store\r\n"+
		"X-Robots-Tag: noindex\r\n"+
		"Content-Security-Policy: %s\r\n"+
		"X-Frame-Options: DENY\r\n"+
		"Referrer-Policy: same-origin\r\n"+
		"Connection: close\r\n\r\n", body.Len(), asleepPolicy)
	if _, err := io.WriteString(w, header); err != nil {
		return fmt.Errorf("writing page header: %w", err)
	}
	if _, err := w.Write(body.Bytes()); err != nil {
		return fmt.Errorf("writing page: %w", err)
	}
	return nil
}

// asleepRetryAfter is what the 503 for a workload that asks tells a client
// to wait. Asking again sooner wakes nothing: only a person, or their
// tooling, can.
const asleepRetryAfter = 5 * time.Minute

// The 503 a workload that asks gives everything that isn't a person's
// browser: what it is, and how to wake it. It names nothing inside the
// cluster, since anyone who reaches the Service reads it.
const (
	asleepMessage = "This environment is asleep. A request doesn't wake it: only a person who opens it in a " +
		"browser and clicks Wake it, or their tooling, can."
	asleepHowTo = "Open it in a browser and click Wake it; set the napwake.dev/wake-requested annotation on its " +
		"ManagedWorkload; or run kubectl napwake wake: " + wakeRequestDocs
	asleepDocs = "https://docs.napwake.dev/concepts/wake-on-request/#asking-before-waking"

	// wakeRequestDocs says how tooling wakes a workload, for the people
	// whose tooling reaches it.
	wakeRequestDocs = "https://docs.napwake.dev/guides/pause/#wake-now"
)

var asleepJSON = func() string {
	body, _ := json.Marshal(struct { // a struct of strings always marshals
		Status  string `json:"status"`
		Message string `json:"message"`
		Wake    string `json:"wake"`
		Docs    string `json:"docs"`
	}{"asleep", asleepMessage, asleepHowTo, asleepDocs})
	return string(body) + "\n"
}()

const asleepText = asleepMessage + "\n" + asleepHowTo + "\n" + asleepDocs + "\n"

// writeAsleep answers a request that doesn't wake the workload: a 503,
// with a short body in JSON for a client that asks for it, and in plain
// text otherwise.
func writeAsleep(w io.Writer, req *http.Request) error {
	body, contentType := asleepText, "text/plain; charset=utf-8"
	if wantsJSON(req) {
		body, contentType = asleepJSON, "application/json"
	}
	header := fmt.Sprintf("HTTP/1.1 503 Service Unavailable\r\n"+
		"Content-Type: %s\r\n"+
		"Content-Length: %d\r\n"+
		"Cache-Control: no-store\r\n"+
		"Retry-After: %d\r\n"+
		"X-Robots-Tag: noindex\r\n"+
		"Connection: close\r\n\r\n", contentType, len(body), int(asleepRetryAfter.Seconds()))
	if req.Method == http.MethodHead {
		_, err := io.WriteString(w, header)
		return err
	}
	_, err := io.WriteString(w, header+body)
	return err
}

// wantsJSON reports whether req asks for JSON before plain text or HTML.
func wantsJSON(req *http.Request) bool {
	before, _, found := strings.Cut(strings.ToLower(req.Header.Get("Accept")), "json")
	return found && !strings.Contains(before, "text/")
}

// botMarkers are what crawlers, link previewers, scanners and headless
// browsers put in a User-Agent that otherwise reads as a browser's. They're
// matched with what follows "bot" or a word's end, so a phone model such as
// a Cubot's isn't taken for one.
var botMarkers = []string{
	"bot/", "bot;", "bot-", "bot)", "bot.", "bot+",
	"spider", "crawl", "slurp", "headless", "lighthouse", "zgrab", "compatible;",
}

// looksLikeBrowser reports whether a User-Agent is a person's browser:
// every browser in use starts its User-Agent with "Mozilla/", and crawlers
// that do too name themselves after it. One that's wrongly taken for a
// browser is only shown the asleep page, which wakes nothing either.
func looksLikeBrowser(agent string) bool {
	if !strings.HasPrefix(agent, "Mozilla/") {
		return false
	}
	agent = strings.ToLower(agent)
	for _, marker := range botMarkers {
		if strings.Contains(agent, marker) {
			return false
		}
	}
	return !strings.HasSuffix(agent, "bot")
}

// isPersonLoadingAPage reports whether req is a person's browser loading a
// page, which a workload that asks shows the asleep page to.
func isPersonLoadingAPage(req *http.Request) bool {
	return req.Method == http.MethodGet && isPageLoad(req) && looksLikeBrowser(req.UserAgent())
}

// sinceLabel says how long ago the wake started, for the page.
func sinceLabel(d time.Duration) string {
	switch {
	case d < 2*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	default:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}
