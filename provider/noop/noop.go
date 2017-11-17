package noop

import "github.com/sirupsen/logrus"

const ProviderName = "noop"

type NoopProvider struct{}

func NewNoopProvider() *NoopProvider {
	return &NoopProvider{}
}

func (p NoopProvider) String() string {
	return ProviderName
}

func (p *NoopProvider) Execute(nets []string) error {
	logrus.Infof("%s Execute(%v)", ProviderName, nets)
	return nil
}
