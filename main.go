package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"

	lxd "github.com/lxc/lxd/client"
	api "github.com/lxc/lxd/shared/api"
)

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

	e, err := c.GetEvents()
	if err != nil {
		log.Fatalln("LXD error:", err.Error())
	}

	containers := make([]string, len(devices), len(devices))
	for i := 0; i < len(devices); i++ {
		containers[i] = ""
	}

	state := State{
		devices:    devices,
		containers: containers,
		lock:       sync.RWMutex{},
	}

	e.AddHandler([]string{"lifecycle"}, func(e api.Event) {
		event := &api.EventLifecycle{}
		err := json.Unmarshal(e.Metadata, event)
		if err != nil {
			log.Fatalln("error:", err.Error())
		}

		components := strings.Split(event.Source, "/")
		containerName := components[len(components)-1]

		log.Printf("%s: %s\n", containerName, event.Action)

		switch event.Action {
		case "container-started":
			state.requestGpu(c, containerName)
		case "container-shutdown":
			state.releaseGpu(c, containerName)
		default:
			return
		}
	})

	log.Println("Going to sleep...")
	select {}
}
