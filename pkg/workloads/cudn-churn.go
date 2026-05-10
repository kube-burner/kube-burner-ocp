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

package workloads

import (
	"context"
	"fmt"
	"math"
	"time"

	log "github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var cudnGVR = schema.GroupVersionResource{
	Group:    "k8s.ovn.org",
	Version:  "v1",
	Resource: "clusteruserdefinednetworks",
}

type cudnChurnDeleter struct {
	clientSet     kubernetes.Interface
	dynamicClient dynamic.Interface
	cudnIndices   []int
	nsPerCudn     int
}

func newCudnChurnDeleter(clientSet kubernetes.Interface, dynamicClient dynamic.Interface, totalNamespaces, nsPerCudn, churnPercent int) *cudnChurnDeleter {
	numCudns := totalNamespaces / nsPerCudn
	numToChurn := int(math.Ceil(float64(churnPercent) * float64(numCudns) / 100.0))
	churnStartIdx := numCudns - numToChurn
	cudnIndices := make([]int, numToChurn)
	for i := range numToChurn {
		cudnIndices[i] = churnStartIdx + i
	}
	return &cudnChurnDeleter{
		clientSet:     clientSet,
		dynamicClient: dynamicClient,
		cudnIndices:   cudnIndices,
		nsPerCudn:     nsPerCudn,
	}
}

func (d *cudnChurnDeleter) deleteAndWait() error {
	ctx := context.Background()
	if err := d.deleteNamespaces(ctx); err != nil {
		return err
	}
	if err := d.deleteCudns(ctx); err != nil {
		return err
	}
	return d.waitForNamespaces(ctx)
}

func (d *cudnChurnDeleter) deleteNamespaces(ctx context.Context) error {
	log.Info("Deleting namespaces for churned CUDNs (not waiting for completion)")
	for _, cudnIdx := range d.cudnIndices {
		for j := 0; j < d.nsPerCudn; j++ {
			nsName := fmt.Sprintf("cudn-density-%d", cudnIdx*d.nsPerCudn+j)
			err := d.clientSet.CoreV1().Namespaces().Delete(ctx, nsName, metav1.DeleteOptions{})
			if err != nil && !errors.IsNotFound(err) {
				return fmt.Errorf("failed to delete namespace %s: %v", nsName, err)
			}
			log.Debugf("Deleted namespace %s", nsName)
		}
	}
	return nil
}

func (d *cudnChurnDeleter) deleteCudns(ctx context.Context) error {
	log.Info("Deleting churned CUDNs (waiting for completion)")
	for _, cudnIdx := range d.cudnIndices {
		cudnName := fmt.Sprintf("cudn-%d", cudnIdx)
		err := d.dynamicClient.Resource(cudnGVR).Delete(ctx, cudnName, metav1.DeleteOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete CUDN %s: %v", cudnName, err)
		}
		log.Debugf("Deleted CUDN %s", cudnName)
	}

	pollCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return wait.PollUntilContextCancel(pollCtx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		for _, cudnIdx := range d.cudnIndices {
			cudnName := fmt.Sprintf("cudn-%d", cudnIdx)
			_, err := d.dynamicClient.Resource(cudnGVR).Get(ctx, cudnName, metav1.GetOptions{})
			if err == nil {
				log.Debugf("Waiting for CUDN %s to be deleted", cudnName)
				return false, nil
			}
			if !errors.IsNotFound(err) {
				return false, fmt.Errorf("error checking CUDN %s: %v", cudnName, err)
			}
		}
		log.Info("All churned CUDNs deleted")
		return true, nil
	})
}

func (d *cudnChurnDeleter) waitForNamespaces(ctx context.Context) error {
	log.Info("Waiting for namespaces to finish terminating")
	pollCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return wait.PollUntilContextCancel(pollCtx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		for _, cudnIdx := range d.cudnIndices {
			for j := 0; j < d.nsPerCudn; j++ {
				nsName := fmt.Sprintf("cudn-density-%d", cudnIdx*d.nsPerCudn+j)
				_, err := d.clientSet.CoreV1().Namespaces().Get(ctx, nsName, metav1.GetOptions{})
				if err == nil {
					log.Debugf("Waiting for namespace %s to be deleted", nsName)
					return false, nil
				}
				if !errors.IsNotFound(err) {
					return false, fmt.Errorf("error checking namespace %s: %v", nsName, err)
				}
			}
		}
		log.Info("All churned namespaces deleted")
		return true, nil
	})
}
