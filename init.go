package main

import (
	"log"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"
	lxd "github.com/lxc/lxd/client"
	"golang.org/x/xerrors"
)

func initGPUDevices() error {
	deviceCount, err := nvml.GetDeviceCount()
	if err != nil {
		return xerrors.Errorf("failed to nvml.GetDeviceCount(): %w", err)
	}
	log.Printf("Detected %d GPUs.", deviceCount)

	for i := uint(0); i < deviceCount; i++ {
		device, err := nvml.NewDevice(i)
		if err != nil {
			return xerrors.Errorf("failed to nvml.NewDevice(%d): %w", i, err)
		}
		log.Printf("GPU %d: %s", i, device.PCI.BusID)
		devices = append(devices, device)
	}

	return nil
}

func initLxdServer() error {
	lis, err := lxd.ConnectLXDUnix("", nil)
	if err != nil {
		return xerrors.Errorf("failed to lxd.ConnectLXDUnix(): %w", err)
	}

	lxdServer = lis
	return nil
}

func initClusterInfo() error {
	clusterInfo, _, err := lxdServer.GetCluster()
	if err != nil {
		return xerrors.Errorf("failed to lxdServer.GetCluster(): %w", err)
	}
	if clusterInfo.Enabled {
		log.Print("LXD is running in cluster mode")
	} else {
		log.Print("LXD is running in standalone mode")
	}
	return nil
}
