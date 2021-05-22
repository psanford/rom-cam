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
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/inconshreveable/log15"
	"github.com/myusuf3/imghash"
	"github.com/slack-go/slack"
)

var dev = flag.String("dev", "/dev/video0", "v4l device")
var minDistance = flag.Int("distance", 4, "Min image distance")
var fileDir = flag.String("file-dir", "", "Directory to write images to")
var bucket = flag.String("bucket", "", "S3 bucket")

func main() {
	flag.Parse()
	handler := log15.StreamHandler(os.Stdout, log15.LogfmtFormat())
	log15.Root().SetHandler(handler)
	lgr := log15.New()

	var (
		stdout   bytes.Buffer
		stderr   bytes.Buffer
		prevHash uint64

		webhookURL = os.Getenv("SLACK_WEBHOOK_URL")
	)

	// set AWS_ACCESS_KEY_ID
	//     AWS_SECRET_ACCESS_KEY
	sess := session.New(&aws.Config{
		Region: aws.String("us-east-1"),
	})
	s3client := s3.New(sess)

	t := time.NewTicker(1 * time.Second)
	for {
		<-t.C

		stdout.Reset()
		stderr.Reset()

		cmd := exec.Command("ffmpeg", "-f", "video4linux2", "-input_format", "mjpeg", "-video_size", "1280x720", "-i", *dev, "-vframes", "1", "-f", "mjpeg", "-")

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

		curHash := imghash.Average(img)
		dist := imghash.Distance(prevHash, curHash)
		fmt.Printf("prev: %d cur: %d  distance: %d\n", prevHash, curHash, dist)
		if dist > uint64(*minDistance) {
			lgr.Info("motion_detected")

			ts := time.Now()

			if *fileDir != "" {
				filename := filepath.Join(*fileDir, ts.Format("15_04_05")+".jpg")

				err = ioutil.WriteFile(filename, imgb, 0644)
				if err != nil {
					lgr.Error("write_file_err", "err", err)
				}
			}

			sum := sha256.Sum256(imgb)

			filename := ts.Format("15_04_05") + "_" + hex.EncodeToString(sum[:]) + ".jpg"

			if *bucket != "" {
				_, err = s3client.PutObject(&s3.PutObjectInput{
					Bucket: bucket,
					Key:    &filename,
					Body:   bytes.NewReader(imgb),
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
			}

		} else {
			lgr.Info("No motion detected")
		}

		prevHash = curHash
	}
}
