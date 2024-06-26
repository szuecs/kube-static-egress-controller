package provider

import (
	"context"
	"net"
)

type Resource struct {
	Name      string
	Namespace string
}

type EgressConfig struct {
	Resource
	IPAddresses map[string]*net.IPNet
}

type Provider interface {
	Ensure(ctx context.Context, configs map[Resource]map[string]*net.IPNet) error
	String() string
}
