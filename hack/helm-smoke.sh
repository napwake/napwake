#!/usr/bin/env bash
# Installs the Helm chart with watchNamespaces into a kind cluster and walks
# the quickstart with only the namespaced Roles the chart grants: a
# Deployment opted in by label pauses once its idle clock runs out, its
# Service is routed to the doorman, and a request to the Service wakes it,
# restores its replicas and is answered. One in an unwatched namespace is
# left alone. A StatefulSet opted in with dry-run, as the README's
# quickstart does, is measured but not paused until kubectl napwake
# enable ends dry-run. Neither the operator nor the doorman logs an error.
# An upgrade that stops watching the namespace is refused. Last, helm
# uninstall is refused while ManagedWorkloads are left, an upgrade with
# the namespaces the operator watches marks the release deployed again,
# and the uninstall goes ahead once the workloads are no longer opted in,
# which restores the paused StatefulSet. The e2e suite installs through kustomize, with a
# ClusterRole, so this is what tests the chart.
#
# KIND_CLUSTER names the cluster, which is created and deleted here, so it
# must not already exist. It's this checkout's own by default, and the
# image is named after it, so smoke tests from two worktrees at once never
# share either. KEEP_CLUSTER=true keeps it for debugging.
set -euo pipefail

CLUSTER=${KIND_CLUSTER:-$("$(dirname "$0")/kind-cluster-name.sh" smoke)}
KIND=${KIND:-kind}
IMG_TAG=$CLUSTER
IMG=napwake:$IMG_TAG
RELEASE_NAMESPACE=napwake-system
WATCHED=smoke-watched
UNWATCHED=smoke-unwatched
APP=web
REPLICAS=2
DB=db
WEB_IMAGE=registry.k8s.io/e2e-test-images/agnhost:2.52
CURL_IMAGE=curlimages/curl:8.7.1
METRICS_SERVER_IMAGE=registry.k8s.io/metrics-server/metrics-server:v0.7.2
METRICS_SERVER_MANIFEST="$(dirname "$0")/../test/e2e/testdata/metrics-server-v0.7.2.yaml"
# A ManagedWorkload that routed through the doorman is deleted only once its
# ports have drained, twice the doorman's RouteDrain, so its deletion is
# given that plus a minute for the requeue and the finalizer's write.
ROUTE_DRAIN=$(awk '/^\tRouteDrain = [0-9]+ \* time\.Second$/ {print $3}' "$(dirname "$0")/../internal/doorman/routes.go")
if [ -z "$ROUTE_DRAIN" ]; then
  echo "can't read RouteDrain from internal/doorman/routes.go" >&2
  exit 1
fi
DELETE_TIMEOUT=$((2 * ROUTE_DRAIN + 60))

if "$KIND" get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  echo "kind cluster $CLUSTER already exists; set KIND_CLUSTER to a new name" >&2
  exit 1
fi

# A kubeconfig of its own, so the cluster's context never becomes the
# current one in the caller's kubeconfig.
KUBECONFIG=$(mktemp)
export KUBECONFIG
PLUGIN_DIR=$(mktemp -d)

diagnose() {
  echo "--- ManagedWorkloads"
  kubectl get managedworkloads -A -o yaml || true
  echo "--- EndpointSlices in $WATCHED"
  kubectl get endpointslices -n "$WATCHED" -o wide || true
  echo "--- events in $WATCHED"
  kubectl get events -n "$WATCHED" --sort-by=.lastTimestamp || true
  echo "--- Napwake's pods and logs"
  kubectl get pods -n "$RELEASE_NAMESPACE" -o wide || true
  kubectl get events -n "$RELEASE_NAMESPACE" --sort-by=.lastTimestamp || true
  kubectl logs -n "$RELEASE_NAMESPACE" -l app.kubernetes.io/instance=napwake --prefix --previous --tail=50 || true
  kubectl logs -n "$RELEASE_NAMESPACE" -l app.kubernetes.io/instance=napwake --prefix --tail=200 || true
}

cleanup() {
  local status=$?
  if [ "$status" -ne 0 ]; then
    diagnose
  fi
  if [ "${KEEP_CLUSTER:-false}" = true ]; then
    echo "kept kind cluster $CLUSTER; its kubeconfig is $KUBECONFIG"
  else
    "$KIND" delete cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" >/dev/null 2>&1 || true
    docker rmi "$IMG" >/dev/null 2>&1 || true
    rm -f "$KUBECONFIG"
    rm -rf "$PLUGIN_DIR"
  fi
  exit "$status"
}
trap cleanup EXIT

# eventually runs a check until it passes or timeout seconds pass.
eventually() {
  local timeout=$1 what=$2
  shift 2
  local deadline=$((SECONDS + timeout))
  until "$@" >/dev/null 2>&1; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "timed out after ${timeout}s waiting for $what" >&2
      return 1
    fi
    sleep 2
  done
}

