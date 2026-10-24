{{/*
Fills in .Values, in place, with this chart's default for every value it
lacks. Helm renders with the chart's own values.yaml as the defaults,
except on helm upgrade --reuse-values, which renders with the previous
release's chart's, lacking every value added since: each would render as
nil, or fail the render. defaultsVersion says whose defaults Helm used.
When they're another version's, the missing values are taken from
files/defaults.yaml, a copy of values.yaml, which templates can't read.
With the chart's own nothing is filled, so a default removed with null
stays removed. Every template includes it first.
*/}}
{{- define "napwake.defaults" -}}
{{- $defaults := .Files.Get "files/defaults.yaml" | fromYaml -}}
{{- if or (not $defaults) (hasKey $defaults "Error") -}}
{{- fail "the chart's files/defaults.yaml, which it fills in missing values from, is missing or unreadable" -}}
{{- end -}}
{{- if ne (toString .Values.defaultsVersion) (toString $defaults.defaultsVersion) -}}
{{- include "napwake.fill" (list .Values $defaults) -}}
{{- $_ := set .Values "defaultsVersion" $defaults.defaultsVersion -}}
{{- end -}}
{{- end }}

{{/*
Adds to a map of values, given first, every key of the defaults, given
second, that it lacks, and does the same in each map both have. A value
that's there is kept, false, 0 and "" included, and lists aren't merged,
as Helm does with values it's given.
*/}}
{{- define "napwake.fill" -}}
{{- $values := index . 0 -}}
{{- range $key, $default := index . 1 -}}
{{- if not (hasKey $values $key) -}}
{{- $_ := set $values $key $default -}}
{{- else if and (kindIs "map" $default) (kindIs "map" (index $values $key)) -}}
{{- include "napwake.fill" (list (index $values $key) $default) -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Chart name, truncated to 63 chars.
*/}}
{{- define "napwake.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name, cut to 40 characters so that the longest name
built from it, the doorman's <fullname>-doorman-auth-delegator, fits the
63 a Service or label allows, and the doorman's name never equals it.
*/}}
{{- define "napwake.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 40 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 40 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 40 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart label value.
*/}}
{{- define "napwake.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels applied to every resource.
*/}}
{{- define "napwake.labels" -}}
helm.sh/chart: {{ include "napwake.chart" . }}
{{ include "napwake.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels used by Deployment and Service.
*/}}
{{- define "napwake.selectorLabels" -}}
app.kubernetes.io/name: {{ include "napwake.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
control-plane: controller-manager
{{- end }}

{{/*
Doorman name and labels. Distinct from the operator's, so the
operator's Service and NetworkPolicy don't select doorman pods.
*/}}
{{- define "napwake.doormanName" -}}
{{- printf "%s-doorman" (include "napwake.fullname" .) }}
{{- end }}

{{/*
The headless Service whose DNS name resolves to the doorman's Ready pods,
which forwarders connect to, and its labels: not the doorman's, so the
doorman's ServiceMonitor doesn't select it.
*/}}
{{- define "napwake.doormanPodsName" -}}
{{- printf "%s-pods" (include "napwake.doormanName" .) }}
{{- end }}

{{- define "napwake.doormanPodsLabels" -}}
helm.sh/chart: {{ include "napwake.chart" . }}
app.kubernetes.io/name: {{ include "napwake.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: doorman-pods
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "napwake.doormanSelectorLabels" -}}
app.kubernetes.io/name: {{ include "napwake.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
control-plane: doorman
{{- end }}

{{/*
A guard hook's labels, given the root context and the operation it
guards. Distinct from the operator's, so the operator's metrics Service
and NetworkPolicy don't select the hook's pod.
*/}}
{{- define "napwake.guardLabels" -}}
helm.sh/chart: {{ include "napwake.chart" .root }}
app.kubernetes.io/name: {{ include "napwake.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
control-plane: {{ .op }}-guard
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
{{- end }}

{{- define "napwake.doormanLabels" -}}
helm.sh/chart: {{ include "napwake.chart" . }}
{{ include "napwake.doormanSelectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "napwake.guardName" -}}
{{- printf "%s-%s-guard" (include "napwake.fullname" .root) .op }}
{{- end }}

{{/*
Service account name.
*/}}
{{- define "napwake.serviceAccountName" -}}
{{- if .Values.serviceAccount.name }}
{{- .Values.serviceAccount.name }}
{{- else }}
{{- include "napwake.fullname" . }}
{{- end }}
{{- end }}

{{/*
Controller image.
*/}}
{{- define "napwake.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag }}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}

{{/*
The operator's rules on namespaced resources: a ClusterRole's, or with
watchNamespaces, a Role's in each of them.
*/}}
{{- define "napwake.managerRules" -}}
- apiGroups: [""]
  resources: [configmaps]
  verbs: [get]
- apiGroups: [""]
  resources: [events]
  verbs: [create, patch]
- apiGroups: [events.k8s.io]
  resources: [events]
  verbs: [create, patch]
- apiGroups: [""]
  resources: [services]
  verbs: [get, list, watch]
- apiGroups: [networking.k8s.io]
  resources: [ingresses]
  verbs: [get, list, watch]
- apiGroups: [""]
  resources: [pods]
  verbs: [get, list, watch]
- apiGroups: [discovery.k8s.io]
  resources: [endpointslices]
  verbs: [create, delete, get, list, patch, update, watch]
- apiGroups: [""]
  resources: [persistentvolumeclaims]
  verbs: [get, list, watch]
- apiGroups: [apps]
  resources: [deployments, statefulsets]
  verbs: [get, list, patch, watch]
- apiGroups: [apps]
  resources: [deployments/scale, statefulsets/scale]
  verbs: [get, update]
- apiGroups: [autoscaling]
  resources: [horizontalpodautoscalers]
  verbs: [get, list, watch]
- apiGroups: [keda.sh]
  resources: [scaledobjects]
  verbs: [get, list, patch, watch]
- apiGroups: [napwake.dev]
  resources: [managedworkloads]
  verbs: [create, delete, get, list, patch, update, watch]
- apiGroups: [napwake.dev]
  resources: [managedworkloads/finalizers]
  verbs: [update]
- apiGroups: [napwake.dev]
  resources: [managedworkloads/status]
  verbs: [get, patch, update]
- apiGroups: [metrics.k8s.io]
  resources: [pods]
  verbs: [get, list]
{{- end }}

{{/*
The operator's rules on cluster-scoped resources, which only a ClusterRole
can grant: reading namespaces, for their labels, nodes, for their
instance types, and Karpenter's NodePools, to say how they consolidate.
*/}}
{{- define "napwake.managerClusterRules" -}}
- apiGroups: [""]
  resources: [namespaces, nodes]
  verbs: [get, list, watch]
- apiGroups: [karpenter.sh]
  resources: [nodepools]
  verbs: [list]
{{- end }}

{{/*
The doorman's rules, all on namespaced resources.
*/}}
{{- define "napwake.doormanRules" -}}
- apiGroups: [napwake.dev]
  resources: [managedworkloads]
  verbs: [get, list, watch, patch]
- apiGroups: [discovery.k8s.io]
  resources: [endpointslices]
  verbs: [get, list, watch]
- apiGroups: [""]
  resources: [pods]
  verbs: [get]
- apiGroups: ["", events.k8s.io]
  resources: [events]
  verbs: [create, patch]
{{- end }}
