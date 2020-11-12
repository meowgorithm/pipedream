Pipe Dream
==========

<p>
    <a href="https://github.com/meowgorithm/pipedream/releases"><img src="https://img.shields.io/github/release/meowgorithm/pipedream.svg" alt="Latest Release"></a>
    <a href="https://pkg.go.dev/github.com/meowgorithm/pipedream?tab=doc"><img src="https://godoc.org/github.com/golang/gddo?status.svg" alt="GoDoc"></a>
    <a href="https://github.com/meowgorithm/pipedream/actions"><img src="https://github.com/meowgorithm/pipedream/workflows/build/badge.svg" alt="Build Status"></a>
</p>

Easy multipart uploads for Amazon S3, DigitalOcean Spaces and S3-compatible
services. Available as a CLI and Go library.

## CLI

### Usage

```bash
# Set your secrets, region and endpoint in the environment
export ACCESS_KEY="..."
export SECRET_KEY="..."
export ENDPOINT="sfo2.digitaloceanspaces.com" # for AWS set REGION

# Pipe in data or redirect in a file
pipedream --bucket images --path pets/puppy.jpg < puppy.jpg

# Get fancy
export now=$(date +"%Y-%m-%d_%H:%M:%S_%Z")
cat /data/dump.rdb | gzip | pipedream --bucket backups --path dump-$now.rdb.gz

# For more info
pipedream -h
```

### Installation

Download a build from the [releases][releases] page. macOS, Linux and Windows
builds are available for a variety of architectures.

macOS users can also use Homebrew:

```
brew tap meowgorithm/tap && brew install meowgorithm/tap/pipedream
```

Or you can just use `go get`:

```bash
go get github.com/meowgorithm/pipedream/pipedream
```

[releases]: https://github.com/meowgorithm/pipedream/releases

## Library

The library uses an event based model, sending events through a channel.

```go
import "github.com/meowgorithm/pipedream"

// Create a new multipart upload object
m := pipedream.MultipartUpload{
    AccessKey: os.Getenv("ACCESS_KEY"),
    SecretKey: os.Getenv("SECRET_KEY"),
    Endpoint:  "sfo2.digitaloceanspaces.com", // you could use Region for AWS
    Bucket:    "my-fave-bucket",
}

// Get an io.Reader, like an *os.File or os.Stdout
f, err := os.Open("big-redis-dump.rdb")
if err != nil {
    fmt.Printf("Rats: %v\n", err)
    os.Exit(1)
}
defer f.Close()

// Send up the data. Pipdream returns a channel where you can listen for events
ch := m.Send(f, "backups/dump.rdb")
done := make(chan struct{})

// Listen for activity. For more detailed reporting, see the docs
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
```

[Full source][example] of this example. For an example with more detailed
reporting, see the source code in the [CLI][cli].

[example]: https://github.com/meowgorithm/pipedream/blob/master/example/main.go
[cli]: https://github.com/meowgorithm/pipedream/tree/master/pipedream

## Awknowledgements

Thanks to to Apoorva Manjunath‘s [S3 multipart upload example](https://github.com/apoorvam/aws-s3-multipart-upload)
for the S3 implementation details.

## License

[MIT](https://github.com/meowgorithm/pipedream/raw/master/LICENSE)