# is checks that a jsonpath of an object has a value.
is() {
  local object=$1 ns=$2 path=$3 want=$4
  [ "$(kubectl get "$object" -n "$ns" -o "jsonpath=$path")" = "$want" ]
}

not_empty() {
  local object=$1 ns=$2 path=$3
  [ -n "$(kubectl get "$object" -n "$ns" -o "jsonpath=$path")" ]
}

gone() {
  local object=$1 ns=$2
  local found
  found=$(kubectl get "$object" -n "$ns" --ignore-not-found -o name) || return 1
  [ -z "$found" ]
}

# The EndpointSlices the operator writes to route the workload's Services
# to the doorman. Their names are hashed, so they're found by label.
DOORMAN_SLICES="endpointslice.kubernetes.io/managed-by=doorman.napwake.dev,napwake.dev/managed-workload=$APP"

routed() {
  [ -n "$(kubectl get endpointslices -n "$WATCHED" -l "$DOORMAN_SLICES" -o 'jsonpath={.items[*].endpoints[*].addresses[0]}')" ]
}

unrouted() {
  local slices
  slices=$(kubectl get endpointslices -n "$WATCHED" -l "$DOORMAN_SLICES" -o 'jsonpath={.items[*].metadata.name}') &&
    [ -z "$slices" ]
}

"$KIND" create cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" --wait 2m
make docker-build IMG="$IMG"
make load-test-e2e-images KIND_CLUSTER="$CLUSTER" E2E_IMAGES="$WEB_IMAGE $CURL_IMAGE $METRICS_SERVER_IMAGE"
"$KIND" load docker-image "$IMG" --name "$CLUSTER"
PLUGIN="$PLUGIN_DIR/kubectl-napwake"
go build -o "$PLUGIN" ./cmd/kubectl-napwake

echo "installing metrics-server, which the idle clock reads CPU from"
kubectl apply -f "$METRICS_SERVER_MANIFEST"
# kind's kubelets serve self-signed certificates.
kubectl patch deployment metrics-server -n kube-system --type=json \
  -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'

kubectl create namespace "$WATCHED"
kubectl create namespace "$UNWATCHED"
helm install napwake charts/napwake --namespace "$RELEASE_NAMESPACE" --create-namespace --wait --timeout 5m \
  --set image.repository=napwake --set image.tag="$IMG_TAG" --set image.pullPolicy=Never \
  --set "watchNamespaces={$WATCHED}"
kubectl wait --for=condition=Available apiservice/v1beta1.metrics.k8s.io --timeout=3m

for ns in "$WATCHED" "$UNWATCHED"; do
  kubectl apply -f - <<YAML
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $APP
  namespace: $ns
  labels: {napwake.dev/managed: "true"}
  annotations: {napwake.dev/idle-after: 1m}
spec:
  replicas: $REPLICAS
  selector: {matchLabels: {app: $APP}}
  template:
    metadata: {labels: {app: $APP}}
    spec:
      containers:
        - name: web
          image: $WEB_IMAGE
          imagePullPolicy: IfNotPresent
          args: [netexec, --http-port=8080]
          ports: [{name: http, containerPort: 8080}]
          readinessProbe:
            httpGet: {path: /healthz, port: http}
            periodSeconds: 2
          resources: {requests: {cpu: 100m, memory: 16Mi}}
---
apiVersion: v1
kind: Service
metadata: {name: $APP, namespace: $ns}
spec:
  selector: {app: $APP}
  ports: [{name: http, port: 80, targetPort: http}]
YAML
done

# The README's quickstart: a StatefulSet opted in to be measured first.
kubectl apply -f - <<YAML
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: $DB
  namespace: $WATCHED
  labels: {napwake.dev/managed: "true"}
  annotations: {napwake.dev/dry-run: "true", napwake.dev/idle-after: 1m}
spec:
  serviceName: $DB
  replicas: 1
  selector: {matchLabels: {app: $DB}}
  template:
    metadata: {labels: {app: $DB}}
    spec:
      containers:
        - name: db
          image: $WEB_IMAGE
          imagePullPolicy: IfNotPresent
          args: [netexec, --http-port=8080]
          ports: [{name: http, containerPort: 8080}]
          readinessProbe:
            httpGet: {path: /healthz, port: http}
            periodSeconds: 2
          resources: {requests: {cpu: 100m, memory: 16Mi}}
---
apiVersion: v1
kind: Service
metadata: {name: $DB, namespace: $WATCHED}
spec:
  selector: {app: $DB}
  ports: [{name: http, port: 8080, targetPort: http}]
