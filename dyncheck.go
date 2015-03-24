package main

import (
	"fmt"
	"github.com/nesv/go-dynect/dynect"
	"github.com/nlopes/slack"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"log"
	"os"
	"regexp"
	"strings"
)

type Config struct {
	Customer       string
	Username       string
	Password       string
	MinTTL         int `yaml:"minTTL"`
	Verbose        bool
	PrintResults   bool   `yaml:"print_results"`
	SlackResults   bool   `yaml:"slack_results"`
	SlackToken     string `yaml:"slack_token"`
	SlackChannelID string `yaml:"slack_channel_id"`
}

type Status struct {
	Data map[string]int
}

func check(err error) {
	if err != nil {
		log.Println(err)
	}
}

func mustCheck(err error) {
	if err != nil {
		log.Panic(err)
	}
}

func newStatus() *Status {
	return &Status{Data: make(map[string]int)}
}

func printResults(text string) {
	fmt.Println(text)
}

func slackResults(text string, conf Config) (err error) {
	api := slack.New(conf.SlackToken)
	params := slack.PostMessageParameters{}
	a, b, err := api.PostMessage(conf.SlackChannelID, text, params)
	fmt.Printf("%v %v %v", a, b, err)
	return
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
	if err != nil {
		log.Println("No status file found, a new one will be created.")
		savedStatus = []byte("data: {}")
	}
	err = yaml.Unmarshal(savedStatus, &status)
	mustCheck(err)

	client := dynect.NewClient(conf.Customer)
	client.Verbose(conf.Verbose)
	err = client.Login(conf.Username, conf.Password)
	mustCheck(err)

	var zones dynect.ZonesResponse
	for i := 0; i < 5; i++ {
		err = client.Do("GET", "Zone", nil, &zones)
		if err == nil {
			log.Printf("Got Zone list")
			break
		}
		log.Printf("Retrying Zone GET.. %s/5", i+1)
		if i == 4 {
			log.Println("Failed to get zones:")
			log.Panic(err)
		}
	}

	// this will be used down there but we compile it here to avoid compiling the RE every loop
	re := regexp.MustCompile(`216\.146\..*`)

	counter := 0.0
	total := len(zones.Data)
	for _, zone := range zones.Data {

		// zones returns a full path lile /REST/zone/<zone name> so we trim it
		uri := strings.TrimPrefix(zone, "/REST/Zone/")

		// first we check if we have a current serial to compare
		var zoneData dynect.ZoneResponse
		err = client.Do("GET", "Zone/"+uri, nil, &zoneData)
		check(err)

		log.Printf("%6.2f%% done", (counter/float64(total))*100.0)

		serial, present := status.Data[zoneData.Data.Zone]
		if present && serial == zoneData.Data.Serial {
			if conf.Verbose {
				log.Printf("Skipping %s", zoneData.Data.Zone)
			}
			statusToSave.Data[zoneData.Data.Zone] = serial
			continue
		}

		// get all records and check the TTL on each one
		var records dynect.AllRecordsResponse
		err = client.Do("GET", "AllRecord/"+uri, nil, &records)
		check(err)

		failed := 0
		for _, record := range records.Data {
			recData := strings.TrimPrefix(record, "/REST/")

			var node dynect.RecordResponse
			err = client.Do("GET", recData, nil, &node)
			check(err)

			// match the A record address with known dynect IPs
			// this is a hacky way to check if a record is a HTTP Redirect service and must be ignored
			if re.MatchString(node.Data.RData.Address) {
				continue
			}

			if node.Data.TTL < conf.MinTTL {
				offendingNodes = append(offendingNodes, node)
				failed++
			}
		}
		if failed == 0 {
			statusToSave.Data[zoneData.Data.Zone] = zoneData.Data.Serial
		}
		counter++
	}

	// now we save the new status using a tempfile
	log.Print("Saving status file...")
	tempFile, err := ioutil.TempFile(os.TempDir(), "dyncheck")
	mustCheck(err)
	toWrite, err := yaml.Marshal(statusToSave)
	mustCheck(err)
	_, err = tempFile.Write(toWrite)
	mustCheck(err)
	err = os.Rename(tempFile.Name(), statusFile)
	mustCheck(err)
	log.Println("done")

	// decide what to do with results
	if len(offendingNodes) > 0 {
		var textSlice []string
		textSlice = append(textSlice, fmt.Sprintf("Those nodes have TTLs lower than %d", conf.MinTTL))
		for _, n := range offendingNodes {
			textSlice = append(textSlice, fmt.Sprintf("%s", n.Data.FQDN))
		}
		text := strings.Join(textSlice, "\n")

		switch {
		case conf.PrintResults == true:
			printResults(text)
		case conf.SlackResults == true:
			err = slackResults(text, conf)
			check(err)
		}
	}
}
