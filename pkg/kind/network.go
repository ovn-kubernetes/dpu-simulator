package kind

import (
	"encoding/binary"
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

// containerBridgeGatewayIP returns the first usable IP in the subnet because
// Docker and Podman assign that address to the bridge gateway when creating a
// container network with an explicit subnet.
func containerBridgeGatewayIP(subnet *net.IPNet) net.IP {
	netAddr := binary.BigEndian.Uint32(subnet.IP.To4())
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, netAddr+1)
	return ip
}