YAML
kubectl rollout status "deployment/$APP" -n "$WATCHED" --timeout=2m
kubectl rollout status "statefulset/$DB" -n "$WATCHED" --timeout=2m

echo "waiting for the label to create a ManagedWorkload, and its idle clock to pause it"
eventually 60 "a ManagedWorkload from the label" is managedworkload/$APP "$WATCHED" '{.metadata.name}' "$APP"
eventually 300 "the workload to pause" is managedworkload/$APP "$WATCHED" '{.status.phase}' Paused
eventually 60 "the Deployment to scale to 0" is deployment/$APP "$WATCHED" '{.spec.replicas}' 0
eventually 60 "the Service to be routed to the doorman" \
  is managedworkload/$APP "$WATCHED" '{.status.conditions[?(@.type=="WakeOnRequest")].reason}' DoormanRouted
is managedworkload/$APP "$WATCHED" '{.status.conditions[?(@.type=="WakeOnRequest")].status}' True
eventually 60 "the doorman's EndpointSlices" routed

echo "sending a request to the paused workload's Service"
# kube-proxy programs the doorman's endpoints a moment after they're
# written, and until then the Service has none and refuses. curl retries
# only that, never a connection the doorman holds.
kubectl run curl -n "$WATCHED" --restart=Never --image="$CURL_IMAGE" --image-pull-policy=IfNotPresent --command -- \
  curl -sS --fail-with-body --max-time 150 --retry 10 --retry-delay 1 --retry-connrefused "http://$APP/hostname"
eventually 200 "the request to finish" is pod/curl "$WATCHED" '{.status.phase}' Succeeded
answer=$(kubectl logs curl -n "$WATCHED" | tail -n 1)
case "$answer" in
  "$APP"-*) echo "answered by $answer" ;;
  *)
    echo "the held request wasn't answered by the woken workload: $answer" >&2
    exit 1
    ;;
esac

echo "checking the request woke it with all its replicas"
eventually 120 "the workload to be Running" is managedworkload/$APP "$WATCHED" '{.status.phase}' Running
is managedworkload/$APP "$WATCHED" '{.status.activity.lastActivitySource}' request
eventually 120 "$REPLICAS ready replicas" is deployment/$APP "$WATCHED" '{.status.readyReplicas}' "$REPLICAS"
eventually 60 "the doorman's EndpointSlices to be removed" unrouted

echo "checking the unwatched namespace was left alone"
if kubectl get managedworkload "$APP" -n "$UNWATCHED" >/dev/null 2>&1; then
  echo "a ManagedWorkload was created in $UNWATCHED" >&2
  exit 1
fi
is deployment/$APP "$UNWATCHED" '{.spec.replicas}' "$REPLICAS"

echo "checking the dry-run StatefulSet was measured, not paused"
would_pause() {
  local pauses
  pauses=$(kubectl get managedworkload "$DB" -n "$WATCHED" -o 'jsonpath={.status.dryRun.pauses}')
  [ "${pauses:-0}" -ge 1 ]
}
is managedworkload/$DB "$WATCHED" '{.spec.target.kind}' StatefulSet
is managedworkload/$DB "$WATCHED" '{.spec.dryRun}' true
eventually 120 "dry-run to count a pause it would have made" would_pause
is statefulset/$DB "$WATCHED" '{.spec.replicas}' 1

echo "ending dry-run with kubectl napwake enable"
"$PLUGIN" enable "statefulset/$DB" -n "$WATCHED"
is statefulset/$DB "$WATCHED" '{.metadata.annotations.napwake\.dev/dry-run}' false
eventually 60 "the ManagedWorkload to leave dry-run" is managedworkload/$DB "$WATCHED" '{.spec.dryRun}' ''
eventually 180 "the StatefulSet to pause" is managedworkload/$DB "$WATCHED" '{.status.phase}' Paused
eventually 60 "the StatefulSet to scale to 0" is statefulset/$DB "$WATCHED" '{.spec.replicas}' 0

echo "checking the operator and the doorman logged no errors"
# Every pod of both, not one per Deployment. A conflict is an optimistic
# write that lost a race; controller-runtime retries it at once.
logs=$(kubectl logs -n "$RELEASE_NAMESPACE" -l app.kubernetes.io/instance=napwake --prefix --tail=-1)
errors=$(grep -E '"level":"error"|forbidden|unknown namespace' <<<"$logs" |
  grep -v 'the object has been modified; please apply your changes to the latest version' || true)
if [ -n "$errors" ]; then
  echo "$errors" >&2
  echo "the operator or the doorman logged errors" >&2
  exit 1
fi

