package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/inconshreveable/log15"
	"github.com/slack-go/slack"
)

var ffmpegPath = "/home/psanford/projects/rom-cam/ffmpeg"
var segmentSize = 10 * time.Second // this matches the gop for the logitec cam

var dev = flag.String("dev", "/dev/video0", "v4l device")
var saveTSDir = flag.String("save-ts", "", "Directory to save segments to")
var bucket = flag.String("bucket", "", "S3 bucket")

var fixedLoc = time.FixedZone("UTC-7", -7*60*60)

func main() {
	flag.Parse()
	handler := log15.StreamHandler(os.Stdout, log15.LogfmtFormat())
	log15.Root().SetHandler(handler)
	lgr := log15.New()

	ctx := context.Background()

	webhookURL := os.Getenv("SLACK_WEBHOOK_URL")

	// set AWS_ACCESS_KEY_ID
	//     AWS_SECRET_ACCESS_KEY
	sess := session.New(&aws.Config{
		Region: aws.String("us-east-1"),
	})
	s3client := s3.New(sess)

	segmentChan := make(chan Segment, 1)

	err := captureSource(ctx, lgr, segmentChan)
	if err != nil {
		panic(err)
	}
	first := true

	for segment := range segmentChan {
		if *saveTSDir != "" {
			fp := filepath.Join(*saveTSDir, fmt.Sprintf("%d.ts", segment.ts.Unix()))
			ioutil.WriteFile(fp, segment.data, 0600)
			lgr.Info("wrote_local_file", "path", fp)
		}
		if first {
			// skip first 10 seconds of recording,
			// the camera adjusts when it first starts
			first = false
			continue
		}

		motionFrames, err := hasMotion(ctx, lgr, segment)
		if err != nil {
			panic(err)
		}

		if motionFrames > 1 {
			lgr.Info("motion-detected", "frames", motionFrames)
			if *bucket != "" {
				tsFilename := fmt.Sprintf("ts/%d.ts", segment.ts.Unix())

				_, err = s3client.PutObject(&s3.PutObjectInput{
					Bucket: bucket,
					Key:    &tsFilename,
					Body:   bytes.NewReader(segment.data),
				})
				if err != nil {
					lgr.Error("s3_put_obj_err", "err", err)
					continue
				}

				mp4, err := toMP4(ctx, segment)
				if err != nil {
					lgr.Error("to_mp4_err", "err", err)
					continue
				}

				mp4Filename := fmt.Sprintf("mp4/%d.mp4", segment.ts.Unix())
				_, err = s3client.PutObject(&s3.PutObjectInput{
					Bucket:      bucket,
					Key:         &mp4Filename,
					Body:        bytes.NewReader(mp4),
					ContentType: aws.String("video/mp4"),
				})
				if err != nil {
					lgr.Error("s3_put_obj_err", "err", err)
					continue
				}

				gif, err := toGIF(ctx, segment)
				if err != nil {
					lgr.Error("to_gif_err", "err", err)
					continue
				}

				gifFilename := fmt.Sprintf("gif/%d.gif", segment.ts.Unix())
				_, err = s3client.PutObject(&s3.PutObjectInput{
					Bucket:      bucket,
					Key:         &gifFilename,
					Body:        bytes.NewReader(gif),
					ContentType: aws.String("image/gif"),
				})
				if err != nil {
					lgr.Error("s3_put_obj_err", "err", err)
					continue
				}

				if webhookURL != "" {
					mp4Req, _ := s3client.GetObjectRequest(&s3.GetObjectInput{
						Bucket: bucket,
						Key:    &mp4Filename,
					})

					mp4PresignedURL, err := mp4Req.Presign(6 * time.Hour)
					if err != nil {
						lgr.Error("s3_presign_err", "err", err)
						continue
					}

					gifReq, _ := s3client.GetObjectRequest(&s3.GetObjectInput{
						Bucket: bucket,
						Key:    &gifFilename,
					})

					gifPresignedURL, err := gifReq.Presign(6 * time.Hour)
					if err != nil {
						lgr.Error("s3_presign_err", "err", err)
						continue
					}

					err = slack.PostWebhook(webhookURL, &slack.WebhookMessage{
						Attachments: []slack.Attachment{
							{
								Title:     segment.ts.In(fixedLoc).Format(time.RFC3339),
								TitleLink: mp4PresignedURL,
								ImageURL:  gifPresignedURL,
								Fields: []slack.AttachmentField{
									{
										Title: "Frames",
										Value: strconv.Itoa(motionFrames),
										Short: true,
									},
								},
							},
						},
					})
					if err != nil {
						lgr.Error("slack_webhook_err", "err", err)
					}
				}
			}
		}
	}
}

type Segment struct {
	ts   time.Time
	data []byte
}

