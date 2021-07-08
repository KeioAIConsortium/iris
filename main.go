package main

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"
	lxd "github.com/lxc/lxd/client"
	api "github.com/lxc/lxd/shared/api"
)

var lxdServer lxd.InstanceServer
var clusterInfo *api.Cluster
var devices []*nvml.Device

func rootHandler(w http.ResponseWriter, r *http.Request) {
	rawContainers, err := lxdServer.GetContainers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var containers []*api.Container
	for i := range rawContainers {
		containers = append(containers, &rawContainers[i])
	}

	clusterState := makeClusterState(containers)
	clusterState.logManagedContainers(clusterInfo.ServerName)
	managedContainerNames :=
		clusterState.getManagedContainers(clusterInfo.ServerName)

	var managedContainers []*api.Container
	for _, container := range containers {
		if regexp.MustCompile("^jupyterhub-singleuser-instance").MatchString(container.Name) && stringSliceContains(managedContainerNames, container.Name) {
			managedContainers = append(managedContainers, container)
		}
	}

	address, err := getAvailableGpuPciAddress(managedContainers, devices)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type response struct {
		Pci string `json:"pci"`
	}
	res, err := json.Marshal(response{
		Pci: address,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

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

	if err = initGPUDevices(); err != nil {
		log.Printf("failed to initGPUDevices(): %v", err)
		return
	}

	if err = initLxdServer(); err != nil {
		log.Printf("failed to initLxdServer(): %v", err)
	}

	if err = initClusterInfo(); err != nil {
		log.Printf("failed to initClusterInfo(): %v", err)
		return
	}

	log.Print("Initialization complete.")

	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandler)

	s := http.Server{
		Addr:    ":80",
		Handler: mux,
	}
	if err = s.ListenAndServe(); err != nil {
		log.Printf("failed to http.Server.ListenAndServe(): %v", err)
		return
	}

	log.Print("Going to sleep...")
	select {}
}
