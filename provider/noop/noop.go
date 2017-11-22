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

func (p *NoopProvider) Create(nets []string) error {
	logrus.Infof("%s Create(%v)", ProviderName, nets)
	return nil
}

func (p *NoopProvider) Update(nets []string) error {
	logrus.Infof("%s Update(%v)", ProviderName, nets)
	return nil
}

func (p *NoopProvider) Delete() error {
	logrus.Infof("%s Delete()", ProviderName)
	return nil
}
