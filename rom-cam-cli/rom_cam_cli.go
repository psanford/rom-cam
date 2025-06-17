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
	rootCmd.AddCommand(blockDetectCommand())

	return rootCmd.Execute()
}

var (
	dstFile         string
	filterNoise     bool
	noiseFilter     bool
	blockSize       int
	blockThreshold  int
	minActiveBlocks int
	showMotion      bool
)

type motionFrame struct {
	Idx  int
	Diff int
}

func edgeDetectCommand() *cobra.Command {
	var cmd = &cobra.Command{
		Use:   "edge-detect <file>",
		Short: "Show edge-detection-output",
		Run:   edgeDetectAction,
	}

	cmd.Flags().StringVarP(&dstFile, "file", "", "", "Dest file, default mpv")
	cmd.Flags().BoolVarP(&noiseFilter, "noise-filter", "n", false, "Enable noise filtering")
	cmd.Flags().BoolVarP(&showMotion, "show-motion", "m", false, "Analyze and show motion detection results")

	return cmd
}

func edgeDetectAction(cmd *cobra.Command, args []string) {
	if len(args) < 1 {
		log.Fatalf(cmd.Use)
	}

	// Use the flag value to determine if we should analyze for motion

	if !showMotion {
		// Original behavior - just display the video
		dstArgs := []string{"-f", "mpegts", "-"}
		if dstFile != "" {
			dstArgs = []string{dstFile}
		}

		filters := "edgedetect"
		if noiseFilter {
			// Apply noise reduction filter before edge detection
			filters = "hqdn3d=4:4:3:3,edgedetect"
		}

		ffmpegArgs := append([]string{"-i", args[0], "-pix_fmt", "gray", "-vf", filters}, dstArgs...)

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
		return
	}

	// Process for motion detection (similar to hasMotion in romcam.go)
	filters := "edgedetect"
	if noiseFilter {
		// Apply noise reduction filter before edge detection
		filters = "hqdn3d=4:4:3:3,edgedetect"
	}

	ffmpegIn := exec.Command("ffmpeg", "-i", args[0], "-vcodec", "rawvideo", "-pix_fmt", "gray", "-vf", filters, "-f", "rawvideo", "-")

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

	var (
		width         = 640
		height        = 480
		bytesPerPixel = 1

		prev         = make([]uint8, width*height*bytesPerPixel)
		next         = make([]uint8, width*height*bytesPerPixel)
		motionFrames = []motionFrame{}
	)

	for i := 0; ; i++ {
		_, err = io.ReadFull(src, next)
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

		motionIndicator := "  "
		if diff > 20000 {
			motionIndicator = "* "
			motionFrames = append(motionFrames, motionFrame{
				Idx:  i,
				Diff: diff,
			})
		}

		log.Printf("Frame %d: Difference %d %s", i, diff, motionIndicator)

		copy(prev, next)
	}

	log.Printf("\nMotion Detection Results:")
	log.Printf("Total frames with motion: %d", len(motionFrames))

	if len(motionFrames) > 1 {
		log.Printf("MOTION DETECTED (threshold: at least 2 motion frames)")

		// Find the frame with the most motion
		bestFrame := motionFrames[0]
		for _, f := range motionFrames[1:] {
			if f.Diff > bestFrame.Diff {
				bestFrame = f
			}
		}
		log.Printf("Highest motion frame: %d (diff: %d)", bestFrame.Idx, bestFrame.Diff)
	} else {
		log.Printf("No significant motion detected")
	}

	err = ffmpegIn.Wait()
	if err != nil {
		log.Printf("ffmpeg exit err: %s", err)
	}
}

func bgSubtractCommand() *cobra.Command {
	var cmd = &cobra.Command{
		Use:   "bg-sub <file>",
		Short: "Show bg-subtraction-output",
		Run:   bgSubtractAction,
	}

	cmd.Flags().StringVarP(&dstFile, "file", "", "", "Dest file, default mpv")
	cmd.Flags().BoolVarP(&filterNoise, "filter-noise", "", false, "Filter out some noise")
	cmd.Flags().BoolVarP(&noiseFilter, "noise-filter", "n", false, "Enable noise reduction")

	return cmd
}

