package config

import (
	"fmt"
	"net"
)

func validateIPv4CIDR(cidr string) error {
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return err
	}
	if subnet.IP.To4() == nil {
		return fmt.Errorf("%s is not an IPv4 subnet", cidr)
	}
	ones, _ := subnet.Mask.Size()
	if ones > 30 {
		return fmt.Errorf("%s does not have enough usable IPv4 addresses", cidr)
	}
	return nil
}

func validateIPv4CIDRCapacity(cidr string, requiredUsableIPs int) error {
	if err := validateIPv4CIDR(cidr); err != nil {
		return err
	}
	if requiredUsableIPs <= 0 {
		return nil
	}

	_, subnet, _ := net.ParseCIDR(cidr)
	usableIPs := usableIPv4AddressCount(subnet)
	if usableIPs < requiredUsableIPs {
		return fmt.Errorf("%s has %d usable IPv4 addresses, requires at least %d", cidr, usableIPs, requiredUsableIPs)
	}
	return nil
}

func usableIPv4AddressCount(subnet *net.IPNet) int {
	ones, _ := subnet.Mask.Size()
	if ones > 30 {
		return 0
	}
	return int((uint64(1) << uint(32-ones)) - 2)
}
