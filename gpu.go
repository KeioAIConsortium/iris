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

func initClusterState(containers []*api.Container) ClusterState {
	locationLookup := map[string]string{}
	for _, container := range containers {
		locationLookup[container.Name] = container.Location
	}

	return ClusterState{
		locationLookup: locationLookup,
	}
}

func (cs *ClusterState) logManagedContainers(server string) {
	log.Print("Currently managed containers:")
	for _, containerName := range cs.getManagedContainers(server) {
		log.Printf("- %s", containerName)
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

func getPciAddress(device *nvml.Device) string {
	return strings.ToLower(device.PCI.BusID[4:])
}

func stringSliceContains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func getAvailableGpuPciAddress(containers []*api.Container, devices []*nvml.Device) (string, error) {
	gpuLookup := map[string]int{}

	for _, device := range devices {
		gpuLookup[getPciAddress(device)] = 0
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
				if pciAddress == getPciAddress(device) {
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

	availableGpuLookup := map[string]int{}

	for _, device := range devices {
		processes, err := device.GetAllRunningProcesses()
		if err != nil {
			log.Fatalln("error:", err.Error())
		}
		if len(processes) == 0 {
			availableGpuLookup[getPciAddress(device)] = gpuLookup[getPciAddress(device)]
		}
	}

	leastAssignedGPUAddress := ""
	num := math.MaxInt32

	log.Print("Available GPUs:")
	for address, assignedNum := range availableGpuLookup {
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
