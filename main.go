package main

import (
	crand "crypto/rand"
	"math"
	"math/rand"
	"math/big"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"regexp"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"

	lxd "github.com/lxc/lxd/client"
	api "github.com/lxc/lxd/shared/api"
)

// Log表示用(改行文字を含むため)
func jsonifyPretty(value interface{}) string {
	jsonValue, _ := json.MarshalIndent(value, "", "  ")
	return string(jsonValue)
}

func getPciAddress(device nvml.Device) string {
	return strings.ToLower(device.PCI.BusID[4:])
}

type State struct {
	devices    []nvml.Device
	containers []string
	lock       sync.RWMutex
}

func (s *State) deviceCount() int {
	s.lock.Lock()
	defer s.lock.Unlock()

	return len(s.devices)
}

func (s *State) requestGpu(c lxd.InstanceServer, containerName string) error {
	container, etag, err := c.GetContainer(containerName)
	if err != nil {
		return err
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	for _, name := range s.containers {
		// The container already has a GPU assigned to it
		if name == containerName {
			return fmt.Errorf("Container already has a GPU assigned to it")
		}
	}

	index := -1
	for i, name := range s.containers {
		if index == -1 && name == "" {
			index = i
			break
		}
	}

	if index == -1 {
		return fmt.Errorf("No GPUs available")
	}

	log.Printf("Attaching GPU %d to container %s\n", index, containerName)

	deviceName := "gpu" + strconv.Itoa(index)
	container.Devices[deviceName] = map[string]string{
		"type": "gpu",
		"pci":  getPciAddress(s.devices[index]),
	}

	op, err := c.UpdateContainer(containerName, container.Writable(), etag)
	if err != nil {
		return err
	}

	err = op.Wait()
	if err != nil {
		return err
	}

	s.containers[index] = containerName

	return nil
}

func (s *State) releaseGpu(c lxd.InstanceServer, containerName string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	container, etag, err := c.GetContainer(containerName)
	if err != nil {
		return err
	}

	devices := container.Devices
	deletedIndex := -1
	for key := range devices {
		var index int
		_, err := fmt.Sscanf(key, "gpu%d", &index)
		if err == nil {
			if s.containers[index] != containerName {
				return fmt.Errorf("Iris expected GPU %d to belong to %s but was assigned to %s!! Bailing...", index, s.containers[index], containerName)
			}

			delete(devices, key)
			deletedIndex = index
			break
		}
	}

	op, err := c.UpdateContainer(containerName, container.Writable(), etag)
	if err != nil {
		return err
	}

	err = op.Wait()
	if err != nil {
		return err
	}

	// TODO: This will "leak" GPUs if for example a GPU has already been
	// released manually and update fails, for example
	s.containers[deletedIndex] = ""

	return nil
}

func initState(containers []api.Container, devices []nvml.Device) State {
	// confirm singleuser-instance containers only
	r, _ := regexp.Compile("^jupyterhub-singleuser-instance")

	containersState := make([]string, len(devices), len(devices))

	for i := 0; i < len(devices); i++ {
		containersState[i] = ""
	}

	for i, container := range containers {
		if r.MatchString(container.Name) == false {
			continue
		}

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
				log.Fatalf("%s: found invalid gpu device (no pci address specified): %s", container.Name, jsonifyPretty(device))
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
			containersState[i] = container.Name

			log.Println(container.Name, jsonifyPretty(device))
		}
	}

	return State{
		devices:    devices,
		containers: containersState,
		lock:       sync.RWMutex{},
	}
}

type ClusterState struct {
	locationLookup map[string]string
	lock           sync.RWMutex
}

func initClusterState(containers []api.Container) ClusterState {
	locationLookup := map[string]string{}
	for _, container := range containers {
		locationLookup[container.Name] = container.Location
	}

	return ClusterState{
		locationLookup: locationLookup,
		lock:           sync.RWMutex{},
	}
}

func (cs *ClusterState) query(containerName string) (string, error) {
	cs.lock.Lock()
	defer cs.lock.Unlock()

	server, ok := cs.locationLookup[containerName]
	if !ok {
		return "", fmt.Errorf("Container %s not in lookup table!", containerName)
	}

	return server, nil
}

func (cs *ClusterState) add(containerName string, server string) error {
	cs.lock.Lock()
	defer cs.lock.Unlock()

	s, ok := cs.locationLookup[containerName]
	if ok {
		return fmt.Errorf("Container %s already exists in lookup table at %s!", containerName, s)
	}

	cs.locationLookup[containerName] = server
	return nil
}

