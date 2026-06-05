package kind

import (
	"fmt"
	"net"
)

func parseIPv4CIDR(cidr string) (*net.IPNet, error) {
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	if subnet.IP.To4() == nil {
		return nil, fmt.Errorf("%s is not an IPv4 subnet", cidr)
	}
	ones, _ := subnet.Mask.Size()
	if ones > 30 {
		return nil, fmt.Errorf("%s does not have enough usable IPv4 addresses", cidr)
	}
	return subnet, nil
}