func bgSubtractAction(cmd *cobra.Command, args []string) {
	if len(args) < 1 {
		log.Fatalf(cmd.Use)
	}

	filters := "edgedetect"
	if noiseFilter {
		// Apply noise reduction filter before edge detection
		filters = "hqdn3d=4:4:3:3,edgedetect"
	}

	ffmpegIn := exec.Command("ffmpeg", "-i", args[0], "-vcodec", "rawvideo", "-pix_fmt", "gray", "-vf", filters, "-f", "rawvideo", "-")

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

func blockDetectCommand() *cobra.Command {
	var cmd = &cobra.Command{
		Use:   "block-detect <file>",
		Short: "Use block-based motion detection",
		Run:   blockDetectAction,
	}

	cmd.Flags().StringVarP(&dstFile, "file", "", "", "Dest file, default mpv")
	cmd.Flags().IntVarP(&blockSize, "block-size", "b", 32, "Size of blocks (pixels)")
	cmd.Flags().IntVarP(&blockThreshold, "threshold", "t", 500, "Block change threshold")
	cmd.Flags().IntVarP(&minActiveBlocks, "min-blocks", "m", 3, "Minimum active blocks to trigger motion")
	cmd.Flags().BoolVarP(&noiseFilter, "noise-filter", "n", false, "Enable noise filtering")
	cmd.Flags().BoolVarP(&showMotion, "show-motion-only", "s", false, "Only show motion detection results, no video display")

	return cmd
}

func blockDetectAction(cmd *cobra.Command, args []string) {
	if len(args) < 1 {
		log.Fatalf(cmd.Use)
	}

	filters := "edgedetect"
	if noiseFilter {
		// Apply noise reduction filter before edge detection
		filters = "hqdn3d=4:4:3:3,edgedetect"
	}

	ffmpegIn := exec.Command("ffmpeg", "-i", args[0], "-vcodec", "rawvideo", "-pix_fmt", "gray", "-vf", filters, "-f", "rawvideo", "-")

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

	var dst io.WriteCloser
	var mpv *exec.Cmd

	// Only setup video display if we're not in motion-only mode
	if !showMotion {
		ffmpegOut := exec.Command("ffmpeg", "-f", "rawvideo", "-pix_fmt", "gray", "-s:v", "640x480", "-r", "10", "-i", "-", "-c:v", "libx264", "-f", "mpegts", "-")

		toMpv, err := ffmpegOut.StdoutPipe()
		if err != nil {
			panic(err)
		}
		dst, err = ffmpegOut.StdinPipe()
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
			mpv = exec.Command("mpv", "-")
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
	} else {
		// In motion-only mode, we don't need to display the video
		// so create a null writer to satisfy the interface
		dst = &nullWriter{}
	}

	var (
		width         = 640
		height        = 480
		bytesPerPixel = 1

		origAlgMotionFrames  = 0
		blockAlgMotionFrames = 0

		prev   = make([]uint8, width*height*bytesPerPixel)
		next   = make([]uint8, width*height*bytesPerPixel)
		output = make([]uint8, width*height*bytesPerPixel)
	)

	// Calculate number of blocks in each dimension
	blocksWide := width / blockSize
	blocksHigh := height / blockSize

	for i := 0; ; i++ {
		_, err = io.ReadFull(src, next)
		if err == io.EOF {
			break
		} else if err != nil {
			panic(err)
		}

		// Initialize output to black
		for j := 0; j < len(output); j++ {
			output[j] = 0
		}

		if i == 0 {
			copy(prev, next)
			dst.Write(output)
			continue
		}

		// Original algorithm for comparison
		sumPrev := 0
		sumNext := 0
		for h := 0; h < height; h++ {
			for w := 0; w < width; w++ {
				idx := h*width + w
				sumPrev += int(prev[idx])
				sumNext += int(next[idx])
			}
		}

		pixDiff := sumPrev - sumNext
		if pixDiff < 0 {
			pixDiff = sumNext - sumPrev
		}

		// Block-based algorithm
		activeBlocks := 0
		for blockY := 0; blockY < blocksHigh; blockY++ {
			for blockX := 0; blockX < blocksWide; blockX++ {
				blockSum := 0
				pixelChanges := 0
				totalPixels := 0

				// Calculate sum of differences in this block
				for y := 0; y < blockSize; y++ {
					for x := 0; x < blockSize; x++ {
						h := blockY*blockSize + y
						w := blockX*blockSize + x

						if h < height && w < width {
							totalPixels++
							idx := h*width + w
							diff := int(prev[idx]) - int(next[idx])
							if diff < 0 {
								diff = -diff
							}

							// Count significant pixel changes for noise filtering
							if diff > 10 {
								pixelChanges++
							}

							blockSum += diff
						}
					}
				}

				// Apply additional filtering to blocks if enabled
				if noiseFilter && totalPixels > 0 {
					// Require at least 10% of pixels in a block to have changed
					minRequiredPixels := totalPixels / 10

					if pixelChanges < minRequiredPixels {
						// If not enough pixels changed, ignore this block's differences
						blockSum = 0
					}
				}

				// If this block has significant change, mark it as active
				if blockSum > blockThreshold {
					activeBlocks++

					// Highlight active blocks in output
					for y := 0; y < blockSize; y++ {
						for x := 0; x < blockSize; x++ {
							h := blockY*blockSize + y
							w := blockX*blockSize + x

							if h < height && w < width {
								idx := h*width + w
								output[idx] = 255 // White indicates motion
							}
						}
					}
				}
			}
		}

		motionTxt := ""
		if pixDiff > 20000 {
			origAlgMotionFrames++
			motionTxt = "*"
		}

		if activeBlocks >= minActiveBlocks {
			blockAlgMotionFrames++
			motionTxt += " +"
		}

		log.Printf("%d orig pixdiff: %d block-based: %d/%d blocks %s",
			i, pixDiff, activeBlocks, blocksWide*blocksHigh, motionTxt)

		copy(prev, next)
		dst.Write(output)
	}

	dst.Close()

	log.Printf("\nMotion Detection Results:")
	log.Printf("Original algorithm detected motion: %t (%d frames)", origAlgMotionFrames > 1, origAlgMotionFrames)
	log.Printf("Block-based algorithm detected motion: %t (%d frames with min %d active blocks)", blockAlgMotionFrames > 0, blockAlgMotionFrames, minActiveBlocks)

	if blockAlgMotionFrames > 0 {
		log.Printf("\nBLOCK-BASED MOTION DETECTED")
		log.Printf("Block size: %dx%d pixels, Threshold: %d, Min active blocks: %d",
			blockSize, blockSize, blockThreshold, minActiveBlocks)
	} else {
		log.Printf("\nNo significant motion detected using block-based algorithm")
	}

	log.Printf("\nwait in")
	err = ffmpegIn.Wait()
	if err != nil {
		log.Fatalf("ffmpegin err: %s", err)
	}

	if !showMotion {
		log.Printf("wait out")
		// ffmpegOut only exists if we're not in motion-only mode
		err = mpv.Wait()
		if err != nil {
			log.Fatalf("mpv err: %s", err)
		}
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

// nullWriter implements io.WriteCloser but discards all data
type nullWriter struct{}

func (w *nullWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}

func (w *nullWriter) Close() error {
	return nil
}
