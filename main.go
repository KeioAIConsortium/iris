package main

import (
	//  "fmt"
	"encoding/json"
	"log"
	"strings"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"

	lxd "github.com/lxc/lxd/client"
	api "github.com/lxc/lxd/shared/api"
)

func jsonifyPretty(value interface{}) string {
	jsonValue, _ := json.MarshalIndent(value, "", "  ")
	return string(jsonValue)
}

func main() {
	err := nvml.Init()
	if err != nil {
		log.Fatalln("NVML error:", err.Error())
	}
	defer nvml.Shutdown()

	count, err := nvml.GetDeviceCount()
	if err != nil {
		log.Fatalln("Error getting device count:", err.Error())
	}

	var devices []nvml.Device
	for i := uint(0); i < count; i++ {
		device, err := nvml.NewDevice(i)
		if err != nil {
			log.Fatalln("NVML error:", err.Error())
		}
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

	e.AddHandler([]string{"lifecycle"}, func(e api.Event) {
		event := &api.EventLifecycle{}
		err := json.Unmarshal(e.Metadata, event)
		if err != nil {
			log.Fatalln("error:", err.Error())
		}

		components := strings.Split(event.Source, "/")
		containerName := components[len(components)-1]

		log.Printf("%s: %s\n", containerName, event.Action)

		if event.Action != "container-started" && event.Action != "container-shutdown" {
			return
		}
	})

	log.Println("Going to sleep...")
	select {}
}
