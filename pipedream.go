// Package pipedream provides a simple interface to multipart Amazon S3
// uploads. It also works with other S3 compatible services such as
// DigitalOcean's spaces.
//
// The general workflow is to create MultipartUpload struct, run Send on the
// struct, and then listen for events on the returned channel.
//
// Example usage:
//
//     package main
//
//     import (
//         "fmt"
//         "os"
//
//         "github.com/meowgorithm/pipedream"
//     )
//
//     func main() {
//
//         // Prep the multipart upload
//         m := pipedream.MultipartUpload{
//              AccessKey: os.Getenv("ACCESS_KEY"),
//              SecretKey: os.Getenv("SECRET_KEY"),
//              Endpoint:  "sfo2.digitaloceanspaces.com", // you could use Region for AWS
//              Bucket:    "my-fave-bucket",
//         }
//
//         // Get an io.Reader
//         f, err := os.Open("big-redis-dump.rdb")
//         if err != nil {
//              fmt.Printf("Rats: %v\n", err)
//              os.Exit(1)
//         }
//         defer f.Close()
//
//         // Send it up! Pipdream returns a channel where you can listen for events.
//         ch := m.Send(f, "backups/dump.rdb")
//         done := make(chan struct{})
//
//         // Listen for activity. For more detailed reporting, see the docs below.
//         go func() {
//             for {
//                 e := <-ch
//                 switch e.(type) {
//                 case pipedream.Complete:
//                     fmt.Println("It worked!")
//                     close(done)
//                     return
//                 case pipedream.Error:
//                     fmt.Println("Rats, it didn't work.")
//                     close(done)
//                     return
//                }
//            }
//         }()
//
//         <-done
//     }
//
// There's also a command line interface available at
// https://github.com/meowgorithm/pipedream/pipedream
package pipedream

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

const (
	// Kilobyte is a convenience measurement useful when setting upload part
	// sizes.
	Kilobyte int64 = 1024

	// Megabyte is a convenience measurement useful when setting upload part
	// sizes.
	Megabyte int64 = Kilobyte * 1024

	// DefaultRegion is the region to use as a default. This should be used for
	// services that don't use regions, like DigitalOcean spaces.
	DefaultRegion = "us-east-1"
)

// Event represents activity that occurred during the upload. Events are sent
// through the channel returned by MultipartUpload.Send(). To figure out which
// event was received use a type switch or type assertion.
type Event interface {
	// This is a dummy method for type safety.
	event()
}

// Progress is an Event indicating upload progress. It's sent when a part has
// successfully uploaded.
type Progress struct {
	PartNumber int
	Bytes      int
}

// Retry is an Event indicating there was an error uploading a part and the
// part is being retried. An Error will be send if the retries are exhaused and
// the upload fails.
type Retry struct {
	PartNumber  int
	RetryNumber int
	MaxRetries  int
}

// Complete is an Event sent when an upload has completed successfully. When
// a Complete is received there will be no further activity send on the
// channel, so you can confidently move on.
type Complete struct {
	Bytes  int
	Result *s3.CompleteMultipartUploadOutput
}

// Error is an event indicating that an Error occurred during the upload. When
// an Error is received the operation has failed and no further activity will
// be send, so you can confidently move on.
type Error struct {
	Err error
}

// Error returns the a string representation of the error. It satisfies the
// Error interface.
func (e Error) Error() string {
	return e.Err.Error()
}

// Implement dummy methods to satisfy Event interface. We're doing this for
// type safety.
func (p Progress) event() {}
func (r Retry) event()    {}
func (c Complete) event() {}
func (e Error) event()    {}

// MultipartUpload handles multipart uploads to S3 and S3-compatible systems.
type MultipartUpload struct {
	Endpoint    string
	Region      string
	Bucket      string
	AccessKey   string
	SecretKey   string
	MaxRetries  int
	MaxPartSize int64

	svc               *s3.S3
	res               *s3.CreateMultipartUploadOutput
	completedParts    []*s3.CompletedPart
	currentPartNumber int
	path              string
	reader            io.Reader
}

// Send uploads data from a given io.Reader (such as an *os.File or os.Stdin)
// to a given path in a bucket.
func (m *MultipartUpload) Send(reader io.Reader, path string) chan Event {
	m.reader = reader
	m.path = path
	ch := make(chan Event)
	go m.run(ch)
	return ch
}

