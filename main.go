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

type Channel struct {
	ID      string
	Name    string
	Guild   string
	LastMsg string
}

var (
	token     string
	authKey   string
	channels  []Channel
	tRegex    = regexp.MustCompile("\\[\\[(?:(\\d+)(?:@(\\w+))?)\\]\\]")
	tbaHeader http.Header
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

	return parsed["overall_status_str"].(string)
}

func processMessage(dg *discordgo.Session, msg *discordgo.Message) {
	for _, match := range tRegex.FindAllStringSubmatch(msg.Content, -1) {
		var event string
		var eventCode string
		if match[2] == "" { // figure out event to report on
			eventCode, event = determineEvent(match[1], time.Now().Year())
		}

		status := fmt.Sprintf("At %s, %s", event, getTeamEventStatus(match[1], eventCode, time.Now().Year()))
		status = strings.Replace(status, "<b>", "**", -1)
		status = strings.Replace(status, "</b>", "**", -1)
		dg.ChannelMessageSend(msg.ChannelID, status)
	}
}

func discordListener() {
	var err error
	channels = make([]Channel, 0, 50)

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatal(err)
	}

	userGuilds, err := dg.UserGuilds(0, "", "")
	if err != nil {
		log.Fatal(err)
	}
	guilds := make([]string, len(userGuilds))

	for i, guild := range userGuilds {
		guilds[i] = guild.ID

		guildChannels, err := dg.GuildChannels(guild.ID)
		if err != nil {
			log.Fatal(err)
		}

		pLen := len(channels)
		channels = channels[0 : pLen+len(guildChannels)]
		for j, channel := range guildChannels {
			if channel.Bitrate > 0 || channel.Type != discordgo.ChannelTypeGuildText {
				continue
			}

			channels[pLen+j] = Channel{
				ID:      channel.ID,
				Name:    channel.Name,
				Guild:   guild.Name,
				LastMsg: channel.LastMessageID}
		}
	}

	log.Printf("%#v\n", channels)

	i := -1
	for {
		time.Sleep(50 * time.Millisecond)
		i = (i + 1) % len(channels)

		msgs, err := dg.ChannelMessages(channels[i].ID, 0, "", channels[i].LastMsg, "")
		if err != nil {
			// Pretty much means there were no new messages, so skip
			continue
		}

		ts := time.Now().Add(-1 * time.Minute)
		var id string
		for _, msg := range msgs {
			if msg.ID == channels[i].LastMsg {
				continue
			}

			tm, err := time.Parse(time.RFC3339, string(msg.Timestamp))
			if err != nil {
				log.Fatal(err)
			}

			if tm.After(ts) {
				id = msg.ID
				ts = tm
			}

			go processMessage(dg, msg)
		}

		if id != "" {
			channels[i].LastMsg = id
		}
	}
}

func main() {
	token = os.Getenv("TOKEN")
	port := os.Getenv("PORT")
	authKey = os.Getenv("XTBAAUTHKEY")
	tbaHeader = make(http.Header)
	tbaHeader.Add("X-TBA-Auth-Key", authKey)
	log.Println(token)

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

	go discordListener()

	router.Run(":" + port)
}
