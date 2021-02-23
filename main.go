package main

import (
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/big"
	"math/rand"
	"net/http"
	"strings"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"
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

type Response struct {
	Pci string `json:"pci"`
}

var randSource = NewRandSource()

func NewRandSource() *rand.Rand {
	seed, _ := crand.Int(crand.Reader, big.NewInt(math.MaxInt64))
	return rand.New(rand.NewSource(seed.Int64()))
}

func getNotUsedGpuPciAddress(devices []nvml.Device) (string, error) {
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
		da = deviceAddresses[randSource.Int()%len(deviceAddresses)]
	}

	res := &Response{Pci: da}

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

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ret, err := getNotUsedGpuPciAddress(devices)

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
