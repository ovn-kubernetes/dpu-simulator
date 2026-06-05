package netutil

import (
	"encoding/binary"
	"fmt"
	"net"
)

// GetFirstUsableIPv4AddressInSubnet returns the first usable IPv4 address in subnet.
func GetFirstUsableIPv4AddressInSubnet(subnet *net.IPNet) (net.IP, error) {
	if subnet == nil {
		return nil, fmt.Errorf("subnet is nil")
	}
	ip := subnet.IP.To4()
	if ip == nil {
		return nil, fmt.Errorf("only IPv4 subnets are supported")
	}
	ones, bits := subnet.Mask.Size()
	if bits != 32 {
		return nil, fmt.Errorf("only IPv4 subnets are supported")
	}
	if ones > 30 {
		return nil, fmt.Errorf("%s does not have enough usable IPv4 addresses", subnet)
	}

	netAddr := binary.BigEndian.Uint32(ip)
	result := make(net.IP, 4)
	binary.BigEndian.PutUint32(result, netAddr+1)
	return result, nil
}
