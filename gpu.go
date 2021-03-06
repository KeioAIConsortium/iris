package main

import (
	"log"
	"math"
	"strings"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"
	api "github.com/lxc/lxd/shared/api"
	"golang.org/x/xerrors"
)

type ClusterState struct {
	locationLookup map[string]string
}

func getClusterState(containers []*api.Container) *ClusterState {
	locationLookup := map[string]string{}
	for _, container := range containers {
		locationLookup[container.Name] = container.Location
	}

	return &ClusterState{
		locationLookup: locationLookup,
	}
}

func (cs *ClusterState) getManagedContainers(server string) []string {
	var managedContainers []string
	for k, v := range cs.locationLookup {
		if v == server {
			managedContainers = append(managedContainers, k)
		}
	}
	return managedContainers
}

func getPCIAddress(device *nvml.Device) string {
	return strings.ToLower(device.PCI.BusID[4:])
}

func getAvailableGPUAddress(containers []*api.Container, devices []*nvml.Device) (string, error) {
	gpuLookup := map[string]int{}

	for _, device := range devices {
		gpuLookup[getPCIAddress(device)] = 0
	}

	for _, container := range containers {
		for _, device := range container.ExpandedDevices {
			deviceType, ok := device["type"]
			if !ok {
				return "", xerrors.Errorf("not found 'type' field for device: %v", device)
			}
			if deviceType != "gpu" {
				continue
			}

			pciAddress, ok := device["pci"]
			if !ok {
				// NOTE: there are containers in which no pci address is specified because of previous system specifications
				// but after starting all containers in the new environment, the problem will be solved
				continue
			}

			found := false
			for _, device := range devices {
				if pciAddress == getPCIAddress(device) {
					found = true
				}
			}
			if !found {
				return "", xerrors.Errorf("not found attached gpu for %s", container.Name)
			}

			gpuLookup[pciAddress]++

			log.Printf("%s: %v", container.Name, device)
		}
	}

	availableGPULookup := map[string]int{}

	for _, device := range devices {
		processes, err := device.GetAllRunningProcesses()
		if err != nil {
			return "", xerrors.Errorf("failed to GetAllRunningProcesses(): %w", err)
		}
		if len(processes) == 0 {
			availableGPULookup[getPCIAddress(device)] = gpuLookup[getPCIAddress(device)]
		}
	}

	leastAssignedGPUAddress := ""
	num := math.MaxInt32

	log.Print("Available GPUs:")
	for address, assignedNum := range availableGPULookup {
		log.Printf("%s: assigned to %d containers", address, assignedNum)

		if num > assignedNum {
			leastAssignedGPUAddress = address
			num = assignedNum
		}
	}

	// MEMO: assign the gpu whose associated containers' number is the smallest even through the gpu has working processes
	if leastAssignedGPUAddress == "" {
		for address, assignedNum := range gpuLookup {
			if num > assignedNum {
				leastAssignedGPUAddress = address
				num = assignedNum
			}
		}
	}

	return leastAssignedGPUAddress, nil
}
