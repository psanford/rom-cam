package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Comcast/gots/packet"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/inconshreveable/log15"
	"github.com/nareix/joy4/codec/h264parser"
	"github.com/paulstuart/ping"
	"github.com/psanford/rom-cam/config"
	"github.com/psanford/rom-cam/kernelmodule"
	"github.com/psanford/rom-cam/segment"
	"github.com/psanford/rom-cam/webserver"
	"github.com/slack-go/slack"
)

var (
	segmentSize = 10 * time.Second // this matches the gop for the logitec cam
	fixedLoc    = time.FixedZone("UTC-7", -7*60*60)

	confPath = flag.String("config", "", "Path to config file")

	ffmpegPath = ""
	dev        = ""
)

const (
	H264IDRFrame    = 1
	H264NonIDRFrame = 5
)

func main() {
	flag.Parse()

	handler := log15.StreamHandler(os.Stdout, log15.LogfmtFormat())
	log15.Root().SetHandler(handler)
	lgr := log15.New()

	ctx := context.Background()

	conf, err := config.LoadConfig(*confPath)
	if err != nil {
		log.Fatalf("load config err: %s", err)
	}

	ffmpegPath = conf.FFMPEGPath
	dev = conf.Device

	if conf.LoadKernelModule {
		err := kernelmodule.LoadUVCVideo()
		if err != nil {
			lgr.Error("load_kernel_module_err", "err", err)
		}
	}

	s := server{
		conf: *conf,
		ring: segment.NewRing(3),
	}

	if conf.WebserverListenAddr != "" {
		go func() {
			lgr.Info("starting_webserver", "addr", conf.WebserverListenAddr)
			err := webserver.ListenAndServe(lgr, s.ring, ffmpegPath, conf.WebserverListenAddr)
			if err != nil {
				lgr.Error("listen and serve err: %s", err)
			}
		}()
	}

	go s.watchForHomeDevices()

	s.run(ctx, lgr)
}

type server struct {
	conf          config.Config
	ring          *segment.Ring
	someoneIsHome int32
}