func (cs *ClusterState) rename(oldContainerName string, newContainerName string) error {
	cs.lock.Lock()
	defer cs.lock.Unlock()

	server, ok := cs.locationLookup[oldContainerName]
	if !ok {
		return fmt.Errorf("Container %s not in lookup table!", oldContainerName)
	}

	if s, ok := cs.locationLookup[newContainerName]; ok {
		return fmt.Errorf("Container %s already exists in lookup table at %s!", newContainerName, s)
	}

	cs.locationLookup[newContainerName] = server
	delete(cs.locationLookup, oldContainerName)
	return nil
}

func (cs *ClusterState) remove(containerName string, server string) error {
	cs.lock.Lock()
	defer cs.lock.Unlock()

	s, ok := cs.locationLookup[containerName]
	if !ok {
		return fmt.Errorf("Container %s not in lookup table!", containerName)
	}
	if s != server {
		return fmt.Errorf("Lookup table shows container %s at %s, but expected %s", containerName, s, server)
	}

	delete(cs.locationLookup, containerName)
	return nil
}

func (cs *ClusterState) getManagedContainers(server string) []string {
	cs.lock.Lock()
	defer cs.lock.Unlock()

	var managedContainers []string
	for k, v := range cs.locationLookup {
		if v == server {
			managedContainers = append(managedContainers, k)
		}
	}
	return managedContainers
}

func (cs *ClusterState) logManagedContainers(server string) {
	log.Println("Currently managed containers:")
	for _, containerName := range cs.getManagedContainers(server) {
		log.Println("-", containerName)
	}
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
	Pci string `json: "pci"`
}

func getLeastUsedGpuPciAddress(devices []nvml.Device) (string, error) {
	var deviceAddresses []string

	for _, d := range devices {
		dp, err := d.GetAllRunningProcesses()
		if err != nil {
			log.Fatalln("error:", err.Error())
		}
		if len(dp) == 0 {
			deviceAddresses = append(deviceAddresses, getPciAddress(d))
		}
	}

	var da string = ""
	if len(deviceAddresses) > 0 {
		seed, _ := crand.Int(crand.Reader, big.NewInt(math.MaxInt64))
		rand.Seed(seed.Int64())
		da = deviceAddresses[rand.Int() % len(deviceAddresses)]
	}

	res := &Response{Pci: da}
	res_json, _ := json.Marshal(res)

	return string(res_json), nil
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

	e, err := c.GetEvents()
	if err != nil {
		log.Fatalln("LXD error:", err.Error())
	}

	// TODO: data race between lifecycle handler and container check
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
			return stringSliceContains(managedContainerNames, container.Name)
		})
	//state := initState(containers, devices)
	initState(managedContainers, devices)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ret, err := getLeastUsedGpuPciAddress(devices)

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

	e.AddHandler([]string{"lifecycle"}, func(e api.Event) {
		event := &api.EventLifecycle{}
		err := json.Unmarshal(e.Metadata, event)
		if err != nil {
			log.Fatalln("error:", err.Error())
		}

		components := strings.Split(event.Source, "/")
		containerName := components[len(components)-1]

		log.Printf("%s: %s\n", containerName, event.Action)
		log.Println(jsonifyPretty(event))

		switch event.Action {
		case "container-created":
			err := clusterState.add(containerName, clusterInfo.ServerName)
			if err != nil {
				log.Fatalln("error:", err.Error())
			}
			clusterState.logManagedContainers(clusterInfo.ServerName)
			return
		case "container-deleted":
			err := clusterState.remove(containerName, clusterInfo.ServerName)
			if err != nil {
				log.Fatalln("error:", err.Error())
			}
			clusterState.logManagedContainers(clusterInfo.ServerName)
			return
		case "container-renamed":
			newContainerName, ok := event.Context["new_name"].(string)
			if !ok {
				log.Fatalln("\"new_name\" key in event.Context not found")
			}
			err := clusterState.rename(containerName, newContainerName)
			if err != nil {
				log.Fatalln("error:", err.Error())
			}
			clusterState.logManagedContainers(clusterInfo.ServerName)
			return
		default:
		}

		server, err := clusterState.query(containerName)
		if err != nil {
			log.Fatalln("error:", err.Error())
		}
		if server != clusterInfo.ServerName {
			log.Printf("Container %s belongs to %s, ignoring\n", containerName, server)
			return
		}

		switch event.Action {
		case "container-started":
			log.Println("Attaching GPU to container")
			//state.requestGpu(c, containerName)
		case "container-shutdown":
			log.Println("Releasing GPU from container")
			//state.releaseGpu(c, containerName)
		default:
			return
		}
	})

	log.Println("Going to sleep...")
	select {}
}
