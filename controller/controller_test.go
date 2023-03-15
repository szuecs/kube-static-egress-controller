package controller

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/szuecs/kube-static-egress-controller/provider"
	"github.com/szuecs/kube-static-egress-controller/provider/noop"
)

type mockEgressConfigSource struct {
	configs     []provider.EgressConfig
	configsChan <-chan provider.EgressConfig
}

func (s mockEgressConfigSource) ListConfigs(_ context.Context) ([]provider.EgressConfig, error) {
	return s.configs, nil
}

func (s mockEgressConfigSource) Config() <-chan provider.EgressConfig {
	return s.configsChan
}

func TestControllerRun(t *testing.T) {
	_, netA, _ := net.ParseCIDR("1.0.0.1/32")
	prov := noop.NewNoopProvider()
	configsChan := make(chan provider.EgressConfig)
	configSource := mockEgressConfigSource{
		configs: []provider.EgressConfig{
			{
				Resource: provider.Resource{
					Name:      "a",
					Namespace: "y",
				},
				IPAddresses: map[string]*net.IPNet{
					netA.String(): netA,
				},
			},
		},
		configsChan: configsChan,
	}
	controller := NewEgressController(prov, configSource, 0)

	// test adding the an egress config.
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		cancel()
	}()
	controller.Run(ctx)

	require.Len(t, controller.configsCache, 1)
	require.Contains(t, controller.configsCache, provider.Resource{
		Name:      "a",
		Namespace: "y",
	})

	// test adding the an egress config.
	ctx, cancel = context.WithCancel(context.Background())

	go func() {
		configsChan <- provider.EgressConfig{
			Resource: provider.Resource{
				Name:      "a",
				Namespace: "x",
			},
			IPAddresses: map[string]*net.IPNet{
				netA.String(): netA,
			},
		}
		cancel()
	}()
	controller.Run(ctx)

	require.Len(t, controller.configsCache, 2)
	require.Contains(t, controller.configsCache, provider.Resource{
		Name:      "a",
		Namespace: "x",
	})

	// test removing the config
	ctx, cancel = context.WithCancel(context.Background())

	go func() {
		configsChan <- provider.EgressConfig{
			Resource: provider.Resource{
				Name:      "a",
				Namespace: "x",
			},
		}
		cancel()
	}()
	controller.Run(ctx)

	require.Len(t, controller.configsCache, 1)
}
