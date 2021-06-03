// Copyright Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/rest"

	"istio.io/client-go/pkg/apis/networking/v1beta1"
	"istio.io/client-go/pkg/clientset/versioned"
	versionedclient "istio.io/client-go/pkg/clientset/versioned"
)

type Watcher struct {
	istioClient          *versioned.Clientset
	k8sClient            *kubernetes.Clientset
	namespace            string
	Watch                watch.Interface
	requiredTerminations sync.WaitGroup
	sdFileName           string
}

func NewWatcher(restConfig *rest.Config) *Watcher {
	// istio client
	ic, err := versionedclient.NewForConfig(restConfig)
	if err != nil {
		log.Fatalf("Failed to create istio client: %s", err)
	}

	// k8s client
	k8sClientSet, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		log.Fatalf("Failed to create k8s client: %s", err)

	}
	namespace := "" // get workload from all namespaces
	watchWLE, err := ic.NetworkingV1beta1().WorkloadEntries(namespace).Watch(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Fatalf("Failed to get Workload Entry watch: %v", err)
	}
	w := &Watcher{
		istioClient: ic,
		k8sClient:   k8sClientSet,
		namespace:   namespace,
		Watch:       watchWLE,
		sdFileName:  "staticConfigurations.json",
	}
	log.Println("workload entry watcher created")
	return w
}

// Start the workload entry watcher. It could be stopped with keyboard interrupt
func (w *Watcher) Start(stop <-chan struct{}) {
	if w.namespace == "" {
		var err error
		w.namespace, err = discoverPromNamespace(w.k8sClient)
		if err != nil {
			log.Fatalf("Failed to find prometheus deployment namespace: %v\n", err)
		}
	}

	go func() {
		w.requiredTerminations.Add(1)
		for event := range w.Watch.ResultChan() {
			// get the static configurations
			fileSDConfig, err := w.getOrCreatePromSDConfigMap(w.k8sClient)
			if err != nil {
				log.Fatalf("get or create config map failed: %v\n", err)
			}
			var staticConfigurations []map[string][]string
			if err := json.Unmarshal([]byte(fileSDConfig.Data[w.sdFileName]), &staticConfigurations); err != nil {
				log.Println("static configuration json generation failed")
			}

			staticConfigurations = dedupConfig(staticConfigurations)

			// handle events from the workload entries watch
			wle, ok := event.Object.(*v1beta1.WorkloadEntry)
			if !ok {
				log.Print("unexpected type")
			}
			switch event.Type {
			case watch.Deleted:
				log.Printf("handle deleted workload %s", wle.Spec.Address)
				toDelete := 0
			outer:
				for i, target := range staticConfigurations {
					for _, ip := range target["targets"] {
						if ip == wle.Spec.Address {
							toDelete = i
							break outer
						}
					}
				}
				staticConfigurations = append(staticConfigurations[:toDelete], staticConfigurations[toDelete+1:]...)
				log.Printf("Deleted VM workload %s\n", wle.ObjectMeta.Name)
			default: // add or update
				newTargetAddr := fmt.Sprintf("%s:15020", wle.Spec.Address)

				// Remove duplicates from the node IPs.
				existsDupEP := isDuplicate(staticConfigurations, newTargetAddr)
				if !existsDupEP {
					log.Printf("handle update workload %s", wle.Spec.Address)
					newTarget := make(map[string][]string)
					newTarget["targets"] = append(newTarget["targets"], newTargetAddr)
					staticConfigurations = append(staticConfigurations, newTarget)
					log.Printf("Registered VM workload %s \n", wle.ObjectMeta.Name)
					break
				}
				log.Printf("VM workload %s exists\n", wle.ObjectMeta.Name)
			}

			// assign the updated static configurations to the config map
			marshaledString, err := json.Marshal(staticConfigurations)
			if err != nil {
				log.Printf("update static configuration json failed: %v", err)
			}
			fileSDConfig.Data[w.sdFileName] = string(marshaledString)
			if err := updatePromSDConfigMap(w.k8sClient, fileSDConfig, w.namespace); err != nil {
				log.Printf("update config map failed: %v\n", err)
			}
		}
		w.requiredTerminations.Done()
	}()
	w.waitForShutdown(stop)
}

func (w *Watcher) waitForShutdown(stop <-chan struct{}) {
	go func() {
		<-stop
		w.Watch.Stop()
		w.requiredTerminations.Wait()
	}()
}

// get or create ConfigMap from a namespace with Prometheus deployment
func (w *Watcher) getOrCreatePromSDConfigMap(client *kubernetes.Clientset) (*v1.ConfigMap, error) {
	configMap, err := client.CoreV1().ConfigMaps(w.namespace).
		Get(context.TODO(), "file-sd-config", metav1.GetOptions{})
	if err == nil {
		// config map exists, return directly
		return configMap, nil
	}
	cfg := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "file-sd-config",
		},
		Data: make(map[string]string),
	}
	cfg.Data[w.sdFileName] = ""
	if configMap, err = client.CoreV1().ConfigMaps(w.namespace).Create(context.TODO(), cfg,
		metav1.CreateOptions{}); err != nil {
		return nil, err
	}
	return configMap, nil
}

func discoverPromNamespace(client *kubernetes.Clientset) (string, error) {
	label := "prometheus"
	labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app": label}}
	listOptions := metav1.ListOptions{
		LabelSelector: labels.Set(labelSelector.MatchLabels).String(),
	}

	podsList, err := client.CoreV1().Pods("").List(context.TODO(), listOptions)
	if err != nil {
		return "", err
	}

	promNamespace := podsList.Items[0].Namespace
	log.Printf("discover prometheus deployment in namespace %s\n", promNamespace)
	return promNamespace, nil
}

// WaitSignal awaits for SIGINT or SIGTERM and closes the channel
func (w *Watcher) WaitSignal(stop chan struct{}) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	close(stop)
}

func updatePromSDConfigMap(client *kubernetes.Clientset, fileSDConfig *v1.ConfigMap, ns string) error {
	// Write the update config map back to cluster
	if _, err := client.CoreV1().ConfigMaps(ns).Update(context.TODO(), fileSDConfig,
		metav1.UpdateOptions{}); err != nil {
		return err
	}
	return nil
}

func isDuplicate(existing []map[string][]string, newTarget string) bool {
	for _, target := range existing {
		for _, ip := range target["targets"] {
			if ip == newTarget {
				return true
			}
		}
	}
	return false
}

func dedupConfig(values []map[string][]string) []map[string][]string {
	set := make(map[string]bool)
	var config []map[string][]string

	for _, target := range values {
		var flag bool
		for _, ip := range target["targets"] {
			if _, v := set[ip]; !v {
				set[ip] = true
				continue
			}
			flag = true
		}
		if !flag {
			config = append(config, target)
		}
	}
	return config
}
