package netutil

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetFirstUsableIPv4AddressInSubnet(t *testing.T) {
	tests := []struct {
		name        string
		cidr        string
		expected    string
		expectError bool
	}{
		{
			name:     "slash 24 subnet",
			cidr:     "172.30.0.0/24",
			expected: "172.30.0.1",
		},
		{
			name:     "slash 30 subnet",
			cidr:     "192.0.2.0/30",
			expected: "192.0.2.1",
		},
		{
			name:        "slash 31 subnet has no usable host address",
			cidr:        "192.0.2.0/31",
			expectError: true,
		},
		{
			name:        "IPv6 subnet",
			cidr:        "fd00::/64",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, subnet, err := net.ParseCIDR(tt.cidr)
			assert.NoError(t, err)

			ip, err := GetFirstUsableIPv4AddressInSubnet(subnet)
			if tt.expectError {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, ip.String())
		})
	}
}
