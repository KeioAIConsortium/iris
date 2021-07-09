package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"
	lxd "github.com/lxc/lxd/client"
	api "github.com/lxc/lxd/shared/api"
	"golang.org/x/xerrors"
)

var lxdServer lxd.InstanceServer
var clusterInfo *api.Cluster
var devices []*nvml.Device

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
	c, _, err := lxdServer.GetCluster()
	if err != nil {
		return xerrors.Errorf("failed to lxdServer.GetCluster(): %w", err)
	}
	if c.Enabled {
		log.Print("LXD is running in cluster mode")
	} else {
		log.Print("LXD is running in standalone mode")
	}

	clusterInfo = c
	return nil
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

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

	clusterState := getClusterState(containers)
	managedContainerNames := clusterState.getManagedContainers(clusterInfo.ServerName)

	log.Printf("Currently managed containers: %s", strings.Join(managedContainerNames, ", "))

	var managedContainers []*api.Container
	for _, container := range containers {
		if strings.HasPrefix(container.Name, "jupyterhub-singleuser-instance") && contains(managedContainerNames, container.Name) {
			managedContainers = append(managedContainers, container)
		}
	}

	address, err := getAvailableGPUPCIAddress(managedContainers, devices)
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

	if err := initGPUDevices(); err != nil {
		log.Printf("failed to initGPUDevices(): %v", err)
		return
	}

	if err := initLxdServer(); err != nil {
		log.Printf("failed to initLxdServer(): %v", err)
		return
	}

	if err := initClusterInfo(); err != nil {
		log.Printf("failed to initClusterInfo(): %v", err)
		return
	}

	log.Print("Initialization complete.")

	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandler)

	if err := http.ListenAndServe(":80", mux); err != nil {
		log.Printf("failed to http.Server.ListenAndServe(): %v", err)
		return
	}
}
