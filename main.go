package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"
	lxd "github.com/lxc/lxd/client"
	api "github.com/lxc/lxd/shared/api"
)

type Response struct {
	Pci string `json:"pci"`
}

var lxdServer lxd.InstanceServer
var clusterInfo *api.Cluster
var devices []*nvml.Device

func jsonifyPretty(value interface{}) string {
	jsonValue, _ := json.Marshal(value)
	return string(jsonValue)
}

func getPciAddress(device *nvml.Device) string {
	return strings.ToLower(device.PCI.BusID[4:])
}

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

func stringSliceContains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func getAvailableGpuPciAddress(containers []*api.Container, devices []*nvml.Device) string {
	gpuLookup := map[string]int{}

	for _, device := range devices {
		gpuLookup[getPciAddress(device)] = 0
	}

	for _, container := range containers {
		for _, device := range container.ExpandedDevices {
			deviceType, ok := device["type"]
			if !ok {
				log.Fatalf("%s: expected \"type\" for device: %s", container.Name, jsonifyPretty(device))
			}
			if deviceType != "gpu" {
				continue
			}

			pciAddress, ok := device["pci"]
			if !ok {
				// NOTE: there are containers in which no pci address is specified because of previous system specifications
				// but after starting all containers in the new environment, the problem will be solved
				continue
				// log.Fatalf("%s: found invalid gpu device (no pci address specified): %s", container.Name, jsonifyPretty(device))
			}

			found := false
			for _, device := range devices {
				if pciAddress == getPciAddress(device) {
					found = true
				}
			}
			if !found {
				log.Fatalf("%s: attached gpu not found: %s", container.Name, jsonifyPretty(device))
			}

			gpuLookup[pciAddress]++

			log.Printf("%s: %v", container.Name, jsonifyPretty(device))
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

	res := &Response{Pci: leastAssignedGPUAddress}

	return jsonifyPretty(res)
}

var containerNameReg = regexp.MustCompile("^jupyterhub-singleuser-instance")

func rootHandler(w http.ResponseWriter, r *http.Request) {
	rawContainers, err := lxdServer.GetContainers()
	if err != nil {
		log.Fatalln("LXD error:", err.Error())
	}

	var containers []*api.Container
	for i := range rawContainers {
		containers = append(containers, &rawContainers[i])
	}

	clusterState := initClusterState(containers)
	clusterState.logManagedContainers(clusterInfo.ServerName)
	managedContainerNames :=
		clusterState.getManagedContainers(clusterInfo.ServerName)

	var managedContainers []*api.Container
	for _, container := range containers {
		if containerNameReg.MatchString(container.Name) && stringSliceContains(managedContainerNames, container.Name) {
			managedContainers = append(managedContainers, container)
		}
	}

	ret := getAvailableGpuPciAddress(managedContainers, devices)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, ret)
}

func main() {
	log.Print("Initializing NVML...")
	err := nvml.Init()
	if err != nil {
		log.Fatalf("NVML error: %v", err)
	}
	defer func() {
		err := nvml.Shutdown()
		if err != nil {
			log.Fatalf("failed to nvml.Shutdown() successfully: %v", err)
		}
	}()

	count, err := nvml.GetDeviceCount()
	if err != nil {
		log.Fatalf("Error getting device count: %v", err)
	}
	log.Printf("Detected %d GPUs.", count)

	for i := uint(0); i < count; i++ {
		device, err := nvml.NewDevice(i)
		if err != nil {
			log.Fatalf("NVML error: %v", err)
		}
		log.Printf("GPU %d: %s", i, device.PCI.BusID)
		devices = append(devices, device)
	}

	lxdServer, err = lxd.ConnectLXDUnix("", nil)
	if err != nil {
		log.Fatalf("LXD error: %v", err)
	}

	clusterInfo, _, err = lxdServer.GetCluster()
	if err != nil {
		log.Fatalf("LXD error: %v", err)
	}
	if clusterInfo.Enabled {
		log.Print("LXD is running in cluster mode")
	} else {
		log.Print("LXD is running in standalone mode")
	}

	log.Print("Initialization is done")

	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandler)

	s := http.Server{
		Addr:    ":80",
		Handler: mux,
	}
	s.ListenAndServe()

	log.Print("Going to sleep...")
	select {}
}
