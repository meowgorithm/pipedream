package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/meowgorithm/babyenv"
	"github.com/meowgorithm/pipedream"
	"github.com/muesli/reflow/indent"
	"github.com/muesli/reflow/wordwrap"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"
)

const (
	wrapAt      = 78
	errorIndent = 4
)

var (
	// Version stores the version of the application. It's set during build
	// time.
	Version = "(unknown; built from source)"

	color  = termenv.ColorProfile().Color
	check  = termenv.String("✔").Foreground(color("78")).String()
	ex     = termenv.String("✘").Foreground(color("203")).String()
	subtle = termenv.Style{}.Foreground(color("240")).Styled
	arrow  = subtle(">")

	// Flags
	endpoint    string
	region      string
	bucket      string
	remotePath  string
	maxRetries  int
	maxPartSize int
	silent      bool
	showVersion bool
)

type config struct {
	AccessKey string `env:"ACCESS_KEY,required"`
	SecretKey string `env:"SECRET_KEY,required"`
	Endpoint  string `env:"ENDPOINT" default:"s3.amazonaws.com"`
	Region    string `env:"REGION" default:"us-east-1"`
}

var rootCmd = &cobra.Command{
	Use:   "INPUT | pipedream [flags]\n  pipedream [flags] < INPUT",
	Short: "An S3 multipart uploader",
	Long:  info(),
	Args:  cobra.NoArgs,
	RunE:  run,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&endpoint, "endpoint", "e", "", "the endpoint to upload to (default \"s3.amazonaws.com\")")
	rootCmd.PersistentFlags().StringVarP(&region, "region", "r", "", "the region to use; AWS only (default \"us-east-1\")")
	rootCmd.PersistentFlags().StringVarP(&bucket, "bucket", "b", "", "the bucket/space to upload to")
	rootCmd.PersistentFlags().StringVarP(&remotePath, "path", "p", "", "the remote path at which we should put the file")
	rootCmd.PersistentFlags().IntVarP(&maxRetries, "retries", "t", 3, "the maximum number of times to retry uploading a part")
	rootCmd.PersistentFlags().IntVarP(&maxPartSize, "part-size", "m", 5, "the maximum size per part, in megabytes")
	rootCmd.PersistentFlags().BoolVarP(&silent, "silent", "s", false, "silence output, except errors")
	rootCmd.PersistentFlags().BoolVarP(&showVersion, "version", "v", false, "output version information")
}

func info() string {
	b := strings.Builder{}
	b.WriteString(wordwrap.String("A multipart uploader for Amazon S3, DigitalOcean Spaces, and S3-compatible systems.\n\n", wrapAt))
	b.WriteString("Example:\n\n")
	b.WriteString(wordwrap.String("    cat dump.rdb | gzip | pipedream -bucket backups -path dump.rdb.gz\n\n", wrapAt))
	b.WriteString(wordwrap.String("ACCESS_KEY and SECRET_KEY must be set in the environment. ENDPOINT and REGION can also be set in the environment, but corresponding flags will take precedence. Also note that if you're using AWS you don't need to set the endpoint. Conversely, if you're using DigitalOcean you don't need to set the region.\n", wrapAt))
	return b.String()
}

func run(cmd *cobra.Command, args []string) error {
	if showVersion {
		fmt.Println(Version)
		os.Exit(0)
	}

	// Get environment
	var cfg config
	if err := babyenv.Parse(&cfg); err != nil {
		return fmt.Errorf("Could not parse config: %v", err)
	}

	var missing []string

	// Validate CLI args
	if endpoint == "" && cfg.Endpoint != "" {
		endpoint = cfg.Endpoint
	} else if endpoint == "" {
		missing = append(missing, "endpoint")
	}
	if region == "" && cfg.Region != "" {
		region = cfg.Region
	} else if region == "" {
		missing = append(missing, "region")
	}
	if bucket == "" {
		missing = append(missing, "bucket")
	}
	if remotePath == "" {
		missing = append(missing, "path")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing %s", pipedream.EnglishJoin(missing, true))
	}

	// Is stdin a pipe?
	info, err := os.Stdin.Stat()
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeCharDevice != 0 || info.Size() <= 0 {
		return errors.New("input must be through a pipe")
	}

	m := pipedream.MultipartUpload{
		AccessKey:   cfg.AccessKey,
		SecretKey:   cfg.SecretKey,
		Endpoint:    endpoint,
		Region:      region,
		MaxRetries:  maxRetries,
		MaxPartSize: pipedream.Megabyte * int64(maxPartSize),
		Bucket:      bucket,
	}

	now := time.Now()

	ch := m.Send(os.Stdin, remotePath)
	done := make(chan struct{})

	fmt.Printf("%s Starting upload...\n", arrow)

	go func() {
		for {
			select {
			case e := <-ch:
				switch e := e.(type) {
				case pipedream.Progress:
					if !silent {
						bytes := humanize.Bytes(uint64(e.Bytes))
						fmt.Printf("%s Uploaded part #%d %s\n", arrow, e.PartNumber, subtle(bytes))
					}
				case pipedream.Retry:
					if !silent {
						details := fmt.Sprintf("try %d of %d", e.RetryNumber, e.MaxRetries)
						fmt.Printf("Retrying part #%d %s\n", e.PartNumber, subtle(details))
					}
				case pipedream.Error:
					if !silent {
						errMsg := strings.Replace(e.Error(), "\n", "", -1)
						errMsg = strings.Replace(errMsg, "\t", " ", -1)
						errMsg = indent.String(wordwrap.String(errMsg, wrapAt-errorIndent), errorIndent)
						fmt.Printf("%s Upload failed:\n\n%s\n\n", ex, errMsg)
					}
					close(done)
				case pipedream.Complete:
					if !silent {
						fmt.Printf("%s Done. Sent %s in %s.\n", check, humanize.Bytes(uint64(e.Bytes)), time.Since(now).Round(time.Millisecond))
					}
					close(done)
				}
			}
		}
	}()

	<-done

	return nil
}

func main() {
	rootCmd.Execute()
}
