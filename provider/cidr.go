package provider

import (
	"net"
	"sort"

	"github.com/apparentlymart/go-cidr/cidr"
)

// GenerateRoutes generates the minimal number of needed routes based on a set
// of routing configurations.
func GenerateRoutes(configs map[Resource]map[string]*net.IPNet) map[string]struct{} {
	cidrs := make([]*net.IPNet, 0, len(configs))
	for _, rs := range configs {
		for _, ipnet := range rs {
			cidrs = append(cidrs, ipnet)
		}
	}

	sort.Slice(cidrs, func(i, j int) bool {
		countI := cidr.AddressCount(cidrs[i])
		countJ := cidr.AddressCount(cidrs[j])
		if countI == countJ {
			return cidrs[i].String() < cidrs[j].String()
		}
		return countI < countJ
	})

	newCIDRs := make(map[string]struct{}, len(cidrs))
	i := 0
	for _, c := range cidrs {
		contained := false
		if i < len(cidrs)-1 {
			for _, block := range cidrs[i+1:] {
				if networkContained(c, block) {
					contained = true
					break
				}
			}
		}

		if !contained {
			newCIDRs[c.String()] = struct{}{}
		}
		i++
	}

	return newCIDRs
}

// networkContained returns true if the subBlock is completely contained inside
// the superBlock.
func networkContained(subBlock, superBlock *net.IPNet) bool {
	first, last := cidr.AddressRange(subBlock)
	return superBlock.Contains(first) && superBlock.Contains(last)
}