echo "checking helm upgrade is refused when it would stop watching $WATCHED"
if refusal=$(helm upgrade napwake charts/napwake --namespace "$RELEASE_NAMESPACE" --reuse-values \
  --set "watchNamespaces={$UNWATCHED}" --wait --timeout 5m 2>&1); then
  echo "$refusal" >&2
  echo "helm upgrade stopped watching $WATCHED, leaving its ManagedWorkloads with no operator" >&2
  exit 1
fi
# Helm 4 prints the hook's output as one log message with its line breaks
# escaped, so the refusal's first line, up to the first break, must name
# what's left and link the docs on its own.
lead=$(grep -o 'Napwake refuses this upgrade: [^\\]*' <<<"$refusal" || true)
if ! grep -qF "$WATCHED/$APP" <<<"$lead" || ! grep -qF 'upgrading/#narrowing-watchnamespaces' <<<"$lead"; then
  echo "$refusal" >&2
  echo "the refused upgrade didn't lead with a line naming the ManagedWorkloads it would leave, and the docs" >&2
  exit 1
fi
kubectl get deployment napwake -n "$RELEASE_NAMESPACE" -o 'jsonpath={.spec.template.spec.containers[0].args}' |
  grep -qF -- "--watch-namespaces=$WATCHED" || {
  echo "the refused upgrade changed the operator's namespaces" >&2
  exit 1
}

# The release's last revision is now the refused upgrade's, which Helm
# uninstalls with, so the guard must still find the ManagedWorkloads the
# operator took on in a namespace those values don't watch.
echo "checking helm uninstall is refused while ManagedWorkloads are left"
if refusal=$(helm uninstall napwake --namespace "$RELEASE_NAMESPACE" --wait --timeout 5m 2>&1); then
  echo "$refusal" >&2
  echo "helm uninstall went ahead, leaving ManagedWorkloads with no operator" >&2
  exit 1
fi
lead=$(grep -o 'Napwake refuses to uninstall: [^\\]*' <<<"$refusal" || true)
if ! grep -qF "$WATCHED/$APP" <<<"$lead" || ! grep -qF "$WATCHED/$DB" <<<"$lead" ||
  ! grep -qF 'installation/#uninstall' <<<"$lead"; then
  echo "$refusal" >&2
  echo "the refused uninstall didn't lead with a line naming the ManagedWorkloads left, and the docs" >&2
  exit 1
fi
is deployment/napwake "$RELEASE_NAMESPACE" '{.status.readyReplicas}' 1

# After a refused upgrade, the docs recover with an upgrade that keeps the
# namespaces the operator watches, read from its flags. Helm 3 fails a
# helm rollback here: it compares the rollback with the refused revision,
# which lacks the Roles that run.
echo "checking an upgrade with the operator's namespaces marks the release deployed again, as the docs say"
running=$(kubectl get deployment -n "$RELEASE_NAMESPACE" -l control-plane=controller-manager \
  -o 'jsonpath={.items[0].spec.template.spec.containers[0].args}' | grep -o -- '--watch-namespaces=[^"]*' | cut -d= -f2)
if [ "$running" != "$WATCHED" ]; then
  echo "the operator watches '$running', not $WATCHED" >&2
  exit 1
fi
generations=$(kubectl get deployments -n "$RELEASE_NAMESPACE" -l app.kubernetes.io/instance=napwake \
  -o 'jsonpath={.items[*].metadata.generation}')
helm upgrade napwake charts/napwake --namespace "$RELEASE_NAMESPACE" --reuse-values \
  --set "watchNamespaces={$running}" --wait --timeout 5m
status=$(helm status napwake --namespace "$RELEASE_NAMESPACE" | awk '/^STATUS:/ {print $2}')
if [ "$status" != deployed ]; then
  echo "after the upgrade the release is $status, not deployed" >&2
  exit 1
fi
if [ "$(kubectl get deployments -n "$RELEASE_NAMESPACE" -l app.kubernetes.io/instance=napwake \
  -o 'jsonpath={.items[*].metadata.generation}')" != "$generations" ]; then
  echo "the upgrade that marks the release deployed changed the operator's or the doorman's Deployment" >&2
  exit 1
fi

echo "stopping managing the workloads, which restores the paused StatefulSet, then uninstalling"
kubectl label deployment "$APP" -n "$WATCHED" napwake.dev/managed-
kubectl label statefulset "$DB" -n "$WATCHED" napwake.dev/managed-
eventually "$DELETE_TIMEOUT" "the Deployment's ManagedWorkload to be deleted" gone managedworkload/$APP "$WATCHED"
eventually "$DELETE_TIMEOUT" "the StatefulSet's ManagedWorkload to be deleted" gone managedworkload/$DB "$WATCHED"
is statefulset/$DB "$WATCHED" '{.spec.replicas}' 1
helm uninstall napwake --namespace "$RELEASE_NAMESPACE" --wait --timeout 5m
echo "ok"
