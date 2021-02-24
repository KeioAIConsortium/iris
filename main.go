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

// TODO: get GPU address the number of which assigned to singleuser is the smallest without runnning any processes

// for log output becuase this includes new-line character
func jsonifyPrettyForLog(value interface{}) string {
	jsonValue, _ := json.MarshalIndent(value, "", "  ")
	return string(jsonValue)
}

func jsonifyPretty(value interface{}) string {
	jsonValue, _ := json.Marshal(value)
	return string(jsonValue)
}

func getPciAddress(device nvml.Device) string {
	return strings.ToLower(device.PCI.BusID[4:])
}

type ClusterState struct {
	locationLookup map[string]string
}

func initClusterState(containers []api.Container) ClusterState {
	locationLookup := map[string]string{}
	for _, container := range containers {
		locationLookup[container.Name] = container.Location
	}

	return ClusterState{
		locationLookup: locationLookup,
	}
}

func (cs *ClusterState) logManagedContainers(server string) {
	log.Println("Currently managed containers:")
	for _, containerName := range cs.getManagedContainers(server) {
		log.Println("-", containerName)
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

func filterContainers(ss []api.Container, test func(api.Container) bool) (ret []api.Container) {
	for _, s := range ss {
		if test(s) {
			ret = append(ret, s)
		}
	}
	return
}

type Response struct {
	Pci string `json:"pci"`
}

func getAvailableGpuPciAddress(containers []api.Container, devices []nvml.Device) (string, error) {
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

			log.Println(container.Name, jsonifyPretty(device))
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

	log.Println("Available GPUs")
	for k, v := range availableGpuLookup {
		log.Println(k, ": ", "assigned to", v, " containers")

		if num > v {
			leastAssignedGPUAddress = k
			num = v
		}
	}

	// 割り当てるGPUがない場合プロセスが動いていようと一番コンテナに割り当てる数が少ないGPUを割り当てる
	if leastAssignedGPUAddress == "" {
		for k, v := range gpuLookup {
			if num > v {
				leastAssignedGPUAddress = k
				num = v
			}
		}
	}

	res := &Response{Pci: leastAssignedGPUAddress}

	return jsonifyPretty(res), nil
}

func main() {
	log.Println("Initializing NVML...")
	err := nvml.Init()
	if err != nil {
		log.Fatalln("NVML error:", err.Error())
	}
	defer nvml.Shutdown()

	count, err := nvml.GetDeviceCount()
	if err != nil {
		log.Fatalln("Error getting device count:", err.Error())
	}
	log.Printf("Detected %d GPUs.\n", count)

	var devices []nvml.Device
	for i := uint(0); i < count; i++ {
		device, err := nvml.NewDevice(i)
		if err != nil {
			log.Fatalln("NVML error:", err.Error())
		}
		log.Println("GPU ", i, ": ", device.PCI.BusID)
		devices = append(devices, *device)
	}

	c, err := lxd.ConnectLXDUnix("", nil)
	if err != nil {
		log.Fatalln("LXD error:", err.Error())
	}

	clusterInfo, _, err := c.GetCluster()
	if err != nil {
		log.Fatalln("LXD error:", err.Error())
	}
	if clusterInfo.Enabled {
		log.Println("LXD is running in cluster mode")
	} else {
		log.Println("LXD is running in standalone mode")
	}

	// confirm singleuser-instance containers only
	reg, _ := regexp.Compile("^jupyterhub-singleuser-instance")

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		containers, err := c.GetContainers()
		if err != nil {
			log.Fatalln("LXD error:", err.Error())
		}

		clusterState := initClusterState(containers)
		clusterState.logManagedContainers(clusterInfo.ServerName)
		managedContainerNames :=
			clusterState.getManagedContainers(clusterInfo.ServerName)
		managedContainers :=
			filterContainers(containers, func(container api.Container) bool {
				return reg.MatchString(container.Name) && stringSliceContains(managedContainerNames, container.Name)
			})

		ret, err := getAvailableGpuPciAddress(managedContainers, devices)

		if err != nil {
			log.Fatalln("error:", err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, err.Error())
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, ret)
	})

	s := http.Server{
		Addr:    ":80",
		Handler: mux,
	}
	s.ListenAndServe()

	log.Println("Going to sleep...")
	select {}
}
