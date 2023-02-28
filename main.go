package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"

	integrase "github.com/MetroReviews/metro-integrase/lib"
	"github.com/MetroReviews/metro-integrase/types"
	"github.com/infinitybotlist/eureka/crypto"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

type MetroIBLAdapter struct {
	Pool    *pgxpool.Pool
	Context context.Context
}

func (adp MetroIBLAdapter) GetConfig() types.ListConfig {
	return types.ListConfig{
		SecretKey:   os.Getenv("SECRET_KEY"),
		ListID:      os.Getenv("LIST_ID"),
		StartupLogs: true,
		DomainName:  "https://metro-v4.infinitybots.xyz",
	}
}

type link struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// From popplio-v2 utils.go with redundant code removed
func validateExtraLinks(links []link) error {
	for _, link := range links {
		if len(link.Name) > 64 || len(link.Value) > 512 {
			return errors.New("one of your links has a name/value that is too long")
		}

		if strings.ReplaceAll(link.Name, " ", "") == "" || strings.ReplaceAll(link.Value, " ", "") == "" {
			return errors.New("one of your links has a name/value that is empty")
		}

		if !strings.HasPrefix(link.Value, "https://") {
			return errors.New("extra link '" + link.Name + "' must be HTTPS")
		}

		for _, ch := range link.Name {
			allowedChars := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_ "

			if !strings.ContainsRune(allowedChars, ch) {
				return errors.New("extra link '" + link.Name + "' has an invalid character: " + string(ch))
			}
		}
	}

	return nil
}

type addBot struct {
	BotID            string   `db:"bot_id"`
	QueueName        string   `db:"queue_name"`
	ClientID         string   `db:"client_id"`
	Tags             []string `db:"tags"`
	Prefix           string   `db:"prefix"`
	Owner            string   `db:"owner"`
	Short            string   `db:"short"`
	Long             string   `db:"long"`
	Invite           string   `db:"invite"`
	Library          string   `db:"library"`
	ExtraLinks       []link   `db:"extra_links"`
	Vanity           string   `db:"vanity"`
	Banner           *string  `db:"banner"`
	CrossAdd         bool     `db:"cross_add"`
	Note             string   `db:"note"`
	Token            string   `db:"token"`
	ListSource       string   `db:"list_source"`
	ExternalSource   string   `db:"external_source"`
}

func (a addBot) ResolveToSQL() (string, []any) {
	var colNames []string
	var colNums []string
	var args []any

	for i, field := range reflect.VisibleFields(reflect.TypeOf(a)) {
		// Get value of field from reflect.StructField
		value := reflect.ValueOf(a).FieldByName(field.Name)
		args = append(args, value.Interface())
		colNames = append(colNames, field.Name)
		colNums = append(colNums, "$"+strconv.Itoa(i+1))
	}

	return "INSERT INTO bots (" + strings.Join(colNames, ", ") + ") VALUES (" + strings.Join(colNums, ", ") + ")", args
}

func (adp MetroIBLAdapter) addBot(bot *types.FullBot) error {
	prefix := bot.Prefix

	if prefix == "" {
		prefix = "/"
	}

	invite := bot.Invite

	if invite == "" {
		invite = "https://discord.com/oauth2/authorize?client_id=" + bot.BotID + "&permissions=0&scope=bot%20applications.commands"
	}

	var banner *string

	if bot.Banner != "" {
		banner = &bot.Banner
	}

	extraLinks := []link{
		{
			Name:  "Website",
			Value: bot.Website,
		},
		{
			Name:  "Support",
			Value: bot.Support,
		},
		{
			Name:  "Donate",
			Value: bot.Donate,
		},
	}

	err := validateExtraLinks(extraLinks)

	if err != nil {
		return err
	}

	sql, args := addBot{
		BotID:            bot.BotID,
		QueueName:        bot.Username,
		ClientID:         bot.BotID, // We assume to be equal when adding, our script will update this if needed (usually not required)
		Tags:             bot.Tags,
		Prefix:           prefix,
		Owner:            bot.Owner,
		Short:            bot.Description,
		Long:             bot.LongDescription,
		Invite:           invite,
		Library:          bot.Library,
		ExtraLinks:       extraLinks,
		Vanity:           crypto.RandString(32),
		Banner:           banner,
		CrossAdd:         bot.CrossAdd,
		Note:             bot.ReviewNote,
		Token:            crypto.RandString(101),
		ExternalSource:   "metro",
		ListSource:       bot.ListSource,
	}.ResolveToSQL()

	_, err = adp.Pool.Exec(
		adp.Context,
		sql,
		args...,
	)

	if err != nil {
		return err
	}

	return nil
}

