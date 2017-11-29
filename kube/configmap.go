package kube

import (
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	v1 "k8s.io/client-go/pkg/api/v1"
)

type ConfigMapWatcher struct {
	client   kubernetes.Interface
	ns       string
	selector string
	opts     meta_v1.ListOptions
	quitCH   <-chan struct{}
}

func NewConfigMapWatcher(client kubernetes.Interface, ns, selector string, quitCH <-chan struct{}) (*ConfigMapWatcher, error) {
	return &ConfigMapWatcher{
		client:   client,
		ns:       ns,
		selector: selector,
		opts:     meta_v1.ListOptions{LabelSelector: selector},
		quitCH:   quitCH,
	}, nil
}

type Tuple struct {
	Key   string
	Value string
}

func NewTuple(k, v string) Tuple {
	return Tuple{Key: k, Value: v}
}

func (t Tuple) String() string {
	return fmt.Sprintf("%s: %s", t.Key, t.Value)
}

func (cmw *ConfigMapWatcher) WatchConfigMaps(resultCH chan<- map[string][]string) error {
	w := cmw.client.CoreV1().ConfigMaps(cmw.ns)
	opts := cmw.opts
	opts.Watch = true
	watcher, err := w.Watch(opts)
	if err != nil {
		return err
	}

	evCH := watcher.ResultChan()
	defer log.Infoln("Watcher: stopped to watch ConfigMaps")

	for {
		log.Debug("begin for")
		select {
		case ev := <-evCH:

			cmap, ok := ev.Object.(*v1.ConfigMap)
			if !ok {
				log.Errorf("Failed to cast event to ConfigMap %v %v", ev.Type, ev.Object)
				time.Sleep(10 * time.Second)
				continue // TODO(sszuecs) we might want to increase a metrics counter
			}
			results := make(map[string][]string)
			k := fmt.Sprintf("%s/%s", cmap.Namespace, cmap.Name)

			switch ev.Type {
			case watch.Deleted:
				log.Infof("Watcher: ConfigMap event %s: %s", ev.Type, cmap.Name)
				results[k] = []string{}
			case watch.Error:
				log.Errorf("Watcher: ConfigMap event %s: %s - ignore", ev.Type, cmap.Name)
				time.Sleep(10 * time.Second)
				continue
			case watch.Added:
				fallthrough
			case watch.Modified:
				results[k] = []string{}
				log.Infof("Watcher: ConfigMap event %s: %s", ev.Type, cmap.Name)
				for _, v := range cmap.Data {
					results[k] = append(results[k], v)
				}
			}
			log.Debugf("Watcher: got tuples: %v", results)
			resultCH <- results
		case <-cmw.quitCH:
			watcher.Stop()
			return nil
		}
	}
}

func (cmw *ConfigMapWatcher) ListConfigMaps() (map[string][]string, error) {
	res := make(map[string][]string)
	cmaps, err := cmw.listConfigMaps()
	if err != nil {
		return nil, err
	}

	for _, cmap := range cmaps.Items {
		k := fmt.Sprintf("%s/%s", cmap.Namespace, cmap.Name)
		for _, v := range cmap.Data {
			res[k] = append(res[k], v)
		}
	}
	return res, nil
}

func (cmw *ConfigMapWatcher) listConfigMaps() (*v1.ConfigMapList, error) {
	return cmw.client.CoreV1().ConfigMaps(cmw.ns).List(cmw.opts)
}
