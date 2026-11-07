// Copyright 2025 The Kube-burner Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package measurements

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	rav1 "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/routeadvertisements/v1"
	raclient "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/routeadvertisements/v1/apis/clientset/versioned"
	rainformers "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/routeadvertisements/v1/apis/informers/externalversions"
	udnclient "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/userdefinednetwork/v1/apis/clientset/versioned/typed/userdefinednetwork/v1"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

const raAcceptedConditionType = "Accepted"

// raObserver records RouteAdvertisement creation and acceptance independently
// of the import and export connectivity checks.
type raObserver struct {
	cudnClient            udnclient.ClusterUserDefinedNetworkInterface
	metrics               *sync.Map
	metricContext         raMetricContext
	verifiedAcceptedCount uint64
	factory               rainformers.SharedInformerFactory
	stopCh                chan struct{}
	stopOnce              sync.Once
}

// start registers an informer for RouteAdvertisement resource creation/update events.
// This is always needed to measure RA accepted latency regardless of the ra-connection-check mode.
func (o *raObserver) start(client raclient.Interface, cudnClient udnclient.ClusterUserDefinedNetworkInterface) {
	o.cudnClient = cudnClient
	o.stopCh = make(chan struct{})
	o.factory = rainformers.NewSharedInformerFactory(client, time.Minute)
	raInformer := o.factory.K8s().V1().RouteAdvertisements().Informer()
	raInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    o.handleAdd,
		UpdateFunc: o.handleUpdate,
	})
	o.factory.Start(o.stopCh)
	o.factory.WaitForCacheSync(o.stopCh)
}

// stop joins this observer's informer independently of other measurements.
func (o *raObserver) stop() {
	if o.factory == nil {
		return
	}
	o.stopOnce.Do(func() {
		close(o.stopCh)
		o.factory.Shutdown()
	})
}

func (o *raObserver) acceptedCount() uint64 {
	return atomic.LoadUint64(&o.verifiedAcceptedCount)
}

// handleAdd records the RouteAdvertisement name, creation time, and acceptance.
// Assumes one RA maps to exactly one CUDN.
func (o *raObserver) handleAdd(obj any) {
	ra := obj.(*rav1.RouteAdvertisements)
	if len(ra.Spec.NetworkSelectors) == 0 {
		log.Errorf("No networkSelectors found")
		return
	}
	networkSelector := ra.Spec.NetworkSelectors[0].ClusterUserDefinedNetworkSelector
	if networkSelector == nil || networkSelector.NetworkSelector.MatchLabels == nil {
		log.Errorf("No labels found in networkSelector")
		return
	}

	// Preserve the existing matchLabels-only selection of the first network.
	selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: networkSelector.NetworkSelector.MatchLabels,
	})
	if err != nil {
		log.Error(err)
		return
	}
	listOptions := metav1.ListOptions{}
	listOptions.LabelSelector = selector.String()
	udns, err := o.cudnClient.List(context.TODO(), listOptions)
	if err != nil {
		log.Error(err)
		return
	}
	if len(udns.Items) == 0 {
		log.Errorf("No CUDN found for RA selector")
		return
	}
	if ra.CreationTimestamp.IsZero() {
		log.Errorf("RA %s: missing creationTimestamp", ra.Name)
		return
	}
	cudn := udns.Items[0].Name
	raName := ra.Name
	t := ra.CreationTimestamp.Time.UTC()
	acceptedLatency := float64(-1)
	if transitionTime, ok := getRAAcceptedTransitionTime(ra); ok {
		acceptedLatency = float64(transitionTime.Sub(t.UTC()).Milliseconds())
		log.Debugf("RA %s already has Accepted=True, acceptedLatency: %vms", raName, acceptedLatency)
	}
	log.Debugf("RA %s discovered at: %v created at: %v cudn: %s", raName, time.Now().UTC(), t.UTC(), cudn)
	_, loaded := o.metrics.LoadOrStore(raName, raMetric{
		Name:            raName,
		Timestamp:       t.UTC(),
		cudn:            cudn,
		MetricName:      raLatencyMeasurement,
		UUID:            o.metricContext.uuid,
		Metadata:        o.metricContext.metadata,
		JobName:         o.metricContext.jobName,
		Scenario:        "ExportRoutes",
		AcceptedLatency: acceptedLatency,
	})
	if !loaded && acceptedLatency >= 0 {
		atomic.AddUint64(&o.verifiedAcceptedCount, 1)
	}
}

// handleUpdate records Accepted latency the first time the RA reaches Accepted=True.
func (o *raObserver) handleUpdate(_, newObj any) {
	ra := newObj.(*rav1.RouteAdvertisements)
	transitionTime, accepted := getRAAcceptedTransitionTime(ra)
	if !accepted {
		return
	}
	raName := ra.Name
	val, ok := o.metrics.Load(raName)
	if !ok {
		return
	}
	m := val.(raMetric)
	if m.AcceptedLatency >= 0 {
		return // Already recorded
	}
	m.AcceptedLatency = float64(transitionTime.Sub(m.Timestamp).Milliseconds())
	o.metrics.Store(raName, m)
	atomic.AddUint64(&o.verifiedAcceptedCount, 1)
	log.Debugf("RA %s Accepted after %vms", raName, m.AcceptedLatency)
}

// getRAAcceptedTransitionTime returns the lastTransitionTime of the Accepted=True
// condition, and true if found.
func getRAAcceptedTransitionTime(ra *rav1.RouteAdvertisements) (time.Time, bool) {
	for _, condition := range ra.Status.Conditions {
		if condition.Type == raAcceptedConditionType && condition.Status == metav1.ConditionTrue {
			if !condition.LastTransitionTime.IsZero() {
				return condition.LastTransitionTime.Time.UTC(), true
			}
			log.Warnf("RA %s: Accepted=True but missing/invalid lastTransitionTime, using current time", ra.GetName())
			return time.Now().UTC(), true
		}
	}
	return time.Time{}, false
}
