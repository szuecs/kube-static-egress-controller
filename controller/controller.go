package controller

import (
	"context"
	"net"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"github.com/szuecs/kube-static-egress-controller/provider"
)

var lastSyncTimestamp = prometheus.NewGauge(
	prometheus.GaugeOpts{
		Namespace: "kube_static_egress",
		Subsystem: "controller",
		Name:      "last_sync_timestamp_seconds",
		Help:      "Timestamp of last successful sync with the DNS provider",
	},
)

func init() {
	prometheus.MustRegister(lastSyncTimestamp)
}

type EgressConfigSource interface {
	ListConfigs(ctx context.Context) ([]provider.EgressConfig, error)
	Config() <-chan provider.EgressConfig
}

// EgressController is the controller for creating Egress configuration via a
// provider.
type EgressController struct {
	interval         time.Duration
	configSource     EgressConfigSource
	configsCache     map[provider.Resource]map[string]*net.IPNet
	provider         provider.Provider
	cacheInitialized bool
}

// NewEgressController initializes a new EgressController.
func NewEgressController(prov provider.Provider, configSource EgressConfigSource, interval time.Duration) *EgressController {
	return &EgressController{
		interval:     interval,
		provider:     prov,
		configSource: configSource,
		configsCache: make(map[provider.Resource]map[string]*net.IPNet),
	}
}

// Run runs the EgressController main loop.
func (c *EgressController) Run(ctx context.Context) {
	log.Info("Running controller")

	for {
		configs, err := c.configSource.ListConfigs(ctx)
		if err != nil {
			log.Errorf("Failed to list Egress configurations: %v", err)
			time.Sleep(3 * time.Second)
			continue // retry
		}

		for _, config := range configs {
			if len(config.IPAddresses) > 0 {
				c.configsCache[config.Resource] = config.IPAddresses
			}
		}
		ensureEgressRules(ctx, c.provider, c.configsCache)
		break // successfully initialized cache, move on
	}

	for {
		select {
		case <-time.After(c.interval):
			ensureEgressRules(ctx, c.provider, c.configsCache)
		case config := <-c.configSource.Config():
			if len(config.IPAddresses) == 0 {
				delete(c.configsCache, config.Resource)
			} else {
				log.Infof("Observed IP Addresses %v for %v", config.IPAddresses, config.Resource)
				c.configsCache[config.Resource] = config.IPAddresses
			}
			ensureEgressRules(ctx, c.provider, c.configsCache)
		case <-ctx.Done():
			log.Info("Terminating controller loop.")
			return
		}
	}
}

func ensureEgressRules(ctx context.Context, prov provider.Provider, configsCache map[provider.Resource]map[string]*net.IPNet) {
	err := prov.Ensure(ctx, configsCache)
	if err != nil {
		log.Errorf("Failed to ensure configuration: %v", err)
		return
	}
	// successfully synced
	lastSyncTimestamp.SetToCurrentTime()
}
