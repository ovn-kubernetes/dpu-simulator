package kind

import (
	"strings"
	"testing"
)

func TestNetworkInfoFromDockerInspect(t *testing.T) {
	info, err := networkInfoFromInspect("kind", containerNetworkInspect{
		ID:     "0123456789abcdef",
		Name:   "kind",
		Driver: "bridge",
		IPAM: struct {
			Config []struct {
				Subnet string `json:"Subnet"`
			} `json:"Config"`
		}{
			Config: []struct {
				Subnet string `json:"Subnet"`
			}{
				{Subnet: "172.18.0.0/16"},
			},
		},
	})
	if err != nil {
		t.Fatalf("networkInfoFromInspect returned error: %v", err)
	}
	if info.name != "kind" {
		t.Fatalf("expected name kind, got %q", info.name)
	}
	if info.bridge != "br-0123456789ab" {
		t.Fatalf("expected Docker bridge br-0123456789ab, got %q", info.bridge)
	}
	if info.subnet.String() != "172.18.0.0/16" {
		t.Fatalf("expected subnet 172.18.0.0/16, got %s", info.subnet.String())
	}
}

func TestNetworkInfoFromDockerBridgeOption(t *testing.T) {
	info, err := networkInfoFromInspect("kind", containerNetworkInspect{
		Name:   "kind",
		Driver: "bridge",
		Options: map[string]string{
			"com.docker.network.bridge.name": "kind0",
		},
		IPAM: struct {
			Config []struct {
				Subnet string `json:"Subnet"`
			} `json:"Config"`
		}{
			Config: []struct {
				Subnet string `json:"Subnet"`
			}{
				{Subnet: "172.18.0.0/16"},
			},
		},
	})
	if err != nil {
		t.Fatalf("networkInfoFromInspect returned error: %v", err)
	}
	if info.bridge != "kind0" {
		t.Fatalf("expected Docker bridge option kind0, got %q", info.bridge)
	}
}

func TestNetworkInfoFromPodmanInspect(t *testing.T) {
	info, err := networkInfoFromInspect("dpu-sim-gateway", containerNetworkInspect{
		NameLower:        "dpu-sim-gateway",
		DriverLower:      "bridge",
		NetworkInterface: "podman2",
		Subnets: []struct {
			Subnet string `json:"subnet"`
		}{
			{Subnet: "172.30.0.0/24"},
		},
	})
	if err != nil {
		t.Fatalf("networkInfoFromInspect returned error: %v", err)
	}
	if info.bridge != "podman2" {
		t.Fatalf("expected Podman bridge podman2, got %q", info.bridge)
	}
	if info.subnet.String() != "172.30.0.0/24" {
		t.Fatalf("expected subnet 172.30.0.0/24, got %s", info.subnet.String())
	}
}

func TestForwardRuleArgsIncludeSimulatorComment(t *testing.T) {
	src, err := parseIPv4CIDR("172.18.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	dst, err := parseIPv4CIDR("172.30.0.0/24")
	if err != nil {
		t.Fatal(err)
	}

	args := strings.Join(forwardRuleArgs("br-kind", "br-gw", src, dst), " ")
	if !strings.Contains(args, "--comment dpu-simulator-kind-gateway-routing") {
		t.Fatalf("expected simulator comment in rule args, got %q", args)
	}
}

func TestRawPreroutingRuleArgsIncludeSimulatorComment(t *testing.T) {
	src, err := parseIPv4CIDR("172.30.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	dst, err := parseIPv4CIDR("172.18.0.0/16")
	if err != nil {
		t.Fatal(err)
	}

	args := strings.Join(rawPreroutingRuleArgs("br-gw", src, dst), " ")
	if !strings.Contains(args, "--comment dpu-simulator-kind-gateway-routing") {
		t.Fatalf("expected simulator comment in rule args, got %q", args)
	}
	if !strings.Contains(args, "-i br-gw") {
		t.Fatalf("expected input bridge match in rule args, got %q", args)
	}
}

func TestPodmanUsesRootlessPasta(t *testing.T) {
	tests := []struct {
		name               string
		rootless           bool
		rootlessNetworkCmd string
		wantRootlessPasta  bool
	}{
		{
			name:               "rootless pasta",
			rootless:           true,
			rootlessNetworkCmd: "pasta",
			wantRootlessPasta:  true,
		},
		{
			name:               "rootful pasta",
			rootless:           false,
			rootlessNetworkCmd: "pasta",
			wantRootlessPasta:  false,
		},
		{
			name:               "rootless non-pasta",
			rootless:           true,
			rootlessNetworkCmd: "slirp4netns",
			wantRootlessPasta:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &podmanInfo{}
			info.Host.Security.Rootless = tt.rootless
			info.Host.RootlessNetworkCmd = tt.rootlessNetworkCmd

			if got := podmanUsesRootlessPasta(info); got != tt.wantRootlessPasta {
				t.Fatalf("podmanUsesRootlessPasta() = %v, want %v", got, tt.wantRootlessPasta)
			}
		})
	}
}
