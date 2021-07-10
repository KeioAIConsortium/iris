package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"
	lxd "github.com/lxc/lxd/client"
	api "github.com/lxc/lxd/shared/api"
)

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

type Iris struct {
	lxdServer lxd.InstanceServer
	cluster   *api.Cluster
	devices   []*nvml.Device
}

func (iris *Iris) GetAvailableGPUAddress(w http.ResponseWriter, r *http.Request) {
	rawContainers, err := iris.lxdServer.GetContainers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var containers []*api.Container
	for i := range rawContainers {
		containers = append(containers, &rawContainers[i])
	}

	clusterState := getClusterState(containers)
	managedContainerNames := clusterState.getManagedContainers(iris.cluster.ServerName)

	log.Printf("Currently managed containers: %s", strings.Join(managedContainerNames, ", "))

	var managedContainers []*api.Container
	for _, container := range containers {
		if strings.HasPrefix(container.Name, "jupyterhub-singleuser-instance") && contains(managedContainerNames, container.Name) {
			managedContainers = append(managedContainers, container)
		}
	}

	address, err := getAvailableGPUAddress(managedContainers, iris.devices)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type response struct {
		PCI string `json:"pci"`
	}
	res, err := json.Marshal(response{
		PCI: address,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(res) //nolint:errcheck
}

func main() {
	log.Print("Initializing NVML...")
	err := nvml.Init()
	if err != nil {
		log.Fatalf("failed to nvml.Init(): %v", err)
	}
	defer func() {
		err := nvml.Shutdown()
		if err != nil {
			log.Fatalf("failed to nvml.Shutdown() successfully: %v", err)
		}
	}()

	deviceCount, err := nvml.GetDeviceCount()
	if err != nil {
		log.Printf("failed to nvml.GetDeviceCount(): %v", err)
		return
	}
	log.Printf("Detected %d GPUs.", deviceCount)

	var devices []*nvml.Device
	for i := uint(0); i < deviceCount; i++ {
		device, err := nvml.NewDevice(i)
		if err != nil {
			log.Printf("failed to nvml.NewDevice(%d): %v", i, err)
			return
		}
		log.Printf("GPU %d: %s", i, device.PCI.BusID)
		devices = append(devices, device)
	}

	lxdServer, err := lxd.ConnectLXDUnix("", nil)
	if err != nil {
		log.Printf("failed to lxd.ConnectLXDUnix(): %v", err)
		return
	}

	cluster, _, err := lxdServer.GetCluster()
	if err != nil {
		log.Printf("failed to lxdServer.GetCluster(): %v", err)
		return
	}
	if cluster.Enabled {
		log.Print("LXD is running in cluster mode")
	} else {
		log.Print("LXD is running in standalone mode")
	}

	log.Print("Initialization complete.")

	iris := &Iris{
		lxdServer: lxdServer,
		cluster:   cluster,
		devices:   devices,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", iris.GetAvailableGPUAddress)

	if err := http.ListenAndServe(":80", mux); err != nil {
		log.Printf("failed to http.Server.ListenAndServe(): %v", err)
		return
	}
}
