package noop

import (
	log "github.com/sirupsen/logrus"
	"github.com/szuecs/kube-static-egress-controller/provider"
)

const ProviderName = "noop"

type NoopProvider struct{}

func NewNoopProvider() *NoopProvider {
	return &NoopProvider{}
}

func (p NoopProvider) String() string {
	return ProviderName
}

func (p *NoopProvider) Ensure(configs map[provider.Resource]map[string]struct{}) error {
	log.Infof("%s Ensure(%v)", ProviderName, configs)
	return nil
}
