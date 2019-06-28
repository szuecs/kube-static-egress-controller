package kube

import (
	"context"
	"net"

	log "github.com/sirupsen/logrus"
	"github.com/szuecs/kube-static-egress-controller/provider"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type ConfigMapWatcher struct {
	client    kubernetes.Interface
	namespace string
	selector  fields.Selector
	configs   chan<- provider.EgressConfig
}

func NewConfigMapWatcher(client kubernetes.Interface, namespace, selectorStr string, configs chan<- provider.EgressConfig) (*ConfigMapWatcher, error) {
	selector, err := fields.ParseSelector(selectorStr)
	if err != nil {
		return nil, err
	}

	return &ConfigMapWatcher{
		client:    client,
		namespace: namespace,
		selector:  selector,
		configs:   configs,
	}, nil
}

func (c *ConfigMapWatcher) Run(ctx context.Context) {
	informer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				options.LabelSelector = c.selector.String()
				return c.client.CoreV1().ConfigMaps(c.namespace).List(options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				options.LabelSelector = c.selector.String()
				return c.client.CoreV1().ConfigMaps(c.namespace).Watch(options)
			},
		},
		&v1.ConfigMap{},
		0, // skip resync
		cache.Indexers{},
	)

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.add,
		UpdateFunc: c.update,
		DeleteFunc: c.del,
	})

	go informer.Run(ctx.Done())

	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		log.Error("Timed out waiting for caches to sync")
		return
	}

	log.Info("Synced ConfigMap watcher")
}

func (c *ConfigMapWatcher) add(obj interface{}) {
	cm, ok := obj.(*v1.ConfigMap)
	if !ok {
		log.Errorf("Failed to get ConfigMap object")
		return
	}

	c.configs <- configMapToEgressConfig(cm)
}

func (c *ConfigMapWatcher) update(newObj, oldObj interface{}) {
	newCM, ok := newObj.(*v1.ConfigMap)
	if !ok {
		log.Errorf("Failed to get new ConfigMap object")
		return
	}

	c.configs <- configMapToEgressConfig(newCM)
}

func (c *ConfigMapWatcher) del(obj interface{}) {
	cm, ok := obj.(*v1.ConfigMap)
	if !ok {
		log.Errorf("Failed to get ConfigMap object")
		return
	}

	c.configs <- provider.EgressConfig{
		Resource: provider.Resource{
			Name:      cm.Name,
			Namespace: cm.Namespace,
		},
	}
}

func configMapToEgressConfig(cm *v1.ConfigMap) provider.EgressConfig {
	ipAddresses := make(map[string]struct{})
	for key, cidr := range cm.Data {
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			log.Errorf("Failed to parse CIDR %v from %s in ConfigMap %s/%s", cidr, key, cm.Namespace, cm.Name)
			continue
		}
		ipAddresses[ipnet.String()] = struct{}{}
	}

	return provider.EgressConfig{
		Resource: provider.Resource{
			Name:      cm.Name,
			Namespace: cm.Namespace,
		},
		IPAddresses: ipAddresses,
	}
}
