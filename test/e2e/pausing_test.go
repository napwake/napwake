//go:build e2e

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

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/napwake/napwake/test/utils"
)

// Every pause used to have a few seconds in which a request through the
// workload's Service was refused: its pods had left the Service's endpoints,
// and the doorman's weren't in place yet. Now the doorman stands beside the
// pods from the moment the pause starts, so through a pause every request is
// answered, by a pod or by the doorman, and none is refused or reset.
var _ = Describe("Requests across a pause", func() {
	// steadyRequests is a pod that sends a request through the Service every
	// fifth of a second for seconds, and prints each one's curl exit code and
	// HTTP status, when it ended, and curl's error for one that got no
	// answer: 7 refused, 56 reset, 52 empty, 28 not connected within 5
	// seconds, as to a pod that has gone. A request the doorman holds is
	// connected, and waits.
	steadyRequests := func(name, ns, service string, seconds int) {
		script := fmt.Sprintf(`end=$(( $(date +%%s) + %d ))
while [ "$(date +%%s)" -lt "$end" ]; do
  code=$(curl -sS -o /dev/null -w '%%{http_code}' --connect-timeout 5 --max-time 150 http://%s/hostname 2>/tmp/err)
  echo "request $? $code $(date -u +%%T) $(cat /tmp/err)"
  sleep 0.2
done`, seconds, service)
		_, err := utils.Run(exec.Command("kubectl", "run", name, "-n", ns, "--restart=Never",
			"--image=curlimages/curl:8.7.1", "--image-pull-policy=IfNotPresent", "--command", "--", "sh", "-c", script))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_, _ = utils.Run(exec.Command("kubectl", "delete", "pod", name, "-n", ns, "--wait=false"))
		})
		Eventually(func() (string, error) { return jsonpath("pod", name, ns, "{.status.phase}") }).
			Should(Equal("Running"))
	}

	// unanswered are the requests a steadyRequests pod sent that got no
	// answer, and how many it sent in all, once it's done.
	unanswered := func(name, ns string) (failed []string, sent int) {
		logs, _ := finishedPodLogs(name, ns)
		for _, line := range utils.GetNonEmptyLines(logs) {
			fields := strings.Fields(line)
			if len(fields) < 4 || fields[0] != "request" {
				continue
			}
			sent++
			if fields[1] != "0" {
				failed = append(failed, line)
			}
		}
		return failed, sent
	}

	// startGracefulApp runs the web app with a shutdown that serves for a
	// few seconds after SIGTERM, as an app should: one that exits at once
	// refuses the connections kube-proxy sends it in the second before it
	// drops the pod, in a pause as in any rollout.
	startGracefulApp := func(name, ns string) {
		manifest := strings.Replace(webManifest(name, ns, 2), "args: [netexec, --http-port=8080]",
			"args: [netexec, --http-port=8080, --delay-shutdown=5]", 1)
		Expect(manifest).To(ContainSubstring("--delay-shutdown=5"))
		Expect(kubectlApply(manifest)).To(Succeed())
		rollout(ns, "deployment/"+name)
	}

	requestPause := func(ns, name string) {
		_, err := utils.Run(exec.Command("kubectl", "annotate", "managedworkload", name, "-n", ns, "--overwrite",
			"napwake.dev/pause-requested="+time.Now().UTC().Format(time.RFC3339Nano)))
		Expect(err).NotTo(HaveOccurred())
	}

	It("answers every request as a pause is called off for one", func() {
		ns := createNamespace("napwake-e2e-pause-in-use")
		const name = "e2e-steady"
		startGracefulApp(name, ns)
		manage(ns, "Deployment", name, "1h")
		Eventually(func() (string, error) {
			return jsonpath("managedworkload", name, ns, "{.status.phase}")
		}).Should(Equal("Running"))

		steadyRequests("steady-requests", ns, name, 75)
		By("asking for a pause while requests come")
		requestPause(ns, name)

		failed, sent := unanswered("steady-requests", ns)
		Expect(failed).To(BeEmpty(), "no request may be refused or reset")
		Expect(sent).To(BeNumerically(">", 100))

		By("checking the pause was called off, with the workload never down")
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "get", "events.events.k8s.io", "-n", ns,
				"-o", `jsonpath={range .items[?(@.reason=="PauseCancelled")]}{.note}{"\n"}{end}`))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(ContainSubstring("a request came as it paused; the pause is cancelled"))
		}, 2*time.Minute, 2*time.Second).Should(Succeed())
		Eventually(func() (string, error) {
			return jsonpath("managedworkload", name, ns, "{.status.phase}")
		}, 2*time.Minute, 2*time.Second).Should(Equal("Running"))
		Expect(jsonpath("managedworkload", name, ns, "{.status.idleWait.wrongPauses}")).To(BeEmpty(),
			"a pause asked for isn't the idle clock's mistake")
	})

	It("answers every request across a pause that goes through", func() {
		ns := createNamespace("napwake-e2e-pause-through")
		const name = "e2e-asks"
		startGracefulApp(name, ns)
		// A workload that asks first is woken only by a person's click, so
		// the requests passed on to its pods as it pauses don't call the
		// pause off: it goes through with them coming, and once its pods
		// have stopped, the doorman answers them that it's asleep.
		Expect(kubectlApply(fmt.Sprintf(`
apiVersion: napwake.dev/v1alpha1
kind: ManagedWorkload
metadata: {name: %[1]s, namespace: %[2]s}
spec:
  target: {kind: Deployment, name: %[1]s}
  idlePolicy: {idleAfter: 1h}
  prediction: {confidence: 85}
  wake: {mode: Ask}
`, name, ns))).To(Succeed())
		Eventually(func() (string, error) {
			return jsonpath("managedworkload", name, ns, "{.status.phase}")
		}).Should(Equal("Running"))

		steadyRequests("steady-requests", ns, name, 75)
		By("asking for a pause while requests come")
		requestPause(ns, name)
		Eventually(func() (string, error) {
			return jsonpath("managedworkload", name, ns, "{.status.phase}")
		}, 2*time.Minute, time.Second).Should(Equal("Paused"))

		failed, sent := unanswered("steady-requests", ns)
		Expect(failed).To(BeEmpty(), "no request may be refused or reset")
		Expect(sent).To(BeNumerically(">", 100))
		Expect(jsonpath("managedworkload", name, ns, "{.status.pause.routedAt}")).NotTo(BeEmpty(),
			"the doorman fronted it before its pods stopped")
		logs, _ := finishedPodLogs("steady-requests", ns)
		Expect(logs).To(ContainSubstring("request 0 200"), "the pods answered while they ran")
		Expect(logs).To(ContainSubstring("request 0 503"), "and the doorman, that it's asleep, once they'd stopped")
	})
})
