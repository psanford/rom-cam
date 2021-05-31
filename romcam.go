package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"image/jpeg"
	"io/ioutil"
	"os"
	"os/exec"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/inconshreveable/log15"
	"github.com/lucasb-eyer/go-colorful"
	"github.com/myusuf3/imghash"
	"github.com/slack-go/slack"
)

var dev = flag.String("dev", "/dev/video0", "v4l device")
var minDistance = flag.Int("distance", 4, "Min image distance")
var fileDir = flag.String("file-dir", "", "Directory to write images to")
var bucket = flag.String("bucket", "", "S3 bucket")
var delayMillis = flag.Int("delay-ms", 1000, "Delay in milliseconds")

func main() {
	flag.Parse()
	handler := log15.StreamHandler(os.Stdout, log15.LogfmtFormat())
	log15.Root().SetHandler(handler)
	lgr := log15.New()

	var (
		stdout   bytes.Buffer
		stderr   bytes.Buffer
		prevHash uint64

		prevSaveTime time.Time

		webhookURL = os.Getenv("SLACK_WEBHOOK_URL")
	)

	// set AWS_ACCESS_KEY_ID
	//     AWS_SECRET_ACCESS_KEY
	sess := session.New(&aws.Config{
		Region: aws.String("us-east-1"),
	})
	s3client := s3.New(sess)

	t := time.NewTicker(time.Duration(*delayMillis) * time.Millisecond)
	for {
		<-t.C

		stdout.Reset()
		stderr.Reset()

		cmd := exec.Command("ffmpeg", "-f", "video4linux2", "-input_format", "mjpeg", "-video_size", "1280x720", "-i", *dev, "-vframes", "1", "-vf", "drawtext=fontfile=/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf:text='%{localtime}':fontcolor=white@0.8:x=7:y=7", "-f", "mjpeg", "-")

		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err != nil {
			lgr.Error("ffmpeg_err", "err", stderr.String())
			continue
		}

		imgb := stdout.Bytes()

		img, err := jpeg.Decode(bytes.NewReader(imgb))
		if err != nil {
			lgr.Error("jpeg_decode_err", "err", err)
			continue
		}

		var (
			sumL  float64
			count int
		)

		bounds := img.Bounds()
		for x := 0; x < bounds.Max.X; x++ {
			for y := 0; y < bounds.Max.Y; y++ {
				c, _ := colorful.MakeColor(img.At(x, y))
				_, _, l := c.Hsl()
				sumL += l
				count++
			}
		}

		avgL := sumL / float64(count)
		curHash := imghash.Average(img)
		dist := imghash.Distance(prevHash, curHash)
		fmt.Printf("prev: %d cur: %d  distance: %d avgL:%f\n", prevHash, curHash, dist, avgL)

		sinceLastSave := time.Since(prevSaveTime)
		mustSaveTime := sinceLastSave > time.Hour

		if (avgL > 0.15 && dist > uint64(*minDistance)) || mustSaveTime {
			if mustSaveTime {
				lgr.Info("since_last_save_more_than_1_hour", "since", sinceLastSave)
			} else {
				lgr.Info("motion_detected")
			}

			ts := time.Now()

			sum := sha256.Sum256(imgb)
			filename := ts.Format("2006-01-02_15:04:05") + "_" + hex.EncodeToString(sum[:]) + ".jpg"

			if *fileDir != "" {
				err = ioutil.WriteFile(filename, imgb, 0644)
				if err != nil {
					lgr.Error("write_file_err", "err", err)
				}
			}

			if *bucket != "" {
				_, err = s3client.PutObject(&s3.PutObjectInput{
					Bucket:      bucket,
					Key:         &filename,
					Body:        bytes.NewReader(imgb),
					ContentType: aws.String("image/jpeg"),
				})
				if err != nil {
					lgr.Error("s3_put_obj_err", "err", err)
					continue
				}

				req, _ := s3client.GetObjectRequest(&s3.GetObjectInput{
					Bucket: bucket,
					Key:    &filename,
				})

				presignedURL, err := req.Presign(6 * time.Hour)
				if err != nil {
					lgr.Error("s3_presign_err", "err", err)
					continue
				}

				if webhookURL != "" {
					err = slack.PostWebhook(webhookURL, &slack.WebhookMessage{
						Attachments: []slack.Attachment{
							{
								Title:     filename,
								TitleLink: presignedURL,
								ImageURL:  presignedURL,
							},
						},
					})
					if err != nil {
						lgr.Error("slack_webhook_err", "err", err)
					}
				}

				prevSaveTime = time.Now()

			}

		} else {
			lgr.Info("No motion detected")
		}

		prevHash = curHash
	}
}
