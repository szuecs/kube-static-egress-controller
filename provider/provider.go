package provider

import "net"

type Resource struct {
	Name      string
	Namespace string
}

type EgressConfig struct {
	Resource
	IPAddresses map[string]*net.IPNet
}

type Provider interface {
	Ensure(configs map[Resource]map[string]*net.IPNet) error
	String() string
}
