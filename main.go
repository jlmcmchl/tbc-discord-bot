package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gin-gonic/gin"
)

var (
	token       string
	authKey     string
	tRegex      = regexp.MustCompile("\\[\\[(?:(\\d+)(?:@(\\w+))?)\\]\\]")
	pRegex      = regexp.MustCompile("")
	nameRegex   = `(\w+)`
	urlRegex    = `([-a-zA-Z0-9@:%._\+~#=]{2,256}\.[a-z]{2,6}\b[-a-zA-Z0-9@:%_\+.~#?&//=]*)`
	roundsRegex = `(\d+)`
	dateRegex   = `(\d\d\/\d\d@\d\d:\d\d)`
	draftRegex  = regexp.MustCompile("(?m:Name: " + nameRegex + "\nTeams: " + urlRegex + "\nRounds: " + roundsRegex + "\nDate: " + dateRegex + ")")
	dateTimeFmt = "01/02@15:04"
	tbaHeader   http.Header
)

func makeRequest(method, url string) (out []byte, err error) {
	req, err := http.NewRequest(method, url, strings.NewReader(""))
	if err != nil {
		return []byte(""), err
	}

	req.Header = tbaHeader

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return []byte(""), err
	}

	defer resp.Body.Close()
	return ioutil.ReadAll(resp.Body)
}

func determineEvent(team string, year int) (event_code, event string) {
	var err error
	data, err := makeRequest(http.MethodGet,
		fmt.Sprintf("https://www.thebluealliance.com/api/v3/team/frc%s/events/%d/simple",
			team, year))
	if err != nil {
		log.Printf("%#v\n", err)
	}

	var parsed []map[string]interface{}
	err = json.Unmarshal(data, &parsed)
	if err != nil {
		log.Printf("%#v\n", err)
	}

	for _, event := range parsed {
		start, err := time.Parse("2006-01-02", event["start_date"].(string))
		if err != nil {
			log.Printf("%#v\n", err)
		}
		end, err := time.Parse("2006-01-02", event["end_date"].(string))
		if err != nil {
			log.Printf("%#v\n", err)
		}
		end.Add(24 * time.Hour)

		if start.Before(time.Now()) && time.Now().Before(end) {
			return event["event_code"].(string), event["name"].(string)
		}
	}

	// No current event, get most recent even this year
	var currCode string
	var currName string
	currTime := time.Now().Add(-365 * 24 * time.Hour)
	for _, event := range parsed {
		end, err := time.Parse("2006-01-02", event["end_date"].(string))
		if err != nil {
			log.Printf("%#v\n", err)
		}
		end.Add(24 * time.Hour)

		if end.Before(time.Now()) && currTime.Before(end) {
			currCode = event["event_code"].(string)
			currName = event["name"].(string)
			currTime = end
		}
	}

	return currCode, currName
}

func getTeamEventStatus(team, event string, year int) string {
	var err error
	data, err := makeRequest(http.MethodGet,
		fmt.Sprintf("https://www.thebluealliance.com/api/v3/team/frc%s/event/%d%s/status",
			team, year, event))
	if err != nil {
		log.Printf("%#v\n", err)
	}

	if bytes.Compare([]byte("null"), data) == 0 {
		return "It looks like the event hasn't started, or there's no updates from TBA. Try again later on in the event for status updates!"
	}

	var parsed map[string]interface{}
	err = json.Unmarshal(data, &parsed)
	if err != nil {
		log.Printf("%#v\n", err)
	}

	if _, ok := parsed["Errors"]; ok {
		return "***Come on joe, you know better.***"
	}

	return parsed["overall_status_str"].(string)
}

func teamStatus(dg *discordgo.Session, msg *discordgo.MessageCreate) {
	if msg.Author.ID == dg.State.User.ID {
		return
	}

	for _, match := range tRegex.FindAllStringSubmatch(msg.Content, -1) {
		var event string
		var eventCode string
		if match[2] == "" { // figure out event to report on
			eventCode, event = determineEvent(match[1], time.Now().Year())
		}

		var status string
		if eventCode == "" {
			status = "***Come on Joe, you know better.***"
		} else {
			status = fmt.Sprintf("At %s, %s", event, getTeamEventStatus(match[1], eventCode, time.Now().Year()))
		}

		status = strings.Replace(status, "<b>", "**", -1)
		status = strings.Replace(status, "</b>", "**", -1)
		dg.ChannelMessageSend(msg.ChannelID, status)
	}
}

func draftProposal(dg *discordgo.Session, msg *discordgo.MessageCreate) {
	if msg.Author.ID == dg.State.User.ID {
		return
	}

	prop := draftRegex.FindStringSubmatch(msg.Content)
	if prop == nil {
		return
	}
	log.Printf("%#v\n", prop)

}

func setupDiscord() {
	var err error

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatal(err)
	}

	dg.AddHandler(teamStatus)
	dg.AddHandler(draftProposal)

	err = dg.Open()
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	token = os.Getenv("TOKEN")
	port := os.Getenv("PORT")
	authKey = os.Getenv("XTBAAUTHKEY")
	tbaHeader = make(http.Header)
	tbaHeader.Add("X-TBA-Auth-Key", authKey)

	if port == "" {
		log.Fatal("$PORT must be set")
	}
	if port == "" {
		log.Fatal("$TOKEN must be set")
	}
	if port == "" {
		log.Fatal("$XTBAAUTHKEY must be set")
	}

	router := gin.New()
	router.Use(gin.Logger())
	router.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, string("Hello World!"))
	})

	go setupDiscord()

	router.Run(":" + port)
}
