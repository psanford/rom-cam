package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func main() {
	err := Execute()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "rom-cam-cli",
	Short: "rom cam debug tools",
}

func Execute() error {
	rootCmd.AddCommand(edgeDetectCommand())
	rootCmd.AddCommand(bgSubtractCommand())

	return rootCmd.Execute()
}

var (
	dstFile string
)

func edgeDetectCommand() *cobra.Command {
	var cmd = &cobra.Command{
		Use:   "edge-detect <file>",
		Short: "Show edge-detection-output",
		Run:   edgeDetectAction,
	}

	cmd.Flags().StringVarP(&dstFile, "file", "", "", "Dest file, default mpv")

	return cmd
}

func edgeDetectAction(cmd *cobra.Command, args []string) {
	if len(args) < 1 {
		log.Fatalf(cmd.Use)
	}

	dstArgs := []string{"-f", "mpegts", "-"}
	if dstFile != "" {
		dstArgs = []string{dstFile}
	}

	ffmpegArgs := append([]string{"-i", args[0], "-pix_fmt", "gray", "-vf", "edgedetect"}, dstArgs...)

	ffmpeg := exec.Command("ffmpeg", ffmpegArgs...)

	if dstFile == "" {
		ffmpegOut, err := ffmpeg.StdoutPipe()
		if err != nil {
			log.Fatalf("ffmpeg pipe err: %s", err)
		}

		mpv := exec.Command("mpv", "-")
		mpv.Stdin = ffmpegOut

		err = mpv.Start()
		if err != nil {
			log.Fatalf("start mpv err: %s", err)
		}

		defer func() {
			err = mpv.Wait()
			if err != nil {
				log.Printf("mpv exit err: %s", err)
			}
		}()
	}

	err := ffmpeg.Start()
	if err != nil {
		log.Fatalf("start ffmpeg err: %s", err)
	}

	err = ffmpeg.Wait()
	if err != nil {
		log.Printf("ffmpeg exit err: %s", err)
	}
}

var filterNoise bool

func bgSubtractCommand() *cobra.Command {
	var cmd = &cobra.Command{
		Use:   "bg-sub <file>",
		Short: "Show bg-subtraction-output",
		Run:   bgSubtractAction,
	}

	cmd.Flags().StringVarP(&dstFile, "file", "", "", "Dest file, default mpv")
	cmd.Flags().BoolVarP(&filterNoise, "filter-noise", "", false, "Filter out some noise")

	return cmd
}

