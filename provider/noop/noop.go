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

func (p *NoopProvider) Upsert(nets []string) error {
	logrus.Infof("%s Upsert(%v)", ProviderName, nets)
	return nil
}

func (p *NoopProvider) Delete() error {
	logrus.Infof("%s Delete()", ProviderName)
	return nil
}
