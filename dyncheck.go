package main

import (
	"fmt"
	"github.com/nesv/go-dynect/dynect"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"os"
	"strings"
)

type Config struct {
	Customer string
	Username string
	Password string
	MinTTL   int `yaml:"minTTL"`
	Verbose  bool
}

type Status struct {
	Data map[string]int
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func mustCheck(err error) {
	if err != nil {
		panic(err)
	}
}

func newStatus() *Status {
	return &Status{Data: make(map[string]int)}
}

func main() {

	if len(os.Args) != 3 {
		fmt.Println("Usage: dyncheck <config file> <status file>")
		os.Exit(2)
	}

	configFile := os.Args[1]
	statusFile := os.Args[2]

	var offendingNodes []dynect.RecordResponse

	data, err := ioutil.ReadFile(configFile)
	mustCheck(err)
	var conf Config
	err = yaml.Unmarshal(data, &conf)
	mustCheck(err)

	status := newStatus()
	statusToSave := newStatus()

	savedStatus, err := ioutil.ReadFile(statusFile)
	mustCheck(err)
	err = yaml.Unmarshal(savedStatus, &status)
	mustCheck(err)

	// status := newStatus()
	// status.Data["test"] = 10
	// toWrite, err := yaml.Marshal(status)
	// mustCheck(err)
	// err = ioutil.WriteFile("status.yaml", toWrite, os.FileMode(int(0755)))
	// mustCheck(err)

	client := dynect.NewClient(conf.Customer)
	client.Verbose(conf.Verbose)
	err = client.Login(conf.Username, conf.Password)
	mustCheck(err)

	var zones dynect.ZonesResponse
	err = client.Do("GET", "Zone", nil, &zones)
	mustCheck(err)

	for i, zone := range zones.Data {

		fmt.Println(i)
		if i > 5 {
			break
		}

		// zones returns a full path lile /REST/zone/<zone name> so we trim it
		uri := strings.TrimPrefix(zone, "/REST/Zone/")

		// first we check if we have a current serial to compare
		var zoneData dynect.ZoneResponse
		err = client.Do("GET", "Zone/"+uri, nil, &zoneData)
		check(err)

		serial, present := status.Data[zoneData.Data.Zone]
		if present && serial == zoneData.Data.Serial {
			if conf.Verbose {
				fmt.Println("Skipping")
			}
			statusToSave.Data[zoneData.Data.Zone] = serial
			continue
		}
		statusToSave.Data[zoneData.Data.Zone] = zoneData.Data.Serial

		var records dynect.AllRecordsResponse
		err = client.Do("GET", "AllRecord/"+uri, nil, &records)
		check(err)

		for _, record := range records.Data {
			recData := strings.TrimPrefix(record, "/REST/")

			var node dynect.RecordResponse
			err = client.Do("GET", recData, nil, &node)
			check(err)

			if node.Data.TTL < conf.MinTTL {
				offendingNodes = append(offendingNodes, node)
			}
		}
	}

	// now we save the new status using a tempfile
	tempFile, err := ioutil.TempFile(os.TempDir(), "dyncheck")
	mustCheck(err)
	toWrite, err := yaml.Marshal(statusToSave)
	mustCheck(err)
	_, err = tempFile.Write(toWrite)
	mustCheck(err)
	err = os.Rename(tempFile.Name(), statusFile)
	mustCheck(err)

	// print the results
	if len(offendingNodes) > 0 {
		fmt.Printf("Those nodes have TTLs lower than %d\n", conf.MinTTL)
		for _, n := range offendingNodes {
			fmt.Printf("%s\n", n.Data.FQDN)
		}
	}
}
