package main

import (
	"fmt"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/szuecs/kube-static-egress-controller/kube"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("%s <clusterendpoint>\n", os.Args[0])
		os.Exit(1)
	}
	log.SetLevel(log.DebugLevel)

	kubeConfig := fmt.Sprintf("%s/.kube/config", os.Getenv("HOME"))
	kubeMaster := os.Args[1]
	config, err := clientcmd.BuildConfigFromFlags(kubeMaster, kubeConfig)
	if err != nil {
		log.Fatalf("Failed to create config: %v", err)
	}

	quitCH := make(chan struct{})

	watcher, err := kube.NewConfigMapWatcher(config, "", "egress=static", quitCH)
	if err != nil {
		log.Fatalf("Failed to create ConfigMapWatcher: %v", err)
	}

	list, err := watcher.ListConfigMaps()
	if err != nil {
		log.Fatalf("Failed to list ConfigMaps: %v", err)
	}
	log.Infof("Bootstrap list: %v", list)

	watchCH := make(chan string, len(list)+2)
	for _, s := range list {
		log.Debugf("next item: %s", s)
		watchCH <- s
		log.Debugf("done watchCH <- %s", s)
	}

	// watcher
	go func(ch chan<- string) {
		log.Debugln("enter watcher")
		err = watcher.WatchConfigMaps(ch)
		if err != nil {
			log.Fatalf("Failed to enter ConfigMap watcher: %v", err)
		}
		log.Infoln("quit watcher")
	}(watchCH)

	// merger
	cfCH := make(chan []string)
	go func(ch <-chan string, cfCH chan<- []string, quitCH <-chan struct{}) {
		log.Debugln("enter merger")
		result := make(map[string]bool, 0)
		for {
			select {
			case s := <-ch:
				result[s] = true
				log.Debugf("append: %s to %v", s, result)
			case <-time.After(5 * time.Second):
				log.Infof("Current state: %v", result)
				res := make([]string, 0)
				for k := range result {
					res = append(res, k)
				}
				cfCH <- res
			case <-quitCH:
				break
			}
		}
		log.Infoln("quit merger")
	}(watchCH, cfCH, quitCH)

	// CF
	go func(cfCH <-chan []string, quitCH <-chan struct{}) {
		for {
			select {
			case a := <-cfCH:
				log.Infof("Got: %v", a)
			case <-quitCH:
				break
			}
		}
	}(cfCH, quitCH)

	// quit
	select {
	case <-time.After(30 * time.Second):
		quitCH <- struct{}{}
	}
	log.Infoln("shutdown")
}
