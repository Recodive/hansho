// Pair a user with the Hansho app and send them a page.
//
//	HANSHO_HUB_URL=https://hub.example.com HANSHO_API_KEY=ck_... go run . user-42
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	hansho "github.com/recodive/hansho/sdk/go"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := hansho.New(os.Getenv("HANSHO_HUB_URL"), os.Getenv("HANSHO_API_KEY"))
	user := os.Args[1]

	err := c.PutChannel(ctx, "oncall", hansho.Channel{Name: "On call", DefaultLevel: hansho.LevelActive, MaxLevel: hansho.LevelPage})
	check(err)
	ticket, err := c.CreatePairingTicket(ctx, user)
	check(err)
	fmt.Println("Render this as a QR code and scan it in the Hansho app:\n", ticket.QR)
	fmt.Print("Press Enter once the app has paired... ")
	fmt.Scanln()

	res, err := c.SendMessage(ctx, hansho.Message{
		ExternalUserID: user, Channel: "oncall", Level: hansho.LevelPage,
		Title: "Primary DB down", Body: "Failover did not complete",
	})
	check(err)
	fmt.Println("sent", res.MessageID, "to", res.Devices, "device(s); it repeats until acknowledged")
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
