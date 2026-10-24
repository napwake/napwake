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

package v1alpha1

import (
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Freed",type=string,JSONPath=`.status.cost.freedThisMonth`,description="What pausing freed this month (UTC), at list price"
// +kubebuilder:printcolumn:name="Saved",type=string,JSONPath=`.status.cost.savedThisMonth`,description="What pausing took off the node bill this month (UTC), at least"
// +kubebuilder:printcolumn:name="Would Free",type=string,JSONPath=`.status.dryRun.estimatedFreed`,description="In dry-run: what the pauses Napwake would have made would have freed since dry-run began"
// +kubebuilder:printcolumn:name="Forecast",type=string,JSONPath=`.status.prediction.reason`,priority=1,description="Whether the forecast is acting, or what it waits for"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ManagedWorkload declares a workload whose lifecycle is managed by Napwake.
type ManagedWorkload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec ManagedWorkloadSpec `json:"spec"`

	// +optional
	Status ManagedWorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ManagedWorkloadList contains a list of ManagedWorkload.
type ManagedWorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ManagedWorkload `json:"items"`
}

// --- Spec ---

// ManagedWorkloadSpec defines the desired lifecycle behavior for a workload.
type ManagedWorkloadSpec struct {
	// Target identifies the workload to manage (e.g. a Deployment or
	// StatefulSet). It can't be changed, since a paused target would be left
	// at zero: create another ManagedWorkload to manage another workload.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="target is immutable; create another ManagedWorkload to manage another workload"
	Target WorkloadRef `json:"target"`

	// IdlePolicy configures automatic pausing: the operator pauses the
	// workload once it has had no activity for IdlePolicy.IdleAfter.
	// +optional
	IdlePolicy *IdlePolicySpec `json:"idlePolicy,omitempty"`

	// DependsOn lists the workloads this one needs, such as a database or a
	// message broker. A dependency isn't paused while anything that depends
	// on it is awake, and waking this workload wakes its dependencies. At
	// most 20.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=20
	// +optional
	DependsOn []DependencyRef `json:"dependsOn,omitempty"`

	// Wake configures waking the workload when a request reaches it while
	// it's paused.
	// +optional
	Wake *WakeSpec `json:"wake,omitempty"`

	// Prediction configures the Holt-Winters forecasting engine that confirms
	// idle detection and wakes paused workloads ahead of predicted demand.
	// +kubebuilder:default={}
	// +optional
	Prediction PredictionSpec `json:"prediction,omitzero"`

	// CostTracking sets this workload's own cost rates. Cost is always
	// tracked: without them, it's priced at the list price of the nodes the
	// workload's pods run on (status.cost.listRates), or at Napwake's
	// default rates for nodes it has no price for.
	// +optional
	CostTracking *CostTrackingSpec `json:"costTracking,omitempty"`

	// DryRun measures the workload without ever pausing it: the operator
	// tracks activity, emits events, and records in status.dryRun the pauses
	// it would have made. A workload already paused when dry-run is turned
	// on is woken, so turning it on never leaves one off.
	// +optional
	DryRun bool `json:"dryRun,omitempty"`
}

// +kubebuilder:validation:Enum=Deployment;StatefulSet
type TargetKind string

const (
	TargetKindDeployment  TargetKind = "Deployment"
	TargetKindStatefulSet TargetKind = "StatefulSet"
)

// WakeSpec configures waking on request. While the workload is paused, its
// Services route to the doorman, which holds each connection, wakes the
// workload, and passes the connection through once it's Ready.
type WakeSpec struct {
	// Mode is what wakes the paused workload through the doorman. Request,
	// the default: any request but a known probe's. Ask: only a person,
	// clicking Wake it on the page the doorman shows a browser; anything
	// else is answered 503 and wakes nothing, so crawlers and scanners on a
	// public URL can't keep it awake.
	// +kubebuilder:default=Request
	// +optional
	Mode WakeMode `json:"mode,omitempty"`

	// MaxWait is how long the doorman holds a connection while the workload
	// wakes, at most 1h. After it, the connection is closed; the wake
	// carries on.
	// +kubebuilder:default="2m"
	// +kubebuilder:validation:XValidation:rule="duration(self) > duration('0s') && duration(self) <= duration('1h')",message="must be a duration above zero and at most 1h, such as 90s or 5m"
	// +optional
	MaxWait *metav1.Duration `json:"maxWait,omitempty"`

	// Page answers a browser loading a page with a "waking up" page that
	// refreshes until the workload is Running, instead of holding the
	// request. Other requests are held either way. With mode: Ask, a browser
	// is always shown a page, since that's where a person asks.
	// +kubebuilder:default=true
	// +optional
	Page *bool `json:"page,omitempty"`
}

// WakeMode is what wakes a paused workload through the doorman.
// +kubebuilder:validation:Enum=Request;Ask
type WakeMode string

const (
	// WakeOnRequest wakes the workload on any request the doorman can't
	// tell is a probe.
	WakeOnRequest WakeMode = "Request"
	// WakeOnAsk wakes it only when a person asks for it, on the page the
	// doorman shows a browser.
	WakeOnAsk WakeMode = "Ask"
)

// Asks reports whether the doorman asks a person before waking the
// workload, wake.mode being Ask. Unset, it doesn't.
func (w *WakeSpec) Asks() bool {
	return w != nil && w.Mode == WakeOnAsk
}

// MaxDependsOn is the most dependencies dependsOn may list.
const MaxDependsOn = 20

