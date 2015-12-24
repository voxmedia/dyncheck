package main

import (
	"bytes"
	"fmt"
	"github.com/nesv/go-dynect/dynect"
	"github.com/nlopes/slack"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"log"
	"os"
	"regexp"
	"strings"
	"text/template"
)

type Config struct {
	Customer       string
	Username       string
	Password       string
	MinTTL         int `yaml:"minTTL"`
	Verbose        bool
	PrintResults   bool   `yaml:"print_results"`
	PrintZoneResults   bool   `yaml:"print_zone_results"`
	SlackResults   bool   `yaml:"slack_results"`
	SlackToken     string `yaml:"slack_token"`
	SlackChannelID string `yaml:"slack_channel_id"`
}

type Status struct {
	Data map[string]int
}

type tmplData struct {
	SlackToken string
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

	var t tmplData
	parsedConfig := new(bytes.Buffer)
	t.SlackToken = os.Getenv("OPSBOT_SLACK_TOKEN")
	tmpl, err := template.ParseFiles(configFile)
	mustCheck(err)
	err = tmpl.Execute(parsedConfig, t)
	mustCheck(err)

	var conf Config

	err = yaml.Unmarshal(parsedConfig.Bytes(), &conf)
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
	redirect := regexp.MustCompile(`216\.146\..*`)
	cname_regex := regexp.MustCompile(`/CNAME`)
	a_record_regex := regexp.MustCompile(`/ARecord`)
  zone_extractor_regex := regexp.MustCompile(`([^/]+)/\d+$`)
  a_records := make(map[string][]string)
  cname_records := make(map[string][]string)

	counter := 0.0
	total := len(zones.Data)

	for _, zone := range zones.Data {

		// zones returns a full path lile /REST/zone/<zone name> so we trim it
		uri := strings.TrimPrefix(zone, "/REST/Zone/")

		// first we check if we have a current serial to compare
		var zoneData dynect.ZoneResponse
		err = client.Do("GET", "Zone/"+uri, nil, &zoneData)
		check(err)

		log.Printf("%s %6.2f%% done", zoneData.Data.Zone, (counter/float64(total))*100.0)

		serial, present := status.Data[zoneData.Data.Zone]

		if present && serial == zoneData.Data.Serial {
			if conf.Verbose {
				log.Printf("Skipping %s", zoneData.Data.Zone)
			}
			statusToSave.Data[zoneData.Data.Zone] = serial
			//continue
		}


		// get all records and check the TTL on each one
		var records dynect.AllRecordsResponse
		err = client.Do("GET", "AllRecord/"+uri, nil, &records)
		check(err)

		failed := 0
		for _, record := range records.Data {
			recData := strings.TrimPrefix(record, "/REST/")

			var node dynect.RecordResponse

      // only test A Records and CNAME Records
			if cname_regex.MatchString(record) || a_record_regex.MatchString(record) {
        //log.Println(record)
        err = client.Do("GET", recData, nil, &node)
        check(err)
        zone_string := zone_extractor_regex.FindStringSubmatch(record)[1]
        address := node.Data.RData.Address
        // match the A record address with known dynect IPs
        // this is a hacky way to check if a record is a HTTP Redirect service and must be ignored
        if redirect.MatchString(address) {
          continue
        }

        if cname_regex.MatchString(record) {
          cname_address := node.Data.RData.CName
          cname_records[cname_address] = append(cname_records[cname_address],zone_string)
          //log.Println(fmt.Sprintf("CNAME: %s",cname_address))
        } else if a_record_regex.MatchString(record) {
          a_records[address] = append(a_records[address], zone_string)
          //log.Println(fmt.Sprintf("ARECORD: %s",address))
        }


        if node.Data.TTL < conf.MinTTL {
          offendingNodes = append(offendingNodes, node)
          failed++
        }

      } else {
        if conf.Verbose {
          log.Printf("skipping %s", record)
        }
      }

      if failed == 0 {
        statusToSave.Data[zoneData.Data.Zone] = zoneData.Data.Serial
      }
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




	var textSlice []string
	// decide what to do with results
	if len(offendingNodes) > 0 {
		textSlice = append(textSlice, fmt.Sprintf("Those nodes have TTLs lower than %d", conf.MinTTL))
		for _, n := range offendingNodes {
			textSlice = append(textSlice, fmt.Sprintf("%s", n.Data.FQDN))
		}
	}



  if cont.PrintZoneResults == true {
    textSlice = append(textSlice, fmt.Sprintf("%s", "CNAMES"))
    for k, zones := range cname_records {
      textSlice = append(textSlice, fmt.Sprintf("\n%s", k))
      for _, zone := range(zones) {
        textSlice = append(textSlice, fmt.Sprintf("\t%s", zone))
      }
    }

    textSlice = append(textSlice, fmt.Sprintf("%s", "ARECORDS"))
    for k, zones := range a_records {
      textSlice = append(textSlice, fmt.Sprintf("\n%s", k))
      for _, zone := range(zones) {
        textSlice = append(textSlice, fmt.Sprintf("\t%s", zone))
      }
    }
  }

  text := strings.Join(textSlice, "\n")

  if conf.PrintResults == true {
    log.Println("printing results")
    printResults(text)
  }

  if conf.SlackResults == true {
    log.Println("sending to slack")
    err = slackResults(text, conf)
    check(err)
  }
}