func bgSubtractAction(cmd *cobra.Command, args []string) {
	if len(args) < 1 {
		log.Fatalf(cmd.Use)
	}

	ffmpegIn := exec.Command("ffmpeg", "-i", args[0], "-vcodec", "rawvideo", "-pix_fmt", "gray", "-vf", "edgedetect", "-f", "rawvideo", "-")

	var stderr bytes.Buffer
	ffmpegIn.Stderr = &stderr
	src, err := ffmpegIn.StdoutPipe()
	if err != nil {
		panic(err)
	}

	err = ffmpegIn.Start()
	if err != nil {
		panic(err)
	}

	ffmpegOut := exec.Command("ffmpeg", "-f", "rawvideo", "-pix_fmt", "gray", "-s:v", "640x480", "-r", "10", "-i", "-", "-c:v", "libx264", "-f", "mpegts", "-")

	toMpv, err := ffmpegOut.StdoutPipe()
	if err != nil {
		panic(err)
	}
	dst, err := ffmpegOut.StdinPipe()
	if err != nil {
		panic(err)
	}

	if dstFile != "" {
		f, err := os.Create(dstFile)
		if err != nil {
			panic(err)
		}
		go func() {
			io.Copy(f, toMpv)
			f.Close()
		}()
	} else {
		mpv := exec.Command("mpv", "-")
		mpv.Stdin = toMpv

		err = mpv.Start()
		if err != nil {
			log.Fatalf("start mpv err: %s", err)
		}

		defer func() {
			log.Printf("wait mpv")
			err = mpv.Wait()
			if err != nil {
				log.Fatalf("mpv err: %s", err)
			}
		}()
	}

	err = ffmpegOut.Start()
	if err != nil {
		panic(err)
	}

	var (
		width         = 640
		height        = 480
		bytesPerPixel = 1

		origAlgMotionFrames = 0
		newAlgMotionFrames  = 0

		prevs = make([][]uint8, 0)
		next  = make([]uint8, width*height*bytesPerPixel)
		diff  = make([]uint8, width*height*bytesPerPixel)
	)

	for i := 0; ; i++ {
		_, err = io.ReadFull(src, next)
		if err == io.EOF {
			break
		} else if err != nil {
			panic(err)
		}

		if i == 0 {
			prev := make([]uint8, width*height*bytesPerPixel)
			copy(prev, next)
			prevs = append(prevs, prev)
			continue
		}

		sumPrev := 0
		sumNext := 0
		filteredCount := 0

		for h := 0; h < height; h++ {
			for w := 0; w < width; w++ {
				idx := h*width + w

				sumPrev += int(prevs[0][idx])
				sumNext += int(next[idx])

				diff[idx] = xorIdx(idx, next, prevs)

				if diff[idx] > 0 {
					filteredCount++
				}
			}
		}

		if filterNoise {
			filteredCount = 0
			for h := 0; h < height; h++ {
				for w := 0; w < width; w++ {
					idx := h*width + w

					if diff[idx] == 0 {
						continue
					}

					checks := [][]int{
						{h - 1, w - 1},
						{h - 1, w},
						{h - 1, w + 1},
						{h, w - 1},
						{h, w + 1},
						{h + 1, w - 1},
						{h + 1, w},
						{h + 1, w + 1},
					}

					var hasActiveAdjacent bool
					for _, check := range checks {
						hh := check[0]
						ww := check[1]
						if hh < 0 || hh >= 480 {
							continue
						}
						if ww < 0 || ww >= 640 {
							continue
						}

						adjIdx := hh*width + ww
						if diff[adjIdx] > 0 {
							hasActiveAdjacent = true
							break
						}
					}

					if !hasActiveAdjacent {
						diff[idx] = 0
					} else {
						filteredCount++
					}
				}
			}
		}

		pixDiff := sumPrev - sumNext
		if pixDiff < 0 {
			pixDiff = sumNext - sumPrev
		}

		motionTxt := ""
		if pixDiff > 20000 {
			origAlgMotionFrames++
			motionTxt = "*"
		}

		if filteredCount > 1000 {
			newAlgMotionFrames++
			motionTxt += " +"
		}
		log.Printf("%d orig pixdiff: %d new: %d %s", i, pixDiff, filteredCount, motionTxt)

		prev := make([]uint8, width*height*bytesPerPixel)
		copy(prev, next)

		if len(prevs) >= 2 {
			prevs = prevs[1:]
		}
		prevs = append(prevs, prev)

		dst.Write(diff)
	}

	dst.Close()

	log.Printf("old hasMotion: %t %d", origAlgMotionFrames > 1, origAlgMotionFrames)
	log.Printf("new hasMotion: %t %d", newAlgMotionFrames > 0, newAlgMotionFrames)

	log.Printf("wait in")
	err = ffmpegIn.Wait()
	if err != nil {
		log.Fatalf("ffmpegin err: %s", err)
	}

	log.Printf("wait out")
	err = ffmpegOut.Wait()
	if err != nil {
		log.Fatalf("ffmpegout err: %s", err)
	}
}

func xorIdx(idx int, check []uint8, prevs [][]uint8) uint8 {
	for _, prev := range prevs {
		if check[idx] > 0 && prev[idx] > 0 {
			return 0
		}
		if check[idx] == 0 && prev[idx] == 0 {
			return 0
		}
	}
	return check[idx]
}
