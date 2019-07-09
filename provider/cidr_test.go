package provider

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateRoutes(tt *testing.T) {
	_, netA, _ := net.ParseCIDR("10.0.0.0/16")
	_, netB, _ := net.ParseCIDR("10.0.0.0/17")
	_, netC, _ := net.ParseCIDR("10.1.0.0/17")

	for _, tc := range []struct {
		msg      string
		configs  map[Resource]map[string]*net.IPNet
		expected []string
	}{
		{
			msg: "Subnet should be covered by superblock",
			configs: map[Resource]map[string]*net.IPNet{
				Resource{
					Name:      "a",
					Namespace: "x",
				}: map[string]*net.IPNet{
					netA.String(): netA,
					netB.String(): netB,
				},
			},
			expected: []string{
				netA.String(),
			},
		},
		{
			msg: "non-overlapping subnets should be used.",
			configs: map[Resource]map[string]*net.IPNet{
				Resource{
					Name:      "a",
					Namespace: "x",
				}: map[string]*net.IPNet{
					netA.String(): netA,
					netB.String(): netB,
					netC.String(): netC,
				},
			},
			expected: []string{
				netC.String(),
				netA.String(),
			},
		},
	} {
		tt.Run(tc.msg, func(t *testing.T) {
			nets := GenerateRoutes(tc.configs)
			require.Equal(t, tc.expected, nets)
		})
	}
}
