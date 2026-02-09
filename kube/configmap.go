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
	clients   map[string]kubernetes.Interface
	namespace string
	selector  fields.Selector
	configs   chan provider.EgressConfig
}

func NewConfigMapWatcher(clients map[string]kubernetes.Interface, namespace, selectorStr string, configs chan provider.EgressConfig) (*ConfigMapWatcher, error) {
	selector, err := fields.ParseSelector(selectorStr)
	if err != nil {
		return nil, err
	}

	return &ConfigMapWatcher{
		clients:   clients,
		namespace: namespace,
		selector:  selector,
		configs:   configs,
	}, nil
}

func (c *ConfigMapWatcher) Run(ctx context.Context) {
	for cluster, client := range c.clients {
		c.runForClient(ctx, client, cluster)
	}
}

func (c *ConfigMapWatcher) runForClient(ctx context.Context, client kubernetes.Interface, cluster string) {
	informer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				options.LabelSelector = c.selector.String()
				return client.CoreV1().ConfigMaps(c.namespace).List(ctx, options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				options.LabelSelector = c.selector.String()
				return client.CoreV1().ConfigMaps(c.namespace).Watch(ctx, options)
			},
		},
		&v1.ConfigMap{},
		0, // skip resync
		cache.Indexers{},
	)

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.add(cluster),
		UpdateFunc: c.update(cluster),
		DeleteFunc: c.del(cluster),
	})

	go informer.Run(ctx.Done())

	if !cache.WaitForCacheSync(ctx.Done(), informer.HasSynced) {
		log.Error("Timed out waiting for caches to sync")
		return
	}

	log.Info("Synced ConfigMap watcher")
}

func (c *ConfigMapWatcher) add(cluster string) func(obj interface{}) {
	return func(obj interface{}) {
		cm, ok := obj.(*v1.ConfigMap)
		if !ok {
			log.Errorf("Failed to get ConfigMap object")
			return
		}

		c.configs <- configMapToEgressConfig(cm, cluster)
	}
}

func (c *ConfigMapWatcher) update(cluster string) func(oldObj, newObj interface{}) {
	return func(oldObj, newObj interface{}) {
		newCM, ok := newObj.(*v1.ConfigMap)
		if !ok {
			log.Errorf("Failed to get new ConfigMap object")
			return
		}

		c.configs <- configMapToEgressConfig(newCM, cluster)
	}
}

func (c *ConfigMapWatcher) del(cluster string) func(obj interface{}) {
	return func(obj interface{}) {
		cm, ok := obj.(*v1.ConfigMap)
		if !ok {
			log.Errorf("Failed to get ConfigMap object")
			return
		}

		c.configs <- provider.EgressConfig{
			Resource: provider.Resource{
				Name:      cm.Name,
				Namespace: cm.Namespace,
				Cluster:   cluster,
			},
		}
	}
}

func (c *ConfigMapWatcher) ListConfigs(ctx context.Context) ([]provider.EgressConfig, error) {
	egressConfigs := []provider.EgressConfig{}
	for cluster, client := range c.clients {
		configs, err := c.listConfigsForClient(ctx, client, cluster)
		if err != nil {
			return nil, err
		}
		egressConfigs = append(egressConfigs, configs...)
	}
	return egressConfigs, nil
}

func (c *ConfigMapWatcher) listConfigsForClient(ctx context.Context, client kubernetes.Interface, cluster string) ([]provider.EgressConfig, error) {
	opts := metav1.ListOptions{
		LabelSelector: c.selector.String(),
	}

	configMaps, err := client.CoreV1().ConfigMaps(c.namespace).List(ctx, opts)
	if err != nil {
		return nil, err
	}

	configs := make([]provider.EgressConfig, 0, len(configMaps.Items))
	for _, cm := range configMaps.Items {
		configs = append(configs, configMapToEgressConfig(&cm, cluster))
	}
	return configs, nil
}

func (c *ConfigMapWatcher) Config() <-chan provider.EgressConfig {
	return c.configs
}

func configMapToEgressConfig(cm *v1.ConfigMap, cluster string) provider.EgressConfig {
	ipAddresses := make(map[string]*net.IPNet)
	for key, cidr := range cm.Data {
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			log.Errorf("Failed to parse CIDR '%s' from '%s' in ConfigMap %s/%s", cidr, key, cm.Namespace, cm.Name)
			continue
		}
		ipAddresses[ipnet.String()] = ipnet
	}

	return provider.EgressConfig{
		Resource: provider.Resource{
			Name:      cm.Name,
			Namespace: cm.Namespace,
			Cluster:   cluster,
		},
		IPAddresses: ipAddresses,
	}
}
