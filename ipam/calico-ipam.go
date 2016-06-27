package main

import (
	"encoding/json"
	"fmt"
	"net"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/projectcalico/libcalico/lib/ipam"
)

func main() {
	skel.PluginMain(cmdAdd, cmdDel)
}

// IPAMConfig represents the IP related network configuration.
type IPAMConfig struct {
	Name string
	Type string    `json:"type"`
	Args *IPAMArgs `json:"-"`
}

type IPAMArgs struct {
	types.CommonArgs
	IP net.IP `json:"ip,omitempty"`
}

type Net struct {
	Name string      `json:"name"`
	IPAM *IPAMConfig `json:"ipam"`
}

func LoadIPAMConfig(bytes []byte, args string) (*IPAMConfig, error) {
	n := Net{}
	if err := json.Unmarshal(bytes, &n); err != nil {
		return nil, err
	}

	if args != "" {
		n.IPAM.Args = &IPAMArgs{}
		err := types.LoadArgs(args, n.IPAM.Args)
		if err != nil {
			return nil, err
		}
	}

	if n.IPAM == nil {
		return nil, fmt.Errorf("missing 'ipam' key")
	}

	// Copy net name into IPAM so not to drag Net struct around
	n.IPAM.Name = n.Name

	return n.IPAM, nil
}

func cmdAdd(args *skel.CmdArgs) error {
	_, err := LoadIPAMConfig(args.StdinData, args.Args)
	if err != nil {
		return err
	}

	ipamClient, err := ipam.NewIPAMClient()
	if err != nil {
		return err
	}

	//TODO - does this code need to fetch the pools or should ipamClient do it automatically.
	_, pool, _ := net.ParseCIDR("192.168.0.0/16")

	// TODO - Use the workloadID as the handle (i.e. need to know about k8s)
	addresses, _, _ := ipamClient.AutoAssign(1, 0, "", map[string]string{}, nil, pool, nil)

	ipNetwork := net.IPNet{IP: addresses[0], Mask: net.CIDRMask(32, 32)}

	r := &types.Result{
		IP4: &types.IPConfig{IP: ipNetwork},
	}
	return r.Print()
}

func cmdDel(args *skel.CmdArgs) error {
	_, err := LoadIPAMConfig(args.StdinData, args.Args)
	if err != nil {
		return err
	}

	//TODO  - need to release the address - but don't have an API yet
	// Release by handle - which is worloadID.

	return nil
}

//TODO: Add ability to control IPv4 vs IPv6 assignment
// TODO - Tests - all have an add and delete
// k8s vs non-k8s
// ipv4 vs ipv6 in network config
// IP in the CNI args