// MinIdleAfter is the shortest idlePolicy.idleAfter the CRD accepts. Any
// shorter, a workload would be paused about as soon as each wake finished,
// and spend its time restarting.
const MinIdleAfter = time.Minute

// WakeMaxWaitLimit is the longest wake.maxWait the CRD accepts. Each held
// connection takes one of the doorman's limited places, so a connection
// held for hours keeps other callers out long after its own caller has
// given up.
const WakeMaxWaitLimit = time.Hour

// DependencyRef names a workload this one needs. It refers to the
// dependency's Deployment or StatefulSet, not to its ManagedWorkload.
type DependencyRef struct {
	// Namespace of the dependency. Defaults to the ManagedWorkload's own.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	Kind TargetKind `json:"kind"`

	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// WaitForReady holds this workload's resume, without scaling it, until
	// the dependency's pods are Ready. Use it when this workload fails if it
	// starts before the dependency can serve, e.g. a database replaying its
	// write-ahead log. By default dependencies wake at the same time.
	// +optional
	WaitForReady bool `json:"waitForReady,omitempty"`
}

// WorkloadRef identifies the target workload by kind and name.
// The workload must exist in the same namespace as the ManagedWorkload CR.
type WorkloadRef struct {
	// +kubebuilder:default=Deployment
	Kind TargetKind `json:"kind"`

	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// PredictionSpec configures the Holt-Winters forecasting engine.
type PredictionSpec struct {
	// Mode is how soon the forecast acts on what it has learned: eager acts
	// sooner, and on less regular patterns, and wakes the workload for
	// nothing more often; cautious acts later, only on patterns it forecasts
	// well, for workloads costly to keep awake for nothing. A wrong forecast
	// only wakes a workload early or keeps it up a while longer, never
	// costing availability, since a request still wakes it through the
	// doorman. They act at a confidence of 60, 75 and 85.
	// +kubebuilder:default=balanced
	// +optional
	Mode PredictionMode `json:"mode,omitempty"`

	// Confidence, when set, is the accuracy percentage (50-100) a season's
	// forecasts must reach to drive decisions, in place of Mode's. Accuracy
	// is 1 - WAPE: 75 means the forecast's total error over the window is
	// 25% of the demand in it. A season that falls 5 points below it stops
	// driving decisions until it earns it again. The minimum is 50 because
	// below that a forecast that is wrong more than it is right would wake
	// workloads and hold off pauses.
	// +kubebuilder:validation:Minimum=50
	// +kubebuilder:validation:Maximum=100
	// +optional
	Confidence int `json:"confidence,omitempty"`
}

// PredictionMode is how soon the forecast acts on what it has learned.
// +kubebuilder:validation:Enum=eager;balanced;cautious
type PredictionMode string

const (
	PredictionEager    PredictionMode = "eager"
	PredictionBalanced PredictionMode = "balanced"
	PredictionCautious PredictionMode = "cautious"
)

// IdlePolicySpec configures automatic pausing. The operator tracks when the
// workload was last active and pauses it once it has been inactive for
// IdleAfter. Any single activity source keeps the workload awake.
type IdlePolicySpec struct {
	// IdleAfter is how long the workload must go without any activity
	// before the operator pauses it, at least 1m.
	// +kubebuilder:default="1h"
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('1m')",message="must be a duration of at least 1m, such as 90m or 2h"
	// +optional
	IdleAfter *metav1.Duration `json:"idleAfter,omitempty"`

	// Activity configures what counts as activity, beyond the deploys and
	// activity annotations that always count.
	// +optional
	Activity *ActivitySpec `json:"activity,omitempty"`

	// AutoResume wakes a paused workload ahead of the demand the forecast
	// predicts.
	// +optional
	AutoResume bool `json:"autoResume,omitempty"`

	// PauseEarly pauses the workload before IdleAfter runs out, once it has
	// been idle for 20 minutes, when a confident forecast expects every
	// hour until IdleAfter would run out, and at least the hour under way
	// and the next, to be quiet, and a request would wake it once paused:
	// the doorman has a Ready pod, and would route every Service that
	// selects the workload's pods, on every port but those
	// napwake.dev/doorman-ignore-ports lists. A headless Service, or one
	// with a UDP or SCTP port, keeps it from pausing early. So a wrong
	// forecast costs a request a wake, never an answer.
	// +optional
	PauseEarly bool `json:"pauseEarly,omitempty"`
}

// ActivitySpec configures the activity sources for idle detection.
type ActivitySpec struct {
	// CPUThreshold is the CPU utilization, as a percentage of the workload's
	// CPU requests, above which the workload counts as active: from 0.1 to
	// 100, a whole number such as 10, or a decimal written as a string, such
	// as "0.5", to at most two places.
	// +kubebuilder:validation:XIntOrString
	// +kubebuilder:validation:Pattern=`^[0-9]{1,3}(\.[0-9]{1,2})?$`
	// +kubebuilder:validation:XValidation:rule="type(self) == string ? double(self) >= 0.1 && double(self) <= 100.0 : self >= 1 && self <= 100",message="must be a percentage from 0.1 to 100, such as 10 or \"0.5\""
	// +kubebuilder:default=10
	// +optional
	CPUThreshold *intstr.IntOrString `json:"cpuThreshold,omitempty"`

	// Prometheus queries that measure activity, such as an ingress request
	// rate. A result above zero counts as activity; an empty result or zero
	// doesn't. Requires the operator's --prometheus-url. At most 10.
	// +kubebuilder:validation:MaxItems=10
	// +optional
	Prometheus []PrometheusActivity `json:"prometheus,omitempty"`
}

// PrometheusActivity is a PromQL instant query used as an activity source.
type PrometheusActivity struct {
	// PromQL is the query. Example:
	// sum(rate(nginx_ingress_controller_requests{exported_namespace="staging",exported_service="api"}[5m]))
	// +kubebuilder:validation:MinLength=1
	PromQL string `json:"promQL"`
}

// CostTrackingSpec configures how this workload's cost is priced.
type CostTrackingSpec struct {
	// Rates are what you pay. Each one set is used in place of the list
	// price of the workload's nodes and of Napwake's default rates; one
	// left out is priced as if Rates weren't set.
	// +optional
	Rates *CostRates `json:"rates,omitempty"`
}

// CostRates are per-unit cost rates, in USD, none of them negative.
type CostRates struct {
	// CPUPerHour is the cost per vCPU-hour.
	// +kubebuilder:validation:XValidation:rule="!quantity(string(self)).isLessThan(quantity('0'))",message="must not be negative"
	// +optional
	CPUPerHour *resource.Quantity `json:"cpuPerHour,omitempty"`

	// MemoryPerHour is the cost per GiB-hour.
	// +kubebuilder:validation:XValidation:rule="!quantity(string(self)).isLessThan(quantity('0'))",message="must not be negative"
	// +optional
	MemoryPerHour *resource.Quantity `json:"memoryPerHour,omitempty"`

	// StoragePerMonth is the cost per GiB-month of PersistentVolumeClaim
	// storage.
	// +kubebuilder:validation:XValidation:rule="!quantity(string(self)).isLessThan(quantity('0'))",message="must not be negative"
	// +optional
	StoragePerMonth *resource.Quantity `json:"storagePerMonth,omitempty"`
}

// --- Status ---

// +kubebuilder:validation:Enum=Creating;Running;Idle;Pausing;Paused;Resuming
type WorkloadPhase string

const (
	PhaseCreating WorkloadPhase = "Creating"
	PhaseRunning  WorkloadPhase = "Running"
	PhaseIdle     WorkloadPhase = "Idle"
	PhasePausing  WorkloadPhase = "Pausing"
	PhasePaused   WorkloadPhase = "Paused"
	PhaseResuming WorkloadPhase = "Resuming"
)

// ManagedWorkloadStatus reflects the observed state of the workload.
type ManagedWorkloadStatus struct {
	// Phase is the current lifecycle phase of the workload.
	// +optional
	Phase WorkloadPhase `json:"phase,omitempty"`

	// Conditions provide detailed status information following the standard
	// Kubernetes condition pattern (Type, Status, Reason, Message).
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Pause records what pausing changed, from before the workload is scaled
	// to zero until it's running again.
	// +optional
	Pause *PauseStatus `json:"pause,omitempty"`

	// LearnedDependencies are the workloads Napwake found this one depends
	// on without a dependsOn: from addresses in its environment that name
	// their Services, and from requests it sent that woke them. They're held
	// awake and woken like dependsOn, less the ones the
	// napwake.dev/ignore-dependencies annotation names.
	// +optional
	LearnedDependencies *LearnedDependencies `json:"learnedDependencies,omitempty"`

	// LastScaledUp is the last time something other than Napwake scaled
	// the workload up while it was paused, which wakes it.
	// +optional
	LastScaledUp *ScaledUp `json:"lastScaledUp,omitempty"`

	// TurnedAway counts the requests the doorman turned away during the
	// workload's current pause, or its last one, with wake.mode Ask:
	// answered that it's asleep, and not woken. Many mean something
	// automated calls it. It's brought up to date about once a minute.
	// +optional
	TurnedAway *TurnedAway `json:"turnedAway,omitempty"`

	// Prediction reflects the current state of the forecasting engine.
	// +optional
	Prediction *PredictionStatus `json:"prediction,omitempty"`

	// Cost holds accumulated resource cost data for the current month.
	// +optional
	Cost *CostStatus `json:"cost,omitempty"`

	// Doorman lists the doorman ports routing this workload's Services while
	// it's paused. Empty while it's awake.
	// +listType=atomic
	// +optional
	Doorman []DoormanRoute `json:"doorman,omitempty"`

	// DoormanDraining lists doorman ports that routed this workload until
	// recently. No other workload is given one until its drain ends, even by
	// an operator that has just restarted or taken over leadership, since
	// clients that haven't yet seen the Service move back to the workload's
	// pods still send traffic to it.
	// +listType=atomic
	// +optional
	DoormanDraining []DrainingPort `json:"doormanDraining,omitempty"`

	// Activity tracks when the workload was last active, for idle detection.
	// +optional
	Activity *ActivityStatus `json:"activity,omitempty"`

	// IdleWait records the pauses made while the workload was in use, which
	// lengthen how long its idle clock waits, and what Napwake measured of
	// its CPU in use and idle, from which it suggests a CPU threshold.
	// +optional
	IdleWait *IdleWaitStatus `json:"idleWait,omitempty"`

	// DryRun is what dry-run has measured: what Napwake would have done
	// had it been allowed to pause the workload. Set only while spec.dryRun
	// is true.
	// +optional
	DryRun *DryRunStatus `json:"dryRun,omitempty"`

	// LastPauseRequest is the last napwake.dev/pause-requested value
	// Napwake acted on, so each request is acted on once, across restarts.
	// The PauseRequest condition says what came of it.
	// +optional
	LastPauseRequest string `json:"lastPauseRequest,omitempty"`

	// LastWakeRequest is the last napwake.dev/wake-requested value
	// Napwake acted on, so each request is acted on once, across restarts.
	// The WakeRequest condition says what came of it.
	// +optional
	LastWakeRequest string `json:"lastWakeRequest,omitempty"`

	// LastActedAt is when the target was last paused or woken: by
	// Napwake, or scaled up from zero outside it, such as with kubectl
	// scale or by a GitOps tool.
	// +optional
	LastActedAt *metav1.Time `json:"lastActedAt,omitempty"`

	// LastTransitionTime is when the workload last changed phases.
	// +optional
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`
}

// DoormanRoute maps one Service port to the doorman port that stands in for
// it while the workload is paused.
type DoormanRoute struct {
	// Service is the name of the Service in the workload's namespace.
	Service string `json:"service"`

	// PortName is the Service port's name; empty for an unnamed single port.
	// +optional
	PortName string `json:"portName,omitempty"`

	// DoormanPort is the doorman's listening port for this Service port.
	DoormanPort int32 `json:"doormanPort"`

	// Forwarded is true when the Service sits behind GKE container-native
	// load balancing, so its traffic reaches the doorman through a
	// forwarder in the workload's namespace rather than directly.
	// +optional
	Forwarded bool `json:"forwarded,omitempty"`
}

// DrainingPort is a doorman port no other workload may have before Until.
type DrainingPort struct {
	Port  int32       `json:"port"`
	Until metav1.Time `json:"until"`
}

// +kubebuilder:validation:Enum=created;woke;request;cpu;rollout;annotation;prometheus;unobserved;scaled-up
type ActivitySource string

const (
	ActivitySourceCreated ActivitySource = "created"
	ActivitySourceWoke    ActivitySource = "woke"
	// ActivitySourceRequest is a wake by a request the doorman held.
	ActivitySourceRequest    ActivitySource = "request"
	ActivitySourceCPU        ActivitySource = "cpu"
	ActivitySourceRollout    ActivitySource = "rollout"
	ActivitySourceAnnotation ActivitySource = "annotation"
	ActivitySourcePrometheus ActivitySource = "prometheus"
	// ActivitySourceUnobserved restarts the clock after the operator could
	// not watch the workload, so a gap in observation never causes a pause.
	ActivitySourceUnobserved ActivitySource = "unobserved"
	// ActivitySourceScaledUp is a paused workload scaled up by something
	// other than Napwake, such as a person or a GitOps tool.
	ActivitySourceScaledUp ActivitySource = "scaled-up"
)

// LearnedDependencies are dependencies Napwake found, and what from.
type LearnedDependencies struct {
	// From identifies what they were learned from, the pod template and the
	// ignore annotation, so they're learned again when either changes.
	From string `json:"from"`

	// At is when they were last learned. They're also learned again
	// hourly, for ConfigMaps changed since.
	At metav1.Time `json:"at"`

	// +listType=atomic
	// +optional
	Dependencies []LearnedDependency `json:"dependencies,omitempty"`
}

// LearnedDependency is one workload this one was found to depend on.
type LearnedDependency struct {
	Namespace string     `json:"namespace"`
	Kind      TargetKind `json:"kind"`
	Name      string     `json:"name"`

	// Source is how it was found: in the workload's environment, or from a
	// request it sent that woke the dependency.
	Source LearnedSource `json:"source"`

	// Via is the environment variable that holds its address, and Address
	// the address, as scheme://host:port only, for one found in the
	// environment.
	// +optional
	Via string `json:"via,omitempty"`
	// +optional
	Address string `json:"address,omitempty"`

	// LastSeen is when a request that woke the dependency last confirmed one
	// learned from a wake, written at most once a day. Held awake and woken
	// with the dependent while it's kept, the dependency isn't woken by its
	// requests, so one in steady use goes unconfirmed too: after 14 days it's
	// forgotten, and learned again from the next request that wakes it.
	// +optional
	LastSeen *metav1.Time `json:"lastSeen,omitempty"`
}

// +kubebuilder:validation:Enum=environment;wake
type LearnedSource string

const (
	LearnedFromEnvironment LearnedSource = "environment"
	LearnedFromWake        LearnedSource = "wake"
)

// TurnedAway is how many requests were turned away during one pause. A
// person's browser, shown the page that asks, isn't counted, nor are the
// probes and health checks the doorman knows.
type TurnedAway struct {
	// PausedAt is when the pause they were turned away during began.
	PausedAt metav1.Time `json:"pausedAt"`
	Requests int64       `json:"requests"`
}

// ScaledUp is a paused workload scaled up outside Napwake.
type ScaledUp struct {
	At metav1.Time `json:"at"`
	// By is the field manager that set the replicas, such as
	// argocd-controller or kubectl-client-side-apply, or unknown when none
	// is recorded, as for a scale through the scale subresource by kubectl
	// scale, an HPA or KEDA.
	By string `json:"by"`
	// GitOps is the GitOps tool behind By, such as Argo CD or Flux, which
	// will set the replicas again each time Napwake pauses the workload
	// until it's told to leave them.
	// +optional
	GitOps   string `json:"gitOps,omitempty"`
	Replicas int32  `json:"replicas"`
}

// ActivityStatus records the state of the activity clock.
type ActivityStatus struct {
	// LastActivityTime is the most recent activity from any source.
	LastActivityTime metav1.Time `json:"lastActivityTime"`

	// LastActivitySource is what produced LastActivityTime.
	LastActivitySource ActivitySource `json:"lastActivitySource"`

	// PauseAt is when the idle action will run if no further activity is
	// seen: LastActivityTime plus IdleAfter.
	// +optional
	PauseAt *metav1.Time `json:"pauseAt,omitempty"`

	// LastEvaluatedTime is when the operator last checked for activity.
	// +optional
	LastEvaluatedTime *metav1.Time `json:"lastEvaluatedTime,omitempty"`

	// TemplateHash fingerprints the target's pod template, so a deploy or
	// configuration change counts as activity while replica changes don't.
	// +optional
	TemplateHash string `json:"templateHash,omitempty"`

	// HeldByDependents are the awake workloads that use this one, as
	// namespace/name, while they keep it from pausing, as the
	// HeldByDependents condition says: at most 10, the first by name.
	// +optional
	HeldByDependents []string `json:"heldByDependents,omitempty"`

	// ForecastBusyFrom is the start of the hour the forecast expects demand
	// in, while it keeps the workload from pausing, as the IdleVetoed
	// condition says.
	// +optional
	ForecastBusyFrom *metav1.Time `json:"forecastBusyFrom,omitempty"`

	// ForecastQuietUntil is the end of the hours the forecast expected to
	// be quiet when it brought the workload's pause forward, with
	// idlePolicy.pauseEarly: set from that pause, or would-be pause in
	// dry-run, until it ends.
	// +optional
	ForecastQuietUntil *metav1.Time `json:"forecastQuietUntil,omitempty"`
}

// IdleWaitStatus records the pauses made while the workload was in use, and
// its CPU use idle.
type IdleWaitStatus struct {
	// WrongPauses counts the pauses in a row that something undid soon after
	// they began, within idleAfter or 15 minutes, whichever is shorter: a
	// request, a person's click, a wake request or an activity annotation.
	// The workload was in use, unseen. Each doubles how long its idle clock
	// waits without activity before a pause, up to idleAfter plus an hour. A
	// pause nothing undoes that soon sets it back to zero.
	// +optional
	WrongPauses int32 `json:"wrongPauses,omitempty"`

	// LastWrongPause is the latest of them.
	// +optional
	LastWrongPause *WrongPause `json:"lastWrongPause,omitempty"`

	// IdleCPU is the most CPU the workload used, as a percentage of its
	// requests, in the minutes before its pauses that nothing undid, over
	// the last week: what it uses idle. Unset until such a pause.
	// +optional
	IdleCPU string `json:"idleCPU,omitempty"`

	// IdleCPUAt is when IdleCPU was last measured.
	// +optional
	IdleCPUAt *metav1.Time `json:"idleCPUAt,omitempty"`
}

// WrongPause is a pause something undid soon after it began.
type WrongPause struct {
	// PausedAt is when the pause began.
	PausedAt metav1.Time `json:"pausedAt"`

	// After is how soon after the pause began it was undone.
	After metav1.Duration `json:"after"`

	// By is what undid it.
	By WakeCause `json:"by"`

	// From is where the request that undid it came from, as the doorman
	// recorded it.
	// +optional
	From string `json:"from,omitempty"`

	// InUseCPU is the most CPU the workload used, as a percentage of its
	// requests, through the stretch without activity that ran its clock out,
	// in which, as it turned out, it was in use. Unset when none was
	// measured.
	// +optional
	InUseCPU string `json:"inUseCPU,omitempty"`
}

// WakeCause is what woke a workload from a pause.
// +kubebuilder:validation:Enum=request;click;wakeRequest;annotation
type WakeCause string

const (
	WokenByRequest     WakeCause = "request"
	WokenByClick       WakeCause = "click"
	WokenByWakeRequest WakeCause = "wakeRequest"
	WokenByAnnotation  WakeCause = "annotation"
)

// DryRunStatus sums up the pauses Napwake would have made in dry-run. A
// would-be pause starts when the activity clock runs out and ends at the
// next activity, when a paused workload would have been woken.
type DryRunStatus struct {
	// Since is when dry-run started measuring the workload.
	Since metav1.Time `json:"since"`

	// Pauses is how many times the workload would have been paused,
	// including one under way.
	Pauses int32 `json:"pauses"`

	// Slept is how long the finished would-be pauses lasted in all. One
	// under way, which began at status.lastTransitionTime while the phase
	// is Idle, is added when it ends.
	Slept metav1.Duration `json:"slept"`

	// FreedCPUHours and FreedMemoryHours are the vCPU-hours and GiB-hours
	// the replicas requested during Slept: what the would-be pauses would
	// have freed. They're kept to a billionth of an hour, so short pauses
	// of small workloads add up.
	FreedCPUHours    resource.Quantity `json:"freedCPUHours"`
	FreedMemoryHours resource.Quantity `json:"freedMemoryHours"`

	// EstimatedFreed is the freed hours priced at the workload's cost
	// rates, to the cent, with the would-be pause under way counted up to
	// the last status write, at most 5 minutes ago. It's for display: the
	// hours are the record.
	EstimatedFreed string `json:"estimatedFreed"`

	// Resources is what the workload ran when the current would-be pause
	// began, which is what pausing it would free.
	// +optional
	Resources *ResourceSnapshot `json:"resources,omitempty"`

	// QueryActiveSincePause says a Prometheus activity query has shown
	// activity at every check since the would-be pause under way began, as
	// one can when it was requested. Until a check finds every query quiet,
	// a query showing activity doesn't end it, as it wouldn't wake the
	// workload from a real pause.
	// +optional
	QueryActiveSincePause bool `json:"queryActiveSincePause,omitempty"`
}

// ResourceSnapshot is what a workload runs, read at one moment, so it can be
// priced later without reading the target again.
type ResourceSnapshot struct {
	// Replicas is the replica count at the time of the snapshot.
	Replicas int32 `json:"replicas"`

	// CPUMillis and MemoryBytes are what one replica's pod requests,
	// sidecars injected when it was created included.
	CPUMillis   int64 `json:"cpuMillis"`
	MemoryBytes int64 `json:"memoryBytes"`

	// StorageBytes is the capacity of the PersistentVolumeClaims the
	// workload's pods mount, in all.
	StorageBytes int64 `json:"storageBytes"`

	// NodePools are the node pools the workload's pods ran in, and how
	// many ran in each, which what it saves is measured against. Unset
	// when no pod was on a node.
	// +optional
	// +listType=atomic
	NodePools []NodePoolPods `json:"nodePools,omitempty"`
}

// NodePoolPods is how many of a workload's pods ran in one node pool.
type NodePoolPods struct {
	// Name is the pool's name, from the first of its nodes' labels
	// karpenter.sh/nodepool, cloud.google.com/gke-nodepool,
	// eks.amazonaws.com/nodegroup and kubernetes.azure.com/agentpool, or
	// empty for nodes with none of them, which make up one pool.
	Name string `json:"name"`

	Pods int32 `json:"pods"`
}

// PauseStatus records what a pause changed, so it can be undone exactly. It's
// written before the workload is scaled to zero, so a pause interrupted at
// any point is finished or undone from it.
type PauseStatus struct {
	// PreviousReplicas is the replica count before pausing, used to
	// restore on resume.
	PreviousReplicas int32 `json:"previousReplicas"`

	// RoutedAt is when the doorman's EndpointSlices first stood in front of
	// the workload's Services for this pause, beside its pods, which still
	// served. It's scaled to zero a few seconds after, once proxies send new
	// connections to the doorman too, so none is refused as the pods stop.
	// Unset for a workload the doorman doesn't route.
	// +optional
	RoutedAt *metav1.Time `json:"routedAt,omitempty"`

	// PausedAt is when the workload was scaled to zero. Unset until then.
	// The pause waits from then for its pods to stop.
	// +optional
	PausedAt *metav1.Time `json:"pausedAt,omitempty"`

	// ScaledObject is the KEDA ScaledObject held at zero while the workload
	// is paused, so KEDA doesn't scale it up. Resuming releases it.
	// +optional
	ScaledObject string `json:"scaledObject,omitempty"`

	// ScaledObjectPausedReplicas is the ScaledObject's own
	// autoscaling.keda.sh/paused-replicas annotation from before the pause,
	// put back when it's released. Unset when it had none.
	// +optional
	ScaledObjectPausedReplicas *string `json:"scaledObjectPausedReplicas,omitempty"`

	// Resources is what the workload ran as it paused, which is what the
	// pause frees, and the node pools that's measured against for what it
	// saves.
	// +optional
	Resources *ResourceSnapshot `json:"resources,omitempty"`

	// ResumedAt is when the resume under way scaled the workload back up.
	// Scaled to zero after it, by something other than Napwake, the
	// workload is left off rather than scaled up again.
	// +optional
	ResumedAt *metav1.Time `json:"resumedAt,omitempty"`

	// FirstReadyAt is when the resume under way first saw one of the
	// workload's replicas Ready. The rest are waited for 2 minutes from
	// then, and the workload runs with those that are Ready by that time.
	// +optional
	FirstReadyAt *metav1.Time `json:"firstReadyAt,omitempty"`

	// WakeAnnotations are the activity annotations as the pause began. One
	// that has changed since wakes the workload whatever time it states, so
	// a clock behind the operator's can't lose a wake.
	// +optional
	WakeAnnotations *WakeAnnotations `json:"wakeAnnotations,omitempty"`

	// QueryActiveSincePause says a Prometheus activity query has shown
	// activity at every check since the pause began, as one can when the
	// pause was requested. Until a check finds every query quiet, a query
	// showing activity doesn't wake the workload, so it doesn't undo the
	// pause at once.
	// +optional
	QueryActiveSincePause bool `json:"queryActiveSincePause,omitempty"`

	// Requested says the pause was asked for, with the
	// napwake.dev/pause-requested annotation, rather than decided by the
	// idle clock or the forecast.
	// +optional
	Requested bool `json:"requested,omitempty"`

	// CPUBefore is the most CPU the workload used, as a percentage of its
	// requests, through the stretch without activity that ran its clock out,
	// and CPUJustBefore the most in its last minutes: within idleAfter or 15
	// minutes of the pause, whichever is shorter. A pause undone soon after
	// shows what it uses in use; one that holds, what it uses idle. Unset
	// when the clock measured none, as for a requested pause.
	// +optional
	CPUBefore string `json:"cpuBefore,omitempty"`
	// +optional
	CPUJustBefore string `json:"cpuJustBefore,omitempty"`
}

// WakeAnnotations are the napwake.dev/last-activity, last-request and
// active-until annotations, by name, on the ManagedWorkload and its target.
type WakeAnnotations struct {
	// +optional
	Workload map[string]string `json:"workload,omitempty"`
	// +optional
	Target map[string]string `json:"target,omitempty"`
}

// PredictionStatus reflects the current state of the Holt-Winters engine's
// dual-season lifecycle.
type PredictionStatus struct {
	// DailyPhase is the daily season's lifecycle phase
	// (Observing, Suggesting, or Active).
	DailyPhase string `json:"dailyPhase"`

	// DailyConfidence is the forecast's accuracy over the last 24 observed
	// hours, as a percentage.
	DailyConfidence int `json:"dailyConfidence"`

	// WeeklyPhase is the weekly season's lifecycle phase
	// (Observing, Suggesting, or Active).
	WeeklyPhase string `json:"weeklyPhase"`

	// WeeklyConfidence is the forecast's accuracy over the last 168
	// observed hours, a whole week of weekdays and weekend, as a percentage.
	WeeklyConfidence int `json:"weeklyConfidence"`

	// TimeZone is the IANA time zone the forecast counts hours of the day
	// and days of the week in, such as Europe/London: the operator's
	// --timezone.
	// +optional
	TimeZone string `json:"timeZone,omitempty"`
	// Threshold is the confidence the forecast acts at: spec.prediction's
	// confidence, or its mode's.
	// +optional
	Threshold int `json:"threshold,omitempty"`

	// Acting says the forecast drives decisions: it holds off a pause before
	// an hour it expects to be busy, and with autoResume, wakes the workload
	// ahead of one; with idlePolicy.pauseEarly, it brings a pause forward
	// before hours it expects to be quiet.
	Acting bool `json:"acting"`

	// StartsActing is when the forecast is expected to start acting, if the
	// coming days repeat the last week it saw. It's unset while the forecast
	// is acting, and when it isn't expected to start within four weeks.
	// +optional
	StartsActing *metav1.Time `json:"startsActing,omitempty"`

	// Reason says in a word why the forecast is or isn't acting: Acting,
	// Observing (it hasn't scored a day of its forecasts yet), EarningConfidence
	// (its forecasts aren't yet right enough), Relearning (demand broke from
	// what it had learned), or NotLearning (the workload is paused without
	// the doorman, so its demand can't be seen).
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message says the same in a sentence, and what the forecast waits for.
	// +optional
	Message string `json:"message,omitempty"`

	// History is what the forecast learned from the workload's CPU history
	// in Prometheus when Napwake began managing it.
	// +optional
	History *PredictionHistory `json:"history,omitempty"`

	// State is what the forecasting engine has learned, compressed and
	// encoded, so that it survives an operator restart. It is written with
	// each hourly observation. State that can't be read is discarded, with a
	// warning event, and the engine starts learning again.
	// +optional
	State string `json:"state,omitempty"`
}

// PredictionHistory is what the forecast learned from the workload's CPU
// history in Prometheus, up to four weeks of it, before it learned live.
type PredictionHistory struct {
	// Reason says in a word what came of reading it: Learned,
	// NoPrometheusURL, NoHistory, NoCPURequests or QueryFailed.
	Reason string `json:"reason"`

	// Hours is how many hours of history the forecast learned.
	// +optional
	Hours int `json:"hours,omitempty"`

	// From and To bound the history learned.
	// +optional
	From *metav1.Time `json:"from,omitempty"`
	// +optional
	To *metav1.Time `json:"to,omitempty"`

	// Message says what was learned, or why nothing was.
	// +optional
	Message string `json:"message,omitempty"`
}

// CostStatus is what the workload has cost, what pausing it has freed, and
// what that has saved, this calendar month in UTC.
//
// Everything is priced on what the workload's pods request, sidecars
// injected when they were created included, rather than on what they use:
// requests are what a pod reserves on a node, so they're what the workload
// costs in capacity, and what a cluster autoscaler can remove once it's
// paused.
//
// Freed is what pausing released on its nodes; saved is the share of that
// which provably came off the bill: what the cluster's nodes couldn't
// hold if the paused replicas came back now, which would take nodes that
// aren't there.
//
// The resource-hours are the record, kept to a billionth of an hour. The
// dollar figures are derived from them at the workload's current rates
// whenever they're brought up to date, and are rounded to the cent for
// display, so CostThisMonth plus FreedThisMonth is
// CostWithoutNapwakeThisMonth to within a cent.
type CostStatus struct {
	// Tracked is how much of the month the totals cover. It's less than
	// the time since the month began when the workload was created during
	// the month, or when the operator wasn't running.
	Tracked metav1.Duration `json:"tracked"`

	// LastAccumulatedAt is when the totals were last brought up to date:
	// at every phase change, so time counts in the phase it was spent in,
	// and every few minutes in between. A longer gap, such as while the
	// operator wasn't running, counts as two hours at most, since what the
	// workload did meanwhile isn't known.
	// +optional
	LastAccumulatedAt *metav1.Time `json:"lastAccumulatedAt,omitempty"`

	// AwakeCPUHours and AwakeMemoryHours are the vCPU-hours and GiB-hours
	// the workload's replicas requested while it was awake, in any phase
	// but Paused.
	AwakeCPUHours    resource.Quantity `json:"awakeCPUHours"`
	AwakeMemoryHours resource.Quantity `json:"awakeMemoryHours"`

	// StorageHours is the GiB-hours its claims provisioned, awake or
	// paused: a pause doesn't free storage.
	StorageHours resource.Quantity `json:"storageHours"`

	// PausedCPUHours and PausedMemoryHours are the vCPU-hours and GiB-hours
	// the replicas Napwake paused had requested, for as long as they were
	// paused: what pausing freed.
	PausedCPUHours    resource.Quantity `json:"pausedCPUHours"`
	PausedMemoryHours resource.Quantity `json:"pausedMemoryHours"`

	// SavedCPUHours and SavedMemoryHours are the workload's share of the
	// vCPU-hours and GiB-hours the cluster's nodes were short of holding
	// every replica Napwake paused in their node pools, measured every
	// 5 minutes: capacity that would have to be added for them, so a lower
	// bound on what came off the bill. At most the paused hours.
	SavedCPUHours    resource.Quantity `json:"savedCPUHours"`
	SavedMemoryHours resource.Quantity `json:"savedMemoryHours"`

	// CostThisMonth is the awake hours and the storage hours, priced.
	CostThisMonth string `json:"costThisMonth"`

	// FreedThisMonth is the paused hours, priced: what pausing freed on
	// its nodes, which is money only once nodes go.
	FreedThisMonth string `json:"freedThisMonth"`

	// SavedThisMonth is the saved hours, priced: what pausing took off
	// the bill, at least. "unknown" while SavingsUnknown says why it
	// can't be measured.
	SavedThisMonth string `json:"savedThisMonth"`

	// SavingsUnknown is why the saved hours can't be measured, such as
	// the operator not seeing the pods of every namespace; empty when
	// they're measured.
	// +optional
	SavingsUnknown string `json:"savingsUnknown,omitempty"`

	// CostWithoutNapwakeThisMonth is what the workload would have cost
	// this month had it never been paused: CostThisMonth plus
	// FreedThisMonth.
	CostWithoutNapwakeThisMonth string `json:"costWithoutNapwakeThisMonth"`

	// ProjectedMonthlyCost, ProjectedMonthlyFreed and
	// ProjectedMonthlySavings are CostThisMonth, FreedThisMonth and
	// SavedThisMonth carried from Tracked to the whole month at the same
	// rate. They're "pending" until a day has been tracked, since a
	// workload's pattern is daily.
	ProjectedMonthlyCost    string `json:"projectedMonthlyCost"`
	ProjectedMonthlyFreed   string `json:"projectedMonthlyFreed"`
	ProjectedMonthlySavings string `json:"projectedMonthlySavings"`

	// Running is what the workload ran when the totals were last brought
	// up to date while it was awake, which its awake time is priced on.
	// Its replicas and storage are read each time, and what a replica
	// requests at PricedAt.
	// +optional
	Running *ResourceSnapshot `json:"running,omitempty"`

	// PricedAt is when what a replica requests and ListRates were last
	// read, from the workload's pods and their nodes. They're read again
	// hourly while it's awake, and when its pod template changes.
	// +optional
	PricedAt *metav1.Time `json:"pricedAt,omitempty"`

	// PricedTemplateHash is the pod template's hash at PricedAt.
	// +optional
	PricedTemplateHash string `json:"pricedTemplateHash,omitempty"`

	// ListRates are the list rates of the nodes the workload's pods last
	// ran on, from their instance type and region, on-demand or spot as
	// ListPrice says, which its cost, what it frees and what it saves are
	// priced at unless costTracking.rates sets its own. Kept while it's
	// paused, so a pause is priced at where it ran. Unset when its nodes
	// aren't in Napwake's price table, and the default rates apply.
	// +optional
	ListRates *CostRates `json:"listRates,omitempty"`

	// ListPrice is which list prices ListRates are: OnDemand; Spot, for
	// pods on spot or preemptible nodes, priced at their type's spot
	// price; or SpotAndOnDemand, for pods on both. A spot node of a type
	// without a spot price is priced on-demand. Unset with ListRates.
	// +optional
	ListPrice ListPrice `json:"listPrice,omitempty"`

	// ResourceReduction is what the current pause freed on its nodes.
	// +optional
	ResourceReduction *ResourceReduction `json:"resourceReduction,omitempty"`
}

// Why the forecast is or isn't acting, in PredictionStatus.Reason.
const (
	ForecastActing            = "Acting"
	ForecastObserving         = "Observing"
	ForecastEarningConfidence = "EarningConfidence"
	ForecastRelearning        = "Relearning"
	ForecastNotLearning       = "NotLearning"
)

// ListPrice is which list prices a workload's nodes were priced at.
// +kubebuilder:validation:Enum=OnDemand;Spot;SpotAndOnDemand
type ListPrice string

const (
	ListPriceOnDemand        ListPrice = "OnDemand"
	ListPriceSpot            ListPrice = "Spot"
	ListPriceSpotAndOnDemand ListPrice = "SpotAndOnDemand"
)

// ResourceReduction tracks the workload-level resources freed by Napwake
// pauses. These resources are released on the
// node when pods are removed, but the node itself is only removed if a
// cluster autoscaler determines it is underutilized.
type ResourceReduction struct {
	// CPUMillis is the total CPU millicores freed by removing pods.
	CPUMillis int64 `json:"cpuMillis"`

	// MemoryBytes is the total memory bytes freed by removing pods.
	MemoryBytes int64 `json:"memoryBytes"`

	// Replicas is the number of pod replicas removed.
	Replicas int32 `json:"replicas"`
}
