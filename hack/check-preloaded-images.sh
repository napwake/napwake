#!/usr/bin/env bash
# Checks that the kind cluster KUBECONFIG reaches runs each image given,
# name:tag@sha256:digest as E2E_IMAGES lists them, by its digest with
# imagePullPolicy Never, as a manifest that pins an image by digest names
# it. A node that has an image only by its tag can't: the kubelet would
# pull it, and with Never it reports ErrImageNeverPull instead. Each pod
# runs the image's own entrypoint, and only has to start, not succeed.
#
#   KUBECONFIG=bin/e2e/<cluster>/kubeconfig hack/check-preloaded-images.sh $(make -s e2e-images)
set -euo pipefail

if [ "$#" -eq 0 ]; then
  echo "usage: $0 IMAGE..." >&2
  exit 2
fi

NAMESPACE=default
LABEL=napwake.dev/image-check
TIMEOUT=120

cleanup() {
  kubectl delete pods -n "$NAMESPACE" -l "$LABEL" --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

declare -a pods
i=0
for img in "$@"; do
  i=$((i + 1))
  pod=image-check-$i
  kubectl run "$pod" -n "$NAMESPACE" --labels="$LABEL=true" --restart=Never \
    --image="$img" --image-pull-policy=Never >/dev/null
  pods+=("$pod=$img")
done

# state prints the pod's phase, its container's waiting reason and the
# image ID the runtime resolved it to, split by |, each empty while unset.
state() {
  kubectl get pod "$1" -n "$NAMESPACE" -o \
    'jsonpath={.status.phase}|{.status.containerStatuses[0].state.waiting.reason}|{.status.containerStatuses[0].imageID}'
}

failed=0
deadline=$((SECONDS + TIMEOUT))
for entry in "${pods[@]}"; do
  pod=${entry%%=*}
  img=${entry#*=}
  while :; do
    IFS='|' read -r phase reason image_id <<<"$(state "$pod")"
    case "$reason" in
      ErrImageNeverPull | ErrImagePull | ImagePullBackOff | InvalidImageName)
        echo "$img: the node doesn't have it by its digest ($reason)" >&2
        failed=1
        break
        ;;
    esac
    if [ -n "$image_id" ] || { [ -n "$phase" ] && [ "$phase" != Pending ]; }; then
      echo "$img: runs by its digest"
      break
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "$img: still $phase ($reason) after ${TIMEOUT}s" >&2
      kubectl describe pod "$pod" -n "$NAMESPACE" >&2 || true
      failed=1
      break
    fi
    sleep 1
  done
done
if [ "$failed" -ne 0 ]; then
  echo "the node pulls these images rather than run what was preloaded: make load-test-e2e-images tags each by its digest" >&2
  exit 1
fi
