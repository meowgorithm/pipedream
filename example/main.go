package main

import (
	"fmt"
	"os"

	"github.com/meowgorithm/pipedream"
)

func main() {

	m := pipedream.MultipartUpload{
		AccessKey: os.Getenv("ACCESS_KEY"),
		SecretKey: os.Getenv("SECRET_KEY"),
		Endpoint:  "sfo2.digitaloceanspaces.com", // you could use Region for AWS
		Bucket:    "my-fave-bucket",
	}

	// Get an io.Reader
	f, err := os.Open("big-redis-dump.rdb")
	if err != nil {
		fmt.Printf("Rats: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	// Send it up! Pipdream returns a channel where you can listen for events.
	ch := m.Send(f, "backups/dump.rdb")
	done := make(chan struct{})

	// Listen for activity. For more detailed reporting, see the docs.
	go func() {
		for {
			e := <-ch
			switch e.(type) {
			case pipedream.Complete:
				fmt.Println("It worked!")
				close(done)
				return
			case pipedream.Error:
				fmt.Println("Rats, it didn't work.")
				close(done)
				return
			}
		}
	}()

	<-done
}
