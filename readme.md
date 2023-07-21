# rom-cam: a simple raspberrypi record on motion security camera

This is a simple record on motion security camera written in Go. It is intended
to be run on a raspberry pi and is especially targeted for [gokrazy](https://gokrazy.org).

## Dependencies

rom-cam assumes you have a v4l2 device that supports h264 output.
This could be a usb webcam or a raspberry pi camera module.
If you are using the rpi camera module you will need to use the v4l module,
not the libcamera interface.

rom-cam depends on having an ffmpeg binary available for video capture and
processing.

## Architecture overview

### Stage 1: capture

Video capture occurs from a v4l2 device using ffmpeg. rom-cam captures h264 video
wrapped in mpegts frames. rom-cam splits the video into approximately 10 second segments.
We split the video on h264 IDR frames with non-VCL NAL units. This allows each segment
to be independently playable. If your GOP size does not go evenly into 10s, rom-cam will
split on the next IDR after 10 seconds has passed.

### Stage 1.5: output to local hls server

After a segment is captured it is stored in a small ring buffer that is available to
the local webserver. This allows us to stream the video locally via HLS.

### Stage 2: motion detection

Each segment is passed to the motion detector. The current motion detector is very simple.
It uses ffmpeg to apply greyscale edge detection to the video. Then in rom-cam we simply
count the total intensity of the edge detected pixels. If difference between two frames
is above a threshold we count that as a motion frame. Currently there needs to be at least
2 motion frames detected in a segment to count as monition.

There are a number of improvements we could make to this algorithm fairly easily, but it
currently works well enough for my environment.

### Stage 3: motion segment upload and notification

Segments that are flagged as having motion are uploaded to an s3 bucket. We also will
convert the segment to an animated gif and post that to a Slack channel, if configured.

## Gokrazy OS deployment

Gokrazy is the preferred OS environment to deploy rom-cam in. Raspberry PIs often have
issues with filesystem corruption on sdcards. Gokrazy avoids that problem by using a read
only filesystem by default. Likewise, rom-cam stores data only in memory and writes nothing
to disk.

There is some code for handling kernel module loading that is specific to Gokrazy. rom-cam
should mostly work outside of gokrazy if you disable the kernel module loading.

But seriously, Gokrazy is the best way to run code on a raspberry pi.
