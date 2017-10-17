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
	"strconv"
	"strings"
	"time"

	"database/sql"

	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/stdlib"
	"github.com/robfig/cron"
)

var (
	token         string
	authKey       string
	tRegex        = regexp.MustCompile("\\[\\[(?:(\\d+)(?:@(\\w+))?)\\]\\]")
	pRegex        = regexp.MustCompile("")
	nameRegex     = `(\w+)`
	urlRegex      = `([-a-zA-Z0-9@:%._\/\+~#=]{2,256}\.[a-z]{2,6}\b[-a-zA-Z0-9@:%_\+.~#?&//=]*)`
	roundsRegex   = `(\d+)`
	dateRegex     = `(\d\d\/\d\d@\d\d:\d\d)`
	draftRegex    = regexp.MustCompile("(?m:Name: " + nameRegex + "\nTeams: " + urlRegex + "\nRounds: " + roundsRegex + "\nDate: " + dateRegex + ")")
	dateTimeFmt   = "01/02@15:04"
	tbaHeader     http.Header
	insertDraft   = "INSERT INTO Drafts (Name, Teams, Rounds, Date, Guild, Orig_ch, Msg) VALUES ($1, $2, $3, $4, $5, $6, $7)"
	insertDateFmt = "2006-01-02 15:04:05"
	mutex         = &sync.Mutex{}
	db            *sql.DB
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

func determineEvent(team string, year int) (string, string) {
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

	mutex.Lock()
	defer mutex.Unlock()

	rounds, err := strconv.ParseInt(prop[3], 10, 64)
	if err != nil {
		log.Println(err)
	}

	dt, err := time.Parse(dateTimeFmt, prop[4])
	if err != nil {
		log.Println(err)
	}

	dt = dt.AddDate(time.Now().Year(), 0, 0)

	ch, err := dg.Channel(msg.ChannelID)
	if err != nil {
		log.Fatal(err)
	}

	guild := ch.GuildID

	log.Println(prop[1], prop[2], rounds, dt, guild)

	_, err = db.Exec(insertDraft, prop[1], prop[2], rounds, dt, guild, msg.ChannelID, msg.ID)
	if err != nil {
		log.Println(err)
	}
}

func getDrafts() {
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		log.Fatal(err)
	}

	err = dg.Open()
	if err != nil {
		log.Fatal(err)
	}

	today := time.Now()
	tomorrow := today.AddDate(0, 0, 1)
	log.Println(today, tomorrow)
	rows, err := db.Query("SELECT Draft_Key, Name, Teams, Rounds, Date, Guild, Orig_ch, Msg FROM Drafts WHERE COALESCE(Channel, '') = '' AND Date BETWEEN $1 AND $2", today, tomorrow)
	if err != nil {
		log.Fatal(err)
	}

	for rows.Next() {
		var key int
		var name string
		var teams string
		var rounds int
		var date time.Time
		var guild string
		var orig_ch string
		var msgID string

		err = rows.Scan(&key, &name, &teams, &rounds, &date, &guild, &orig_ch, &msgID)
		if err != nil {
			log.Fatal(err)
		}

		log.Println(key, name, teams, rounds, date, guild)

		name = strings.Replace(strings.TrimSpace(name), " ", "-", -1)
		ch, err := dg.GuildChannelCreate(guild, "draft-"+name, "0")
		if err != nil {
			log.Fatal(err)
		}

		_, err = db.Exec("UPDATE Drafts SET Channel = $1 WHERE Draft_Key = $2", ch.ID, key)
		if err != nil {
			log.Fatal(err)
		}

		role, err := dg.GuildRoleCreate(guild)
		if err != nil {
			log.Fatal(err)
		}

		role, err = dg.GuildRoleEdit(guild, role.ID, name+" Drafter", role.Color, false, role.Permissions, true)
		if err != nil {
			log.Fatal(err)
		}

		message, err := dg.ChannelMessage(orig_ch, msgID)
		if err != nil {
			log.Fatal(err)
		}
		for _, reaction := range message.Reactions {
			users, err := dg.MessageReactions(orig_ch, msgID, reaction.Emoji.ID, 100)
			if err != nil {
				log.Fatal(err)
			}

			for _, user := range users {
				err = dg.GuildMemberRoleAdd(guild, user.ID, role.ID)
				if err != nil {
					log.Fatal(err)
				}
			}
		}

	}
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
	if token == "" {
		log.Fatal("$TOKEN must be set")
	}
	if authKey == "" {
		log.Fatal("$XTBAAUTHKEY must be set")
	}

	var err error
	db, err = sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}

	c := cron.New()
	c.AddFunc("@midnight", getDrafts)
	go c.Run()

	router := gin.New()
	router.Use(gin.Logger())
	router.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, string("Hello World!"))
	})

	router.GET("/getDrafts", func(c *gin.Context) {
		go getDrafts()
		c.String(http.StatusOK, string("Pulling Drafts..."))
	})

	go setupDiscord()

	router.Run(":" + port)
}