func (s *server) run(ctx context.Context, lgr log15.Logger) {
	var creds *credentials.Credentials
	if s.conf.AWSCreds != nil {
		creds = credentials.NewStaticCredentials(s.conf.AWSCreds.AccessKeyID, s.conf.AWSCreds.SecretAccessKey, "")
	}
	sess, err := session.NewSession(&aws.Config{
		Region:      aws.String("us-east-1"),
		Credentials: creds,
	})
	if err != nil {
		panic(err)
	}
	s3client := s3.New(sess)

	segmentChan := make(chan segment.Segment, 1)

	resetChan := make(chan struct{}, 1)

	safeCameraName := s.conf.NameForFile()

	err = captureSource(ctx, lgr, resetChan, segmentChan)
	if err != nil {
		panic(err)
	}

	for segment := range segmentChan {
		s.ring.Push(segment)

		if s.conf.SaveTSDir != "" {
			fp := filepath.Join(s.conf.SaveTSDir, fmt.Sprintf("%d.ts", segment.TS.Unix()))
			os.WriteFile(fp, segment.Data, 0600)
			lgr.Info("wrote_local_file", "path", fp)
		}

		motionFrames, err := hasMotion(ctx, lgr, segment)
		if err != nil {
			lgr.Error("has_motion_err_trigger_reset", "err", err)
			resetChan <- struct{}{}
			continue
		}

		if len(motionFrames) > 1 {
			isHome := atomic.LoadInt32(&s.someoneIsHome) > 0
			lgr.Info("motion-detected", "frames", len(motionFrames), "is_home_dont_save", isHome)

			if isHome {
				continue
			}

			bestFrame := motionFrames[0]
			for _, f := range motionFrames[1:] {
				if f.Diff > bestFrame.Diff {
					bestFrame = f
				}
			}

			if s.conf.Bucket != "" {
				tsFilename := fmt.Sprintf("ts/%s/%d.ts", safeCameraName, segment.TS.Unix())

				_, err = s3client.PutObject(&s3.PutObjectInput{
					Bucket: &s.conf.Bucket,
					Key:    &tsFilename,
					Body:   bytes.NewReader(segment.Data),
				})
				if err != nil {
					lgr.Error("s3_put_obj_err", "err", err)
					continue
				}

				var mp4Filename, gifFilename string

				mp4, err := toMP4(ctx, segment)
				if err != nil {
					lgr.Error("to_mp4_err", "err", err)
				} else {
					mp4Filename = fmt.Sprintf("mp4/%s/%d.mp4", safeCameraName, segment.TS.Unix())
					_, err = s3client.PutObject(&s3.PutObjectInput{
						Bucket:      &s.conf.Bucket,
						Key:         &mp4Filename,
						Body:        bytes.NewReader(mp4),
						ContentType: aws.String("video/mp4"),
					})
					if err != nil {
						lgr.Error("s3_put_obj_err", "err", err)
						continue
					}
				}

				gif, err := toGIF(ctx, segment)
				if err != nil {
					lgr.Error("to_gif_err", "err", err)
				} else {
					gifFilename = fmt.Sprintf("gif/%s/%d.gif", safeCameraName, segment.TS.Unix())
					_, err = s3client.PutObject(&s3.PutObjectInput{
						Bucket:      &s.conf.Bucket,
						Key:         &gifFilename,
						Body:        bytes.NewReader(gif),
						ContentType: aws.String("image/gif"),
					})
					if err != nil {
						lgr.Error("s3_put_obj_err", "err", err)
						continue
					}
				}

				if s.conf.WebhookURL != "" {
					var mp4PresignedURL, gifPresignedURL string
					if mp4Filename != "" {
						mp4Req, _ := s3client.GetObjectRequest(&s3.GetObjectInput{
							Bucket: &s.conf.Bucket,
							Key:    &mp4Filename,
						})

						mp4PresignedURL, err = mp4Req.Presign(6 * time.Hour)
						if err != nil {
							lgr.Error("s3_presign_err", "err", err)
						}
					}

					if gifFilename != "" {
						gifReq, _ := s3client.GetObjectRequest(&s3.GetObjectInput{
							Bucket: &s.conf.Bucket,
							Key:    &gifFilename,
						})

						gifPresignedURL, err = gifReq.Presign(6 * time.Hour)
						if err != nil {
							lgr.Error("s3_presign_err", "err", err)
						}
					}

					err = slack.PostWebhook(s.conf.WebhookURL, &slack.WebhookMessage{
						Attachments: []slack.Attachment{
							{
								Title:     fmt.Sprintf("%s %s", s.conf.Name, segment.TS.In(fixedLoc).Format(time.RFC3339)),
								TitleLink: mp4PresignedURL,
								ImageURL:  gifPresignedURL,
								Fields: []slack.AttachmentField{
									{
										Title: "Frames",
										Value: strconv.Itoa(len(motionFrames)),
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

func (s *server) watchForHomeDevices() {
	if len(s.conf.DisableRecordingForIPs) < 1 {
		return
	}
	for {
		var home int32
		for _, checkIP := range s.conf.DisableRecordingForIPs {
			err := ping.Pinger(checkIP, 1)
			if err == nil {
				home = 1
				break
			}
		}

		atomic.StoreInt32(&s.someoneIsHome, home)
		time.Sleep(60 * time.Second)
	}
}

func captureSource(ctx context.Context, lgr log15.Logger, resetChan chan struct{}, segmentChan chan segment.Segment) error {
	firstResultChan := make(chan error)
	go func() {
		for {
			childCtx, cancel := context.WithCancel(ctx)
			err := captureSourceOnce(childCtx, lgr, segmentChan)
			select {
			case firstResultChan <- err:
			default:
			}

			select {
			case <-resetChan:
				lgr.Info("resetting_src_stream")
				cancel()
			case <-ctx.Done():
				cancel()
				return
			}
		}
	}()

	firstResultErr := <-firstResultChan
	firstResultChan = nil

	return firstResultErr
}

func captureSourceOnce(ctx context.Context, lgr log15.Logger, segmentChan chan segment.Segment) error {
	cmd := cmd(ffmpegPath, "-f", "video4linux2", "-r", "10", "-input_format", "h264", "-video_size", "640x480", "-i", dev, "-vcodec", "copy", "-acodec", "copy", "-f", "mpegts", "-")

	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	br := bufio.NewReader(stdout)

	pktlen := 188

	go func() {
		var (
			pkt           packet.Packet
			hasPendingPPS bool
			segmentIdx    int
		)

		for {
			allocedBuf := make([]byte, 0, pktlen*10*10) // 10fps * 10 seconds
			w := bytes.NewBuffer(allocedBuf)
			ts := time.Now()
			stop := ts.Add(segmentSize)
			count := 0
			shouldBreak := false
			frameCount := 0
		OUTER:
			for {
				if time.Now().After(stop) {
					shouldBreak = true
				}

				if hasPendingPPS {
					if shouldBreak {
						break
					} else {
						hasPendingPPS = false
						w.Write(pkt[:])
					}
				}

				_, err = io.ReadFull(br, pkt[:])
				if err == io.EOF {
					log.Printf("read from ffmpeg src eof")
					return
				}
				if err != nil {
					log.Fatalf("read from ffmpeg src err: %s", err)
				}

				if pkt.HasPayload() {
					b, err := pkt.Payload()
					if err != nil {
						panic(err)
					}
					nalus, streamType := h264parser.SplitNALUs(b)
					if streamType == h264parser.NALU_ANNEXB {
						for _, nalu := range nalus {
							if len(nalu) < 1 {
								continue
							}
							typ := nalu[0] & 0x1f
							switch typ {
							case h264parser.NALU_PPS:
								hasPendingPPS = true
								continue OUTER
							case H264IDRFrame, H264NonIDRFrame:
								frameCount++
							}
						}
					}
				}

				w.Write(pkt[:])
				count++
			}

			segment := segment.Segment{
				TS:     ts,
				Idx:    segmentIdx,
				Data:   w.Bytes(),
				Frames: frameCount,
			}
			segmentIdx++

			select {
			case <-ctx.Done():
				return
			case segmentChan <- segment:
			}
			stderr.Reset()
		}
	}()

	err = cmd.Start()
	if err != nil {
		return err
	}

	shouldDie := true
	go func() {
		<-ctx.Done()
		shouldDie = false
		cmd.Process.Kill()
	}()

	go func() {
		err := cmd.Wait()
		if err != nil && shouldDie {
			lgr.Error("ffmpeg_src_exit_err", "dev", dev, "err", err, "stderr", stderr.String())
			time.Sleep(5 * time.Second)
			os.Exit(1)
		} else {
			lgr.Info("ffmpeg_src_exit_expected", "dev", dev, "err", err)
		}
	}()

	return nil
}

func hasMotion(ctx context.Context, lgr log15.Logger, segment segment.Segment) ([]motionFrame, error) {
	cmd := cmd(ffmpegPath, "-f", "mpegts", "-i", "-", "-vcodec", "rawvideo", "-pix_fmt", "gray", "-vf", "edgedetect", "-f", "rawvideo", "-")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	err = cmd.Start()
	if err != nil {
		return nil, err
	}

	go func() {
		_, err = stdin.Write(segment.Data)
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
		motionFrames = []motionFrame{}
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
			motionFrames = append(motionFrames, motionFrame{
				Idx:  i,
				Diff: diff,
			})
		}

		copy(prev, next)
	}

	err = cmd.Wait()
	if err != nil {
		// lgr.Error("has_motion_exit_err", "err", err, "stderr", stderr.String())
		lgr.Error("has_motion_exit_err", "err", err)
		return motionFrames, hasMotionFFMPEGExitErr
	}

	return motionFrames, nil
}

type motionFrame struct {
	Idx  int
	Diff int
}

var hasMotionFFMPEGExitErr = errors.New("ffmpeg exit err")

func toMKV(ctx context.Context, segment segment.Segment) ([]byte, error) {
	cmd := cmd(ffmpegPath, "-f", "mpegts", "-i", "-", "-vcodec", "copy", "-acodec", "copy", "-f", "matroska", "-")
	cmd.Stderr = io.Discard

	buf := make([]byte, 0, len(segment.Data))
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

	stdin.Write(segment.Data)
	stdin.Close()

	err = cmd.Wait()
	if err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

func toMP4(ctx context.Context, segment segment.Segment) ([]byte, error) {
	cmd := cmd(ffmpegPath, "-f", "mpegts", "-i", "-", "-vcodec", "copy", "-acodec", "copy", "-f", "mp4", "-movflags", "frag_keyframe+empty_moov", "-")
	cmd.Stderr = io.Discard

	buf := make([]byte, 0, len(segment.Data))
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

	stdin.Write(segment.Data)
	stdin.Close()

	err = cmd.Wait()
	if err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

func toJPG(ctx context.Context, segment segment.Segment) ([]byte, error) {
	cmd := cmd(ffmpegPath, "-f", "mpegts", "-i", "-", "-vframes", "1", "-f", "mjpeg", "-")
	cmd.Stderr = io.Discard

	buf := make([]byte, 0, len(segment.Data))
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

	stdin.Write(segment.Data)
	stdin.Close()

	err = cmd.Wait()
	if err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

func toGIF(ctx context.Context, segment segment.Segment) ([]byte, error) {
	cmd := cmd(ffmpegPath, "-f", "mpegts", "-i", "-", "-f", "gif", "-")
	cmd.Stderr = io.Discard

	buf := make([]byte, 0, len(segment.Data))
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

	stdin.Write(segment.Data)
	stdin.Close()

	err = cmd.Wait()
	if err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

func toStillJPG(ctx context.Context, segment segment.Segment, frame int) ([]byte, error) {
	cmd := cmd(ffmpegPath, "-f", "mpegts", "-i", "-", "-f", "gif", "-")
	cmd.Stderr = io.Discard

	buf := make([]byte, 0, len(segment.Data))
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

	stdin.Write(segment.Data)
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