func captureSource(ctx context.Context, lgr log15.Logger, segmentChan chan Segment) error {
	cmd := cmd(ffmpegPath, "-f", "video4linux2", "-r", "10", "-input_format", "h264", "-video_size", "640x480", "-i", *dev, "-vcodec", "copy", "-acodec", "copy", "-f", "mpegts", "-")

	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	br := bufio.NewReader(stdout)

	pktlen := 188

	go func() {
		buf := make([]byte, pktlen)

		for {
			allocedBuf := make([]byte, 0, pktlen*10*10) // 10fps * 10 seconds
			w := bytes.NewBuffer(allocedBuf)
			ts := time.Now()
			stop := ts.Add(segmentSize)
			count := 0
			for time.Now().Before(stop) {
				_, err = io.ReadFull(br, buf)
				if err != nil {
					panic(err)
				}
				w.Write(buf)
				count++
			}

			segment := Segment{
				ts:   ts,
				data: w.Bytes(),
			}

			select {
			case <-ctx.Done():
				close(segmentChan)
				return
			case segmentChan <- segment:
			}
		}
	}()

	return cmd.Start()
}

func hasMotion(ctx context.Context, lgr log15.Logger, segment Segment) (int, error) {
	cmd := cmd(ffmpegPath, "-f", "mpegts", "-i", "-", "-vcodec", "rawvideo", "-pix_fmt", "gray", "-vf", "edgedetect", "-f", "rawvideo", "-")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, err
	}

	err = cmd.Start()
	if err != nil {
		return 0, err
	}

	go func() {
		_, err = stdin.Write(segment.data)
		if err != nil {
			panic(err)
		}

		stdin.Close()
	}()

	var (
		width         = 640
		height        = 480
		bytesPerPixel = 1

		prev         = make([]uint8, width*height*bytesPerPixel)
		next         = make([]uint8, width*height*bytesPerPixel)
		motionFrames = 0
	)

	for i := 0; ; i++ {
		_, err = io.ReadFull(stdout, next)
		if err == io.EOF {
			break
		} else if err != nil {
			panic(err)
		}

		if i == 0 {
			copy(prev, next)
			continue
		}

		sumPrev := 0
		sumNext := 0

		// changeCount := 0
		for h := 0; h < height; h++ {
			for w := 0; w < width; w++ {
				idx := h*width + w

				sumPrev += int(prev[idx])
				sumNext += int(next[idx])
			}
		}

		diff := sumPrev - sumNext
		if diff < 0 {
			diff = sumNext - sumPrev
		}

		if diff > 20000 {
			motionFrames++
		}

		copy(prev, next)
	}

	err = cmd.Wait()
	if err != nil {
		// lgr.Error("has_motion_exit_err", "err", err, "stderr", stderr.String())
		lgr.Error("has_motion_exit_err", "err", err)
	}

	return motionFrames, nil
}

func toMKV(ctx context.Context, segment Segment) ([]byte, error) {
	cmd := cmd(ffmpegPath, "-f", "mpegts", "-i", "-", "-vcodec", "copy", "-acodec", "copy", "-f", "matroska", "-")
	cmd.Stderr = io.Discard

	buf := make([]byte, 0, len(segment.data))
	out := bytes.NewBuffer(buf)

	cmd.Stdout = out
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	err = cmd.Start()
	if err != nil {
		return nil, err
	}

	stdin.Write(segment.data)
	stdin.Close()

	err = cmd.Wait()
	if err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

func toMP4(ctx context.Context, segment Segment) ([]byte, error) {
	cmd := cmd(ffmpegPath, "-f", "mpegts", "-i", "-", "-vcodec", "copy", "-acodec", "copy", "-f", "mp4", "-movflags", "frag_keyframe+empty_moov", "-")
	cmd.Stderr = io.Discard

	buf := make([]byte, 0, len(segment.data))
	out := bytes.NewBuffer(buf)

	cmd.Stdout = out
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	err = cmd.Start()
	if err != nil {
		return nil, err
	}

	stdin.Write(segment.data)
	stdin.Close()

	err = cmd.Wait()
	if err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

func toJPG(ctx context.Context, segment Segment) ([]byte, error) {
	cmd := cmd(ffmpegPath, "-f", "mpegts", "-i", "-", "-vframes", "1", "-f", "mjpeg", "-")
	cmd.Stderr = io.Discard

	buf := make([]byte, 0, len(segment.data))
	out := bytes.NewBuffer(buf)

	cmd.Stdout = out
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	err = cmd.Start()
	if err != nil {
		return nil, err
	}

	stdin.Write(segment.data)
	stdin.Close()

	err = cmd.Wait()
	if err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

func toGIF(ctx context.Context, segment Segment) ([]byte, error) {
	cmd := cmd(ffmpegPath, "-f", "mpegts", "-i", "-", "-f", "gif", "-")
	cmd.Stderr = io.Discard

	buf := make([]byte, 0, len(segment.data))
	out := bytes.NewBuffer(buf)

	cmd.Stdout = out
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	err = cmd.Start()
	if err != nil {
		return nil, err
	}

	stdin.Write(segment.data)
	stdin.Close()

	err = cmd.Wait()
	if err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

func cmd(name string, args ...string) *exec.Cmd {
	//	fmt.Printf("run: %s %s\n", name, strings.Join(args, " "))
	return exec.Command(name, args...)
}
