// Copyright 2020 The Kube-burner Authors.
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

package burner

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/cloud-bulldozer/kube-burner/log"
	"github.com/cloud-bulldozer/kube-burner/pkg/config"
	"github.com/cloud-bulldozer/kube-burner/pkg/util"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	jobName      = "JobName"
	replica      = "Replica"
	jobIteration = "Iteration"
	jobUUID      = "UUID"
)

func setupCreateJob(jobConfig config.Job) Executor {
	log.Infof("Preparing create job: %s", jobConfig.Name)
	var empty interface{}
	selector := util.NewSelector()
	selector.Configure("", fmt.Sprintf("kube-burner-job=%s", jobConfig.Name), "")
	ex := Executor{
		selector: selector,
	}
	for _, o := range jobConfig.Objects {
		if o.Replicas < 1 {
			log.Warnf("Object template %s has replicas %d < 1, skipping", o.ObjectTemplate, o.Replicas)
			continue
		}
		log.Debugf("Processing template: %s", o.ObjectTemplate)
		f, err := os.Open(o.ObjectTemplate)
		if err != nil {
			log.Fatalf("Error getting gvr: %s", err)
		}
		t, err := ioutil.ReadAll(f)
		if err != nil {
			log.Fatalf("Error reading template %s: %s", o.ObjectTemplate, err)
		}
		// Deserialize YAML
		uns := &unstructured.Unstructured{}
		renderedObj := renderTemplate(t, empty)
		_, gvk := yamlToUnstructured(renderedObj, uns)
		gvr, _ := meta.UnsafeGuessKindToResource(*gvk)
		obj := object{
			gvr:          gvr,
			objectSpec:   t,
			replicas:     o.Replicas,
			unstructured: uns,
			inputVars:    o.InputVars,
		}
		log.Infof("Job %s: %d iterations with %d %s replicas", jobConfig.Name, jobConfig.JobIterations, obj.replicas, gvk.Kind)
		ex.objects = append(ex.objects, obj)
	}
	return ex
}

// RunCreateJob executes a creation job
func (ex *Executor) RunCreateJob() {
	log.Infof("Triggering job: %s", ex.Config.Name)
	ex.Start = time.Now().UTC()
	nsLabels := map[string]string{
		"kube-burner-job":  ex.Config.Name,
		"kube-burner-uuid": ex.uuid,
	}
	var wg sync.WaitGroup
	var ns string
	var err error
	RestConfig, err = config.GetRestConfig(ex.Config.QPS, ex.Config.Burst)
	if err != nil {
		log.Fatalf("Error creating restConfig for kube-burner: %s", err)
	}
	ClientSet = kubernetes.NewForConfigOrDie(RestConfig)
	log.Infof("QPS: %v", RestConfig.QPS)
	log.Infof("Burst: %v", RestConfig.Burst)
	dynamicClient = dynamic.NewForConfigOrDie(RestConfig)
	if !ex.Config.NamespacedIterations {
		ns = ex.Config.Namespace
		createNamespace(ClientSet, ns, nsLabels)
	}
	for i := 1; i <= ex.Config.JobIterations; i++ {
		if ex.Config.NamespacedIterations {
			ns = fmt.Sprintf("%s-%d", ex.Config.Namespace, i)
			createNamespace(ClientSet, fmt.Sprintf("%s-%d", ex.Config.Namespace, i), nsLabels)
		}
		for objectIndex, obj := range ex.objects {
			wg.Add(1)
			go ex.replicaHandler(objectIndex, obj, ns, i, &wg)
		}
		// Wait for all replicaHandlers to finish before move forward to the next interation
		wg.Wait()
		if ex.Config.PodWait {
			ex.waitForObjects(ns)
		}
		if ex.Config.JobIterationDelay > 0 {
			log.Infof("Sleeping for %v", ex.Config.JobIterationDelay)
			time.Sleep(ex.Config.JobIterationDelay)
		}
	}
	if ex.Config.WaitWhenFinished && !ex.Config.PodWait {
		wg.Wait()
		for i := 1; i <= ex.Config.JobIterations; i++ {
			if ex.Config.NamespacedIterations {
				ns = fmt.Sprintf("%s-%d", ex.Config.Namespace, i)
			}
			ex.waitForObjects(ns)
			if !ex.Config.NamespacedIterations {
				break
			}
		}
	}
	ex.End = time.Now().UTC()
}

func (ex *Executor) replicaHandler(objectIndex int, obj object, ns string, iteration int, wg *sync.WaitGroup) {
	defer wg.Done()
	labels := map[string]string{
		"kube-burner-uuid":  ex.uuid,
		"kube-burner-job":   ex.Config.Name,
		"kube-burner-index": strconv.Itoa(objectIndex),
	}
	tData := map[string]interface{}{
		jobName:      ex.Config.Name,
		jobIteration: iteration,
		jobUUID:      ex.uuid,
	}
	for k, v := range obj.inputVars {
		tData[k] = v
	}
	for r := 1; r <= obj.replicas; r++ {
		newObject := &unstructured.Unstructured{}
		tData[replica] = r
		renderedObj := renderTemplate(obj.objectSpec, tData)
		// Re-decode rendered object
		yamlToUnstructured(renderedObj, newObject)
		for k, v := range newObject.GetLabels() {
			labels[k] = v
		}
		newObject.SetLabels(labels)
		wg.Add(1)
		go func() {
			// We are using the same wait group for this inner goroutine, maybe we should consider using a new one
			defer wg.Done()
			ex.limiter.Wait(context.TODO())
			_, err := dynamicClient.Resource(obj.gvr).Namespace(ns).Create(context.TODO(), newObject, metav1.CreateOptions{})
			if errors.IsAlreadyExists(err) {
				log.Errorf("Object %s in namespace %s already exists", newObject.GetName(), ns)
			} else if err != nil {
				log.Errorf("Error creating object: %s", err)
			} else {
				log.Infof("Created %s %s in namespace %s", newObject.GetKind(), newObject.GetName(), ns)
			}
		}()
	}
}