func (m *MultipartUpload) run(ch chan Event) {
	// Set defaults
	if m.MaxRetries == 0 {
		m.MaxRetries = 3
	}
	if m.MaxPartSize == 0 {
		m.MaxPartSize = Megabyte * 5
	}
	if m.Endpoint == "" {
		m.Endpoint = "nyc3.digitaloceanspaces.com"
	}
	if m.Region == "" {
		m.Region = DefaultRegion
	}

	// Validate
	var missing []string
	if m.AccessKey == "" {
		missing = append(missing, "AccessKey")
	}
	if m.SecretKey == "" {
		missing = append(missing, "SecretKey")
	}
	if m.Bucket == "" {
		missing = append(missing, "Bucket")
	}
	if len(missing) > 0 {
		ch <- Error{
			Err: errors.New("missing " + EnglishJoin(missing, true)),
		}
		return
	}

	// Make S3 config
	s3Config := &aws.Config{
		Credentials: credentials.NewStaticCredentials(m.AccessKey, m.SecretKey, ""),
		Endpoint:    aws.String(m.Endpoint),
		Region:      aws.String(m.Region),
	}

	// Init S3 session
	newSession := session.New(s3Config)
	m.svc = s3.New(newSession)

	// Upload parts
	totalBytes := 0
	m.currentPartNumber = 1
	buf := make([]byte, m.MaxPartSize)
	for {

		n, err := m.reader.Read(buf)
		if err != nil && err == io.EOF {
			// There's no more data, so we've successfully uploaded all parts.
			break
		}
		if err != nil {
			if abortErr := m.Abort(); abortErr != nil {
				ch <- Error{
					Err: fmt.Errorf("upload error: %v, as well as an error aborting the upload: %v", err, abortErr),
				}
				return
			}
			ch <- Error{err}
			return
		}

		// Request the upload if we haven't already. We wait until we've read
		// some bytes so we can detect the file type.
		if m.res == nil {
			input := &s3.CreateMultipartUploadInput{
				Bucket:      aws.String(m.Bucket),
				Key:         aws.String(m.path),
				ContentType: aws.String(http.DetectContentType(buf[:n])),
			}

			m.res, err = m.svc.CreateMultipartUpload(input)
			if err != nil {
				ch <- Error{err}
				return
			}
		}

		// Perform the upload
		part, err := m.uploadPart(ch, buf[:n], m.currentPartNumber)
		if err != nil {
			if abortErr := m.Abort(); abortErr != nil {
				ch <- Error{
					Err: fmt.Errorf("upload error: %v, as well as an error aborting the upload: %v", err, abortErr),
				}
				return
			}
			ch <- Error{err}
			return
		}

		ch <- Progress{
			PartNumber: m.currentPartNumber,
			Bytes:      n,
		}

		totalBytes += n
		m.completedParts = append(m.completedParts, part)
		m.currentPartNumber++
	}

	res, err := m.complete()
	if err != nil {
		ch <- Error{err}
	}
	ch <- Complete{
		Bytes:  totalBytes,
		Result: res,
	}
}

// uploadPart performs the technical S3 stuff to upload one part of the
// multipart upload. If it fails we'll retry based on the number set in
// multipartUploadManager.MaxRetries.
func (m MultipartUpload) uploadPart(ch chan Event, chunk []byte, partNum int) (*s3.CompletedPart, error) {
	partInput := &s3.UploadPartInput{
		Body:          bytes.NewReader(chunk),
		Bucket:        m.res.Bucket,
		Key:           m.res.Key,
		PartNumber:    aws.Int64(int64(partNum)),
		UploadId:      m.res.UploadId,
		ContentLength: aws.Int64(int64(len(chunk))),
	}

	tryNum := 1
	for tryNum <= m.MaxRetries {

		// Attempt to upload part
		res, err := m.svc.UploadPart(partInput)
		if err != nil {

			// Fail
			if tryNum == m.MaxRetries {
				if aerr, ok := err.(awserr.Error); ok {
					return nil, aerr
				}
				return nil, err
			}

			ch <- Retry{
				PartNumber:  m.currentPartNumber,
				RetryNumber: tryNum,
				MaxRetries:  m.MaxRetries,
			}

			tryNum++

		} else {
			// Success
			return &s3.CompletedPart{
				ETag:       res.ETag,
				PartNumber: aws.Int64(int64(partNum)),
			}, nil
		}
	}

	// This should never happen
	return nil, errors.New("could not upload part")
}

// complete finishes up the upload. This must be called after all parts have
// been sent.
func (m MultipartUpload) complete() (*s3.CompleteMultipartUploadOutput, error) {
	return m.svc.CompleteMultipartUpload(&s3.CompleteMultipartUploadInput{
		Bucket:   m.res.Bucket,
		Key:      m.res.Key,
		UploadId: m.res.UploadId,
		MultipartUpload: &s3.CompletedMultipartUpload{
			Parts: m.completedParts,
		},
	})
}

// Abort cancels the upload.
func (m MultipartUpload) Abort() error {
	_, err := m.svc.AbortMultipartUpload(&s3.AbortMultipartUploadInput{
		Bucket:   m.res.Bucket,
		Key:      m.res.Key,
		UploadId: m.res.UploadId,
	})
	return err
}

// EnglishJoin joins a slice of strings with commas and the word "and" like one
// would in English. Oxford comma optional.
func EnglishJoin(words []string, oxfordComma bool) string {
	b := strings.Builder{}
	for i, w := range words {

		if i == 0 {
			b.WriteString(w)
			continue
		}

		if len(words) > 1 && i == len(words)-1 {
			if oxfordComma && i > 2 {
				b.WriteString(",")
			}
			b.WriteString(" and")
			b.WriteString(" " + w)
			continue
		}

		b.WriteString(", " + w)
	}
	return b.String()
}
