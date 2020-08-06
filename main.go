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
	"k8s.io/client-go/rest"
	"log"
)

func main() {
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Println("get in cluster configuration failed")
		panic(err.Error())
	}
	// Create the stop channel for all of the servers.
	stop := make(chan struct{})

	// start the workload entry watcher
	log.Println("starting new watcher for workload entries")
	watcher := NewWatcher(config)
	watcher.Start(stop)

	log.Println("waiting to be stopped")
	watcher.WaitSignal(stop)
}
