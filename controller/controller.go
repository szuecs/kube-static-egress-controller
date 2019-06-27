package controller

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/szuecs/kube-static-egress-controller/provider"
)

// EgressController is the controller for creating Egress configuration via a
// provider.
type EgressController struct {
	interval     time.Duration
	configsChan  <-chan provider.EgressConfig
	configsCache map[provider.Resource]map[string]struct{}
	provider     provider.Provider
}

// NewEgressController initializes a new EgressController.
func NewEgressController(prov provider.Provider, configsChan <-chan provider.EgressConfig, interval time.Duration) *EgressController {
	return &EgressController{
		interval:     interval,
		configsChan:  configsChan,
		provider:     prov,
		configsCache: make(map[provider.Resource]map[string]struct{}),
	}
}

// Run runs the EgressController main loop.
func (c *EgressController) Run(ctx context.Context) {
	log.Info("Running controller")
	interval := c.interval
	for {
		select {
		case <-time.After(interval):
			err := c.provider.Ensure(c.configsCache)
			if err != nil {
				log.Errorf("Failed to ensure configuration: %v", err)
				continue
			}
			interval = c.interval
		case config := <-c.configsChan:
			if len(config.IPAddresses) == 0 {
				delete(c.configsCache, config.Resource)
			} else {
				log.Infof("Observed IP Addresses %v for %v", config.IPAddresses, config.Resource)
				c.configsCache[config.Resource] = config.IPAddresses
			}

			err := c.provider.Ensure(c.configsCache)
			if err != nil {
				log.Errorf("Failed to ensure configuration: %v", err)
				continue
			}
			interval = c.interval
		case <-ctx.Done():
			log.Info("Terminating controller loop.")
			return
		}
	}
}
