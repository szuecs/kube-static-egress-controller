package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/szuecs/kube-static-egress-controller/provider"
	"github.com/szuecs/kube-static-egress-controller/provider/noop"
)

func TestControllerRun(t *testing.T) {
	prov := noop.NewNoopProvider()
	configsChan := make(chan provider.EgressConfig)
	controller := NewEgressController(prov, configsChan, 0)

	// test adding the an egress config.
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		configsChan <- provider.EgressConfig{
			Resource: provider.Resource{
				Name:      "a",
				Namespace: "x",
			},
			IPAddresses: map[string]struct{}{
				"10.0.0.1": struct{}{},
			},
		}
		cancel()
	}()
	controller.Run(ctx)

	require.Len(t, controller.configsCache, 1)
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

	require.Len(t, controller.configsCache, 0)
}