func (adp MetroIBLAdapter) baseCode(b *types.Bot) error {
	// Check if bot currently exists
	var exists bool

	err := adp.Pool.QueryRow(adp.Context, "SELECT EXISTS(SELECT 1 FROM bots WHERE bot_id = $1)", b.BotID).Scan(&exists)

	if err != nil {
		return err
	}

	// Add if not exists (if we can add)
	if !exists {
		if !b.CanAdd {
			return errors.New("cannot add this bot due to can_add being false")
		}

		bot, err := b.Resolve()

		if err != nil {
			return err
		}

		return adp.addBot(bot)
	}

	adp.Pool.Exec(adp.Context, "UPDATE bots SET claimed_by = $1 WHERE bot_id = $2", b.Reviewer, b.BotID)

	if err != nil {
		return err
	}

	return nil
}

func (adp MetroIBLAdapter) ApproveBot(b *types.Bot) error {
	fmt.Println("/approve =>", b.BotID)
	err := adp.baseCode(b)

	if err != nil {
		return err
	}

	// Set bot type to approved
	_, err = adp.Pool.Exec(adp.Context, "UPDATE bots SET type = 'approved' WHERE bot_id = $1", b.BotID)

	if err != nil {
		return err
	}

	return nil
}

func (adp MetroIBLAdapter) DenyBot(b *types.Bot) error {
	fmt.Println("/deny =>", b.BotID)
	err := adp.baseCode(b)

	if err != nil {
		return err
	}

	// Set bot type to denied
	_, err = adp.Pool.Exec(adp.Context, "UPDATE bots SET type = 'denied' WHERE bot_id = $1", b.BotID)

	if err != nil {
		return err
	}

	return nil
}

func (adp MetroIBLAdapter) ClaimBot(b *types.Bot) error {
	fmt.Println("/claim =>", b.BotID)
	err := adp.baseCode(b)

	if err != nil {
		return err
	}

	// Set bot type to claimed
	_, err = adp.Pool.Exec(adp.Context, "UPDATE bots SET type = 'claimed', last_claimed = NOW() WHERE bot_id = $1 AND type = 'pending'", b.BotID)

	if err != nil {
		return err
	}

	return nil
}

func (adp MetroIBLAdapter) UnclaimBot(b *types.Bot) error {
	fmt.Println("/unclaim =>", b.BotID)
	err := adp.baseCode(b)

	if err != nil {
		return err
	}

	// Set bot type to pending
	_, err = adp.Pool.Exec(adp.Context, "UPDATE bots SET type = 'pending' WHERE bot_id = $1 AND type = 'claimed'", b.BotID)

	if err != nil {
		return err
	}

	return nil
}

func main() {
	godotenv.Load()

	var connUrl string
	var redisUrl string

	flag.StringVar(&connUrl, "db", "postgresql:///infinity", "Database connection URL")
	flag.StringVar(&redisUrl, "redis", "redis://localhost:6379", "Redis connection URL")
	flag.Parse()

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, connUrl)

	if err != nil {
		panic(err)
	}

	r := http.NewServeMux()

	integrase.Prepare(MetroIBLAdapter{
		Pool:    pool,
		Context: ctx,
	}, r)

	http.ListenAndServe(":6821", r)
}
