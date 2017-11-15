package kube

import (
	"time"

	log "github.com/sirupsen/logrus"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	v1 "k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/rest"
)

type ConfigMapWatcher struct {
	client   kubernetes.Interface
	ns       string
	selector string
	opts     meta_v1.ListOptions
	quitCH   <-chan struct{}
}

func NewConfigMapWatcher(config *rest.Config, ns, selector string, quitCH <-chan struct{}) (*ConfigMapWatcher, error) {
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	log.Infof("Connected to cluster at %s", config.Host) // TODO(sszuecs): drop this

	return &ConfigMapWatcher{
		client:   client,
		ns:       ns,
		selector: selector,
		opts:     meta_v1.ListOptions{LabelSelector: selector},
		quitCH:   quitCH,
	}, nil
}

func (cmw *ConfigMapWatcher) WatchConfigMaps(resultCH chan<- string) error {
	w := cmw.client.CoreV1().ConfigMaps(cmw.ns)
	watcher, err := w.Watch(cmw.opts)
	if err != nil {
		return err
	}

	evCH := watcher.ResultChan()
	for {
		select {
		case ev := <-evCH:
			log.Debugf("ConfigMap event %s", ev.Type)
			cmap := ev.Object.(*v1.ConfigMap)
			log.Debugf("ConfigMap event %s: %s", ev.Type, cmap.Name)
			for _, v := range cmap.Data {
				log.Debugf("iterate through data: %s", v)
				resultCH <- v
				log.Debugln("pushed data down the channel")
			}
		case <-cmw.quitCH:
			log.Infoln("stop watching event")
			watcher.Stop()
			return nil
		case <-time.After(2 * time.Second):
			log.Debugln("watch config map...")
		}
	}
}

func (cmw *ConfigMapWatcher) ListConfigMaps() ([]string, error) {
	res := make([]string, 0)
	cmaps, err := cmw.listConfigMaps()
	if err != nil {
		return nil, err
	}

	for _, cmap := range cmaps.Items {
		for _, v := range cmap.Data {
			res = append(res, v)
		}
	}
	return res, nil
}

func (cmw *ConfigMapWatcher) listConfigMaps() (*v1.ConfigMapList, error) {
	return cmw.client.CoreV1().ConfigMaps(cmw.ns).List(cmw.opts)
}
